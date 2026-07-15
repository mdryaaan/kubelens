package watcher

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
)

// registerPodInformer wires the Pod informer into the factory.
//
// Pods carry the container statuses that four of the six detectors read:
// restart counts, waiting reasons, and last-terminated state. This is the
// highest-volume informer in the set, which is why the handler does no work
// beyond converting and emitting.
func registerPodInformer(factory informers.SharedInformerFactory, out chan<- WatchEvent) error {
	informer := factory.Core().V1().Pods().Informer()

	_, err := informer.AddEventHandler(resourceEventHandler(out, convertPod))
	if err != nil {
		return fmt.Errorf("registering pod informer: %w", err)
	}
	return nil
}

func convertPod(obj any, eventType EventType) (WatchEvent, bool) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod == nil {
		return WatchEvent{}, false
	}

	return WatchEvent{
		Kind:      KindPod,
		Type:      eventType,
		Namespace: pod.Namespace,
		Name:      pod.Name,
		Timestamp: podTimestamp(pod),
		Pod:       pod,
	}, true
}

// podTimestamp prefers the pod's own start time over wall-clock now, so an
// incident is dated by when the cluster changed rather than by when kubelens
// happened to observe it.
func podTimestamp(pod *corev1.Pod) time.Time {
	if pod.Status.StartTime != nil && !pod.Status.StartTime.IsZero() {
		return pod.Status.StartTime.Time
	}
	if !pod.CreationTimestamp.IsZero() {
		return pod.CreationTimestamp.Time
	}
	return time.Now().UTC()
}

// ContainerStatus finds a container status by name.
func ContainerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	if pod == nil {
		return nil
	}
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

// ContainerSpec finds a container spec by name, which is where the resource
// limits and probe definitions live that an explanation needs to be actionable.
func ContainerSpec(pod *corev1.Pod, name string) *corev1.Container {
	if pod == nil {
		return nil
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}
