// Package api serves the REST endpoints and the live incident stream the
// dashboard reads.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mdryaaan/kubelens/internal/config"
	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
	"github.com/mdryaaan/kubelens/internal/store"
)

// ClusterSnapshot describes the shape of the watched cluster.
type ClusterSnapshot struct {
	Nodes         int `json:"nodes"`
	TotalPods     int `json:"total_pods"`
	UnhealthyPods int `json:"unhealthy_pods"`
	Deployments   int `json:"deployments"`
}

// SnapshotFunc reports the current cluster shape. Nil means unknown, which the
// API reports honestly rather than as zero.
type SnapshotFunc func() ClusterSnapshot

// Server holds the API dependencies.
type Server struct {
	cfg        config.Config
	store      store.Store
	broker     *Broker
	engine     *detector.Engine
	explainer  *explanation.Engine
	snapshot   SnapshotFunc
	sourceName string
	log        *slog.Logger
	started    time.Time
}

// Options wires a server.
type Options struct {
	Config     config.Config
	Store      store.Store
	Broker     *Broker
	Detector   *detector.Engine
	Explainer  *explanation.Engine
	Snapshot   SnapshotFunc
	SourceName string
	Logger     *slog.Logger
}

// NewServer builds the API server.
func NewServer(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	broker := opts.Broker
	if broker == nil {
		broker = NewBroker()
	}

	return &Server{
		cfg:        opts.Config,
		store:      opts.Store,
		broker:     broker,
		engine:     opts.Detector,
		explainer:  opts.Explainer,
		snapshot:   opts.Snapshot,
		sourceName: opts.SourceName,
		log:        logger,
		started:    time.Now().UTC(),
	}
}

// Broker exposes the event broker, so the pipeline can publish to it.
func (s *Server) Broker() *Broker { return s.broker }

// PublishIncident pushes a newly detected incident to connected dashboards.
func (s *Server) PublishIncident(record store.Record) {
	s.broker.Publish(Event{Type: "incident", Data: record})
}

// PublishExplanation pushes a completed explanation.
//
// Separate from the incident event because explanation is asynchronous and may
// take a model several seconds: the dashboard shows the incident the moment it
// is detected, then fills in the analysis when it arrives, rather than delaying
// the alert until the prose is ready.
func (s *Server) PublishExplanation(exp explanation.Explanation) {
	s.broker.Publish(Event{Type: "explanation", Data: exp})
}

// PublishHealth pushes a health sample.
func (s *Server) PublishHealth(sample store.HealthSample) {
	s.broker.Publish(Event{Type: "health", Data: sample})
}

// ListenAndServe runs the HTTP server until the context is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	server := &http.Server{
		Addr:    s.cfg.Addr,
		Handler: s.Router(),
		// SSE connections are long-lived by design, so no write timeout is set;
		// the read timeout still bounds a client that opens a socket and never
		// sends a request.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		s.log.Info("api listening", "addr", s.cfg.Addr, "source", s.sourceName)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// writeJSON renders a value as JSON with a status code.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	s.applyCORS(w, r)
	w.WriteHeader(status)

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		s.log.Error("encoding response", "path", r.URL.Path, "error", err)
	}
}

// apiError is the error shape every failing endpoint returns.
type apiError struct {
	Error string `json:"error"`
	// Hint is what to do about it, when there is something useful to say.
	Hint string `json:"hint,omitempty"`
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, err error, hint string) {
	if status >= http.StatusInternalServerError {
		s.log.Error("request failed", "path", r.URL.Path, "status", status, "error", err)
	}
	s.writeJSON(w, r, status, apiError{Error: err.Error(), Hint: hint})
}

// applyCORS sets the headers the dashboard needs in development.
//
// The dashboard runs on a different port than the API while developing, so some
// CORS is unavoidable. Echoing only configured origins rather than "*" keeps a
// page in an unrelated tab from reading a cluster's incident history.
func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !s.cfg.AllowedOrigin(origin) {
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Vary", "Origin")
}

// BuildRecord assembles the record written to the store and pushed over SSE.
func BuildRecord(incident detector.Incident, bundle kcontext.IncidentContext) store.Record {
	return store.Record{
		Incident: incident,
		Evidence: store.Evidence{
			Logs:   bundle.Logs,
			Events: bundle.Events,
			Spec:   bundle.Spec,
		},
	}
}

// bundleFrom rebuilds an evidence bundle from a stored record.
//
// Explaining an incident hours later has to use the evidence captured at
// detection time, not whatever the cluster looks like now — the pod is very
// likely gone, and an explanation citing a log that no longer exists is one
// nobody can check.
func bundleFrom(record store.Record) kcontext.IncidentContext {
	return kcontext.IncidentContext{
		Incident: record.Incident,
		Spec:     record.Evidence.Spec,
		Logs:     record.Evidence.Logs,
		Events:   record.Evidence.Events,
	}
}
