package detector

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// probeFailureThreshold is how many Unhealthy warnings must arrive before the
// rule fires.
//
// A single failed probe is normal — it is how a slow-starting container tells
// Kubernetes it is not ready yet, and reporting one would make this rule fire
// on every deploy. Repeated failures are the signal.
const probeFailureThreshold = 3

// probeFailureWindow is how long failures are counted over. Failures spread
// across an hour are not the same problem as three in ninety seconds.
const probeFailureWindow = 5 * time.Minute

// ProbeFailureRule detects readiness and liveness probes that keep failing.
type ProbeFailureRule struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

// NewProbeFailureRule builds the rule.
func NewProbeFailureRule() *ProbeFailureRule {
	return &ProbeFailureRule{failures: make(map[string][]time.Time)}
}

// Category is the failure pattern this rule detects.
func (r *ProbeFailureRule) Category() Category { return ProbeFailure }

// Describe explains what the rule looks for.
func (r *ProbeFailureRule) Describe() string {
	return fmt.Sprintf(
		"At least %d Warning events with reason Unhealthy for the same pod within %s.",
		probeFailureThreshold, probeFailureWindow)
}

// Detect flags a pod whose probes are failing repeatedly.
func (r *ProbeFailureRule) Detect(event watcher.WatchEvent) *Incident {
	if event.Kind != watcher.KindEvent || event.Event == nil || event.Type == watcher.Deleted {
		return nil
	}

	source := event.Event
	if !watcher.IsWarning(source) || source.Reason != "Unhealthy" {
		return nil
	}

	key := event.Key()
	count, first := r.record(key, event.Timestamp)
	if count < probeFailureThreshold {
		return nil
	}

	kind := probeKind(source.Message)

	return &Incident{
		Category:  ProbeFailure,
		Severity:  Warning,
		Namespace: event.Namespace,
		Resource:  "pod/" + event.Name,
		Container: containerFromFieldPath(source.InvolvedObject.FieldPath),
		Title:     fmt.Sprintf("%s probe failing for %s", strings.Title(kind), event.Name),
		Detail: fmt.Sprintf(
			"%d Unhealthy warning(s) in the last %s for pod %q. The %s probe is not "+
				"passing, so Kubernetes considers the pod unable to serve traffic. "+
				"Latest message: %s",
			count, probeFailureWindow, event.Name, kind,
			messageOr(source.Message, "no message")),
		FirstSeen: first,
	}
}

// record adds a failure and returns how many are still inside the window.
func (r *ProbeFailureRule) record(key string, at time.Time) (int, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if at.IsZero() {
		at = time.Now().UTC()
	}

	cutoff := at.Add(-probeFailureWindow)
	kept := make([]time.Time, 0, len(r.failures[key])+1)
	for _, seen := range r.failures[key] {
		if seen.After(cutoff) {
			kept = append(kept, seen)
		}
	}
	kept = append(kept, at)
	r.failures[key] = kept

	return len(kept), kept[0]
}

// probeKind reads which probe failed out of the kubelet's message, since the
// event reason is "Unhealthy" for both and the fix differs: a failing liveness
// probe restarts the container, a failing readiness probe only removes it from
// the service.
func probeKind(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "liveness"):
		return "liveness"
	case strings.Contains(lower, "startup"):
		return "startup"
	case strings.Contains(lower, "readiness"):
		return "readiness"
	}
	return "health"
}

// containerFromFieldPath pulls the container name out of a field path such as
// "spec.containers{api}".
func containerFromFieldPath(fieldPath string) string {
	open := strings.Index(fieldPath, "{")
	closing := strings.Index(fieldPath, "}")
	if open < 0 || closing <= open+1 {
		return ""
	}
	return fieldPath[open+1 : closing]
}
