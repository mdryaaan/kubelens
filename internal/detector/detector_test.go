package detector

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mdryaaan/kubelens/internal/watcher"
)

var testClock = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return testClock }

// podEvent wraps a pod in the watch event the rules consume.
func podEvent(pod *corev1.Pod) watcher.WatchEvent {
	return watcher.WatchEvent{
		Kind: watcher.KindPod, Type: watcher.Modified,
		Namespace: pod.Namespace, Name: pod.Name,
		Timestamp: testClock, Pod: pod,
	}
}

func basePod(name string) *corev1.Pod {
	started := metav1.NewTime(testClock.Add(-10 * time.Minute))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "payments",
			CreationTimestamp: metav1.NewTime(testClock.Add(-10 * time.Minute)),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "api",
			Image: "ghcr.io/acme/payment-api:1.4.2",
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &started},
	}
}

func withWaiting(pod *corev1.Pod, reason, message string, restarts int32) *corev1.Pod {
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		Image:        pod.Spec.Containers[0].Image,
		RestartCount: restarts,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: message}},
	}}
	return pod
}

func withTerminated(pod *corev1.Pod, reason string, exitCode int32, restarts int32) *corev1.Pod {
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:         "api",
		Image:        pod.Spec.Containers[0].Image,
		RestartCount: restarts,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason:     reason,
			ExitCode:   exitCode,
			StartedAt:  metav1.NewTime(testClock.Add(-4 * time.Minute)),
			FinishedAt: metav1.NewTime(testClock.Add(-1 * time.Minute)),
		}},
	}}
	return pod
}

func TestOOMKilledRule(t *testing.T) {
	rule := NewOOMKilledRule()

	got := rule.Detect(podEvent(withTerminated(basePod("payment-api-7d9f"), "OOMKilled", 137, 3)))
	if got == nil {
		t.Fatal("an OOMKilled container was not detected")
	}
	if got.Category != OOMKilled || got.Severity != Critical {
		t.Errorf("category/severity = %s/%s", got.Category, got.Severity)
	}
	if got.Container != "api" {
		t.Errorf("container = %q, want api", got.Container)
	}
	// The memory limit is the fixable fact, so it has to reach the detail.
	if !contains(got.Detail, "512Mi") {
		t.Errorf("detail does not carry the memory limit: %s", got.Detail)
	}
	if !contains(got.Detail, "137") {
		t.Errorf("detail does not carry the exit code: %s", got.Detail)
	}

	// A container that exited cleanly is not an incident.
	if got := rule.Detect(podEvent(withTerminated(basePod("clean"), "Completed", 0, 0))); got != nil {
		t.Errorf("a clean exit was reported as OOMKilled: %+v", got)
	}
	if got := rule.Detect(podEvent(basePod("healthy"))); got != nil {
		t.Errorf("a healthy pod was reported: %+v", got)
	}
}

// A crash loop is only an incident while the restart count is still climbing.
// A pod sitting in CrashLoopBackOff after the cause was fixed is waiting out a
// backoff timer, not failing.
func TestCrashLoopRuleRequiresRisingRestarts(t *testing.T) {
	rule := NewCrashLoopRule()

	first := rule.Detect(podEvent(withWaiting(basePod("auth-service-4c1a"), "CrashLoopBackOff", "back-off 5m0s restarting failed container", 4)))
	if first == nil {
		t.Fatal("a climbing crash loop was not detected")
	}
	if first.Category != CrashLoopBackOff {
		t.Errorf("category = %s", first.Category)
	}

	// Same restart count on the next update: the container has not crashed again.
	same := rule.Detect(podEvent(withWaiting(basePod("auth-service-4c1a"), "CrashLoopBackOff", "", 4)))
	if same != nil {
		t.Errorf("a stable restart count was reported as a new crash: %+v", same)
	}

	// One more crash, and it fires again.
	if again := rule.Detect(podEvent(withWaiting(basePod("auth-service-4c1a"), "CrashLoopBackOff", "", 5))); again == nil {
		t.Error("a further restart was not detected")
	}

	// A different waiting reason is not a crash loop.
	if got := rule.Detect(podEvent(withWaiting(basePod("other"), "ContainerCreating", "", 0))); got != nil {
		t.Errorf("ContainerCreating was reported as a crash loop: %+v", got)
	}
}

func TestImagePullRule(t *testing.T) {
	rule := NewImagePullRule()

	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName"} {
		t.Run(reason, func(t *testing.T) {
			pod := withWaiting(basePod("checkout-api-1"), reason, `rpc error: code = NotFound desc = manifest unknown`, 0)
			got := rule.Detect(podEvent(pod))
			if got == nil {
				t.Fatalf("%s was not detected", reason)
			}
			// The image reference is what a human checks for a typo.
			if !contains(got.Detail, "ghcr.io/acme/payment-api:1.4.2") {
				t.Errorf("detail does not carry the image: %s", got.Detail)
			}
		})
	}

	if got := rule.Detect(podEvent(withWaiting(basePod("ok"), "PodInitializing", "", 0))); got != nil {
		t.Errorf("PodInitializing was reported as an image failure: %+v", got)
	}
}

func warningEvent(name, reason, message string, at time.Time) watcher.WatchEvent {
	return watcher.WatchEvent{
		Kind: watcher.KindEvent, Type: watcher.Added,
		Namespace: "payments", Name: name, Timestamp: at,
		Event: &corev1.Event{
			Reason: reason, Message: message, Type: corev1.EventTypeWarning,
			InvolvedObject: corev1.ObjectReference{
				Kind: "Pod", Name: name, Namespace: "payments",
				FieldPath: "spec.containers{api}",
			},
		},
	}
}

// One failed probe is how a slow-starting container reports it is not ready
// yet. Firing on that would raise an incident on every deploy.
func TestProbeFailureRuleNeedsRepeats(t *testing.T) {
	rule := NewProbeFailureRule()
	message := "Readiness probe failed: HTTP probe failed with statuscode: 503"

	for i := 1; i < probeFailureThreshold; i++ {
		if got := rule.Detect(warningEvent("cart-api-9", "Unhealthy", message, testClock)); got != nil {
			t.Fatalf("fired after only %d failure(s): %+v", i, got)
		}
	}

	got := rule.Detect(warningEvent("cart-api-9", "Unhealthy", message, testClock))
	if got == nil {
		t.Fatalf("did not fire after %d failures", probeFailureThreshold)
	}
	if got.Category != ProbeFailure || got.Severity != Warning {
		t.Errorf("category/severity = %s/%s", got.Category, got.Severity)
	}
	if got.Container != "api" {
		t.Errorf("container = %q, want api parsed from the field path", got.Container)
	}
	if !contains(got.Detail, "readiness") {
		t.Errorf("detail does not name which probe failed: %s", got.Detail)
	}

	// Unrelated warnings must not count towards the threshold.
	fresh := NewProbeFailureRule()
	for i := 0; i < probeFailureThreshold+2; i++ {
		if got := fresh.Detect(warningEvent("cart-api-9", "BackOff", "Back-off restarting", testClock)); got != nil {
			t.Fatalf("a non-probe warning was counted: %+v", got)
		}
	}
}

// Failures spread over hours are not the same problem as three in a minute.
func TestProbeFailureRuleForgetsOldFailures(t *testing.T) {
	rule := NewProbeFailureRule()
	message := "Liveness probe failed: connection refused"

	for i := 0; i < probeFailureThreshold+1; i++ {
		at := testClock.Add(time.Duration(i) * 2 * probeFailureWindow)
		if got := rule.Detect(warningEvent("slow-1", "Unhealthy", message, at)); got != nil {
			t.Fatalf("failures %s apart were treated as a burst: %+v", 2*probeFailureWindow, got)
		}
	}
}

func TestPendingTimeoutRule(t *testing.T) {
	rule := NewPendingTimeoutRule(5*time.Minute, fixedNow)

	pending := basePod("reports-worker-2")
	pending.Status.Phase = corev1.PodPending
	pending.CreationTimestamp = metav1.NewTime(testClock.Add(-12 * time.Minute))
	pending.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
		Reason:  "Unschedulable",
		Message: "0/4 nodes are available: 4 Insufficient cpu.",
	}}

	got := rule.Detect(podEvent(pending))
	if got == nil {
		t.Fatal("a long-Pending pod was not detected")
	}
	if !contains(got.Detail, "Insufficient cpu") {
		t.Errorf("detail does not carry the scheduler message: %s", got.Detail)
	}

	// Inside the threshold it is a normal transient state.
	fresh := basePod("reports-worker-3")
	fresh.Status.Phase = corev1.PodPending
	fresh.CreationTimestamp = metav1.NewTime(testClock.Add(-30 * time.Second))
	if got := rule.Detect(podEvent(fresh)); got != nil {
		t.Errorf("a briefly-Pending pod was reported: %+v", got)
	}

	// A Running pod is never this incident, however old it is.
	if got := rule.Detect(podEvent(basePod("running"))); got != nil {
		t.Errorf("a Running pod was reported as Pending: %+v", got)
	}
}

func deploymentEvent(deploy *appsv1.Deployment) watcher.WatchEvent {
	return watcher.WatchEvent{
		Kind: watcher.KindDeployment, Type: watcher.Modified,
		Namespace: deploy.Namespace, Name: deploy.Name,
		Timestamp: testClock, Deploy: deploy,
	}
}

func TestDeploymentFailureRule(t *testing.T) {
	rule := NewDeploymentFailureRule()
	replicas := int32(4)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api", Namespace: "payments"},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 1, UpdatedReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
				Reason:             "ProgressDeadlineExceeded",
				Message:            `ReplicaSet "payment-api-7d9f" has timed out progressing.`,
				LastTransitionTime: metav1.NewTime(testClock.Add(-6 * time.Minute)),
			}},
		},
	}

	got := rule.Detect(deploymentEvent(deploy))
	if got == nil {
		t.Fatal("a stalled rollout was not detected")
	}
	if got.Resource != "deployment/payment-api" {
		t.Errorf("resource = %q", got.Resource)
	}
	if !contains(got.Detail, "ProgressDeadlineExceeded") || !contains(got.Detail, "1 of 4") {
		t.Errorf("detail is missing the rollout state: %s", got.Detail)
	}

	// A healthy rollout reports Progressing=True.
	deploy.Status.Conditions[0].Status = corev1.ConditionTrue
	if got := rule.Detect(deploymentEvent(deploy)); got != nil {
		t.Errorf("a progressing rollout was reported as failed: %+v", got)
	}
}

// The same crash loop produces informer updates continuously. Without a
// cooldown that is one incident per update, and one inference per update.
func TestEngineCooldownCollapsesRepeats(t *testing.T) {
	now := testClock
	opts := DefaultOptions()
	opts.Now = func() time.Time { return now }
	engine := NewEngine(opts)

	first := engine.Process(podEvent(withTerminated(basePod("payment-api-7d9f"), "OOMKilled", 137, 1)))
	if len(first) != 1 {
		t.Fatalf("got %d incidents, want 1", len(first))
	}

	now = now.Add(30 * time.Second)
	if repeat := engine.Process(podEvent(withTerminated(basePod("payment-api-7d9f"), "OOMKilled", 137, 2))); len(repeat) != 0 {
		t.Fatalf("a repeat inside the cooldown was emitted: %+v", repeat)
	}

	now = now.Add(opts.Cooldown)
	after := engine.Process(podEvent(withTerminated(basePod("payment-api-7d9f"), "OOMKilled", 137, 3)))
	if len(after) != 1 {
		t.Fatalf("got %d incidents after the cooldown, want 1", len(after))
	}
	// It is the same incident, seen again — not a new one.
	if after[0].ID != first[0].ID {
		t.Errorf("the repeat got a new id: %s vs %s", after[0].ID, first[0].ID)
	}
	if after[0].Count < 2 {
		t.Errorf("count = %d, want it to accumulate", after[0].Count)
	}
}

// An OOMKilled container enters CrashLoopBackOff moments later. Reporting both
// is technically true and practically useless — the memory limit is the fix.
func TestEngineReportsTheMostSpecificCause(t *testing.T) {
	engine := NewEngine(DefaultOptions())

	pod := withTerminated(basePod("payment-api-7d9f"), "OOMKilled", 137, 3)
	pod.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{
		Reason: "CrashLoopBackOff", Message: "back-off 5m0s",
	}

	got := engine.Process(podEvent(pod))
	if len(got) != 1 {
		t.Fatalf("got %d incidents, want exactly 1", len(got))
	}
	if got[0].Category != OOMKilled {
		t.Errorf("category = %s, want OOMKilled to win over the crash loop", got[0].Category)
	}
}

func TestEngineActiveAndResolve(t *testing.T) {
	engine := NewEngine(DefaultOptions())
	engine.Process(podEvent(withTerminated(basePod("payment-api-7d9f"), "OOMKilled", 137, 1)))

	active := engine.Active()
	if len(active) != 1 {
		t.Fatalf("got %d active incidents, want 1", len(active))
	}

	if !engine.Resolve(active[0].Fingerprint) {
		t.Fatal("Resolve reported no change for a known fingerprint")
	}
	if len(engine.Active()) != 0 {
		t.Error("a resolved incident is still active")
	}
	if engine.Resolve("nonexistent") {
		t.Error("Resolve claimed to resolve an unknown fingerprint")
	}
}

func TestParseCategory(t *testing.T) {
	for _, in := range []string{"OOMKilled", "oomkilled", "oom_killed", " OOM-Killed "} {
		got, err := ParseCategory(in)
		if err != nil || got != OOMKilled {
			t.Errorf("ParseCategory(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseCategory("DiskFull"); err == nil {
		t.Error("expected an error for an unknown category")
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	a := Fingerprint(OOMKilled, "payments", "pod/api-1", "api")
	if a != Fingerprint(OOMKilled, "payments", "pod/api-1", "api") {
		t.Error("the same condition produced two fingerprints")
	}
	if a == Fingerprint(CrashLoopBackOff, "payments", "pod/api-1", "api") {
		t.Error("different categories collided")
	}
	if a == Fingerprint(OOMKilled, "payments", "pod/api-2", "api") {
		t.Error("different pods collided")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && stringsContains(haystack, needle)
}

func stringsContains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}

// Detection latency is only meaningful for conditions kubelens saw begin.
func TestEngineMarksPreExistingConditions(t *testing.T) {
	opts := DefaultOptions()
	opts.Now = fixedNow
	engine := NewEngine(opts)

	// The container was killed a minute before this engine started watching.
	old := withTerminated(basePod("payment-api-old"), "OOMKilled", 137, 1)
	old.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt =
		metav1.NewTime(testClock.Add(-time.Minute))

	got := engine.Process(podEvent(old))
	if len(got) != 1 {
		t.Fatalf("got %d incidents, want 1", len(got))
	}
	if !got[0].PreExisting {
		t.Error("a condition that began before the engine started was not marked pre-existing")
	}

	// One that begins after kubelens is watching is not.
	fresh := withTerminated(basePod("payment-api-fresh"), "OOMKilled", 137, 1)
	fresh.Status.ContainerStatuses[0].LastTerminationState.Terminated.FinishedAt =
		metav1.NewTime(testClock.Add(time.Second))

	got = engine.Process(podEvent(fresh))
	if len(got) != 1 {
		t.Fatalf("got %d incidents, want 1", len(got))
	}
	if got[0].PreExisting {
		t.Error("a condition that began after start was marked pre-existing")
	}
}

// The crash loop's current cycle began at the last exit, not at pod start —
// otherwise detection latency measures how long the workload has been running.
func TestCrashLoopDatesTheIncidentFromTheLastExit(t *testing.T) {
	pod := withWaiting(basePod("auth-service-1"), "CrashLoopBackOff", "back-off", 3)
	crashedAt := testClock.Add(-45 * time.Second)
	pod.Status.ContainerStatuses[0].LastTerminationState = corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			Reason:     "Error",
			ExitCode:   1,
			StartedAt:  metav1.NewTime(testClock.Add(-5 * time.Minute)),
			FinishedAt: metav1.NewTime(crashedAt),
		},
	}

	got := NewCrashLoopRule().Detect(podEvent(pod))
	if got == nil {
		t.Fatal("no incident")
	}
	if !got.FirstSeen.Equal(crashedAt) {
		t.Errorf("first seen = %v, want the last exit at %v", got.FirstSeen, crashedAt)
	}
}
