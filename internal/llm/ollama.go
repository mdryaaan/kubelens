package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultOllamaURL is where Ollama listens out of the box.
const DefaultOllamaURL = "http://localhost:11434"

// DefaultOllamaModel is a small instruct model that fits on a laptop.
const DefaultOllamaModel = "llama3"

// Ollama talks to a local Ollama daemon.
//
// This is kubelens's default provider on purpose. Container logs are among the
// most sensitive text a company has — they routinely contain internal
// hostnames, customer identifiers, connection strings, and occasionally a
// leaked credential. Shipping them to a third party by default would be the
// wrong posture for a tool whose whole job is reading them. Local inference
// also means anyone can run the full pipeline immediately, with no account.
type Ollama struct {
	baseURL     string
	model       string
	temperature float64
	client      *http.Client
}

// NewOllama builds an Ollama provider, filling in defaults.
func NewOllama(opts Options) *Ollama {
	base := opts.BaseURL
	if base == "" {
		base = DefaultOllamaURL
	}
	model := opts.Model
	if model == "" {
		model = DefaultOllamaModel
	}

	return &Ollama{
		baseURL:     base,
		model:       model,
		temperature: opts.Temperature,
		// Generous, because a laptop running a 7B model against a long log
		// excerpt is genuinely slow, and timing out mid-explanation wastes the
		// work already done.
		client: &http.Client{Timeout: 180 * time.Second},
	}
}

// Name identifies the provider.
func (o *Ollama) Name() string { return ProviderOllama }

// Model identifies the model in use.
func (o *Ollama) Model() string { return o.model }

type ollamaRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt"`
	System  string         `json:"system"`
	Stream  bool           `json:"stream"`
	Format  string         `json:"format"`
	Options ollamaSettings `json:"options"`
}

type ollamaSettings struct {
	Temperature float64 `json:"temperature"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// Explain sends the incident to Ollama and parses the structured explanation.
func (o *Ollama) Explain(ctx context.Context, req Request) (Explanation, error) {
	prompt := BuildPrompt(req)

	explanation, err := o.once(ctx, prompt)
	if err == nil {
		return explanation, nil
	}

	// One retry, and only for a formatting failure. A refused connection is not
	// fixed by asking the model more firmly.
	if !isMalformed(err) {
		return Explanation{}, err
	}

	explanation, retryErr := o.once(ctx, prompt+"\n\n"+RepairPrompt)
	if retryErr != nil {
		return Explanation{}, fmt.Errorf("ollama returned unparseable output twice: %w", retryErr)
	}
	return explanation, nil
}

func (o *Ollama) once(ctx context.Context, prompt string) (Explanation, error) {
	payload, err := json.Marshal(ollamaRequest{
		Model:  o.model,
		Prompt: prompt,
		System: SystemPrompt,
		Stream: false,
		// Ollama's JSON mode constrains decoding to valid JSON, which removes
		// most of the malformed-response problem at the source.
		Format:  "json",
		Options: ollamaSettings{Temperature: o.temperature},
	})
	if err != nil {
		return Explanation{}, fmt.Errorf("encoding ollama request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return Explanation{}, fmt.Errorf("building ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return Explanation{}, fmt.Errorf(
			"calling ollama at %s (is `ollama serve` running?): %w", o.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Explanation{}, fmt.Errorf("ollama returned HTTP %d", resp.StatusCode)
	}

	var out ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Explanation{}, fmt.Errorf("decoding ollama response: %w", err)
	}

	return ParseExplanation(out.Response)
}
