package controller

import (
	"context"
	"fmt"
	"hash/adler32"
	"strings"
	"sync"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/kubernetes/pkg/util/hash"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	LabelReconcilerNameSourceKey = "nebius.ai/helmrelease-trigger-operator"
	HRNameAnnotation             = "nebius.ai/helmreleases-name"
	HRNSAnnotation               = "nebius.ai/helmreleases-namespace"
	HashAnnotation               = "nebius.ai/config-digest"
	DefaultFluxcdNamespace       = "flux-system"
)

var (
	optionsInit    sync.Once
	defaultOptions *controller.Options
)

func ControllerOptions(maxConcurrency int, cacheSyncTimeout time.Duration) controller.Options {
	rateLimiters := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](30*time.Second, 5*time.Minute)
	optionsInit.Do(func() {
		defaultOptions = &controller.Options{
			RateLimiter:             rateLimiters,
			CacheSyncTimeout:        cacheSyncTimeout,
			MaxConcurrentReconciles: maxConcurrency,
		}
	})
	return *defaultOptions
}

func getDataHashCM(data map[string]string) string {
	hasher := adler32.New()
	hash.DeepHashObject(hasher, data)
	return fmt.Sprintf("%x", hasher.Sum32())
}

func getDataHashSecret(data map[string][]byte) string {
	hasher := adler32.New()
	hash.DeepHashObject(hasher, data)
	return fmt.Sprintf("%x", hasher.Sum32())
}

type BaseReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *BaseReconciler) ReconcileResource(
	ctx context.Context,
	req ctrl.Request,
	resource client.Object,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	resourceType := fmt.Sprintf("%T", resource)
	logger.Info(fmt.Sprintf("reconciling %s", resourceType), "name", req.Name, "namespace", req.Namespace)

	if err := r.Get(ctx, req.NamespacedName, resource); err != nil {
		logger.V(1).Error(err, fmt.Sprintf("Getting %s produced an error", resourceType), "resource", req.Name)
		return ctrl.Result{}, err
	}

	listHRName, hrNamespace, oldDigest := r.ExtractFromAnnotations(resource)

	for _, hrName := range listHRName {
		hr, err := r.GetHelmRelease(ctx, hrName, hrNamespace)
		if err != nil {
			logger.V(1).Error(err, "Getting HelmRelease produced an error",
				"helmRelease", hrName, "namespace", hrNamespace)
			if apierrors.IsNotFound(err) {
				logger.V(1).Info("HelmRelease not found, removing from annotations",
					"helmRelease", hrName, "namespace", hrNamespace)

				if err := r.RemoveHRNameFromAnnotations(ctx, resource, hrName); err != nil {
					logger.Error(err, "Failed to remove HelmRelease name from annotations",
						"helmRelease", hrName, "namespace", hrNamespace)
					return ctrl.Result{}, err
				}
				// Re-fetch the resource to ensure we have the latest state after patching
				if err := r.Get(ctx, req.NamespacedName, resource); err != nil {
					logger.V(1).Error(err, fmt.Sprintf("Failed to get %s after removing HelmRelease name from annotations", resourceType),
						"resource", req.Name)
					return ctrl.Result{}, err
				}
				continue
			}
			return ctrl.Result{}, err
		}

		if !r.IsDeployed(hr) {
			logger.V(1).Info("HelmRelease is not deployed, skipping patch", "helmRelease",
				hrName, "namespace", hrNamespace)
			continue
		}

		newDigest := r.GetNewDigest(resource)

		logger.V(1).Info("old digest", "digest", oldDigest)
		logger.V(1).Info("new digest", "digest", newDigest)

		if oldDigest == newDigest {
			logger.V(1).Info(fmt.Sprintf("No changes detected in %s, skipping HelmRelease patch", resourceType), "resource", req.Name)
			return ctrl.Result{}, nil
		}

		if err := r.PatchHR(ctx, hr); err != nil {
			logger.Error(err, "failed to patch HelmRelease", "name", hr.Name)
			return ctrl.Result{}, err
		}

		if err := r.UpdateDigest(ctx, resource, newDigest); err != nil {
			logger.Error(err, fmt.Sprintf("failed to patch %s", resourceType), "name", resource.GetName())
			return ctrl.Result{}, err
		}

		logger.Info("patched HelmRelease with new digest",
			"name", hr.Name,
			"digest", newDigest,
			"version", hr.Status.History[0].Version)
	}

	return ctrl.Result{}, nil
}

func (r *BaseReconciler) RemoveHRNameFromAnnotations(
	ctx context.Context, resource client.Object, hrName string,
) error {
	patchTarget := resource.DeepCopyObject().(client.Object)
	annotations := patchTarget.GetAnnotations()
	if annotations == nil {
		return nil
	}

	listHRName := strings.Split(annotations[HRNameAnnotation], ",")
	newList := []string{}
	for _, name := range listHRName {
		if name != hrName {
			newList = append(newList, name)
		}
	}

	if len(newList) == 0 {
		delete(annotations, HRNameAnnotation)
	} else {
		annotations[HRNameAnnotation] = strings.Join(newList, ",")
	}

	patchTarget.SetAnnotations(annotations)

	patch := client.MergeFrom(resource.DeepCopyObject().(client.Object))
	return r.Patch(ctx, patchTarget, patch)
}

func (r *BaseReconciler) ExtractFromAnnotations(resource client.Object) ([]string, string, string) {
	annotations := resource.GetAnnotations()
	hrNamespace := annotations[HRNSAnnotation]
	if hrNamespace == "" {
		hrNamespace = DefaultFluxcdNamespace
	}
	hrName := annotations[HRNameAnnotation]
	listHRName := strings.Split(hrName, ",")
	oldDigest := annotations[HashAnnotation]
	return listHRName, hrNamespace, oldDigest
}

func (r *BaseReconciler) GetHelmRelease(ctx context.Context, name, namespace string) (*helmv2.HelmRelease, error) {
	hr := &helmv2.HelmRelease{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, hr)
	return hr, err
}

func (r *BaseReconciler) IsDeployed(hr *helmv2.HelmRelease) bool {
	return len(hr.Status.History) > 0 && hr.Status.History[0].Status == "deployed"
}

func (r *BaseReconciler) GetNewDigest(resource client.Object) string {
	switch obj := resource.(type) {
	case *corev1.Secret:
		return getDataHashSecret(obj.Data)
	case *corev1.ConfigMap:
		return getDataHashCM(obj.Data)
	default:
		return ""
	}
}

func (r *BaseReconciler) GetCurrentDigest(hr *helmv2.HelmRelease) string {
	if len(hr.Status.History) == 0 {
		return ""
	}
	return hr.Status.History[0].Digest
}

func (r *BaseReconciler) PatchHR(ctx context.Context, hr *helmv2.HelmRelease) error {
	patchTarget := hr.DeepCopyObject().(*helmv2.HelmRelease)
	ts := time.Now().Format(time.RFC3339Nano)

	if patchTarget.Annotations == nil {
		patchTarget.Annotations = make(map[string]string)
	}
	patchTarget.Annotations["reconcile.fluxcd.io/forceAt"] = ts
	patchTarget.Annotations["reconcile.fluxcd.io/requestedAt"] = ts

	patch := client.MergeFrom(hr.DeepCopyObject().(*helmv2.HelmRelease))
	return r.Patch(ctx, patchTarget, patch)
}

func (r *BaseReconciler) UpdateDigest(ctx context.Context, obj client.Object, newDigest string) error {
	patchTarget := obj.DeepCopyObject().(client.Object)

	annotations := patchTarget.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[HashAnnotation] = newDigest
	patchTarget.SetAnnotations(annotations)

	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	return r.Patch(ctx, patchTarget, patch)
}

func (r *BaseReconciler) ReconcileHRSecretOrConfigMap(ctx context.Context, hr *helmv2.HelmRelease, valueFrom helmv2.ValuesReference) error {
	log := log.FromContext(ctx)
	log.Info("Reconciling resource for autodiscovery",
		"kind", valueFrom.Kind,
		"name", valueFrom.Name,
		"namespace", hr.Namespace)

	var resource client.Object
	switch valueFrom.Kind {
	case "ConfigMap":
		resource = &corev1.ConfigMap{}
	case "Secret":
		resource = &corev1.Secret{}
	default:
		log.Error(fmt.Errorf("unsupported kind: %s", valueFrom.Kind),
			"Failed to reconcile resource for autodiscovery")
		return nil
	}

	objRef := &corev1.ObjectReference{
		Name:      valueFrom.Name,
		Namespace: hr.Namespace,
	}

	if err := r.Get(ctx, types.NamespacedName{
		Name:      objRef.Name,
		Namespace: objRef.Namespace,
	}, resource); err != nil {
		return err
	}

	annotations := r.GetOrGenerateAnnotations(resource, HRNameAnnotation, hr.Namespace, hr.Name)

	labels := map[string]string{
		LabelReconcilerNameSourceKey: "true",
	}

	return r.AddAnnotationsAndLabel(ctx, resource, annotations, labels)
}

func (r *BaseReconciler) GetOrGenerateAnnotations(resource client.Object, hrNameAnnotation, ns, hrName string) map[string]string {
	annotations := map[string]string{
		HashAnnotation: r.GetNewDigest(resource),
	}

	existingAnnotations := resource.GetAnnotations()
	if existingAnnotations == nil {
		existingAnnotations = make(map[string]string)
	}

	existingHRNames := existingAnnotations[hrNameAnnotation]
	if existingHRNames == "" {
		annotations[hrNameAnnotation] = hrName
	} else {
		hrNames := strings.Split(existingHRNames, ",")
		found := false
		for _, name := range hrNames {
			if strings.TrimSpace(name) == hrName {
				found = true
				break
			}
		}
		if !found {
			annotations[hrNameAnnotation] = existingHRNames + "," + hrName
		} else {
			annotations[hrNameAnnotation] = existingHRNames
		}
	}

	annotations[HRNSAnnotation] = ns

	return annotations
}

func (r *BaseReconciler) AddAnnotationsAndLabel(
	ctx context.Context, resource client.Object, annotations, labels map[string]string) error {
	log := log.FromContext(ctx).V(1)

	patchTarget := resource.DeepCopyObject().(client.Object)

	if patchTarget.GetAnnotations() == nil {
		patchTarget.SetAnnotations(make(map[string]string))
	}
	if patchTarget.GetLabels() == nil {
		patchTarget.SetLabels(make(map[string]string))
	}

	updatedAnnotations := false
	for key, value := range annotations {
		if patchTarget.GetAnnotations()[key] != value {
			patchTarget.GetAnnotations()[key] = value
			updatedAnnotations = true
		}
	}

	updatedLabels := false
	for key, value := range labels {
		if patchTarget.GetLabels()[key] != value {
			patchTarget.GetLabels()[key] = value
			updatedLabels = true
		}
	}

	if updatedAnnotations || updatedLabels {
		patch := client.MergeFrom(resource.DeepCopyObject().(client.Object))
		if err := r.Patch(ctx, patchTarget, patch); err != nil {
			return err
		}
		log.Info("Updated resource with annotations and labels for autodiscovery",
			"kind", resource.GetObjectKind().GroupVersionKind().Kind,
			"name", resource.GetName(),
			"namespace", resource.GetNamespace())
	}

	return nil
}

func (r *BaseReconciler) findHelmReleasesForConfigMap(ctx context.Context, obj client.Object) []reconcile.Request {
	configMap, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}

	var helmReleases helmv2.HelmReleaseList
	listOps := &client.ListOptions{
		Namespace: obj.GetNamespace(),
	}
	if err := r.List(ctx, &helmReleases, listOps); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, hr := range helmReleases.Items {
		if r.helmReleaseUsesConfigMap(&hr, configMap) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      hr.Name,
					Namespace: hr.Namespace,
				},
			})
		}
	}
	return requests
}

func (r *BaseReconciler) helmReleaseUsesConfigMap(hr *helmv2.HelmRelease, cm *corev1.ConfigMap) bool {
	for _, valuesRef := range hr.Spec.ValuesFrom {
		if valuesRef.Kind == "ConfigMap" &&
			valuesRef.Name == cm.Name {
			return true
		}
	}
	return false
}

func (r *BaseReconciler) findHelmReleasesForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var helmReleases helmv2.HelmReleaseList
	listOps := &client.ListOptions{
		Namespace: obj.GetNamespace(),
	}
	if err := r.List(ctx, &helmReleases, listOps); err != nil {
		return nil
	}

	var requests []reconcile.Request
	for _, hr := range helmReleases.Items {
		if r.helmReleaseUsesSecret(&hr, secret) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      hr.Name,
					Namespace: hr.Namespace,
				},
			})
		}
	}

	return requests
}

func (r *BaseReconciler) helmReleaseUsesSecret(hr *helmv2.HelmRelease, secret *corev1.Secret) bool {
	for _, valuesRef := range hr.Spec.ValuesFrom {
		if valuesRef.Kind == "Secret" &&
			valuesRef.Name == secret.Name {
			return true
		}
	}
	return false
}
