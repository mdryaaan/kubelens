package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mdryaaan/kubelens/internal/api"
	"github.com/mdryaaan/kubelens/internal/config"
	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
	"github.com/mdryaaan/kubelens/internal/llm"
	"github.com/mdryaaan/kubelens/internal/simulator"
	"github.com/mdryaaan/kubelens/internal/store"
	"github.com/mdryaaan/kubelens/internal/watcher"
)

func newServeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Watch a cluster and serve the API and live incident stream",
		Long: `Serve starts the whole pipeline: the watcher feeds the detector, detected
incidents are stored with the evidence that explains them, and the API streams them
to the dashboard as they happen.

With --demo it runs against the built-in simulator, which needs no cluster, no
kubeconfig, and no network.`,
		Args: cobra.NoArgs,
		Example: `  kubelens serve --demo
  kubelens serve --demo --explain --provider offline
  kubelens serve --kubeconfig ~/.kube/config --namespace payments
  kubelens serve --kubeconfig ~/.kube/config --explain --provider ollama`,
		RunE: runServe,
	}
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return err
	}
	log := newLogger()

	// Ctrl-C has to reach every goroutine, or the process hangs on a watcher
	// that is still blocked on an informer.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	pipeline, err := buildPipeline(cfg, db, log)
	if err != nil {
		return err
	}

	server := api.NewServer(api.Options{
		Config: cfg, Store: db, Detector: pipeline.engine,
		Explainer: pipeline.explainer, Snapshot: pipeline.snapshot,
		SourceName: pipeline.source.Name(), Logger: log,
	})
	pipeline.server = server

	log.Info("kubelens starting",
		"mode", cfg.Mode, "source", pipeline.source.Name(),
		"explain", cfg.Explain, "provider", cfg.Provider, "db", cfg.DBPath)

	errc := make(chan error, 2)

	events := make(chan watcher.WatchEvent, 512)
	go func() {
		if err := pipeline.source.Run(ctx, events); err != nil {
			errc <- fmt.Errorf("watcher: %w", err)
		}
	}()

	go pipeline.consume(ctx, events)
	go pipeline.sampleHealth(ctx)

	go func() {
		if err := server.ListenAndServe(ctx); err != nil {
			errc <- fmt.Errorf("api: %w", err)
		}
	}()

	fmt.Fprintf(cmd.OutOrStdout(), "kubelens API      http://%s/api/health\n", cfg.Addr)
	fmt.Fprintf(cmd.OutOrStdout(), "live incidents    http://%s/api/stream\n", cfg.Addr)
	fmt.Fprintf(cmd.OutOrStdout(), "dashboard         http://localhost:3000  (cd web && npm run dev)\n")

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		// A brief grace period lets the API finish in-flight responses rather
		// than dropping a dashboard's request mid-render.
		time.Sleep(200 * time.Millisecond)
		return nil
	}
}

// pipeline holds everything one serve run needs.
type pipeline struct {
	cfg       config.Config
	log       *slog.Logger
	store     store.Store
	engine    *detector.Engine
	builder   *kcontext.Builder
	explainer *explanation.Engine
	source    watcher.Source
	snapshot  api.SnapshotFunc
	server    *api.Server
}

// buildPipeline wires the source, detector, context builder, and explainer.
//
// The only thing that differs between demo and cluster mode is where the
// events and evidence come from. Everything downstream is identical, which is
// what makes the demo an honest preview of the product rather than a mock of it.
func buildPipeline(cfg config.Config, db store.Store, log *slog.Logger) (*pipeline, error) {
	p := &pipeline{
		cfg:   cfg,
		log:   log,
		store: db,
		engine: detector.NewEngine(detector.Options{
			Cooldown:         cfg.Cooldown,
			PendingThreshold: cfg.PendingThreshold,
		}),
	}

	options := kcontext.Options{LogLines: cfg.LogLines, EventLimit: cfg.EventLimit}

	switch cfg.Mode {
	case config.ModeDemo:
		sim := simulator.New(simulator.Options{
			Seed:     cfg.DemoSeed,
			Interval: cfg.DemoInterval,
		})
		p.source = sim
		p.builder = kcontext.NewBuilder(sim.Logs(), sim.Events(), options)
		p.snapshot = func() api.ClusterSnapshot {
			snap := sim.Snapshot()
			return api.ClusterSnapshot{
				Nodes: snap.Nodes, TotalPods: snap.TotalPods,
				UnhealthyPods: snap.UnhealthyPods, Deployments: snap.Deployments,
			}
		}

	case config.ModeCluster:
		client, err := kubernetesClient(cfg)
		if err != nil {
			return nil, err
		}
		p.source = watcher.NewClusterWatcher(client, watcher.Options{
			Namespaces: cfg.Namespaces,
			Resync:     10 * time.Minute,
		})
		p.builder = kcontext.NewBuilder(
			kcontext.NewPodLogFetcher(client), kcontext.NewAPIEventSource(client), options)
	}

	if cfg.Explain {
		provider, err := llm.New(llm.Options{
			Provider: cfg.Provider, Model: cfg.Model,
			BaseURL: cfg.BaseURL, APIKey: cfg.ResolveAPIKey(),
			Temperature: cfg.Temperature,
		})
		if err != nil {
			return nil, err
		}
		p.explainer = explanation.NewEngine(provider)

		if provider.Name() == llm.ProviderOffline {
			log.Warn(llm.OfflineDisclaimer)
		}
	}

	return p, nil
}

// kubernetesClient builds a client from the resolved kubeconfig.
func kubernetesClient(cfg config.Config) (kubernetes.Interface, error) {
	path := cfg.ResolveKubeconfig()

	// An empty path means in-cluster: the standard loader reads the service
	// account kubelens is running under, which is how it works as a pod.
	restConfig, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig (%s): %w", displayPath(path), err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return client, nil
}

func displayPath(path string) string {
	if path == "" {
		return "in-cluster"
	}
	return path
}

// consume runs the detector over the event stream and persists what it finds.
func (p *pipeline) consume(ctx context.Context, events <-chan watcher.WatchEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				return
			}
			for _, incident := range p.engine.Process(event) {
				p.handle(ctx, incident, event)
			}
		}
	}
}

// handle gathers evidence for one incident, stores it, and streams it out.
func (p *pipeline) handle(ctx context.Context, incident detector.Incident, event watcher.WatchEvent) {
	bundle, err := p.builder.Build(ctx, incident, event)
	if err != nil {
		// No evidence is a degraded incident, not a discarded one: the
		// detection itself is still true and still worth showing.
		p.log.Debug("no evidence available", "resource", incident.Resource, "error", err)
	}

	record := api.BuildRecord(incident, bundle)
	if err := p.store.SaveIncident(record); err != nil {
		p.log.Error("saving incident", "id", incident.ID, "error", err)
		return
	}

	p.log.Info("incident detected",
		"category", incident.Category, "severity", incident.Severity,
		"resource", incident.Resource, "namespace", incident.Namespace)

	if p.server != nil {
		p.server.PublishIncident(record)
	}

	if p.explainer == nil {
		return
	}

	// Explanation runs detached from the detector loop. A model can take
	// seconds, and blocking here would stall detection of everything else
	// happening in the cluster meanwhile.
	go p.explain(ctx, bundle)
}

func (p *pipeline) explain(ctx context.Context, bundle kcontext.IncidentContext) {
	// The parent context is cancelled at shutdown, which would abandon an
	// in-flight explanation; a bounded independent one lets it finish.
	explainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()

	exp, err := p.explainer.Explain(explainCtx, bundle)
	if err != nil {
		p.log.Warn("could not explain incident",
			"resource", bundle.Incident.Resource, "error", err)
		return
	}

	if err := p.store.SaveExplanation(exp); err != nil {
		p.log.Error("saving explanation", "id", exp.IncidentID, "error", err)
		return
	}

	if exp.Fabricated() {
		// Worth a log line of its own: it is the number that says whether the
		// configured model can be trusted on this cluster.
		p.log.Warn("dropped fabricated citations",
			"resource", bundle.Incident.Resource, "count", len(exp.Rejected))
	}

	if p.server != nil {
		p.server.PublishExplanation(exp)
	}
}

// sampleHealth records a cluster health point on a fixed cadence, which is what
// the dashboard's chart plots.
func (p *pipeline) sampleHealth(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	record := func() {
		sample := store.HealthSample{
			SampledAt:     time.Now().UTC().Truncate(time.Second),
			OpenIncidents: len(p.engine.Active()),
		}
		if p.snapshot != nil {
			snap := p.snapshot()
			sample.TotalPods = snap.TotalPods
			sample.UnhealthyPods = snap.UnhealthyPods
			sample.Nodes = snap.Nodes
		}

		if err := p.store.SaveHealth(sample); err != nil {
			p.log.Debug("saving health sample", "error", err)
			return
		}
		if p.server != nil {
			p.server.PublishHealth(sample)
		}
	}

	record()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			record()
		}
	}
}
