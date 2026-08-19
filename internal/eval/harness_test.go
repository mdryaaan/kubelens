package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/llm"
)

func corpusFS() (string, string) {
	return filepath.Join("..", "..", "testdata", "eval"), "labeled-incidents.json"
}

func TestShippedCorpusIsWellFormed(t *testing.T) {
	dir, name := corpusFS()

	corpus, err := LoadCorpus(os.DirFS(dir), name)
	if err != nil {
		t.Fatalf("the shipped corpus does not load: %v", err)
	}
	if len(corpus.Cases) < 30 {
		t.Errorf("corpus has %d cases, want at least 30", len(corpus.Cases))
	}

	perCategory := map[detector.Category]int{}
	for _, tc := range corpus.Cases {
		perCategory[tc.Category]++

		if tc.Description == "" {
			t.Errorf("case %s has no description", tc.ID)
		}
		if tc.Namespace == "" || tc.Resource == "" {
			t.Errorf("case %s does not name a resource", tc.ID)
		}
		if tc.LogFile != "" {
			if _, err := os.Stat(filepath.Join(dir, tc.LogFile)); err != nil {
				t.Errorf("case %s points at a missing log file: %v", tc.ID, err)
			}
		}
	}

	// Every category needs examples, or its precision and recall are undefined
	// and the macro average silently drops it.
	for _, category := range detector.AllCategories() {
		if perCategory[category] < 5 {
			t.Errorf("category %s has only %d cases, want at least 5", category, perCategory[category])
		}
	}
}

// Some failures genuinely produce no container output — an image that never
// pulled, a pod that never scheduled. Those cases carry events instead, and the
// corpus has to allow that or it would only contain the easy half of reality.
func TestCorpusIncludesCasesWithNoLogs(t *testing.T) {
	dir, name := corpusFS()
	corpus, _ := LoadCorpus(os.DirFS(dir), name)

	withoutLogs := 0
	for _, tc := range corpus.Cases {
		if tc.LogFile == "" {
			withoutLogs++
			if len(tc.Events) == 0 {
				t.Errorf("case %s has neither logs nor events", tc.ID)
			}
		}
	}
	if withoutLogs == 0 {
		t.Error("every case has container logs; the corpus omits failures that produce none")
	}
}

func TestCorpusValidationCatchesLabellingMistakes(t *testing.T) {
	bad := map[string]string{
		"no cases":           `{"cases":[]}`,
		"missing id":         `{"cases":[{"category":"OOMKilled","log_file":"a.log"}]}`,
		"duplicate id":       `{"cases":[{"id":"a","category":"OOMKilled","log_file":"a.log"},{"id":"a","category":"OOMKilled","log_file":"b.log"}]}`,
		"unknown category":   `{"cases":[{"id":"a","category":"DiskFull","log_file":"a.log"}]}`,
		"nothing to explain": `{"cases":[{"id":"a","category":"OOMKilled"}]}`,
	}

	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"cases.json": &fstest.MapFile{Data: []byte(body)}}
			if _, err := LoadCorpus(fsys, "cases.json"); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestHarnessScoresTheShippedCorpus(t *testing.T) {
	dir, name := corpusFS()

	result, err := (&Harness{
		FS: os.DirFS(dir), Corpus: name, Provider: llm.NewOffline(),
	}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(result.Scores.Failures) != 0 {
		t.Errorf("cases failed to evaluate: %v", result.Scores.Failures)
	}
	if result.Scores.Cases != 30 {
		t.Errorf("scored %d cases, want 30", result.Scores.Cases)
	}
	// The baseline is a floor, not a target. This guards against a regression
	// that would make the control meaningless, without pretending the number
	// is a model's.
	if result.Scores.Accuracy < 0.8 {
		t.Errorf("baseline accuracy fell to %.3f", result.Scores.Accuracy)
	}
	// The baseline lifts citations out of the evidence, so it cannot fabricate.
	if result.Scores.Fabricated != 0 {
		t.Errorf("the baseline reported %d fabricated citations", result.Scores.Fabricated)
	}
	if result.Disclaimer == "" {
		t.Error("baseline numbers were produced without a disclaimer")
	}
}

// Handing a model the right answer and scoring whether it repeats it measures
// copying, not classification.
func TestHarnessWithholdsTheGroundTruthFromThePrompt(t *testing.T) {
	provider := &recordingProvider{}

	_, err := (&Harness{
		FS: fstest.MapFS{
			"cases.json": &fstest.MapFile{Data: []byte(
				`{"cases":[{"id":"a","category":"OOMKilled","namespace":"n","resource":"pod/p",` +
					`"events":["Warning OOMKilling: Memory cgroup out of memory"]}]}`)},
		},
		Corpus: "cases.json", Provider: provider,
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if provider.last.Category != "" {
		t.Errorf("the ground truth leaked into the request: %q", provider.last.Category)
	}
	// And the rendered prompt must say the category is unknown, not omit it in
	// a way the model reads as an empty label.
	if !strings.Contains(llm.BuildPrompt(provider.last), llm.UnknownCategory) {
		t.Error("the prompt does not tell the model to classify from the evidence")
	}
}

func TestHarnessWithoutAProvider(t *testing.T) {
	dir, name := corpusFS()
	if _, err := (&Harness{FS: os.DirFS(dir), Corpus: name}).Run(context.Background()); err == nil {
		t.Fatal("expected an error with no provider")
	}
}

// A provider that errors on part of the corpus has not earned a clean accuracy
// number on the rest.
func TestFailedCasesCountAgainstTheScore(t *testing.T) {
	provider := &failingProvider{}

	result, err := (&Harness{
		FS: fstest.MapFS{
			"cases.json": &fstest.MapFile{Data: []byte(
				`{"cases":[{"id":"a","category":"OOMKilled","namespace":"n","resource":"pod/p",` +
					`"events":["Warning OOMKilling"]}]}`)},
		},
		Corpus: "cases.json", Provider: provider,
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if result.Scores.Accuracy != 0 {
		t.Errorf("accuracy = %v despite every case failing", result.Scores.Accuracy)
	}
	if len(result.Scores.Failures) != 1 {
		t.Errorf("the failure was not recorded: %v", result.Scores.Failures)
	}
}

func TestHarnessStopsOnACancelledContext(t *testing.T) {
	dir, name := corpusFS()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&Harness{FS: os.DirFS(dir), Corpus: name, Provider: llm.NewOffline()}).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRenderers(t *testing.T) {
	dir, name := corpusFS()
	result, err := (&Harness{
		FS: os.DirFS(dir), Corpus: name, Provider: llm.NewOffline(),
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var jsonOut bytes.Buffer
	if err := WriteJSON(&jsonOut, result); err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(jsonOut.Bytes(), &back); err != nil {
		t.Fatalf("eval JSON does not round trip: %v", err)
	}

	var text bytes.Buffer
	if err := WriteText(&text, result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"accuracy", "macro F1", "confusion matrix", "fabricated citations"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text output is missing %q", want)
		}
	}

	var md bytes.Buffer
	if err := WriteMarkdown(&md, result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Evaluation", "### Per category", "### Confusion matrix"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown output is missing %q", want)
		}
	}
}

// Everything below the disclaimer looks like a model's score and is not.
func TestOutputLeadsWithTheBaselineDisclaimer(t *testing.T) {
	dir, name := corpusFS()
	result, _ := (&Harness{
		FS: os.DirFS(dir), Corpus: name, Provider: llm.NewOffline(),
	}).Run(context.Background())

	for name, render := range map[string]func(*bytes.Buffer) error{
		"text":     func(b *bytes.Buffer) error { return WriteText(b, result) },
		"markdown": func(b *bytes.Buffer) error { return WriteMarkdown(b, result) },
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := render(&buf); err != nil {
				t.Fatal(err)
			}
			out := buf.String()

			disclaimer := strings.Index(out, "not by an LLM")
			firstNumber := strings.Index(out, "accuracy")
			if disclaimer < 0 {
				t.Fatal("the disclaimer is missing entirely")
			}
			if firstNumber >= 0 && disclaimer > firstNumber {
				t.Error("the disclaimer appears after the numbers it qualifies")
			}
		})
	}
}

type recordingProvider struct{ last llm.Request }

func (p *recordingProvider) Name() string  { return "recording" }
func (p *recordingProvider) Model() string { return "recording-model" }
func (p *recordingProvider) Explain(_ context.Context, req llm.Request) (llm.Explanation, error) {
	p.last = req
	return llm.Explanation{Category: detector.OOMKilled, Confidence: 0.5, Summary: "x"}, nil
}

type failingProvider struct{}

func (p *failingProvider) Name() string  { return "failing" }
func (p *failingProvider) Model() string { return "none" }
func (p *failingProvider) Explain(context.Context, llm.Request) (llm.Explanation, error) {
	return llm.Explanation{}, errors.New("connection refused")
}
