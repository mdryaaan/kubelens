// Package eval measures explanation quality against a labelled corpus.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
	"github.com/mdryaaan/kubelens/internal/llm"
)

// Case is one labelled incident.
type Case struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	// Category is the ground truth, assigned by reading the excerpt.
	Category detector.Category `json:"category"`
	// LogFile names the excerpt under eval-logs/.
	LogFile string `json:"log_file"`
	// Events are the Kubernetes events that accompanied the failure. Some
	// categories — a failed image pull, an unschedulable pod — produce no
	// container output at all, and are only explainable from events.
	Events []string `json:"events,omitempty"`
	// Spec carries the resource fields that make the failure explainable.
	Spec map[string]string `json:"spec,omitempty"`
	// Namespace and Resource make the excerpt read like a real incident.
	Namespace string `json:"namespace"`
	Resource  string `json:"resource"`
	Container string `json:"container,omitempty"`
}

// Corpus is the labelled set.
type Corpus struct {
	Version int    `json:"version"`
	Cases   []Case `json:"cases"`
}

// LoadCorpus reads and validates a corpus from a filesystem.
func LoadCorpus(fsys fs.FS, name string) (Corpus, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Corpus{}, fmt.Errorf("reading eval corpus %s: %w", name, err)
	}

	var corpus Corpus
	if err := json.Unmarshal(data, &corpus); err != nil {
		return Corpus{}, fmt.Errorf("parsing eval corpus %s: %w", name, err)
	}
	if err := corpus.Validate(); err != nil {
		return Corpus{}, fmt.Errorf("in %s: %w", name, err)
	}
	return corpus, nil
}

// Validate catches the labelling mistakes that would silently distort a score.
func (c Corpus) Validate() error {
	if len(c.Cases) == 0 {
		return fmt.Errorf("corpus contains no cases")
	}

	seen := make(map[string]bool, len(c.Cases))
	for i, tc := range c.Cases {
		if tc.ID == "" {
			return fmt.Errorf("case %d has no id", i)
		}
		if seen[tc.ID] {
			return fmt.Errorf("duplicate case id %q", tc.ID)
		}
		seen[tc.ID] = true

		if !tc.Category.Valid() {
			return fmt.Errorf("case %s has unknown category %q", tc.ID, tc.Category)
		}
		if tc.LogFile == "" && len(tc.Events) == 0 {
			return fmt.Errorf("case %s has neither logs nor events, so nothing is explainable", tc.ID)
		}
	}

	return nil
}

// Harness runs a provider over the corpus.
//
// It drives the same provider interface and the same citation verification the
// product uses, so a score here describes what a user would actually get. An
// eval that measures a parallel code path measures nothing.
type Harness struct {
	FS       fs.FS
	Corpus   string
	Provider llm.Provider
	// Concurrency bounds parallel inference. One by default: a local model on a
	// laptop is memory-bound, and four concurrent requests make it slower, not
	// faster.
	Concurrency int
}

// Prediction is what the provider produced for one case.
type Prediction struct {
	CaseID     string
	Predicted  detector.Category
	Confidence float64
	// Cited and Fabricated come from the same verification the product runs.
	Cited      int
	Fabricated int
	Latency    time.Duration
	Err        error
}

// Result is a scored run with the metadata needed to interpret it.
type Result struct {
	RanAt    time.Time `json:"ran_at"`
	Corpus   string    `json:"corpus"`
	Cases    int       `json:"cases"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	// Disclaimer is non-empty when the "provider" was not a model. Anything
	// printing these numbers must print this too.
	Disclaimer string        `json:"disclaimer,omitempty"`
	Duration   time.Duration `json:"duration_ms"`
	Scores     Scores        `json:"scores"`
}

// Run evaluates every case in the corpus.
func (h *Harness) Run(ctx context.Context) (Result, error) {
	if h.Provider == nil {
		return Result{}, fmt.Errorf("no provider configured for the eval run")
	}

	corpus, err := LoadCorpus(h.FS, h.Corpus)
	if err != nil {
		return Result{}, err
	}

	started := time.Now()
	predictions := make([]Prediction, 0, len(corpus.Cases))

	for _, tc := range corpus.Cases {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		predictions = append(predictions, h.evaluate(ctx, tc))
	}

	result := Result{
		RanAt:    time.Now().UTC(),
		Corpus:   h.Corpus,
		Cases:    len(corpus.Cases),
		Provider: h.Provider.Name(),
		Model:    h.Provider.Model(),
		Duration: time.Since(started),
		Scores:   Score(corpus, predictions),
	}
	if h.Provider.Name() == llm.ProviderOffline {
		result.Disclaimer = llm.OfflineDisclaimer
	}

	return result, nil
}

// evaluate runs one case.
//
// The ground-truth category is deliberately withheld from the prompt. Handing a
// model the right answer and then scoring whether it repeats it measures
// copying, not classification.
func (h *Harness) evaluate(ctx context.Context, tc Case) Prediction {
	prediction := Prediction{CaseID: tc.ID}

	logs, err := h.readLog(tc)
	if err != nil {
		prediction.Err = err
		return prediction
	}

	evidence := append(append([]string{}, logs...), tc.Events...)
	started := time.Now()

	got, err := h.Provider.Explain(ctx, llm.Request{
		Category:  "", // withheld on purpose
		Namespace: tc.Namespace,
		Resource:  tc.Resource,
		Container: tc.Container,
		Context:   renderCase(tc, logs),
		Evidence:  evidence,
	})
	prediction.Latency = time.Since(started)

	if err != nil {
		prediction.Err = err
		return prediction
	}

	verified, rejected := explanation.Verify(got.CitedEvidence, explanation.Evidence{
		Logs: logs, Events: tc.Events,
	})

	prediction.Predicted = got.Category
	prediction.Confidence = got.Confidence
	prediction.Cited = len(verified)
	prediction.Fabricated = len(rejected)
	return prediction
}

func (h *Harness) readLog(tc Case) ([]string, error) {
	if tc.LogFile == "" {
		return nil, nil
	}

	full := path.Join(path.Dir(h.Corpus), tc.LogFile)
	data, err := fs.ReadFile(h.FS, full)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", full, err)
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

// renderCase builds the evidence block, in the same numbered shape the product
// sends at runtime.
func renderCase(tc Case, logs []string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "INCIDENT\n  namespace: %s\n  resource: %s\n", tc.Namespace, tc.Resource)
	if tc.Container != "" {
		fmt.Fprintf(&b, "  container: %s\n", tc.Container)
	}

	fmt.Fprintf(&b, "\nRESOURCE SPEC\n")
	if len(tc.Spec) == 0 {
		fmt.Fprintf(&b, "  (no spec fields available)\n")
	}
	for _, key := range sortedKeys(tc.Spec) {
		fmt.Fprintf(&b, "  %s: %s\n", key, tc.Spec[key])
	}

	fmt.Fprintf(&b, "\nKUBERNETES EVENTS\n")
	if len(tc.Events) == 0 {
		fmt.Fprintf(&b, "  (none available)\n")
	}
	for _, event := range tc.Events {
		fmt.Fprintf(&b, "  %s\n", event)
	}

	fmt.Fprintf(&b, "\nCONTAINER LOGS (cite these by line number)\n")
	if len(logs) == 0 {
		fmt.Fprintf(&b, "  (none available)\n")
	}
	for i, line := range logs {
		fmt.Fprintf(&b, "  %d | %s\n", i+1, line)
	}

	return b.String()
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
