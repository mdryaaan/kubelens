package detector

import (
	"fmt"
	"sync"
	"time"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// CrashLoopRule detects containers restarting repeatedly.
type CrashLoopRule struct {
	mu       sync.Mutex
	restarts map[string]int32
}

// NewCrashLoopRule builds the rule.
func NewCrashLoopRule() *CrashLoopRule {
	return &CrashLoopRule{restarts: make(map[string]int32)}
}

// Category is the failure pattern this rule detects.
func (r *CrashLoopRule) Category() Category { return CrashLoopBackOff }

// Describe explains what the rule looks for.
func (r *CrashLoopRule) Describe() string {
	return "A container is waiting in CrashLoopBackOff and its restart count is still climbing."
}

// Detect flags a container in CrashLoopBackOff whose restart count is rising.
//
// Both halves matter. A container can sit in CrashLoopBackOff for hours after
// someone has already fixed the cause, because the backoff timer keeps it in
// that state; reporting on the reason alone would raise an incident for a pod
// that is merely waiting to recover. The rising restart count is what
// distinguishes "still failing" from "waiting to retry".
func (r *CrashLoopRule) Detect(event watcher.WatchEvent) *Incident {
	if event.Kind != watcher.KindPod || event.Pod == nil {
		return nil
	}

	pod := event.Pod
	if event.Type == watcher.Deleted {
		r.forget(pod.Namespace + "/" + pod.Name)
		return nil
	}

	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]

		waiting := status.State.Waiting
		if waiting == nil || waiting.Reason != "CrashLoopBackOff" {
			continue
		}

		key := pod.Namespace + "/" + pod.Name + "/" + status.Name
		if !r.restartsIncreased(key, status.RestartCount) {
			continue
		}

		exit := "unknown exit code"
		if terminated := lastTerminated(status); terminated != nil {
			exit = fmt.Sprintf("last exit code %d", terminated.ExitCode)
			if terminated.Reason != "" {
				exit += " (" + terminated.Reason + ")"
			}
		}

		return &Incident{
			Category:  CrashLoopBackOff,
			Severity:  Critical,
			Namespace: pod.Namespace,
			Resource:  "pod/" + pod.Name,
			Container: status.Name,
			Title: fmt.Sprintf("Container %s is crash looping in %s",
				status.Name, pod.Name),
			Detail: fmt.Sprintf(
				"Container %q is in CrashLoopBackOff with %d restart(s) and %s. "+
					"kubelens saw the restart count increase, so the container is still "+
					"failing rather than waiting out a backoff from an already-fixed problem. "+
					"Kubelet reported: %s",
				status.Name, status.RestartCount, exit, messageOr(waiting.Message, "no message")),
			FirstSeen: podTimestamp(event),
		}
	}

	return nil
}

// restartsIncreased reports whether the count moved since the last observation.
//
// The first sighting counts as an increase: kubelens may have started while the
// pod was already looping, and staying silent until the next restart would mean
// missing a broken workload for as long as its backoff lasts.
func (r *CrashLoopRule) restartsIncreased(key string, count int32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous, seen := r.restarts[key]
	r.restarts[key] = count

	if !seen {
		return count > 0
	}
	return count > previous
}

func (r *CrashLoopRule) forget(podKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key := range r.restarts {
		if len(key) > len(podKey) && key[:len(podKey)+1] == podKey+"/" {
			delete(r.restarts, key)
		}
	}
}

func messageOr(message, fallback string) string {
	if message == "" {
		return fallback
	}
	return message
}

// podTimestamp prefers the pod's start time, so an incident is dated by when
// the workload began failing rather than by when the informer fired.
func podTimestamp(event watcher.WatchEvent) time.Time {
	if event.Pod != nil && event.Pod.Status.StartTime != nil && !event.Pod.Status.StartTime.IsZero() {
		return event.Pod.Status.StartTime.Time
	}
	return event.Timestamp
}
