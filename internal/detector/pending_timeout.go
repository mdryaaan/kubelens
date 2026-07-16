package detector

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// PendingTimeoutRule detects pods that never got scheduled.
type PendingTimeoutRule struct {
	threshold time.Duration
	now       func() time.Time
}

// NewPendingTimeoutRule builds the rule with a threshold and a clock.
func NewPendingTimeoutRule(threshold time.Duration, now func() time.Time) *PendingTimeoutRule {
	if threshold <= 0 {
		threshold = 5 * time.Minute
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &PendingTimeoutRule{threshold: threshold, now: now}
}

// Category is the failure pattern this rule detects.
func (r *PendingTimeoutRule) Category() Category { return PendingTimeout }

// Describe explains what the rule looks for.
func (r *PendingTimeoutRule) Describe() string {
	return fmt.Sprintf("A pod has been in the Pending phase for longer than %s.", r.threshold)
}

// Detect flags a pod that has been Pending too long.
//
// Pending is a normal transient state — every pod passes through it — so the
// only thing that makes it an incident is duration. The threshold is
// configurable because what counts as too long depends on the cluster: a
// node-autoscaling cluster legitimately parks pods for minutes while capacity
// arrives, and a fixed-size one does not.
func (r *PendingTimeoutRule) Detect(event watcher.WatchEvent) *Incident {
	if event.Kind != watcher.KindPod || event.Pod == nil || event.Type == watcher.Deleted {
		return nil
	}

	pod := event.Pod
	if pod.Status.Phase != corev1.PodPending {
		return nil
	}

	since := pod.CreationTimestamp.Time
	if since.IsZero() {
		return nil
	}

	waited := r.now().Sub(since)
	if waited < r.threshold {
		return nil
	}

	reason, message := pendingReason(pod)

	return &Incident{
		Category:  PendingTimeout,
		Severity:  Warning,
		Namespace: pod.Namespace,
		Resource:  "pod/" + pod.Name,
		Title: fmt.Sprintf("Pod %s has been Pending for %s",
			pod.Name, waited.Round(time.Second)),
		Detail: fmt.Sprintf(
			"Pod %q has been in the Pending phase for %s, past the %s threshold, and has "+
				"not been scheduled onto a node. Scheduler reason: %s. %s",
			pod.Name, waited.Round(time.Second), r.threshold, reason,
			messageOr(message, "No scheduler message was recorded.")),
		FirstSeen: since,
	}
}

// pendingReason reads why the scheduler could not place the pod. The
// PodScheduled=False condition carries the real answer — insufficient CPU, no
// matching node selector, an unbound volume claim — and it is the single most
// useful field for this category.
func pendingReason(pod *corev1.Pod) (string, string) {
	for i := range pod.Status.Conditions {
		condition := &pod.Status.Conditions[i]
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			reason := condition.Reason
			if reason == "" {
				reason = "Unschedulable"
			}
			return reason, condition.Message
		}
	}

	// A pod whose containers are all waiting on image pulls is Pending too, but
	// for a reason the image rule owns; say so rather than reporting "unknown".
	for i := range pod.Status.ContainerStatuses {
		if waiting := pod.Status.ContainerStatuses[i].State.Waiting; waiting != nil {
			return waiting.Reason, waiting.Message
		}
	}

	return "Unknown", strings.TrimSpace(pod.Status.Message)
}
