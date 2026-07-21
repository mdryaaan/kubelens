// Package llm wraps the explanation model behind a narrow interface so the rest
// of kubelens never depends on a specific vendor.
package llm

import (
	"context"
	"errors"
	"fmt"
)

// Provider explains one incident from its evidence bundle.
//
// A single method, because that is the entire job: read this bounded context,
// return this schema. Keeping the surface this narrow is what makes the offline
// explainer a genuine drop-in, and what keeps adding a vendor from touching the
// detection pipeline — which must keep working whether or not any model does.
type Provider interface {
	// Name identifies the provider in the API and in eval output.
	Name() string
	// Model identifies the specific model in use.
	Model() string
	// Explain returns a validated explanation for one incident.
	Explain(ctx context.Context, req Request) (Explanation, error)
}

// Request is everything a provider needs to explain one incident.
type Request struct {
	// Category is what the deterministic rule already concluded. The model is
	// asked to confirm or correct it, not to classify from scratch — the rule
	// read the actual container status, which is stronger evidence than prose.
	Category  string
	Resource  string
	Namespace string
	Container string
	// Context is the rendered evidence bundle, with logs numbered for citation.
	Context string
	// Evidence is the allowlist a citation must appear in.
	Evidence []string
}

// Options configures provider construction.
type Options struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
	// Temperature is pinned low by callers: explaining a failure wants
	// repeatability, not variety.
	Temperature float64
}

// Known provider identifiers.
const (
	ProviderOllama  = "ollama"
	ProviderClaude  = "claude"
	ProviderOffline = "offline"
)

// ErrMalformed marks a response that could not be parsed into the schema.
//
// It is distinguished from transport errors deliberately: a malformed response
// is worth one retry with a repair instruction, while a refused connection will
// not be fixed by asking the model more firmly.
var ErrMalformed = errors.New("malformed model response")

// New builds a provider from options.
func New(opts Options) (Provider, error) {
	switch opts.Provider {
	case ProviderOllama, "":
		return NewOllama(opts), nil
	case ProviderClaude:
		return NewClaude(opts)
	case ProviderOffline:
		return NewOffline(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want one of: %s, %s, %s)",
			opts.Provider, ProviderOllama, ProviderClaude, ProviderOffline)
	}
}

func isMalformed(err error) bool { return err != nil && errors.Is(err, ErrMalformed) }
