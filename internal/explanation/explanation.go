// Package explanation joins a detected incident to its analysis, and enforces
// that every claim the analysis makes is backed by evidence.
package explanation

import (
	"context"
	"fmt"
	"strings"
	"time"

	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/llm"
)

// Explanation is a stored analysis of one incident.
type Explanation struct {
	IncidentID string `json:"incident_id"`
	// Category is what the model concluded. RuleCategory is what the detector
	// concluded. They are stored separately so a disagreement stays visible
	// instead of one silently overwriting the other.
	Category     detector.Category `json:"category"`
	RuleCategory detector.Category `json:"rule_category"`
	Agrees       bool              `json:"agrees_with_rule"`

	Confidence float64 `json:"confidence"`
	Summary    string  `json:"summary"`
	Fix        string  `json:"suggested_fix"`

	Citations []Citation `json:"citations"`
	// Rejected holds quotes that did not appear in the evidence. They are kept
	// rather than discarded: a reader deserves to know the model invented
	// something, and an operator deserves to see it accumulate.
	Rejected         []string `json:"rejected_citations,omitempty"`
	CitationAccuracy float64  `json:"citation_accuracy"`

	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Disclaimer is non-empty when no model was involved. Anything rendering
	// this explanation must render the disclaimer with it.
	Disclaimer string `json:"disclaimer,omitempty"`

	GeneratedAt time.Time     `json:"generated_at"`
	Duration    time.Duration `json:"duration_ms"`
}

// Engine produces verified explanations.
type Engine struct {
	provider llm.Provider
}

// NewEngine wires an engine over a provider.
func NewEngine(provider llm.Provider) *Engine { return &Engine{provider: provider} }

// Provider exposes the configured provider, for the settings endpoint.
func (e *Engine) Provider() llm.Provider { return e.provider }

// Explain analyses one incident and verifies every claim it makes.
//
// The verification is not optional and cannot be skipped by a caller: an
// explanation only exists on the other side of it. That is deliberate — the
// alternative is an API where forgetting one call publishes unverified model
// output as fact.
func (e *Engine) Explain(ctx context.Context, bundle kcontext.IncidentContext) (Explanation, error) {
	if e.provider == nil {
		return Explanation{}, fmt.Errorf("no explanation provider is configured")
	}

	started := time.Now()

	raw, err := e.provider.Explain(ctx, llm.Request{
		Category:  string(bundle.Incident.Category),
		Namespace: bundle.Incident.Namespace,
		Resource:  bundle.Incident.Resource,
		Container: bundle.Incident.Container,
		Context:   bundle.Render(),
		Evidence:  bundle.EvidenceLines(),
	})
	if err != nil {
		return Explanation{}, fmt.Errorf("explaining %s: %w", bundle.Incident.Resource, err)
	}

	verified, rejected := Verify(raw.CitedEvidence, evidenceFrom(bundle))

	out := Explanation{
		IncidentID:       bundle.Incident.ID,
		Category:         raw.Category,
		RuleCategory:     bundle.Incident.Category,
		Agrees:           raw.Category == bundle.Incident.Category,
		Confidence:       raw.Confidence,
		Summary:          strings.TrimSpace(raw.Summary),
		Fix:              strings.TrimSpace(raw.SuggestedFix),
		Citations:        verified,
		Rejected:         rejected,
		CitationAccuracy: Accuracy(verified, rejected),
		Provider:         e.provider.Name(),
		Model:            e.provider.Model(),
		GeneratedAt:      time.Now().UTC(),
		Duration:         time.Since(started),
	}

	if e.provider.Name() == llm.ProviderOffline {
		out.Disclaimer = llm.OfflineDisclaimer
	}

	return out, nil
}

// evidenceFrom splits the bundle into the two citable sources.
func evidenceFrom(bundle kcontext.IncidentContext) Evidence {
	evidence := Evidence{
		Logs:   make([]string, 0, len(bundle.Logs)),
		Events: make([]string, 0, len(bundle.Events)),
	}
	for _, line := range bundle.Logs {
		evidence.Logs = append(evidence.Logs, line.Text)
	}
	for _, event := range bundle.Events {
		evidence.Events = append(evidence.Events, event.Message)
	}
	return evidence
}

// Unsupported reports whether the analysis rests on nothing verifiable.
//
// An explanation with no surviving citation is not necessarily wrong, but it is
// unsupported, and the dashboard marks it as such rather than presenting it
// with the same authority as one that quotes the log.
func (e Explanation) Unsupported() bool { return len(e.Citations) == 0 }

// Fabricated reports whether the model quoted something that was not there.
func (e Explanation) Fabricated() bool { return len(e.Rejected) > 0 }

// HighlightLines returns the log lines the UI should mark as cited.
func (e Explanation) HighlightLines() []int { return LineNumbers(e.Citations) }
