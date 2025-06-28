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
	SecretReconcilerName = "secret-reconciler"
)

// SecretReconciler reconciles Secret resources in a Kubernetes cluster.
type SecretReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// NewSecretReconciler creates a new SecretReconciler instance.
func NewSecretReconciler(client client.Client, scheme *runtime.Scheme) *SecretReconciler {
	return &SecretReconciler{
		Client: client,
		Scheme: scheme,
	}
}

// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;patch

func (r *SecretReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling Secret", "name", req.Name, "namespace", req.Namespace)

	secret := &corev1.Secret{}
	if err := r.Get(ctx, req.NamespacedName, secret); err != nil {
		logger.V(1).Error(err, "Getting Secret produced an error", "secret", req.Name)
		return ctrl.Result{}, err
	}

	hrName := secret.GetAnnotations()[HRAnnotation]
	hrNamespace := secret.GetAnnotations()[NSAnnotation]

	if hrName == "" {
		logger.V(1).Info("No HelmRelease name found in Secret annotations", "secret", req.Name)
		return ctrl.Result{}, nil
	}
	if hrNamespace == "" {
		logger.V(1).Info("No HelmRelease namespace found in Secret annotations", "secret", req.Name)
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

	oldDigest := secret.GetAnnotations()[HashAnnotation]
	newDigest := hr.Status.History[0].Digest

	logger.V(1).Info("old digest", "digest", oldDigest)
	logger.V(1).Info("new digest", "digest", newDigest)

	if oldDigest == newDigest {
		logger.V(1).Info("No changes detected in Secret, skipping HelmRelease patch", "secret", req.Name)
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

	patchSecret := client.MergeFrom(secret.DeepCopy())
	patchTargetSecret := secret.DeepCopy()
	patchTargetSecret.Annotations[HashAnnotation] = newDigest
	if err := r.Patch(ctx, patchTargetSecret, patchSecret); err != nil {
		logger.Error(err, "failed to patch HelmRelease", "name", patchTargetHR.Name)
	}

	logger.Info("patched HelmRelease with new digest",
		"name", patchTargetHR.Name,
		"digest", newDigest,
		"version", hr.Status.History[0].Version)

	return ctrl.Result{}, nil
}

func (r *SecretReconciler) SetupWithManager(
	mgr ctrl.Manager, maxConcurrency int, cacheSyncTimeout time.Duration) error {

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				secret, ok := e.Object.(*corev1.Secret)
				if !ok {
					return false
				}

				return isValidSecret(secret)
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				secretOld, okOld := e.ObjectOld.(*corev1.Secret)
				secretNew, okNew := e.ObjectNew.(*corev1.Secret)
				if !okOld || !okNew || !isValidSecret(secretNew) {
					return false
				}

				oldHash := getDataHashSecret(secretOld.Data)
				newHash := getDataHashSecret(secretNew.Data)

				return oldHash != newHash
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			GenericFunc: func(e event.GenericEvent) bool {
				secret, ok := e.Object.(*corev1.Secret)
				if !ok {
					return false
				}

				return isValidSecret(secret)
			},
		})).
		WithOptions(ControllerOptions(maxConcurrency, cacheSyncTimeout)).
		Complete(r)
}

func isValidSecret(secret *corev1.Secret) bool {
	if v, ok := secret.Labels[LabelReconcilerNameSourceKey]; ok {
		return v == "true"
	}
	return false
}
