package kcontext

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/watcher"
)

var at = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func sampleIncident() detector.Incident {
	return detector.Incident{
		ID:        "abc123",
		Category:  detector.OOMKilled,
		Severity:  detector.Critical,
		Namespace: "payments",
		Resource:  "pod/payment-api-7d9f",
		Container: "api",
		Title:     "Container api was OOMKilled",
		Detail:    "Container exited with code 137",
	}
}

func samplePodEvent() watcher.WatchEvent {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api-7d9f", Namespace: "payments"},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:  "api",
				Image: "ghcr.io/acme/payment-api:1.4.2",
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("512Mi"),
						corev1.ResourceCPU:    resource.MustParse("500m"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
						Path: "/healthz", Port: intstr.FromInt32(8080),
					}},
					InitialDelaySeconds: 5, PeriodSeconds: 10,
					TimeoutSeconds: 1, FailureThreshold: 3,
				},
			}},
		},
	}
	return watcher.WatchEvent{Kind: watcher.KindPod, Namespace: "payments", Name: pod.Name, Pod: pod}
}

func TestBuildGathersSpecEventsAndLogs(t *testing.T) {
	logs := NewMemoryLogFetcher()
	logs.Set("payments", "payment-api-7d9f", "api", []string{
		"INFO  starting payment-api 1.4.2",
		"WARN  heap usage 91%",
		"FATAL java.lang.OutOfMemoryError: Java heap space",
	})

	events := NewMemoryEventSource()
	events.Set("payments", "pod/payment-api-7d9f", []EventRecord{
		{Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container", Timestamp: at},
	})

	built, err := NewBuilder(logs, events, Options{}).
		Build(context.Background(), sampleIncident(), samplePodEvent())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len(built.Logs) != 3 {
		t.Fatalf("got %d log lines, want 3", len(built.Logs))
	}
	// Line numbers are assigned once, here, because they are what a citation
	// resolves to and what the UI highlights.
	if built.Logs[0].Number != 1 || built.Logs[2].Number != 3 {
		t.Errorf("log lines are not numbered from 1: %+v", built.Logs)
	}
	if len(built.Events) != 1 {
		t.Errorf("got %d events, want 1", len(built.Events))
	}

	// The memory limit is the fact that explains an OOM kill.
	if built.Spec.MemoryLimit != "512Mi" || built.Spec.MemoryRequest != "256Mi" {
		t.Errorf("spec did not carry the memory settings: %+v", built.Spec)
	}
	if built.Spec.Image != "ghcr.io/acme/payment-api:1.4.2" {
		t.Errorf("spec did not carry the image: %+v", built.Spec)
	}
	if !strings.Contains(built.Spec.ReadinessProbe, "/healthz") ||
		!strings.Contains(built.Spec.ReadinessProbe, "failureThreshold=3") {
		t.Errorf("probe description is missing its timing fields: %q", built.Spec.ReadinessProbe)
	}
}

// The output that explains a crash is the tail, not the head: the first forty
// lines of a JVM starting up explain nothing.
func TestBuildKeepsTheTailOfTheLog(t *testing.T) {
	var many []string
	for i := 1; i <= 200; i++ {
		many = append(many, "line "+string(rune('0'+i%10)))
	}
	many[len(many)-1] = "FATAL the actual cause"

	logs := NewMemoryLogFetcher()
	logs.Set("payments", "payment-api-7d9f", "api", many)

	built, err := NewBuilder(logs, nil, Options{LogLines: 10}).
		Build(context.Background(), sampleIncident(), samplePodEvent())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len(built.Logs) != 10 {
		t.Fatalf("got %d lines, want the 10-line budget", len(built.Logs))
	}
	if built.Logs[len(built.Logs)-1].Text != "FATAL the actual cause" {
		t.Errorf("the tail was not kept: %q", built.Logs[len(built.Logs)-1].Text)
	}
	if !built.Truncated {
		t.Error("dropping 190 lines was not recorded as truncation")
	}
}

// One pathological line — a serialised body, a base64 blob — must not consume
// the whole evidence budget.
func TestBuildTruncatesAPathologicalLine(t *testing.T) {
	logs := NewMemoryLogFetcher()
	logs.Set("payments", "payment-api-7d9f", "api", []string{strings.Repeat("x", 5000)})

	built, err := NewBuilder(logs, nil, Options{}).
		Build(context.Background(), sampleIncident(), samplePodEvent())
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len([]rune(built.Logs[0].Text)) > maxLineRunes+32 {
		t.Errorf("a %d-rune line was not truncated", len([]rune(built.Logs[0].Text)))
	}
	if !built.Truncated {
		t.Error("truncating a line was not recorded")
	}
}

// A container that cannot pull its image has never written a line. Refusing to
// explain it would silence the tool exactly when it is needed.
func TestBuildSurvivesMissingLogs(t *testing.T) {
	events := NewMemoryEventSource()
	events.Set("payments", "pod/payment-api-7d9f", []EventRecord{
		{Type: "Warning", Reason: "Failed", Message: "Failed to pull image", Timestamp: at},
	})

	built, err := NewBuilder(NewMemoryLogFetcher(), events, Options{}).
		Build(context.Background(), sampleIncident(), samplePodEvent())
	if err != nil {
		t.Fatalf("Build should degrade, not fail: %v", err)
	}
	if len(built.Logs) != 0 || len(built.Events) != 1 {
		t.Errorf("expected events only, got %d logs and %d events", len(built.Logs), len(built.Events))
	}
}

// With no evidence at all there is nothing to cite, and an explanation would
// have nothing to stand on.
func TestBuildFailsWithNoEvidenceAtAll(t *testing.T) {
	_, err := NewBuilder(NewMemoryLogFetcher(), NewMemoryEventSource(), Options{}).
		Build(context.Background(), sampleIncident(), samplePodEvent())
	if err == nil {
		t.Fatal("expected an error when neither logs nor events are available")
	}
}

// The rendered block is what the model sees. Line numbers must be in the text,
// or a citation cannot be checked against anything.
func TestRenderNumbersTheLogsForCitation(t *testing.T) {
	logs := NewMemoryLogFetcher()
	logs.Set("payments", "payment-api-7d9f", "api", []string{
		"INFO  starting", "FATAL java.lang.OutOfMemoryError: Java heap space",
	})

	built, _ := NewBuilder(logs, nil, Options{}).
		Build(context.Background(), sampleIncident(), samplePodEvent())

	rendered := built.Render()
	for _, want := range []string{
		"INCIDENT", "RESOURCE SPEC", "CONTAINER LOGS",
		"1 | INFO  starting",
		"2 | FATAL java.lang.OutOfMemoryError: Java heap space",
		"512Mi",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered context is missing %q:\n%s", want, rendered)
		}
	}
}

// EvidenceLines is the allowlist the citation checker verifies against. It has
// to be derived from the same struct the model was shown, or the two drift and
// a model gets blamed for citing something it was legitimately given.
func TestEvidenceLinesCoverLogsAndEvents(t *testing.T) {
	logs := NewMemoryLogFetcher()
	logs.Set("payments", "payment-api-7d9f", "api", []string{"FATAL heap exhausted"})
	events := NewMemoryEventSource()
	events.Set("payments", "pod/payment-api-7d9f", []EventRecord{
		{Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container", Timestamp: at},
	})

	built, _ := NewBuilder(logs, events, Options{}).
		Build(context.Background(), sampleIncident(), samplePodEvent())

	lines := built.EvidenceLines()
	if len(lines) != 2 {
		t.Fatalf("got %d evidence lines, want 2", len(lines))
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "FATAL heap exhausted") {
		t.Error("log lines are missing from the allowlist")
	}
	if !strings.Contains(joined, "Back-off restarting failed container") {
		t.Error("event messages are missing from the allowlist")
	}
}

func TestSpecFromDeployment(t *testing.T) {
	replicas := int32(4)
	incident := sampleIncident()
	incident.Resource = "deployment/payment-api"

	event := watcher.WatchEvent{Kind: watcher.KindDeployment, Deploy: deploymentWithReplicas(replicas)}

	spec := specFrom(incident, event)
	if spec.Replicas != "4" {
		t.Errorf("replicas = %q, want 4", spec.Replicas)
	}
	if spec.Image != "ghcr.io/acme/payment-api:1.4.2" {
		t.Errorf("image = %q", spec.Image)
	}
}

func deploymentWithReplicas(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "payment-api", Namespace: "payments"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "api", Image: "ghcr.io/acme/payment-api:1.4.2",
				}},
			}},
		},
	}
}

func TestReadLinesKeepsTheTailAndSurvivesLongLines(t *testing.T) {
	input := strings.Join([]string{"a", "b", "c", "d"}, "\n")

	got, err := readLines(strings.NewReader(input), 2)
	if err != nil {
		t.Fatalf("readLines failed: %v", err)
	}
	if len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Errorf("got %v, want the last two lines", got)
	}

	// A single line beyond bufio's default buffer must not abort the read.
	huge := strings.Repeat("y", 200*1024)
	if _, err := readLines(strings.NewReader(huge), 5); err != nil {
		t.Errorf("a 200KiB line aborted the read: %v", err)
	}
}
