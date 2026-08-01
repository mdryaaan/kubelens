package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/llm"
	"github.com/mdryaaan/kubelens/internal/store"
	"github.com/mdryaaan/kubelens/pkg/version"
)

// HealthResponse is the overview page's headline payload.
type HealthResponse struct {
	Source   string          `json:"source"`
	Mode     string          `json:"mode"`
	UptimeMS int64           `json:"uptime_ms"`
	Cluster  ClusterSnapshot `json:"cluster"`
	// ClusterKnown is false when kubelens cannot count the cluster — an honest
	// "unknown" rather than a zero that reads as "no pods".
	ClusterKnown bool        `json:"cluster_known"`
	Stats        store.Stats `json:"stats"`
	Subscribers  int         `json:"subscribers"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-7 * 24 * time.Hour)
	stats, err := s.store.Stats(since)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, "")
		return
	}

	response := HealthResponse{
		Source:      s.sourceName,
		Mode:        string(s.cfg.Mode),
		UptimeMS:    time.Since(s.started).Milliseconds(),
		Stats:       stats,
		Subscribers: s.broker.Subscribers(),
	}
	if s.snapshot != nil {
		response.Cluster = s.snapshot()
		response.ClusterKnown = true
	}

	s.writeJSON(w, r, http.StatusOK, response)
}

func (s *Server) handleHealthSeries(w http.ResponseWriter, r *http.Request) {
	hours := intParam(r, "hours", 6)
	if hours <= 0 || hours > 24*30 {
		hours = 6
	}

	samples, err := s.store.Health(time.Now().UTC().Add(-time.Duration(hours)*time.Hour),
		intParam(r, "limit", 500))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, "")
		return
	}
	if samples == nil {
		samples = []store.HealthSample{}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"samples": samples,
		"hours":   hours,
	})
}

// IncidentsResponse is the list page's payload.
type IncidentsResponse struct {
	Incidents []store.Record `json:"incidents"`
	Total     int            `json:"total"`
	Limit     int            `json:"limit"`
	Offset    int            `json:"offset"`
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	filter := store.Filter{
		Category:       strings.TrimSpace(r.URL.Query().Get("category")),
		Severity:       strings.TrimSpace(r.URL.Query().Get("severity")),
		Namespace:      strings.TrimSpace(r.URL.Query().Get("namespace")),
		UnresolvedOnly: r.URL.Query().Get("unresolved") == "true",
		Limit:          intParam(r, "limit", store.DefaultLimit),
		Offset:         intParam(r, "offset", 0),
	}

	// An unknown category would silently return nothing, which reads as "the
	// cluster is fine" — the most dangerous way for this endpoint to be wrong.
	if filter.Category != "" && !detector.Category(filter.Category).Valid() {
		s.writeError(w, r, http.StatusBadRequest,
			fmt.Errorf("unknown category %q", filter.Category),
			"valid categories: "+categoryList())
		return
	}

	if hours := intParam(r, "hours", 0); hours > 0 {
		filter.Since = time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	}

	records, err := s.store.Incidents(filter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, "")
		return
	}
	if records == nil {
		records = []store.Record{}
	}

	s.writeJSON(w, r, http.StatusOK, IncidentsResponse{
		Incidents: records,
		Total:     len(records),
		Limit:     filter.NormalisedLimit(),
		Offset:    filter.Offset,
	})
}

func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	record, err := s.store.Incident(id)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, http.StatusNotFound, err, "")
		return
	}
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, "")
		return
	}

	s.writeJSON(w, r, http.StatusOK, record)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if err := s.store.Resolve(id, time.Now().UTC()); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		s.writeError(w, r, status, err, "")
		return
	}

	if s.engine != nil {
		if record, err := s.store.Incident(id); err == nil {
			s.engine.Resolve(record.Incident.Fingerprint)
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"id": id, "resolved": true})
}

// handleExplain generates an explanation on demand.
//
// Explanation is deliberately a separate call rather than something that
// happens automatically on every incident: it costs an inference, and a cluster
// having a bad afternoon can produce hundreds of incidents. The dashboard asks
// for the one a human is actually looking at.
func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.explainer == nil {
		s.writeError(w, r, http.StatusServiceUnavailable,
			errors.New("no explanation provider is configured"),
			"start kubelens with --explain, or set provider=offline to use the baseline")
		return
	}

	record, err := s.store.Incident(id)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, http.StatusNotFound, err, "")
		return
	}
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, "")
		return
	}

	exp, err := s.explainer.Explain(r.Context(), bundleFrom(record))
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err,
			"the provider could not be reached; detection is unaffected")
		return
	}

	if err := s.store.SaveExplanation(exp); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err, "")
		return
	}

	s.PublishExplanation(exp)
	s.writeJSON(w, r, http.StatusOK, exp)
}

// SettingsResponse describes how this instance is configured.
type SettingsResponse struct {
	Mode     string `json:"mode"`
	Source   string `json:"source"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Disclaimer is non-empty when the configured provider is not a model. The
	// settings page renders it, so nobody mistakes baseline output for
	// inference.
	Disclaimer       string   `json:"disclaimer,omitempty"`
	ExplainEnabled   bool     `json:"explain_enabled"`
	Categories       []string `json:"categories"`
	PendingThreshold string   `json:"pending_threshold"`
	Cooldown         string   `json:"cooldown"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	response := SettingsResponse{
		Mode:             string(s.cfg.Mode),
		Source:           s.sourceName,
		Provider:         s.cfg.Provider,
		ExplainEnabled:   s.explainer != nil,
		Categories:       categoryNames(),
		PendingThreshold: s.cfg.PendingThreshold.String(),
		Cooldown:         s.cfg.Cooldown.String(),
	}

	if s.explainer != nil && s.explainer.Provider() != nil {
		provider := s.explainer.Provider()
		response.Provider = provider.Name()
		response.Model = provider.Model()
		if provider.Name() == llm.ProviderOffline {
			response.Disclaimer = llm.OfflineDisclaimer
		}
	}

	s.writeJSON(w, r, http.StatusOK, response)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, version.Current())
}

// withLogging records each request at debug level, and slow ones at info.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)

		// The SSE endpoint is long-lived by design; logging its duration would
		// report every dashboard session as a multi-hour slow request.
		if r.URL.Path == "/api/stream" {
			return
		}

		elapsed := time.Since(started)
		level := slog.LevelDebug
		if elapsed > time.Second {
			level = slog.LevelInfo
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path, "duration", elapsed)
	})
}

// withRecovery turns a handler panic into a 500 instead of killing the process.
//
// kubelens watches clusters unattended for hours. One malformed record must not
// take the whole watcher down with the HTTP server.
func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("handler panicked",
					"path", r.URL.Path, "panic", recovered, "stack", string(debug.Stack()))
				s.writeError(w, r, http.StatusInternalServerError,
					fmt.Errorf("internal error handling %s", r.URL.Path), "")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func intParam(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func categoryNames() []string {
	out := make([]string, 0, len(detector.AllCategories()))
	for _, category := range detector.AllCategories() {
		out = append(out, string(category))
	}
	return out
}

func categoryList() string { return strings.Join(categoryNames(), ", ") }
