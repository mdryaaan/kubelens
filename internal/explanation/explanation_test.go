package explanation

import (
	"context"
	"errors"
	"strings"
	"testing"

	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/llm"
)

// stubProvider returns a scripted explanation, so engine behaviour can be
// tested without a daemon and without the baseline's own heuristics.
type stubProvider struct {
	explanation llm.Explanation
	err         error
	name        string
	calls       int
	last        llm.Request
}

func (s *stubProvider) Name() string {
	if s.name != "" {
		return s.name
	}
	return "stub"
}
func (s *stubProvider) Model() string { return "stub-model" }
func (s *stubProvider) Explain(_ context.Context, req llm.Request) (llm.Explanation, error) {
	s.calls++
	s.last = req
	return s.explanation, s.err
}

func sampleBundle() kcontext.IncidentContext {
	return kcontext.IncidentContext{
		Incident: detector.Incident{
			ID: "abc123", Category: detector.OOMKilled, Severity: detector.Critical,
			Namespace: "payments", Resource: "pod/payment-api-7d9f", Container: "api",
			Detail: "Container exited with code 137, reason OOMKilled",
		},
		Spec: kcontext.ResourceSpec{Image: "ghcr.io/acme/payment-api:1.4.2", MemoryLimit: "512Mi"},
		Logs: []kcontext.LogLine{
			{Number: 1, Text: "INFO  starting payment-api 1.4.2"},
			{Number: 2, Text: "FATAL java.lang.OutOfMemoryError: Java heap space"},
		},
		Events: []kcontext.EventRecord{
			{Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container"},
		},
	}
}

func TestExplainVerifiesCitations(t *testing.T) {
	provider := &stubProvider{explanation: llm.Explanation{
		Category:   detector.OOMKilled,
		Confidence: 0.88,
		Summary:    "The JVM heap exceeded the 512Mi limit during cache warm-up.",
		CitedEvidence: []string{
			"FATAL java.lang.OutOfMemoryError: Java heap space",
			"ERROR postgres connection refused", // never appeared in the evidence
		},
		SuggestedFix: "Raise the memory limit to 1Gi.",
	}}

	got, err := NewEngine(provider).Explain(context.Background(), sampleBundle())
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}

	if len(got.Citations) != 1 {
		t.Fatalf("got %d verified citations, want 1", len(got.Citations))
	}
	if len(got.Rejected) != 1 {
		t.Fatalf("the invented quote was not rejected: %v", got.Rejected)
	}
	// Rejections are kept, not discarded: a reader deserves to know.
	if !strings.Contains(got.Rejected[0], "postgres") {
		t.Errorf("the wrong quote was rejected: %v", got.Rejected)
	}
	if got.CitationAccuracy != 0.5 {
		t.Errorf("citation accuracy = %v, want 0.5", got.CitationAccuracy)
	}
	if !got.Fabricated() {
		t.Error("Fabricated() should report the invented quote")
	}
	if got.Unsupported() {
		t.Error("an explanation with a surviving citation is not unsupported")
	}
	if got.HighlightLines()[0] != 2 {
		t.Errorf("highlight lines = %v, want line 2", got.HighlightLines())
	}
}

// A disagreement between the model and the rule must stay visible rather than
// one silently overwriting the other.
func TestExplainRecordsDisagreementWithTheRule(t *testing.T) {
	provider := &stubProvider{explanation: llm.Explanation{
		Category: detector.ProbeFailure, Confidence: 0.6,
		Summary: "The readiness probe is failing.",
	}}

	got, err := NewEngine(provider).Explain(context.Background(), sampleBundle())
	if err != nil {
		t.Fatal(err)
	}

	if got.Category != detector.ProbeFailure {
		t.Errorf("the model's category was lost: %s", got.Category)
	}
	if got.RuleCategory != detector.OOMKilled {
		t.Errorf("the rule's category was lost: %s", got.RuleCategory)
	}
	if got.Agrees {
		t.Error("a disagreement was recorded as agreement")
	}
}

// An explanation resting on nothing verifiable is not necessarily wrong, but it
// must not be presented with the authority of one that quotes the log.
func TestExplainMarksUnsupportedAnalysis(t *testing.T) {
	provider := &stubProvider{explanation: llm.Explanation{
		Category: detector.OOMKilled, Confidence: 0.9,
		Summary: "It ran out of memory, probably a leak.",
	}}

	got, err := NewEngine(provider).Explain(context.Background(), sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Unsupported() {
		t.Error("an explanation with no citations was not marked unsupported")
	}
	if got.Fabricated() {
		t.Error("citing nothing is not the same as fabricating")
	}
}

// The model needs the evidence and the numbered lines, or it cannot cite
// anything checkable.
func TestExplainSendsTheRenderedEvidence(t *testing.T) {
	provider := &stubProvider{explanation: llm.Explanation{
		Category: detector.OOMKilled, Confidence: 0.5, Summary: "x",
	}}

	if _, err := NewEngine(provider).Explain(context.Background(), sampleBundle()); err != nil {
		t.Fatal(err)
	}

	req := provider.last
	if !strings.Contains(req.Context, "2 | FATAL java.lang.OutOfMemoryError") {
		t.Errorf("the prompt is missing numbered logs:\n%s", req.Context)
	}
	if !strings.Contains(req.Context, "512Mi") {
		t.Error("the prompt is missing the memory limit that explains the failure")
	}
	if len(req.Evidence) != 3 {
		t.Errorf("evidence allowlist has %d entries, want 2 logs + 1 event", len(req.Evidence))
	}
	if req.Category != "OOMKilled" {
		t.Errorf("the rule's category was not passed to the model: %q", req.Category)
	}
}

func TestExplainSurfacesProviderFailure(t *testing.T) {
	provider := &stubProvider{err: errors.New("connection refused")}

	_, err := NewEngine(provider).Explain(context.Background(), sampleBundle())
	if err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if !strings.Contains(err.Error(), "payment-api-7d9f") {
		t.Errorf("the error does not name the incident: %v", err)
	}
}

func TestExplainWithoutAProvider(t *testing.T) {
	if _, err := NewEngine(nil).Explain(context.Background(), sampleBundle()); err == nil {
		t.Fatal("expected an error with no provider configured")
	}
}

// Anything rendering a baseline explanation must render the disclaimer with it,
// so the disclaimer has to be on the record itself.
func TestOfflineProviderAttachesItsDisclaimer(t *testing.T) {
	got, err := NewEngine(llm.NewOffline()).Explain(context.Background(), sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	if got.Disclaimer == "" {
		t.Fatal("baseline output carries no disclaimer")
	}
	if !strings.Contains(got.Disclaimer, "not by an LLM") {
		t.Errorf("disclaimer = %q", got.Disclaimer)
	}

	// A real provider carries none, so the warning keeps its meaning.
	real, err := NewEngine(&stubProvider{
		name:        "ollama",
		explanation: llm.Explanation{Category: detector.OOMKilled, Confidence: 0.5, Summary: "x"},
	}).Explain(context.Background(), sampleBundle())
	if err != nil {
		t.Fatal(err)
	}
	if real.Disclaimer != "" {
		t.Errorf("a real provider was given a baseline disclaimer: %q", real.Disclaimer)
	}
}
