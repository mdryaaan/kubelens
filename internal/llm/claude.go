package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// DefaultClaudeModel is the model used when --model is not given.
const DefaultClaudeModel = "claude-sonnet-4-6"

const (
	defaultAnthropicURL = "https://api.anthropic.com"
	anthropicVersion    = "2023-06-01"
)

// Claude talks to the Anthropic Messages API.
//
// Opt-in rather than default: it needs a key, it costs money per explanation,
// and it ships container logs off the machine. Worth it when the quality of the
// explanation matters more than those three things.
type Claude struct {
	baseURL     string
	model       string
	apiKey      string
	temperature float64
	client      *http.Client
}

// NewClaude builds a Claude provider, reading the key from options or from
// ANTHROPIC_API_KEY.
func NewClaude(opts Options) (*Claude, error) {
	key := opts.APIKey
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		return nil, fmt.Errorf("provider claude needs an API key: set ANTHROPIC_API_KEY " +
			"or use --provider ollama, which runs locally and needs no key")
	}

	base := opts.BaseURL
	if base == "" {
		base = defaultAnthropicURL
	}
	model := opts.Model
	if model == "" {
		model = DefaultClaudeModel
	}

	return &Claude{
		baseURL:     base,
		model:       model,
		apiKey:      key,
		temperature: opts.Temperature,
		client:      &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Name identifies the provider.
func (c *Claude) Name() string { return ProviderClaude }

// Model identifies the model in use.
func (c *Claude) Model() string { return c.model }

type claudeRequest struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature"`
	System      string          `json:"system"`
	Messages    []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Explain sends the incident to the Anthropic API and parses the explanation.
func (c *Claude) Explain(ctx context.Context, req Request) (Explanation, error) {
	prompt := BuildPrompt(req)

	explanation, err := c.once(ctx, prompt)
	if err == nil {
		return explanation, nil
	}
	if !isMalformed(err) {
		return Explanation{}, err
	}

	explanation, retryErr := c.once(ctx, prompt+"\n\n"+RepairPrompt)
	if retryErr != nil {
		return Explanation{}, fmt.Errorf("claude returned unparseable output twice: %w", retryErr)
	}
	return explanation, nil
}

func (c *Claude) once(ctx context.Context, prompt string) (Explanation, error) {
	payload, err := json.Marshal(claudeRequest{
		Model:       c.model,
		MaxTokens:   1024,
		Temperature: c.temperature,
		System:      SystemPrompt,
		Messages:    []claudeMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return Explanation{}, fmt.Errorf("encoding claude request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return Explanation{}, fmt.Errorf("building claude request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return Explanation{}, fmt.Errorf("calling anthropic api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out claudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Explanation{}, fmt.Errorf("decoding claude response (HTTP %d): %w", resp.StatusCode, err)
	}
	if out.Error != nil {
		return Explanation{}, fmt.Errorf("anthropic api error (%s): %s", out.Error.Type, out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return Explanation{}, fmt.Errorf("anthropic api returned HTTP %d", resp.StatusCode)
	}

	var text bytes.Buffer
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}

	return ParseExplanation(text.String())
}
