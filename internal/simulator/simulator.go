package simulator

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/watcher"
)

// Options configures the simulator.
type Options struct {
	// Seed makes a run reproducible. The same seed produces the same cluster,
	// the same pod names, and the same sequence of failures.
	Seed int64
	// Interval is how often a new incident is injected.
	Interval time.Duration
	// Now is injectable so tests do not wait on wall-clock time.
	Now func() time.Time
	// Categories restricts what gets injected. Empty means all of them.
	Categories []detector.Category
}

// Simulator generates a fake cluster and injects failures into it.
//
// It implements watcher.Source, so demo mode and cluster mode feed the exact
// same detector, store, and API. That is the point: a demo that runs through a
// separate code path proves nothing about the product, and drifts the moment
// either side changes.
type Simulator struct {
	opts    Options
	rng     *rand.Rand
	cluster *Cluster

	mu     sync.RWMutex
	logs   *kcontext.MemoryLogFetcher
	events *kcontext.MemoryEventSource
	// injected counts failures so far, and picks the next category in order.
	injected int
}

// New builds a simulator with a seeded cluster.
func New(opts Options) *Simulator {
	if opts.Seed == 0 {
		opts.Seed = 20260820
	}
	if opts.Interval <= 0 {
		opts.Interval = 12 * time.Second
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}

	rng := rand.New(rand.NewSource(opts.Seed)) //nolint:gosec // determinism is the point
	sim := &Simulator{
		opts:   opts,
		rng:    rng,
		logs:   kcontext.NewMemoryLogFetcher(),
		events: kcontext.NewMemoryEventSource(),
	}
	sim.cluster = seedCluster(rng, metav1.NewTime(opts.Now()))

	// Every healthy pod gets steady-state output, so a failing pod's log is a
	// signal found among noise rather than a page containing only the answer.
	for _, pod := range sim.cluster.Pods {
		w, ok := sim.cluster.Workload(pod.Name)
		if !ok {
			continue
		}
		sim.logs.Set(pod.Namespace, pod.Name, w.container, healthyLines(rng, w, opts.Now()))
	}

	return sim
}

// Name describes the source.
func (s *Simulator) Name() string {
	return fmt.Sprintf("simulated cluster (seed %d, %d pods across %d namespaces)",
		s.opts.Seed, s.cluster.PodCount(), s.namespaceCount())
}

// Cluster exposes the generated cluster, for the health snapshot.
func (s *Simulator) Cluster() *Cluster { return s.cluster }

// Logs exposes the in-memory log fetcher, which the context builder reads.
func (s *Simulator) Logs() kcontext.LogFetcher { return s.logs }

// Events exposes the in-memory event source, which the context builder reads.
func (s *Simulator) Events() kcontext.EventSource { return s.events }

// Run streams the initial cluster state, then injects a failure on every tick.
func (s *Simulator) Run(ctx context.Context, out chan<- watcher.WatchEvent) error {
	defer close(out)

	// The initial state is emitted first so the dashboard has a populated
	// cluster before anything breaks — a demo that opens on an empty screen
	// wastes the viewer's first ten seconds.
	for _, pod := range s.cluster.Pods {
		if !send(ctx, out, podEventFor(pod, s.opts.Now())) {
			return nil
		}
	}
	for _, deploy := range s.cluster.Deployments {
		event := watcher.WatchEvent{
			Kind: watcher.KindDeployment, Type: watcher.Added,
			Namespace: deploy.Namespace, Name: deploy.Name,
			Timestamp: s.opts.Now(), Deploy: deploy.DeepCopy(),
		}
		if !send(ctx, out, event) {
			return nil
		}
	}

	// The first failure lands quickly so a viewer sees the product work
	// immediately; later ones settle into the configured interval.
	first := time.NewTimer(2 * time.Second)
	defer first.Stop()

	select {
	case <-ctx.Done():
		return nil
	case <-first.C:
		s.emitNext(ctx, out)
	}

	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.emitNext(ctx, out)
		}
	}
}

// InjectNext applies the next failure and returns the events it produced,
// without needing the run loop. Used by tests and by the eval harness.
func (s *Simulator) InjectNext() []watcher.WatchEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	categories := s.categories()
	if len(categories) == 0 {
		return nil
	}

	now := s.opts.Now()

	// Each category is tried in turn; if none can be applied — every candidate
	// pod is already broken — the cluster is healed and the cycle restarts,
	// which keeps a long-running demo from going quiet.
	for attempt := 0; attempt < len(categories); attempt++ {
		category := categories[(s.injected+attempt)%len(categories)]

		inject, ok := injectors[category]
		if !ok {
			continue
		}
		result, applied := inject(s.rng, s.cluster, now)
		if !applied {
			continue
		}

		s.injected += attempt + 1
		s.record(result)
		return result.Events
	}

	s.heal(now)
	return nil
}

// record stores the evidence an injected failure produced, so the context
// builder finds it exactly as it would find real logs and events.
func (s *Simulator) record(result injection) {
	if len(result.Logs) > 0 {
		s.logs.Set(result.LogNamespace, result.LogPod, result.LogContainer, result.Logs)
	} else if result.LogPod != "" {
		// The failure produced no output because the container never ran.
		// Whatever this pod wrote before must go, or the explanation engine is
		// handed evidence that does not exist.
		s.logs.Clear(result.LogNamespace, result.LogPod)
	}
	if len(result.Records) > 0 && result.EventKey != "" {
		s.events.Set(result.LogNamespace, result.EventKey, result.Records)
	}
}

// heal returns the cluster to a healthy state.
//
// A demo left running for an hour would otherwise end with every pod broken
// and nothing left to inject, which reads as a tool that only ever accumulates
// bad news.
func (s *Simulator) heal(now time.Time) {
	s.cluster = seedCluster(s.rng, metav1.NewTime(now))
	for _, pod := range s.cluster.Pods {
		if w, ok := s.cluster.Workload(pod.Name); ok {
			s.logs.Set(pod.Namespace, pod.Name, w.container, healthyLines(s.rng, w, now))
		}
	}
}

func (s *Simulator) emitNext(ctx context.Context, out chan<- watcher.WatchEvent) {
	for _, event := range s.InjectNext() {
		if !send(ctx, out, event) {
			return
		}
	}
}

// categories returns the injection order, honouring any restriction.
func (s *Simulator) categories() []detector.Category {
	if len(s.opts.Categories) == 0 {
		return injectionOrder
	}

	allowed := make(map[detector.Category]bool, len(s.opts.Categories))
	for _, category := range s.opts.Categories {
		allowed[category] = true
	}

	out := make([]detector.Category, 0, len(injectionOrder))
	for _, category := range injectionOrder {
		if allowed[category] {
			out = append(out, category)
		}
	}
	return out
}

func (s *Simulator) namespaceCount() int {
	seen := make(map[string]bool)
	for _, pod := range s.cluster.Pods {
		seen[pod.Namespace] = true
	}
	return len(seen)
}

// Snapshot describes the simulated cluster for the health endpoint.
type Snapshot struct {
	Nodes         int `json:"nodes"`
	TotalPods     int `json:"total_pods"`
	UnhealthyPods int `json:"unhealthy_pods"`
	Deployments   int `json:"deployments"`
}

// Snapshot returns the current cluster shape.
func (s *Simulator) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return Snapshot{
		Nodes:         len(s.cluster.Nodes),
		TotalPods:     s.cluster.PodCount(),
		UnhealthyPods: s.cluster.Unhealthy(),
		Deployments:   len(s.cluster.Deployments),
	}
}

// send delivers an event unless the context is cancelled.
func send(ctx context.Context, out chan<- watcher.WatchEvent, event watcher.WatchEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- event:
		return true
	}
}
