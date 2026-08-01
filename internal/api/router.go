package api

import (
	"net/http"
)

// Router builds the HTTP routes.
//
// Go 1.22's ServeMux understands method and wildcard patterns, which covers
// everything this API needs. A third-party router would be a dependency bought
// for syntax rather than capability.
func (s *Server) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/health/series", s.handleHealthSeries)
	mux.HandleFunc("GET /api/incidents", s.handleIncidents)
	mux.HandleFunc("GET /api/incidents/{id}", s.handleIncident)
	mux.HandleFunc("POST /api/incidents/{id}/resolve", s.handleResolve)
	mux.HandleFunc("POST /api/incidents/{id}/explain", s.handleExplain)
	mux.HandleFunc("GET /api/settings", s.handleSettings)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/stream", s.streamHandler)

	// The browser sends a preflight before any cross-origin request carrying a
	// Content-Type, which the POST endpoints do.
	mux.HandleFunc("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) {
		s.applyCORS(w, r)
		w.WriteHeader(http.StatusNoContent)
	})

	// Anything outside /api gets a JSON 404 rather than net/http's plain-text
	// default, so a dashboard fetch always has a body it can parse.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, r, http.StatusNotFound, errNotFound(r.URL.Path),
			"the API is served under /api")
	})

	return s.withRecovery(s.withLogging(mux))
}

type notFoundError string

func (e notFoundError) Error() string { return string(e) }

func errNotFound(path string) error { return notFoundError("no route for " + path) }
