package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mdryaaan/kubelens/internal/detector"
)

func sampleRequest() Request {
	return Request{
		Category:  "OOMKilled",
		Namespace: "payments",
		Resource:  "pod/payment-api-7d9f",
		Container: "api",
		Context: "CONTAINER LOGS (cite these by line number)\n" +
			"  1 | INFO  starting payment-api 1.4.2\n" +
			"  2 | FATAL java.lang.OutOfMemoryError: Java heap space\n",
		Evidence: []string{
			"INFO  starting payment-api 1.4.2",
			"FATAL java.lang.OutOfMemoryError: Java heap space",
		},
	}
}

const goodResponse = `{"category":"OOMKilled","confidence":0.86,` +
	`"explanation":"The JVM heap exceeded the 512Mi container limit during cache warm-up.",` +
	`"cited_evidence":["FATAL java.lang.OutOfMemoryError: Java heap space"],` +
	`"suggested_fix":"Raise the memory limit to 1Gi or shrink the startup cache."}`

func TestOllamaExplainParsesAResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		// JSON mode removes most malformed responses at the source.
		if req.Format != "json" {
			t.Errorf("format = %q, want json", req.Format)
		}
		if !strings.Contains(req.Prompt, "OutOfMemoryError") {
			t.Error("the prompt does not carry the evidence")
		}
		if !strings.Contains(req.System, "cited_evidence") {
			t.Error("the system prompt does not carry the citation rule")
		}
		_ = json.NewEncoder(w).Encode(ollamaResponse{Response: goodResponse, Done: true})
	}))
	defer srv.Close()

	got, err := NewOllama(Options{BaseURL: srv.URL}).Explain(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("Explain failed: %v", err)
	}
	if got.Category != detector.OOMKilled {
		t.Errorf("category = %s", got.Category)
	}
	if got.Confidence != 0.86 {
		t.Errorf("confidence = %v", got.Confidence)
	}
	if len(got.CitedEvidence) != 1 {
		t.Errorf("cited evidence = %v", got.CitedEvidence)
	}
}

// One repair retry, and only for a formatting failure.
func TestOllamaRetriesOnceOnMalformedOutput(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req ollamaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		if calls == 1 {
			_ = json.NewEncoder(w).Encode(ollamaResponse{Response: "It ran out of memory."})
			return
		}
		if !strings.Contains(req.Prompt, "could not be parsed") {
			t.Error("the retry did not carry the repair instruction")
		}
		_ = json.NewEncoder(w).Encode(ollamaResponse{Response: goodResponse})
	}))
	defer srv.Close()

	if _, err := NewOllama(Options{BaseURL: srv.URL}).Explain(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("Explain failed: %v", err)
	}
	if calls != 2 {
		t.Errorf("made %d calls, want exactly 2", calls)
	}
}

func TestOllamaGivesUpAfterTwoMalformedResponses(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(ollamaResponse{Response: "still not json"})
	}))
	defer srv.Close()

	if _, err := NewOllama(Options{BaseURL: srv.URL}).Explain(context.Background(), sampleRequest()); err == nil {
		t.Fatal("expected an error after two malformed responses")
	}
	if calls != 2 {
		t.Errorf("made %d calls, want exactly 2", calls)
	}
}

// A refused connection is not fixed by asking the model more firmly.
func TestOllamaDoesNotRetryTransportFailures(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := NewOllama(Options{BaseURL: srv.URL}).Explain(context.Background(), sampleRequest()); err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want exactly 1", calls)
	}
}

// The error has to tell the user what to start, not just that a socket refused.
func TestOllamaErrorNamesTheDaemon(t *testing.T) {
	_, err := NewOllama(Options{BaseURL: "http://127.0.0.1:1"}).
		Explain(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("error should name the daemon to start, got: %v", err)
	}
}

func TestParseExplanationAcceptsRealWorldWrappers(t *testing.T) {
	wrappers := map[string]string{
		"bare object": goodResponse,
		"fenced":      "```json\n" + goodResponse + "\n```",
		"with prose":  "Here is my analysis:\n" + goodResponse + "\nHope that helps.",
		"braces in strings": `{"category":"OOMKilled","confidence":0.5,` +
			`"explanation":"The map {a:1} was allocated eagerly.","cited_evidence":[],"suggested_fix":"x"}`,
	}

	for name, raw := range wrappers {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseExplanation(raw); err != nil {
				t.Errorf("ParseExplanation failed: %v", err)
			}
		})
	}
}

func TestParseExplanationRejectsInvalidPayloads(t *testing.T) {
	bad := map[string]string{
		"empty":              "",
		"no object":          "I could not determine a cause.",
		"unbalanced":         `{"category":"OOMKilled"`,
		"unknown category":   `{"category":"DiskFull","confidence":0.5,"explanation":"x"}`,
		"confidence above 1": `{"category":"OOMKilled","confidence":4,"explanation":"x"}`,
		"negative":           `{"category":"OOMKilled","confidence":-0.5,"explanation":"x"}`,
		"empty explanation":  `{"category":"OOMKilled","confidence":0.5,"explanation":"   "}`,
	}

	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := ParseExplanation(raw)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("error %v does not wrap ErrMalformed", err)
			}
		})
	}
}

// A citation has to stay byte-comparable to the line it quotes, or verification
// becomes a fuzzy match that proves nothing.
func TestParseExplanationDoesNotAlterCitationText(t *testing.T) {
	raw := `{"category":"OOMKilled","confidence":0.5,"explanation":"x",` +
		`"cited_evidence":["  FATAL   java.lang.OutOfMemoryError: Java heap space  ","","  "],` +
		`"suggested_fix":"y"}`

	got, err := ParseExplanation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CitedEvidence) != 1 {
		t.Fatalf("got %d citations, want the empties dropped", len(got.CitedEvidence))
	}
	// Only surrounding whitespace is trimmed; the interior is untouched.
	if got.CitedEvidence[0] != "FATAL   java.lang.OutOfMemoryError: Java heap space" {
		t.Errorf("citation text was altered: %q", got.CitedEvidence[0])
	}
}
