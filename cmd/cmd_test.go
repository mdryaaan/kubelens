package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdryaaan/kubelens/internal/config"
)

// run executes the CLI with args and captures both streams.
//
// The command tree is rebuilt per call because cobra binds flags to package
// level variables; sharing one tree would let one test's --provider leak into
// the next one's assertions.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	resetFlags()

	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)

	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func resetFlags() {
	flags.Demo = false
	flags.Kubeconfig = ""
	flags.Provider = ""
	flags.Explain = false
	flags.Namespaces = nil
	evalOpts.Format = "text"
	evalOpts.Output = ""
	evalOpts.Dir = ""
	evalOpts.Corpus = "labeled-incidents.json"
	evalOpts.MinScore = 0
	versionAsJSON = false
}

func TestMain(m *testing.M) {
	// The CLI reads its corpus from the filesystem main injects, so the tests
	// supply the on-disk copy.
	SetEmbedded(os.DirFS(filepath.Join("..", "testdata", "eval")))
	os.Exit(m.Run())
}

func TestVersion(t *testing.T) {
	out, _, err := run(t, "version")
	if err != nil {
		t.Fatalf("version failed: %v", err)
	}
	if !strings.Contains(out, "kubelens") {
		t.Errorf("version output = %q", out)
	}

	out, _, err = run(t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var info map[string]string
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("version JSON does not parse: %v", err)
	}
	for _, key := range []string{"version", "commit", "goVersion", "platform"} {
		if info[key] == "" {
			t.Errorf("version JSON has no %s", key)
		}
	}
}

func TestEvalRunsAgainstTheBundledCorpus(t *testing.T) {
	out, stderr, err := run(t, "eval", "--provider", "offline")
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}

	for _, want := range []string{"accuracy", "macro F1", "confusion matrix", "fabricated citations"} {
		if !strings.Contains(out, want) {
			t.Errorf("eval output is missing %q:\n%s", want, out)
		}
	}
	// Baseline numbers announce themselves on stderr too, because a report is
	// skimmed and stderr is where a CI log looks.
	if !strings.Contains(stderr, "not by an LLM") {
		t.Errorf("stderr did not carry the baseline disclaimer: %q", stderr)
	}
}

func TestEvalFormats(t *testing.T) {
	out, _, err := run(t, "eval", "--provider", "offline", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("eval JSON does not parse: %v", err)
	}

	out, _, err = run(t, "eval", "--provider", "offline", "--format", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "### Confusion matrix") {
		t.Errorf("eval markdown has no confusion matrix:\n%s", out)
	}

	if _, _, err := run(t, "eval", "--provider", "offline", "--format", "csv"); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func TestEvalMinScoreGate(t *testing.T) {
	if _, _, err := run(t, "eval", "--provider", "offline", "--min-score", "0.9"); err != nil {
		t.Errorf("the corpus should clear a 0.9 gate: %v", err)
	}
	if _, _, err := run(t, "eval", "--provider", "offline", "--min-score", "1.01"); err == nil {
		t.Error("an unreachable gate should fail")
	}
}

func TestEvalWritesToAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eval.md")

	if _, _, err := run(t, "eval", "--provider", "offline", "--format", "markdown", "-o", path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the output file was not written: %v", err)
	}
	if !bytes.Contains(data, []byte("## Evaluation")) {
		t.Error("the output file does not contain the report")
	}
}

func TestRejectsBadFlagValues(t *testing.T) {
	if _, _, err := run(t, "eval", "--provider", "gpt"); err == nil {
		t.Error("expected an error for an unknown provider")
	}
	if _, _, err := run(t, "eval", "--provider", "offline", "--temperature", "9"); err == nil {
		t.Error("expected an error for an out-of-range temperature")
	}
}

// --kubeconfig implies a live cluster; passing one and still running the
// simulator would silently ignore what the user asked for.
func TestKubeconfigSwitchesToClusterMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resetFlags()
	root := NewRootCommand()
	root.SetArgs([]string{"eval", "--kubeconfig", path, "--provider", "offline"})
	// Parse without executing the run, so the resolved config can be inspected.
	if err := root.ParseFlags([]string{"--kubeconfig", path}); err != nil {
		t.Fatal(err)
	}
	flags.Kubeconfig = path

	cfg, err := resolveConfig(root)
	if err != nil {
		t.Fatalf("resolveConfig failed: %v", err)
	}
	if cfg.Mode != config.ModeCluster {
		t.Errorf("mode = %q, want cluster once a kubeconfig is given", cfg.Mode)
	}
}

func TestDemoFlagSelectsTheSimulator(t *testing.T) {
	resetFlags()
	flags.Demo = true

	cfg, err := resolveConfig(NewRootCommand())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != config.ModeDemo {
		t.Errorf("mode = %q, want demo", cfg.Mode)
	}
}

func TestEvalWithoutACorpus(t *testing.T) {
	SetEmbedded(nil)
	defer SetEmbedded(os.DirFS(filepath.Join("..", "testdata", "eval")))

	_, _, err := run(t, "eval", "--provider", "offline")
	if err == nil {
		t.Fatal("expected an error with no corpus available")
	}
	if !strings.Contains(err.Error(), "--dir") {
		t.Errorf("the error should say how to supply one: %v", err)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	out, _, err := run(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"serve", "eval", "version"} {
		if !strings.Contains(out, command) {
			t.Errorf("help does not list %q", command)
		}
	}
	// The two run modes are the first thing anyone needs to know.
	if !strings.Contains(out, "--demo") || !strings.Contains(out, "--kubeconfig") {
		t.Error("help does not surface the two run modes")
	}
}
