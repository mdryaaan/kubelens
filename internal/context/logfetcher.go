package kcontext

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// PodLogFetcher reads container output through the Kubernetes API.
type PodLogFetcher struct {
	client kubernetes.Interface
	// timeout bounds a single log read. The log endpoint streams, and a
	// container writing continuously will happily stream forever.
	timeout time.Duration
}

// NewPodLogFetcher builds a fetcher over a Kubernetes client.
func NewPodLogFetcher(client kubernetes.Interface) *PodLogFetcher {
	return &PodLogFetcher{client: client, timeout: 10 * time.Second}
}

// Fetch returns the last n lines written by a container.
//
// Previous=true is tried first for a container that has restarted: the output
// that explains a crash belongs to the process that crashed, not to the fresh
// one that replaced it. Falling back to the current container matters for the
// categories where nothing has terminated yet.
func (f *PodLogFetcher) Fetch(ctx context.Context, namespace, pod, container string, lines int) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	tail := int64(lines)

	previous, err := f.read(ctx, namespace, pod, container, tail, true)
	if err == nil && len(previous) > 0 {
		return previous, nil
	}

	current, err := f.read(ctx, namespace, pod, container, tail, false)
	if err != nil {
		return nil, fmt.Errorf("reading logs for %s/%s: %w", namespace, pod, err)
	}
	return current, nil
}

func (f *PodLogFetcher) read(ctx context.Context, namespace, pod, container string, tail int64, previous bool) ([]string, error) {
	options := &corev1.PodLogOptions{
		Container: container,
		TailLines: &tail,
		Previous:  previous,
		// Timestamps are omitted: they would double the token cost of every
		// line while the ordering already carries the sequence.
		Timestamps: false,
	}

	stream, err := f.client.CoreV1().Pods(namespace).GetLogs(pod, options).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()

	return readLines(stream, int(tail))
}

// readLines collects up to limit lines, keeping the tail.
func readLines(r io.Reader, limit int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	// A single log line can exceed bufio's default 64KiB — a stack trace on one
	// line, a serialised payload — and the default would abort the whole read
	// with ErrTooLong rather than skipping that line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out []string
	for scanner.Scan() {
		out = append(out, scanner.Text())
		if limit > 0 && len(out) > limit {
			out = out[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		// Partial output is still evidence; a truncated read should not throw
		// away the lines already collected.
		return out, nil
	}
	return out, nil
}

// APIEventSource reads Kubernetes events through the API.
type APIEventSource struct {
	client kubernetes.Interface
}

// NewAPIEventSource builds an event source over a Kubernetes client.
func NewAPIEventSource(client kubernetes.Interface) *APIEventSource {
	return &APIEventSource{client: client}
}

// Recent returns the newest events concerning a namespaced resource.
func (s *APIEventSource) Recent(ctx context.Context, namespace, resource string, limit int) ([]EventRecord, error) {
	name := resource
	if idx := strings.Index(resource, "/"); idx >= 0 {
		name = resource[idx+1:]
	}

	selector := fields.OneTermEqualSelector("involvedObject.name", name).String()
	list, err := s.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: selector,
		Limit:         int64(limit) * 4, // over-fetch, then keep the newest
	})
	if err != nil {
		return nil, fmt.Errorf("listing events for %s/%s: %w", namespace, name, err)
	}

	out := make([]EventRecord, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		out = append(out, EventRecord{
			Type:      item.Type,
			Reason:    item.Reason,
			Message:   item.Message,
			Count:     item.Count,
			Timestamp: newestEventTime(item),
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func newestEventTime(event *corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	return event.FirstTimestamp.Time
}

// MemoryLogFetcher serves logs held in memory.
//
// This is what backs demo mode, and it is also what the tests use. Sharing one
// implementation between the two means the demo exercises the same context
// pipeline a real cluster does, rather than a parallel one that could drift.
type MemoryLogFetcher struct {
	mu   sync.RWMutex
	logs map[string][]string
}

// NewMemoryLogFetcher builds an empty in-memory fetcher.
func NewMemoryLogFetcher() *MemoryLogFetcher {
	return &MemoryLogFetcher{logs: make(map[string][]string)}
}

// Set stores the output for a container.
func (f *MemoryLogFetcher) Set(namespace, pod, container string, lines []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs[logKey(namespace, pod, container)] = lines
}

// Clear removes any stored output for a pod.
//
// Needed because some failures mean the container never ran: a pod that could
// not be scheduled, or one whose image never pulled, has no output at all.
// Leaving earlier lines in place would hand the explanation engine evidence
// that does not exist in reality.
func (f *MemoryLogFetcher) Clear(namespace, pod string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	prefix := namespace + "/" + pod + "/"
	for key := range f.logs {
		if strings.HasPrefix(key, prefix) {
			delete(f.logs, key)
		}
	}
}

// Fetch returns the stored output for a container.
func (f *MemoryLogFetcher) Fetch(_ context.Context, namespace, pod, container string, lines int) ([]string, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	stored, ok := f.logs[logKey(namespace, pod, container)]
	if !ok {
		// Fall back to any container in the pod: a detector that could not name
		// the container should still get the pod's output.
		for key, value := range f.logs {
			if strings.HasPrefix(key, namespace+"/"+pod+"/") {
				stored = value
				ok = true
				break
			}
		}
	}
	if !ok {
		return nil, fmt.Errorf("no logs recorded for %s/%s", namespace, pod)
	}

	if lines > 0 && len(stored) > lines {
		stored = stored[len(stored)-lines:]
	}
	out := make([]string, len(stored))
	copy(out, stored)
	return out, nil
}

// MemoryEventSource serves events held in memory, for demo mode and tests.
type MemoryEventSource struct {
	mu     sync.RWMutex
	events map[string][]EventRecord
}

// NewMemoryEventSource builds an empty in-memory event source.
func NewMemoryEventSource() *MemoryEventSource {
	return &MemoryEventSource{events: make(map[string][]EventRecord)}
}

// Set stores the events for a resource.
func (s *MemoryEventSource) Set(namespace, resource string, events []EventRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[namespace+"/"+resource] = events
}

// Recent returns the newest stored events for a resource.
func (s *MemoryEventSource) Recent(_ context.Context, namespace, resource string, limit int) ([]EventRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored := s.events[namespace+"/"+resource]
	if len(stored) == 0 {
		return nil, fmt.Errorf("no events recorded for %s/%s", namespace, resource)
	}
	return trimEvents(append([]EventRecord(nil), stored...), limit), nil
}

func logKey(namespace, pod, container string) string {
	return namespace + "/" + pod + "/" + container
}
