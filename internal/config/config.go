// Package config resolves kubelens settings from defaults, flags, and the
// environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mode is where kubelens gets its cluster data from.
type Mode string

// The two run modes.
const (
	// ModeDemo runs the deterministic simulator. No cluster, no network.
	ModeDemo Mode = "demo"
	// ModeCluster watches a real cluster through a kubeconfig.
	ModeCluster Mode = "cluster"
)

// Provider names an LLM backend.
const (
	ProviderOllama = "ollama"
	ProviderClaude = "claude"
	// ProviderOffline is a deterministic explainer used when no model is
	// available. It is never presented as model output.
	ProviderOffline = "offline"
)

// Config is the resolved settings for one run.
type Config struct {
	Mode       Mode
	Kubeconfig string
	Namespaces []string

	Provider    string
	Model       string
	BaseURL     string
	APIKey      string
	Temperature float64

	// Explain turns the LLM pass on. Detection never depends on it.
	Explain bool

	Addr        string
	DBPath      string
	CORSOrigins []string

	PendingThreshold time.Duration
	Cooldown         time.Duration

	// DemoSeed makes the simulator reproducible. Two runs with the same seed
	// produce the same cluster and the same incidents, which is what makes a
	// demo something you can rehearse.
	DemoSeed     int64
	DemoInterval time.Duration
	LogLines     int
	EventLimit   int
}

// Default returns the settings used when nothing overrides them.
//
// Demo mode is the default because the first thing anyone does with this tool
// is run it, and requiring a cluster before it will start is how a tool gets
// closed before it is understood.
func Default() Config {
	return Config{
		Mode:             ModeDemo,
		Provider:         ProviderOllama,
		Temperature:      0.1,
		Explain:          false,
		Addr:             "127.0.0.1:8080",
		DBPath:           "kubelens.db",
		CORSOrigins:      []string{"http://localhost:3000"},
		PendingThreshold: 5 * time.Minute,
		Cooldown:         5 * time.Minute,
		DemoSeed:         20260820,
		DemoInterval:     12 * time.Second,
		LogLines:         40,
		EventLimit:       10,
	}
}

// Validate checks the resolved settings.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeDemo, ModeCluster:
	default:
		return fmt.Errorf("unknown mode %q (want %s or %s)", c.Mode, ModeDemo, ModeCluster)
	}

	switch c.Provider {
	case ProviderOllama, ProviderClaude, ProviderOffline:
	default:
		return fmt.Errorf("unknown provider %q (want one of: %s, %s, %s)",
			c.Provider, ProviderOllama, ProviderClaude, ProviderOffline)
	}

	if c.Temperature < 0 || c.Temperature > 2 {
		return fmt.Errorf("temperature %v is outside the usable range [0,2]", c.Temperature)
	}
	if c.Addr == "" {
		return fmt.Errorf("an empty listen address would bind to every interface; set --addr")
	}
	if c.LogLines <= 0 {
		return fmt.Errorf("log-lines must be positive, got %d", c.LogLines)
	}
	if c.EventLimit <= 0 {
		return fmt.Errorf("event-limit must be positive, got %d", c.EventLimit)
	}
	if c.Mode == ModeCluster && c.Kubeconfig != "" {
		if _, err := os.Stat(c.Kubeconfig); err != nil {
			return fmt.Errorf("kubeconfig %s: %w", c.Kubeconfig, err)
		}
	}

	return nil
}

// ResolveKubeconfig applies the standard resolution order: an explicit path,
// then $KUBECONFIG, then ~/.kube/config.
//
// Returning an empty string is meaningful — client-go reads that as "use the
// in-cluster service account", which is what kubelens needs when it runs as a
// pod inside the cluster it is watching.
func (c Config) ResolveKubeconfig() string {
	if c.Kubeconfig != "" {
		return c.Kubeconfig
	}
	if fromEnv := os.Getenv("KUBECONFIG"); fromEnv != "" {
		// KUBECONFIG may list several files; client-go's loader handles that,
		// but the single-path form here takes the first entry.
		return strings.Split(fromEnv, string(os.PathListSeparator))[0]
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	candidate := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(candidate); err != nil {
		return ""
	}
	return candidate
}

// ResolveAPIKey reads the Anthropic key from the config or the environment.
func (c Config) ResolveAPIKey() string {
	if c.APIKey != "" {
		return c.APIKey
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

// AllowedOrigin reports whether an Origin header may read the API.
//
// The dashboard is served from a different port than the API in development,
// so some CORS is unavoidable; allowing exactly the configured origins rather
// than "*" keeps a random page in another tab from reading a cluster's incident
// history.
func (c Config) AllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	for _, allowed := range c.CORSOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}
