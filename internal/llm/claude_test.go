package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mdryaaan/kubelens/internal/detector"
)

func TestClaudeExplainParsesAResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicVersion {
			t.Errorf("anthropic-version = %q, want %q", got, anthropicVersion)
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"category\":\"OOMKilled\",` +
			`\"confidence\":0.9,\"explanation\":\"Heap exceeded the limit.\",` +
			`\"cited_evidence\":[],\"suggested_fix\":\"Raise the limit.\"}"}]}`))
	}))
	defer srv.Close()

	provider, err := NewClaude(Options{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("NewClaude failed: %v", err)
	}

	got, err := provider.Explain(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}
	if got.Category != detector.OOMKilled {
		t.Errorf("category = %s", got.Category)
	}
	if provider.Name() != ProviderClaude || provider.Model() != DefaultClaudeModel {
		t.Errorf("provider identity = %s/%s", provider.Name(), provider.Model())
	}
}

// Failing at construction with advice beats failing later with an opaque 401
// halfway through explaining a cluster's worth of incidents.
func TestNewClaudeWithoutAKeyPointsAtTheAlternative(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")

	_, err := NewClaude(Options{})
	if err == nil {
		t.Fatal("expected an error with no API key")
	}
	if !strings.Contains(err.Error(), "ollama") {
		t.Errorf("error should point at the local alternative, got: %v", err)
	}
}

func TestClaudeReadsTheKeyFromTheEnvironment(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "from-env")

	if _, err := NewClaude(Options{}); err != nil {
		t.Fatalf("the environment key was not used: %v", err)
	}
}

func TestClaudeSurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer srv.Close()

	provider, _ := NewClaude(Options{BaseURL: srv.URL, APIKey: "k"})
	_, err := provider.Explain(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "slow down") {
		t.Errorf("the API message was swallowed: %v", err)
	}
}

func TestNewSelectsProviders(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "k")

	for _, want := range []string{ProviderOllama, ProviderClaude, ProviderOffline, ""} {
		provider, err := New(Options{Provider: want})
		if err != nil {
			t.Fatalf("New(%q) failed: %v", want, err)
		}
		if provider.Model() == "" {
			t.Errorf("provider %q reports no model", want)
		}
	}

	if _, err := New(Options{Provider: "gpt"}); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}
