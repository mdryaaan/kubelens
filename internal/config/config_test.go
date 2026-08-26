package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requiring a cluster before the tool will start is how a tool gets closed
// before it is understood.
func TestDefaultRunsWithoutACluster(t *testing.T) {
	cfg := Default()

	if cfg.Mode != ModeDemo {
		t.Errorf("mode = %q, want demo so the tool runs with no cluster", cfg.Mode)
	}
	if cfg.Provider != ProviderOllama {
		t.Errorf("provider = %q, want the local default", cfg.Provider)
	}
	if cfg.Explain {
		t.Error("the LLM pass should be opt-in; detection must not depend on it")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

// Two runs with the same seed must produce the same demo, or a rehearsed
// walkthrough falls apart on the take that matters.
func TestDefaultSeedIsFixed(t *testing.T) {
	if Default().DemoSeed == 0 {
		t.Error("the demo seed is unset, so the simulator would not be reproducible")
	}
	first, second := Default(), Default()
	if first.DemoSeed != second.DemoSeed {
		t.Errorf("two Default() calls disagree on the seed: %d vs %d",
			first.DemoSeed, second.DemoSeed)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"unknown mode", func(c *Config) { c.Mode = "kubectl" }},
		{"unknown provider", func(c *Config) { c.Provider = "gpt" }},
		{"absurd temperature", func(c *Config) { c.Temperature = 9 }},
		{"negative temperature", func(c *Config) { c.Temperature = -1 }},
		{"empty listen address", func(c *Config) { c.Addr = "" }},
		{"zero log lines", func(c *Config) { c.LogLines = 0 }},
		{"zero event limit", func(c *Config) { c.EventLimit = 0 }},
		{"missing kubeconfig", func(c *Config) {
			c.Mode = ModeCluster
			c.Kubeconfig = filepath.Join(os.TempDir(), "kubelens-nonexistent-kubeconfig")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestValidateAcceptsEveryProvider(t *testing.T) {
	for _, provider := range []string{ProviderOllama, ProviderClaude, ProviderOffline} {
		cfg := Default()
		cfg.Provider = provider
		if err := cfg.Validate(); err != nil {
			t.Errorf("provider %q was rejected: %v", provider, err)
		}
	}
}

func TestResolveKubeconfigPrecedence(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit.yaml")
	if err := os.WriteFile(explicit, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromEnv := filepath.Join(t.TempDir(), "env.yaml")
	if err := os.WriteFile(fromEnv, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KUBECONFIG", fromEnv)

	cfg := Default()
	cfg.Kubeconfig = explicit
	if got := cfg.ResolveKubeconfig(); got != explicit {
		t.Errorf("an explicit path did not win: %q", got)
	}

	cfg.Kubeconfig = ""
	if got := cfg.ResolveKubeconfig(); got != fromEnv {
		t.Errorf("KUBECONFIG was not used: %q", got)
	}

	// KUBECONFIG may list several files; the first entry is the one used.
	t.Setenv("KUBECONFIG", strings.Join([]string{fromEnv, explicit}, string(os.PathListSeparator)))
	if got := cfg.ResolveKubeconfig(); got != fromEnv {
		t.Errorf("a KUBECONFIG list did not resolve to its first entry: %q", got)
	}
}

// An empty result is meaningful: client-go reads it as "use the in-cluster
// service account", which is what kubelens needs when it runs as a pod.
func TestResolveKubeconfigEmptyMeansInCluster(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", t.TempDir())

	if got := Default().ResolveKubeconfig(); got != "" {
		t.Errorf("expected an empty path with no kubeconfig anywhere, got %q", got)
	}
}

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "from-env")

	cfg := Default()
	if got := cfg.ResolveAPIKey(); got != "from-env" {
		t.Errorf("the environment key was not read: %q", got)
	}

	cfg.APIKey = "explicit"
	if got := cfg.ResolveAPIKey(); got != "explicit" {
		t.Errorf("an explicit key did not win: %q", got)
	}
}

// "*" would let any page in any tab read a cluster's incident history.
func TestAllowedOrigin(t *testing.T) {
	cfg := Default()

	if !cfg.AllowedOrigin("http://localhost:3000") {
		t.Error("the dashboard origin was rejected")
	}
	if cfg.AllowedOrigin("https://evil.example.com") {
		t.Error("an unlisted origin was allowed")
	}
	if cfg.AllowedOrigin("") {
		t.Error("an empty origin was allowed")
	}

	cfg.CORSOrigins = []string{"*"}
	if !cfg.AllowedOrigin("https://anything.example") {
		t.Error("an explicit wildcard was not honoured")
	}
}
