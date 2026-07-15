package watcher

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
)

// registerEventInformer wires the Event informer into the factory.
//
// Kubernetes Events are the only place some failures are ever explained —
// a failing readiness probe produces a Warning event and nothing else on the
// pod object — so probe detection depends entirely on this informer.
func registerEventInformer(factory informers.SharedInformerFactory, out chan<- WatchEvent) error {
	informer := factory.Core().V1().Events().Informer()

	_, err := informer.AddEventHandler(resourceEventHandler(out, convertEvent))
	if err != nil {
		return fmt.Errorf("registering event informer: %w", err)
	}
	return nil
}

func convertEvent(obj any, eventType EventType) (WatchEvent, bool) {
	event, ok := obj.(*corev1.Event)
	if !ok || event == nil {
		return WatchEvent{}, false
	}

	// Events are addressed to kubelens by their involved object, not by their
	// own generated name, which is a random suffix nobody can correlate.
	name := event.InvolvedObject.Name
	if name == "" {
		name = event.Name
	}
	namespace := event.InvolvedObject.Namespace
	if namespace == "" {
		namespace = event.Namespace
	}

	return WatchEvent{
		Kind:      KindEvent,
		Type:      eventType,
		Namespace: namespace,
		Name:      name,
		Timestamp: eventTimestamp(event),
		Event:     event,
	}, true
}

// eventTimestamp walks the three places Kubernetes records event time, newest
// representation first. LastTimestamp is empty on events written through the
// newer events.k8s.io path, and EventTime is empty on the older core path.
func eventTimestamp(event *corev1.Event) time.Time {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Time
	}
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Time
	}
	return time.Now().UTC()
}

// IsWarning reports whether an event carries the Warning type.
func IsWarning(event *corev1.Event) bool {
	return event != nil && event.Type == corev1.EventTypeWarning
}
