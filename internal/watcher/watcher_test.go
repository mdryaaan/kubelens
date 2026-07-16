package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestClusterWatcherStreamsPodEvents(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api-7d9f", Namespace: "payments"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out := make(chan WatchEvent, 16)
	// The error is collected rather than asserted inside the goroutine: a
	// t.Errorf after the test function returns panics the whole run.
	errc := make(chan error, 1)
	go func() { errc <- NewClusterWatcher(client, Options{}).Run(ctx, out) }()

	select {
	case event := <-out:
		if event.Kind != KindPod {
			t.Errorf("kind = %s, want Pod", event.Kind)
		}
		if event.Key() != "payments/payment-api-7d9f" {
			t.Errorf("key = %q", event.Key())
		}
		if event.Pod == nil {
			t.Error("the typed pod was not carried on the event")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for a pod event")
	}

	cancel()
	if err := <-errc; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("Run failed: %v", err)
	}
}

// The channel must close when the context is cancelled, or every consumer
// leaks a goroutine blocked on a receive.
func TestClusterWatcherClosesTheChannelOnCancel(t *testing.T) {
	client := fake.NewSimpleClientset()

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan WatchEvent, 4)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = NewClusterWatcher(client, Options{}).Run(ctx, out)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	// Draining a closed channel returns immediately with ok == false.
	for range out {
	}
}

// Informer callbacks run on a shared processor goroutine. Blocking one stalls
// every other handler and eventually the whole cache, so a full buffer must
// drop rather than wait.
func TestEmitDropsRatherThanBlocking(t *testing.T) {
	full := make(chan WatchEvent, 1)
	full <- WatchEvent{Name: "first"}

	done := make(chan struct{})
	go func() {
		emit(full, WatchEvent{Name: "second"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("emit blocked on a full channel")
	}
}

func TestConvertPod(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC))
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "shop"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "api", Image: "api:1"}}},
		Status: corev1.PodStatus{
			StartTime:         &started,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "api", RestartCount: 2}},
		},
	}

	event, ok := convertPod(pod, Modified)
	if !ok {
		t.Fatal("a valid pod was rejected")
	}
	if !event.Timestamp.Equal(started.Time) {
		t.Errorf("timestamp = %v, want the pod start time %v", event.Timestamp, started.Time)
	}

	if status := ContainerStatus(pod, "api"); status == nil || status.RestartCount != 2 {
		t.Errorf("ContainerStatus did not find the container: %+v", status)
	}
	if ContainerStatus(pod, "missing") != nil {
		t.Error("ContainerStatus invented a container")
	}
	if spec := ContainerSpec(pod, "api"); spec == nil || spec.Image != "api:1" {
		t.Errorf("ContainerSpec did not find the container: %+v", spec)
	}

	if _, ok := convertPod("not a pod", Added); ok {
		t.Error("a non-pod object was converted")
	}
}

// Events are addressed by their involved object; their own name is a random
// suffix nothing can correlate.
func TestConvertEventUsesTheInvolvedObject(t *testing.T) {
	last := metav1.NewTime(time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC))
	source := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "cart-api-9.17f2a", Namespace: "shop"},
		Reason:         "Unhealthy",
		Type:           corev1.EventTypeWarning,
		LastTimestamp:  last,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "cart-api-9", Namespace: "shop"},
	}

	event, ok := convertEvent(source, Added)
	if !ok {
		t.Fatal("a valid event was rejected")
	}
	if event.Name != "cart-api-9" {
		t.Errorf("name = %q, want the involved object", event.Name)
	}
	if !event.Timestamp.Equal(last.Time) {
		t.Errorf("timestamp = %v, want %v", event.Timestamp, last.Time)
	}
	if !IsWarning(source) {
		t.Error("a Warning event was not recognised")
	}
}

// EventTime is empty on the old core path and LastTimestamp on the newer one,
// so both have to be read or half of all events get dated to now.
func TestEventTimestampFallsBack(t *testing.T) {
	at := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)

	onlyEventTime := &corev1.Event{EventTime: metav1.NewMicroTime(at)}
	if got := eventTimestamp(onlyEventTime); !got.Equal(at) {
		t.Errorf("EventTime was not used: %v", got)
	}

	onlyFirst := &corev1.Event{FirstTimestamp: metav1.NewTime(at)}
	if got := eventTimestamp(onlyFirst); !got.Equal(at) {
		t.Errorf("FirstTimestamp was not used: %v", got)
	}

	if got := eventTimestamp(&corev1.Event{}); got.IsZero() {
		t.Error("an event with no timestamps got a zero time rather than now")
	}
}

func TestConvertDeployment(t *testing.T) {
	transition := metav1.NewTime(time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api", Namespace: "payments"},
		Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
			LastTransitionTime: transition,
		}}},
	}

	event, ok := convertDeployment(deploy, Modified)
	if !ok {
		t.Fatal("a valid deployment was rejected")
	}
	if !event.Timestamp.Equal(transition.Time) {
		t.Errorf("timestamp = %v, want the condition transition %v", event.Timestamp, transition.Time)
	}
	if DeploymentCondition(deploy, appsv1.DeploymentProgressing) == nil {
		t.Error("DeploymentCondition did not find Progressing")
	}
	if DeploymentCondition(deploy, appsv1.DeploymentAvailable) != nil {
		t.Error("DeploymentCondition invented a condition")
	}
}
