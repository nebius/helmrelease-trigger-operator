package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
)

const (
	ConfigMapReconcilerName = "configmap-reconciler"
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

// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;patch

func (r *ConfigMapReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling ConfigMap", "name", req.Name, "namespace", req.Namespace)

	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, req.NamespacedName, configMap); err != nil {
		logger.V(1).Error(err, "Getting ConfigMap produced an error", "configMap", req.Name)
		return ctrl.Result{}, err
	}

	hrName := configMap.GetAnnotations()[HRAnnotation]
	hrNamespace := configMap.GetAnnotations()[NSAnnotation]

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
		logger.V(1).Error(err, "Getting HelmRelease produced an error",
			"helmRelease", hrName, "namespace", hrNamespace)
		return ctrl.Result{}, err
	}

	if len(hr.Status.History) == 0 {
		logger.V(1).Info("No history found in HelmRelease, skipping helmRelease patch",
			"helmRelease", hrName, "namespace", hrNamespace)
		return ctrl.Result{}, nil
	}

	if hr.Status.History[0].Status != "deployed" {
		logger.V(1).Info("HelmRelease is not deployed, skipping patch", "helmRelease",
			hrName, "namespace", hrNamespace, "status", hr.Status.History[0].Status)
		return ctrl.Result{}, nil
	}

	oldDigest := configMap.GetAnnotations()[HashAnnotation]
	newDigest := getDataHashCM(configMap.Data)

	logger.V(1).Info("old digest", "digest", oldDigest)
	logger.V(1).Info("new digest", "digest", newDigest)

	if oldDigest == newDigest {
		logger.V(1).Info("No changes detected in ConfigMap, skipping HelmRelease patch", "configMap", req.Name)
		return ctrl.Result{}, nil
	}

	patchTargetHR := hr.DeepCopy()

	ts := time.Now().Format(time.RFC3339Nano)
	patchTargetHR.Annotations["reconcile.fluxcd.io/forceAt"] = ts
	patchTargetHR.Annotations["reconcile.fluxcd.io/requestedAt"] = ts

	patchHR := client.MergeFrom(hr.DeepCopy())
	if err := r.Patch(ctx, patchTargetHR, patchHR); err != nil {
		logger.Error(err, "failed to patch HelmRelease", "name", patchTargetHR.Name)
	}

	patchCM := client.MergeFrom(configMap.DeepCopy())
	patchTargetCM := configMap.DeepCopy()
	patchTargetCM.Annotations[HashAnnotation] = newDigest
	if err := r.Patch(ctx, patchTargetCM, patchCM); err != nil {
		logger.Error(err, "failed to patch HelmRelease", "name", patchTargetHR.Name)
	}

	logger.Info("patched HelmRelease with new digest",
		"name", patchTargetHR.Name,
		"digest", newDigest,
		"version", hr.Status.History[0].Version)

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
				configMapOld, okOld := e.ObjectOld.(*corev1.ConfigMap)
				configMapNew, okNew := e.ObjectNew.(*corev1.ConfigMap)

				if !okOld || !okNew || !isValidConfigMap(configMapNew) {
					return false
				}

				oldHash := getDataHashCM(configMapOld.Data)
				newHash := getDataHashCM(configMapNew.Data)

				return oldHash != newHash
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
	if v, ok := cm.Labels[LabelReconcilerNameSourceKey]; ok {
		return v == "true"
	}
	return false
}
