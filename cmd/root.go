// Package cmd defines kubelens's command line interface.
package cmd

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mdryaaan/kubelens/internal/config"
	"github.com/mdryaaan/kubelens/pkg/version"
)

// embedded holds the eval corpus compiled into the binary.
//
// It is injected from main rather than embedded here because `go:embed` cannot
// reach outside its own package directory, and keeping one copy at the
// repository root is what stops the shipped corpus from drifting away from the
// one the tests read.
var embeddedEval fs.FS

// SetEmbedded registers the compiled-in eval corpus.
func SetEmbedded(evalCorpus fs.FS) { embeddedEval = evalCorpus }

// flags holds the values every command shares.
var flags struct {
	Demo       bool
	Kubeconfig string
	Namespaces []string

	Provider    string
	Model       string
	BaseURL     string
	Temperature float64
	Explain     bool

	Addr    string
	DBPath  string
	Origins []string

	PendingThreshold time.Duration
	Cooldown         time.Duration

	Seed         int64
	DemoInterval time.Duration
	LogLines     int
	EventLimit   int

	Verbose bool
}

// NewRootCommand builds the command tree.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "kubelens",
		Short: "Watch a Kubernetes cluster, detect real failures, and explain them with cited evidence",
		Long: `kubelens watches a Kubernetes cluster, detects the failure patterns that
actually take workloads down, and explains each one in plain English — with every
claim tied to the log line or event it was based on.

Detection is deterministic: six rules read container statuses, events, and
deployment conditions, and they work whether or not any model is available. The
explanation layer is an addition on top, and every line it quotes is verified
against the evidence before you see it.

Run it against a real cluster with --kubeconfig, or against the built-in
simulator with --demo, which needs no cluster at all.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Current().String(),
	}

	persistent := root.PersistentFlags()
	persistent.BoolVar(&flags.Demo, "demo", false, "run the built-in simulator instead of a real cluster")
	persistent.StringVar(&flags.Kubeconfig, "kubeconfig", "", "path to a kubeconfig (default: $KUBECONFIG, then ~/.kube/config)")
	persistent.StringSliceVar(&flags.Namespaces, "namespace", nil, "namespaces to watch (default: all)")

	persistent.StringVar(&flags.Provider, "provider", "", "explanation provider: ollama, claude, or offline")
	persistent.StringVar(&flags.Model, "model", "", "model name for the provider")
	persistent.StringVar(&flags.BaseURL, "base-url", "", "override the provider endpoint")
	persistent.Float64Var(&flags.Temperature, "temperature", -1, "sampling temperature for explanations")
	persistent.BoolVar(&flags.Explain, "explain", false, "explain incidents as they are detected")

	persistent.StringVar(&flags.Addr, "addr", "", "address the API listens on")
	persistent.StringVar(&flags.DBPath, "db", "", "path to the incident database")
	persistent.StringSliceVar(&flags.Origins, "cors-origin", nil, "origins allowed to read the API")

	persistent.DurationVar(&flags.PendingThreshold, "pending-threshold", 0, "how long a pod may stay Pending before it counts")
	persistent.DurationVar(&flags.Cooldown, "cooldown", 0, "how long the same incident is suppressed after firing")

	persistent.Int64Var(&flags.Seed, "seed", 0, "seed for --demo, so a run is reproducible")
	persistent.DurationVar(&flags.DemoInterval, "demo-interval", 0, "how often --demo injects a failure")
	persistent.IntVar(&flags.LogLines, "log-lines", 0, "how many log lines each incident may carry")
	persistent.IntVar(&flags.EventLimit, "event-limit", 0, "how many events each incident may carry")

	persistent.BoolVarP(&flags.Verbose, "verbose", "v", false, "log at debug level")

	root.AddCommand(newServeCommand(), newEvalCommand(), newVersionCommand())
	return root
}

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "kubelens: %v\n", err)
		return 1
	}
	return 0
}

// resolveConfig layers flags over the defaults.
func resolveConfig(cmd *cobra.Command) (config.Config, error) {
	cfg := config.Default()
	set := cmd.Flags()

	if flags.Demo {
		cfg.Mode = config.ModeDemo
	}
	if set.Changed("kubeconfig") {
		cfg.Mode = config.ModeCluster
		cfg.Kubeconfig = flags.Kubeconfig
	}
	if len(flags.Namespaces) > 0 {
		cfg.Namespaces = flags.Namespaces
	}

	if set.Changed("provider") {
		cfg.Provider = flags.Provider
	}
	if set.Changed("model") {
		cfg.Model = flags.Model
	}
	if set.Changed("base-url") {
		cfg.BaseURL = flags.BaseURL
	}
	if set.Changed("temperature") {
		cfg.Temperature = flags.Temperature
	}
	if set.Changed("explain") {
		cfg.Explain = flags.Explain
	}

	if set.Changed("addr") {
		cfg.Addr = flags.Addr
	}
	if set.Changed("db") {
		cfg.DBPath = flags.DBPath
	}
	if len(flags.Origins) > 0 {
		cfg.CORSOrigins = flags.Origins
	}

	if set.Changed("pending-threshold") {
		cfg.PendingThreshold = flags.PendingThreshold
	}
	if set.Changed("cooldown") {
		cfg.Cooldown = flags.Cooldown
	}
	if set.Changed("seed") {
		cfg.DemoSeed = flags.Seed
	}
	if set.Changed("demo-interval") {
		cfg.DemoInterval = flags.DemoInterval
	}
	if set.Changed("log-lines") {
		cfg.LogLines = flags.LogLines
	}
	if set.Changed("event-limit") {
		cfg.EventLimit = flags.EventLimit
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// newLogger builds the structured logger.
func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if flags.Verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
