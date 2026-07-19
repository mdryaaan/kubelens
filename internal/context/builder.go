// Package kcontext assembles the bounded evidence bundle that an incident
// explanation is allowed to draw on.
//
// The package lives in internal/context but is named kcontext so that files in
// it can still import the standard library's context package, which they need
// for cancellation.
package kcontext

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/watcher"
)

// Defaults for how much evidence one incident may carry.
//
// These are limits, not targets. Everything gathered here ends up in a model
// prompt, and an unbounded dump costs tokens, buries the relevant line, and
// makes the citation check meaningless because everything is "present".
const (
	DefaultLogLines   = 40
	DefaultEventLimit = 10
	// maxLineRunes truncates a single pathological log line — a serialised
	// request body, a base64 blob — so one line cannot consume the whole budget.
	maxLineRunes = 400
)

// LogLine is one line of container output with the position it came from.
//
// The number is what the UI highlights and what a citation resolves to, so it
// is assigned here, once, rather than being recomputed by every consumer.
type LogLine struct {
	Number int    `json:"number"`
	Text   string `json:"text"`
}

// EventRecord is one Kubernetes event, flattened to what an explanation needs.
type EventRecord struct {
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	Timestamp time.Time `json:"timestamp"`
}

// ResourceSpec is the slice of the workload spec that explains failures.
//
// Limits and requests explain OOM kills and unschedulable pods, the image
// explains pull failures, and the probe settings explain readiness flapping.
// Nothing else from the spec is included, because nothing else answers a
// question these six categories raise.
type ResourceSpec struct {
	Image          string `json:"image,omitempty"`
	MemoryLimit    string `json:"memory_limit,omitempty"`
	MemoryRequest  string `json:"memory_request,omitempty"`
	CPULimit       string `json:"cpu_limit,omitempty"`
	CPURequest     string `json:"cpu_request,omitempty"`
	LivenessProbe  string `json:"liveness_probe,omitempty"`
	ReadinessProbe string `json:"readiness_probe,omitempty"`
	RestartPolicy  string `json:"restart_policy,omitempty"`
	Replicas       string `json:"replicas,omitempty"`
}

// IncidentContext is everything the explanation engine may look at.
//
// This is the single source of truth for citation verification: a claim is
// admissible only if it quotes something in Logs or Events. Nothing outside
// this struct reaches the model, so nothing outside it can be legitimately
// cited.
type IncidentContext struct {
	Incident detector.Incident `json:"incident"`
	Spec     ResourceSpec      `json:"spec"`
	Events   []EventRecord     `json:"events"`
	Logs     []LogLine         `json:"logs"`
	// Truncated records that evidence was dropped to fit the budget, so a
	// reader knows the absence of a line is not proof it did not happen.
	Truncated bool `json:"truncated"`
}

// LogFetcher retrieves recent container output.
type LogFetcher interface {
	// Fetch returns up to lines of recent output for a container.
	Fetch(ctx context.Context, namespace, pod, container string, lines int) ([]string, error)
}

// EventSource retrieves recent Kubernetes events for a resource.
type EventSource interface {
	// Recent returns up to limit events concerning a namespaced resource.
	Recent(ctx context.Context, namespace, resource string, limit int) ([]EventRecord, error)
}

// Options bounds what a builder gathers.
type Options struct {
	LogLines   int
	EventLimit int
}

// Builder assembles incident contexts.
type Builder struct {
	logs   LogFetcher
	events EventSource
	opts   Options
}

// NewBuilder wires a builder over its sources. Either source may be nil, which
// simply means that kind of evidence is unavailable — an explanation from
// fewer sources is degraded, not invalid.
func NewBuilder(logs LogFetcher, events EventSource, opts Options) *Builder {
	if opts.LogLines <= 0 {
		opts.LogLines = DefaultLogLines
	}
	if opts.EventLimit <= 0 {
		opts.EventLimit = DefaultEventLimit
	}
	return &Builder{logs: logs, events: events, opts: opts}
}

// Build gathers the evidence for one incident.
//
// A failure in either source is not fatal. Logs are frequently unavailable for
// exactly the pods that matter most — a container that cannot pull its image
// has never produced a line — and refusing to explain an incident because its
// logs are missing would silence the tool precisely when it is needed.
func (b *Builder) Build(ctx context.Context, incident detector.Incident, event watcher.WatchEvent) (IncidentContext, error) {
	out := IncidentContext{
		Incident: incident,
		Spec:     specFrom(incident, event),
	}

	podName := strings.TrimPrefix(incident.Resource, "pod/")

	if b.events != nil {
		events, err := b.events.Recent(ctx, incident.Namespace, incident.Resource, b.opts.EventLimit)
		if err == nil {
			out.Events = trimEvents(events, b.opts.EventLimit)
		}
	}

	if b.logs != nil && strings.HasPrefix(incident.Resource, "pod/") {
		// One more line than the budget is requested so the builder can tell
		// "this is all the output there was" from "this is the tail of more".
		// Whether evidence was dropped is something the reader needs to know:
		// the absence of a line is otherwise indistinguishable from proof that
		// it never happened.
		raw, err := b.logs.Fetch(ctx, incident.Namespace, podName, incident.Container, b.opts.LogLines+1)
		if err == nil {
			out.Logs, out.Truncated = numberLines(raw, b.opts.LogLines)
		}
	}

	if len(out.Logs) == 0 && len(out.Events) == 0 {
		return out, fmt.Errorf("no evidence available for %s: neither logs nor events could be read",
			incident.Resource)
	}
	return out, nil
}

// numberLines keeps the last n lines and numbers them from 1.
//
// The tail rather than the head: a container that crashed wrote the reason
// immediately before dying, and the first forty lines of a JVM starting up
// explain nothing.
func numberLines(raw []string, limit int) ([]LogLine, bool) {
	truncated := false
	if len(raw) > limit {
		raw = raw[len(raw)-limit:]
		truncated = true
	}

	out := make([]LogLine, 0, len(raw))
	for i, text := range raw {
		text = strings.TrimRight(text, "\r\n")
		if runes := []rune(text); len(runes) > maxLineRunes {
			text = string(runes[:maxLineRunes]) + " …[truncated]"
			truncated = true
		}
		out = append(out, LogLine{Number: i + 1, Text: text})
	}
	return out, truncated
}

// trimEvents keeps the most recent events, newest last so the bundle reads
// chronologically.
func trimEvents(events []EventRecord, limit int) []EventRecord {
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events
}

// specFrom extracts the spec fields that explain this category of failure.
func specFrom(incident detector.Incident, event watcher.WatchEvent) ResourceSpec {
	var spec ResourceSpec

	if event.Deploy != nil {
		if event.Deploy.Spec.Replicas != nil {
			spec.Replicas = fmt.Sprint(*event.Deploy.Spec.Replicas)
		}
		if len(event.Deploy.Spec.Template.Spec.Containers) > 0 {
			applyContainer(&spec, &event.Deploy.Spec.Template.Spec.Containers[0])
		}
		return spec
	}

	pod := event.Pod
	if pod == nil {
		return spec
	}
	spec.RestartPolicy = string(pod.Spec.RestartPolicy)

	container := watcher.ContainerSpec(pod, incident.Container)
	if container == nil && len(pod.Spec.Containers) > 0 {
		container = &pod.Spec.Containers[0]
	}
	if container != nil {
		applyContainer(&spec, container)
	}
	return spec
}

func applyContainer(spec *ResourceSpec, container *corev1.Container) {
	spec.Image = container.Image

	if quantity, ok := container.Resources.Limits[corev1.ResourceMemory]; ok {
		spec.MemoryLimit = quantity.String()
	}
	if quantity, ok := container.Resources.Requests[corev1.ResourceMemory]; ok {
		spec.MemoryRequest = quantity.String()
	}
	if quantity, ok := container.Resources.Limits[corev1.ResourceCPU]; ok {
		spec.CPULimit = quantity.String()
	}
	if quantity, ok := container.Resources.Requests[corev1.ResourceCPU]; ok {
		spec.CPURequest = quantity.String()
	}

	spec.LivenessProbe = describeProbe(container.LivenessProbe)
	spec.ReadinessProbe = describeProbe(container.ReadinessProbe)
}

// describeProbe renders a probe as one line, including the timing fields that
// are usually the actual cause of a flapping readiness check.
func describeProbe(probe *corev1.Probe) string {
	if probe == nil {
		return ""
	}

	var target string
	switch {
	case probe.HTTPGet != nil:
		target = fmt.Sprintf("HTTP GET %s:%s", probe.HTTPGet.Path, probe.HTTPGet.Port.String())
	case probe.TCPSocket != nil:
		target = "TCP " + probe.TCPSocket.Port.String()
	case probe.Exec != nil:
		target = "exec " + strings.Join(probe.Exec.Command, " ")
	default:
		target = "unknown probe type"
	}

	return fmt.Sprintf("%s (initialDelay=%ds period=%ds timeout=%ds failureThreshold=%d)",
		target, probe.InitialDelaySeconds, probe.PeriodSeconds,
		probe.TimeoutSeconds, probe.FailureThreshold)
}

// Render turns the context into the plain-text block a model is shown.
//
// The exact format matters: line numbers are printed alongside the text so the
// model can cite them, and the two evidence sections are labelled so a citation
// can be traced back to which source it came from.
func (c IncidentContext) Render() string {
	var b strings.Builder

	fmt.Fprintf(&b, "INCIDENT\n")
	fmt.Fprintf(&b, "  category (detected by rule): %s\n", c.Incident.Category)
	fmt.Fprintf(&b, "  namespace: %s\n", c.Incident.Namespace)
	fmt.Fprintf(&b, "  resource: %s\n", c.Incident.Resource)
	if c.Incident.Container != "" {
		fmt.Fprintf(&b, "  container: %s\n", c.Incident.Container)
	}
	fmt.Fprintf(&b, "  what the rule observed: %s\n", c.Incident.Detail)

	fmt.Fprintf(&b, "\nRESOURCE SPEC\n")
	for _, field := range c.Spec.fields() {
		fmt.Fprintf(&b, "  %s: %s\n", field[0], field[1])
	}

	fmt.Fprintf(&b, "\nKUBERNETES EVENTS\n")
	if len(c.Events) == 0 {
		fmt.Fprintf(&b, "  (none available)\n")
	}
	for _, event := range c.Events {
		fmt.Fprintf(&b, "  [%s] %s: %s\n", event.Type, event.Reason, event.Message)
	}

	fmt.Fprintf(&b, "\nCONTAINER LOGS (cite these by line number)\n")
	if len(c.Logs) == 0 {
		fmt.Fprintf(&b, "  (none available)\n")
	}
	for _, line := range c.Logs {
		fmt.Fprintf(&b, "  %d | %s\n", line.Number, line.Text)
	}

	if c.Truncated {
		fmt.Fprintf(&b, "\n(evidence was truncated to fit the context budget)\n")
	}

	return b.String()
}

// fields renders the populated spec fields in a stable order.
func (s ResourceSpec) fields() [][2]string {
	candidates := [][2]string{
		{"image", s.Image},
		{"memory limit", s.MemoryLimit},
		{"memory request", s.MemoryRequest},
		{"cpu limit", s.CPULimit},
		{"cpu request", s.CPURequest},
		{"liveness probe", s.LivenessProbe},
		{"readiness probe", s.ReadinessProbe},
		{"restart policy", s.RestartPolicy},
		{"replicas", s.Replicas},
	}

	out := make([][2]string, 0, len(candidates))
	for _, field := range candidates {
		if field[1] != "" {
			out = append(out, field)
		}
	}
	if len(out) == 0 {
		out = append(out, [2]string{"(no spec fields available)", "-"})
	}
	return out
}

// EvidenceLines returns every line a citation may legitimately quote.
//
// This is the allowlist the citation checker verifies against, and it is
// derived from the same struct the model was shown — so the two can never drift
// apart into a model being blamed for citing something it was legitimately given.
func (c IncidentContext) EvidenceLines() []string {
	out := make([]string, 0, len(c.Logs)+len(c.Events))
	for _, line := range c.Logs {
		out = append(out, line.Text)
	}
	for _, event := range c.Events {
		out = append(out, event.Message)
	}
	return out
}
