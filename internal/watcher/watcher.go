// Package watcher observes a Kubernetes cluster and emits typed change events.
package watcher

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

// ResourceKind identifies what a watch event is about.
type ResourceKind string

// The kinds kubelens watches. Deliberately narrow: these three carry every
// signal the six detectors need, and watching more would cost memory in the
// informer cache for data nothing reads.
const (
	KindPod        ResourceKind = "Pod"
	KindEvent      ResourceKind = "Event"
	KindDeployment ResourceKind = "Deployment"
)

// EventType is what happened to the resource.
type EventType string

// Informer callbacks map onto these three.
const (
	Added    EventType = "added"
	Modified EventType = "modified"
	Deleted  EventType = "deleted"
)

// WatchEvent is one observed change, in the shape the detectors consume.
//
// The typed objects are carried by pointer rather than flattened into a
// generic map because every detector needs different fields — container
// statuses, deployment conditions, event reasons — and flattening would mean
// re-parsing what the informer already decoded.
type WatchEvent struct {
	Kind      ResourceKind       `json:"kind"`
	Type      EventType          `json:"type"`
	Namespace string             `json:"namespace"`
	Name      string             `json:"name"`
	Timestamp time.Time          `json:"timestamp"`
	Pod       *corev1.Pod        `json:"-"`
	Event     *corev1.Event      `json:"-"`
	Deploy    *appsv1.Deployment `json:"-"`
	// Old carries the previous state on a Modified event, which is what makes
	// "restart count increased" detectable rather than merely "restart count is
	// high".
	Old *WatchEvent `json:"-"`
}

// Key is the namespaced identity of the resource the event concerns.
func (e WatchEvent) Key() string { return e.Namespace + "/" + e.Name }

// Source produces watch events. The interface exists so the simulator is a
// drop-in for a real cluster: `--demo` and `--kubeconfig` run through exactly
// the same detector, store, and API code, which is the only way the demo can
// be trusted to represent the real thing.
type Source interface {
	// Name describes the source for logs and the API's health endpoint.
	Name() string
	// Run streams events into out until ctx is cancelled. It must close out
	// before returning so consumers terminate cleanly.
	Run(ctx context.Context, out chan<- WatchEvent) error
}

// Options configures a cluster watcher.
type Options struct {
	// Namespaces restricts the watch. Empty means all namespaces.
	Namespaces []string
	// Resync is how often the informer replays its cache. Zero disables it.
	Resync time.Duration
	// BufferSize bounds the outbound channel.
	BufferSize int
}

// ClusterWatcher watches a real cluster through client-go informers.
type ClusterWatcher struct {
	client kubernetes.Interface
	opts   Options
}

// NewClusterWatcher builds a watcher over a Kubernetes client.
func NewClusterWatcher(client kubernetes.Interface, opts Options) *ClusterWatcher {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 256
	}
	return &ClusterWatcher{client: client, opts: opts}
}

// Name describes the source.
func (w *ClusterWatcher) Name() string {
	if len(w.opts.Namespaces) == 0 {
		return "cluster (all namespaces)"
	}
	return fmt.Sprintf("cluster (%v)", w.opts.Namespaces)
}

// Run starts the informers and streams events until the context is cancelled.
//
// One SharedInformerFactory per namespace rather than one filtered factory,
// because client-go scopes a factory to a single namespace at construction
// time; the alternative is a field selector per informer, which does not
// compose with the typed accessors.
func (w *ClusterWatcher) Run(ctx context.Context, out chan<- WatchEvent) error {
	defer close(out)

	namespaces := w.opts.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{""} // the empty string means all namespaces
	}

	var factories []informers.SharedInformerFactory
	for _, ns := range namespaces {
		factory := informers.NewSharedInformerFactoryWithOptions(
			w.client, w.opts.Resync, informers.WithNamespace(ns))

		if err := registerPodInformer(factory, out); err != nil {
			return err
		}
		if err := registerEventInformer(factory, out); err != nil {
			return err
		}
		if err := registerDeploymentInformer(factory, out); err != nil {
			return err
		}

		factories = append(factories, factory)
	}

	for _, factory := range factories {
		factory.Start(ctx.Done())
	}

	// Waiting for sync before returning means the first incident the detector
	// sees is a real change, not the backlog of everything already broken in
	// the cluster being replayed as if it just happened.
	for _, factory := range factories {
		for informerType, synced := range factory.WaitForCacheSync(ctx.Done()) {
			if synced {
				continue
			}
			// WaitForCacheSync also reports false when the context is
			// cancelled. Shutting down cleanly is not a sync failure, and
			// reporting it as one puts a scary error in the log of every
			// normal exit.
			if err := ctx.Err(); err != nil {
				return nil
			}
			return fmt.Errorf("informer cache for %v failed to sync", informerType)
		}
	}

	<-ctx.Done()
	return nil
}

// emit sends an event unless the consumer is gone or the buffer is full.
//
// Dropping on a full buffer is deliberate. Informer callbacks run on the
// shared processor goroutine; blocking one stalls every other handler and
// eventually the whole cache. A dropped event costs one missed detection, a
// blocked handler costs the entire watch.
func emit(out chan<- WatchEvent, event WatchEvent) {
	select {
	case out <- event:
	default:
	}
}

// resourceEventHandler builds informer callbacks that emit typed events.
func resourceEventHandler(out chan<- WatchEvent, convert func(obj any, t EventType) (WatchEvent, bool)) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			if event, ok := convert(obj, Added); ok {
				emit(out, event)
			}
		},
		UpdateFunc: func(oldObj, newObj any) {
			event, ok := convert(newObj, Modified)
			if !ok {
				return
			}
			if previous, ok := convert(oldObj, Modified); ok {
				event.Old = &previous
			}
			emit(out, event)
		},
		DeleteFunc: func(obj any) {
			// A delete can arrive wrapped when the informer missed the actual
			// deletion and is reconstructing it from a relist.
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			if event, ok := convert(obj, Deleted); ok {
				emit(out, event)
			}
		},
	}
}
