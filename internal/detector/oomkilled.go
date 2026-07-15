package detector

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

// OOMKilledRule detects containers the kernel killed for exceeding memory.
type OOMKilledRule struct{}

// NewOOMKilledRule builds the rule.
func NewOOMKilledRule() *OOMKilledRule { return &OOMKilledRule{} }

// Category is the failure pattern this rule detects.
func (r *OOMKilledRule) Category() Category { return OOMKilled }

// Describe explains what the rule looks for.
func (r *OOMKilledRule) Describe() string {
	return "A container terminated with reason OOMKilled — the kernel killed it for exceeding its memory limit."
}

// Detect flags a container whose last termination was an OOM kill.
//
// This rule runs before the crash-loop rule because an OOM-killed container
// enters CrashLoopBackOff moments later, and the memory limit is the fixable
// cause while the crash loop is only its symptom.
func (r *OOMKilledRule) Detect(event watcher.WatchEvent) *Incident {
	if event.Kind != watcher.KindPod || event.Pod == nil || event.Type == watcher.Deleted {
		return nil
	}

	pod := event.Pod
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]

		terminated := lastTerminated(status)
		if terminated == nil || terminated.Reason != "OOMKilled" {
			continue
		}

		limit := "no memory limit set"
		if spec := watcher.ContainerSpec(pod, status.Name); spec != nil {
			if quantity, ok := spec.Resources.Limits[corev1.ResourceMemory]; ok {
				limit = "memory limit " + quantity.String()
			}
		}

		return &Incident{
			Category:  OOMKilled,
			Severity:  Critical,
			Namespace: pod.Namespace,
			Resource:  "pod/" + pod.Name,
			Container: status.Name,
			Title: fmt.Sprintf("Container %s was OOMKilled in %s",
				status.Name, pod.Name),
			Detail: fmt.Sprintf(
				"Container %q exited with code %d, reason OOMKilled, after %d restart(s). "+
					"The container has %s. The kernel terminated the process because it "+
					"requested more memory than the cgroup allowed.",
				status.Name, terminated.ExitCode, status.RestartCount, limit),
			FirstSeen:  terminated.StartedAt.Time,
			DetectedAt: terminated.FinishedAt.Time,
		}
	}

	return nil
}

// lastTerminated returns the most recent termination for a container, whether
// it ended the current run or the previous one.
//
// Both are checked because the timing of the informer update decides which
// field holds the OOM: catch the pod while it is restarting and the kill is in
// LastTerminationState; catch it after the container has exited for good and it
// is in State.
func lastTerminated(status *corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if status == nil {
		return nil
	}
	if status.LastTerminationState.Terminated != nil {
		return status.LastTerminationState.Terminated
	}
	if status.State.Terminated != nil {
		return status.State.Terminated
	}
	return nil
}
