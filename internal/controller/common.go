package controller

import (
	"fmt"
	"hash/adler32"
	"sync"
	"time"

	"k8s.io/client-go/util/workqueue"
	"k8s.io/kubernetes/pkg/util/hash"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	LabelReconcilerNameSourceKey = "uburro.github.com/fluxcd-trigger-operator"
	HRAnnotation                 = "uburro.github.com/helmreleases-name"
	NSAnnotation                 = "uburro.github.com/helmreleases-namespace"
	HashAnnotation               = "uburro.github.com/config-digest"
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
