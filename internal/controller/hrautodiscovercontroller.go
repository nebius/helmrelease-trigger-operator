package controller

import (
	"context"
	"reflect"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	HRAutodicoverReconcilerName = "hr-autodiscovery"
)

type HRAutodicoverReconciler struct {
	BaseReconciler
}

// NewHRAutodicoverReconciler creates a new HRAutodicoverReconciler instance.
func NewHRAutodicoverReconciler(client client.Client, scheme *runtime.Scheme) *HRAutodicoverReconciler {
	return &HRAutodicoverReconciler{
		BaseReconciler: BaseReconciler{
			Client: client,
			Scheme: scheme,
		},
	}
}

// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;patch

func (r *HRAutodicoverReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName(HRAutodicoverReconcilerName)
	log.Info("Reconciling HelmRelease for autodiscovery", "name", req.NamespacedName)
	var hr helmv2.HelmRelease
	if err := r.Get(ctx, req.NamespacedName, &hr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if hr.Spec.ValuesFrom != nil {
		for _, valueFrom := range hr.Spec.ValuesFrom {
			if err := r.ReconcileHRSecretOrConfigMap(ctx, &hr, valueFrom); err != nil {
				if client.IgnoreNotFound(err) == nil {
					log.Info("Resource not found, skipping", "kind", valueFrom.Kind, "name", valueFrom.Name, "namespace", hr.Namespace)
					continue
				}
				log.Error(err, "Failed to process valuesFrom", "kind", valueFrom.Kind, "name", valueFrom.Name, "namespace", hr.Namespace)
				return ctrl.Result{}, err
			}
			log.Info("Successfully reconciled resource for autodiscovery",
				"kind", valueFrom.Kind,
				"name", valueFrom.Name,
				"namespace", hr.Namespace)
		}
	}

	return ctrl.Result{}, nil
}

func (r *HRAutodicoverReconciler) SetupWithManager(mgr ctrl.Manager,
	maxConcurrency int, cacheSyncTimeout time.Duration) error {
	return ctrl.NewControllerManagedBy(mgr).Named(HRAutodicoverReconcilerName).
		For(&helmv2.HelmRelease{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldHR, oldOk := e.ObjectOld.(*helmv2.HelmRelease)
				newHR, newOk := e.ObjectNew.(*helmv2.HelmRelease)

				if !oldOk || !newOk {
					return false
				}
				return !reflect.DeepEqual(oldHR.Spec.ValuesFrom, newHR.Spec.ValuesFrom)
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return false
			},
		})).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.findHelmReleasesForSecret),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					return true
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					return false
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					return false
				},
				GenericFunc: func(e event.GenericEvent) bool {
					return false
				},
			}),
		).
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findHelmReleasesForConfigMap),
			builder.WithPredicates(predicate.Funcs{
				CreateFunc: func(e event.CreateEvent) bool {
					return true
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					return false
				},
				UpdateFunc: func(e event.UpdateEvent) bool {
					return false
				},
				GenericFunc: func(e event.GenericEvent) bool {
					return false
				},
			}),
		).
		WithOptions(ControllerOptions(maxConcurrency, cacheSyncTimeout)).
		Complete(r)
}
