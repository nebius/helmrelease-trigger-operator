package controller

import (
	"context"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

const (
	ConfigMapReconcilerName               = "configmap-reconciler"
	LabelConfigMapReconcilerNameSourceKey = "uburro.github.com/fluxcd-trigger-operator"
	AnnotationHelmReleaseNameKey          = "uburro.github.com/helmreleases-name"
	AnnotationHelmReleaseNamespaceKey     = "uburro.github.com/helmreleases-namespace"
	AnnotationDigistKey                   = "uburro.github.com/config-digest"
	DefaultFluxcdNamespace                = "flux-system"
)

// ConfigMapReconciler reconciles ConfigMap resources in a Kubernetes cluster.
type ConfigMapReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// NewConfigMapReconciler creates a new ConfigMapReconciler instance.
func NewConfigMapReconciler(client client.Client, scheme *runtime.Scheme) *ConfigMapReconciler {
	return &ConfigMapReconciler{
		Client: client,
		Scheme: scheme,
	}
}

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling ConfigMap", "name", req.Name, "namespace", req.Namespace)

	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, configMap); err != nil {
		logger.V(1).Error(err, "Getting ConfigMap produced an error", "configMap", req.Name)
		return ctrl.Result{}, err
	}

	hrName := configMap.GetAnnotations()[AnnotationHelmReleaseNameKey]
	hrNamespace := configMap.GetAnnotations()[AnnotationHelmReleaseNamespaceKey]

	if hrName == "" {
		logger.V(1).Info("No HelmRelease name found in ConfigMap annotations", "configMap", req.Name)
		return ctrl.Result{}, nil
	}
	if hrNamespace == "" {
		logger.V(1).Info("No HelmRelease namespace found in ConfigMap annotations", "configMap", req.Name)
		hrNamespace = DefaultFluxcdNamespace
	}

	hr := &helmv2.HelmRelease{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      hrName,
		Namespace: hrNamespace,
	}, hr); err != nil {
		logger.V(1).Error(err, "Getting HelmRelease produced an error", "helmRelease", hrName, "namespace", hrNamespace)
		return ctrl.Result{}, err
	}

	oldDigest := configMap.GetAnnotations()[AnnotationDigistKey]
	newDigest := hr.Status.History[0].Digest

	if oldDigest == newDigest {
		logger.V(1).Info("No changes detected in ConfigMap, skipping HelmRelease patch", "configMap", req.Name)
		return ctrl.Result{}, nil
	}

	patchTarget := hr.DeepCopy()

	ts := time.Now().Format(time.RFC3339Nano)
	patchTarget.Annotations[AnnotationDigistKey] = newDigest
	patchTarget.Annotations["reconcile.fluxcd.io/forceAt"] = ts
	patchTarget.Annotations["reconcile.fluxcd.io/requestedAt"] = ts

	patch := client.MergeFrom(hr.DeepCopy())

	if err := r.Patch(ctx, patchTarget, patch); err != nil {
		logger.Error(err, "failed to patch HelmRelease", "name", patchTarget.Name)
	}

	logger.Info("patched HelmRelease with new digest", "name", patchTarget.Name, "digest", newDigest, "version", hr.Status.History[0].Version)

	return ctrl.Result{}, nil
}

func (r *ConfigMapReconciler) SetupWithManager(
	mgr ctrl.Manager, maxConcurrency int, cacheSyncTimeout time.Duration) error {

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				configMap, ok := e.Object.(*corev1.ConfigMap)
				if !ok {
					return false
				}

				return isValidConfigMap(configMap)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				configMap, ok := e.ObjectNew.(*corev1.ConfigMap)
				if !ok {
					return false
				}

				return isValidConfigMap(configMap)
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				configMap, ok := e.Object.(*corev1.ConfigMap)
				if !ok {
					return false
				}

				return isValidConfigMap(configMap)
			},
		})).
		WithOptions(ControllerOptions(maxConcurrency, cacheSyncTimeout)).
		Complete(r)
}

func isValidConfigMap(cm *corev1.ConfigMap) bool {
	if v, ok := cm.Labels[LabelConfigMapReconcilerNameSourceKey]; ok {
		return v == "true"
	}
	return false
}

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
