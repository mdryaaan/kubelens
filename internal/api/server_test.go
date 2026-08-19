package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdryaaan/kubelens/internal/config"
	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
	"github.com/mdryaaan/kubelens/internal/llm"
	"github.com/mdryaaan/kubelens/internal/store"
)

func newTestServer(t *testing.T, explainer *explanation.Engine) (*Server, store.Store) {
	t.Helper()

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Default()
	server := NewServer(Options{
		Config: cfg, Store: db, Explainer: explainer,
		SourceName: "simulated cluster (seed 1)",
		Snapshot: func() ClusterSnapshot {
			return ClusterSnapshot{Nodes: 4, TotalPods: 19, UnhealthyPods: 2, Deployments: 8}
		},
	})
	return server, db
}

func seedIncident(t *testing.T, db store.Store, id string, category detector.Category) store.Record {
	t.Helper()

	record := store.Record{
		Incident: detector.Incident{
			ID: id, Fingerprint: id, Category: category, Severity: detector.Critical,
			Namespace: "payments", Resource: "pod/payment-api-7d9f", Container: "api",
			Title: "Container api was OOMKilled", Detail: "exit code 137",
			DetectedAt: time.Now().UTC(), FirstSeen: time.Now().UTC().Add(-time.Minute), Count: 1,
		},
		Evidence: store.Evidence{
			Logs: []kcontext.LogLine{
				{Number: 1, Text: "INFO  starting payment-api"},
				{Number: 2, Text: "FATAL java.lang.OutOfMemoryError: Java heap space"},
			},
			Events: []kcontext.EventRecord{{
				Type: "Warning", Reason: "BackOff",
				Message: "Back-off restarting failed container", Timestamp: time.Now().UTC(),
			}},
			Spec: kcontext.ResourceSpec{MemoryLimit: "512Mi", Image: "ghcr.io/acme/payment-api:1.4.2"},
		},
	}
	if err := db.SaveIncident(record); err != nil {
		t.Fatalf("seeding incident: %v", err)
	}
	return record
}

func doRequest(t *testing.T, server *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	server, db := newTestServer(t, nil)
	seedIncident(t, db, "a", detector.OOMKilled)

	rec := doRequest(t, server, http.MethodGet, "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var got HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got.Cluster.TotalPods != 19 || !got.ClusterKnown {
		t.Errorf("cluster snapshot missing: %+v", got)
	}
	if got.Stats.TotalIncidents != 1 {
		t.Errorf("stats = %+v", got.Stats)
	}
	if got.Mode != string(config.ModeDemo) {
		t.Errorf("mode = %q", got.Mode)
	}
}

// A zero pod count reads as "no pods"; unknown has to be distinguishable.
func TestHealthReportsUnknownClusterHonestly(t *testing.T) {
	db, _ := store.Open(":memory:")
	defer func() { _ = db.Close() }()

	server := NewServer(Options{Config: config.Default(), Store: db})
	rec := doRequest(t, server, http.MethodGet, "/api/health")

	var got HealthResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.ClusterKnown {
		t.Error("an unknown cluster was reported as known")
	}
}

func TestIncidentsListAndFilters(t *testing.T) {
	server, db := newTestServer(t, nil)
	seedIncident(t, db, "a", detector.OOMKilled)
	seedIncident(t, db, "b", detector.CrashLoopBackOff)

	rec := doRequest(t, server, http.MethodGet, "/api/incidents")
	var all IncidentsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if all.Total != 2 {
		t.Errorf("total = %d, want 2", all.Total)
	}

	rec = doRequest(t, server, http.MethodGet, "/api/incidents?category=OOMKilled")
	var filtered IncidentsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &filtered)
	if filtered.Total != 1 {
		t.Errorf("filtered total = %d, want 1", filtered.Total)
	}
}

// Returning nothing for a typo reads as "the cluster is fine", which is the
// most dangerous way this endpoint could be wrong.
func TestIncidentsRejectsAnUnknownCategory(t *testing.T) {
	server, _ := newTestServer(t, nil)

	rec := doRequest(t, server, http.MethodGet, "/api/incidents?category=DiskFull")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var apiErr apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	if !strings.Contains(apiErr.Hint, "OOMKilled") {
		t.Errorf("the error does not list the valid categories: %+v", apiErr)
	}
}

func TestIncidentDetailCarriesEvidence(t *testing.T) {
	server, db := newTestServer(t, nil)
	seedIncident(t, db, "abc123", detector.OOMKilled)

	rec := doRequest(t, server, http.MethodGet, "/api/incidents/abc123")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var record store.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	// The detail page highlights cited lines, so it needs the numbered logs.
	if len(record.Evidence.Logs) != 2 || record.Evidence.Logs[1].Number != 2 {
		t.Errorf("evidence did not reach the detail response: %+v", record.Evidence)
	}
	if record.Evidence.Spec.MemoryLimit != "512Mi" {
		t.Errorf("spec missing from the detail response: %+v", record.Evidence.Spec)
	}
}

func TestIncidentNotFound(t *testing.T) {
	server, _ := newTestServer(t, nil)
	if rec := doRequest(t, server, http.MethodGet, "/api/incidents/missing"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestResolve(t *testing.T) {
	server, db := newTestServer(t, nil)
	seedIncident(t, db, "abc123", detector.OOMKilled)

	if rec := doRequest(t, server, http.MethodPost, "/api/incidents/abc123/resolve"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	record, _ := db.Incident("abc123")
	if !record.Incident.Resolved {
		t.Error("the incident was not resolved")
	}

	if rec := doRequest(t, server, http.MethodPost, "/api/incidents/nope/resolve"); rec.Code != http.StatusNotFound {
		t.Errorf("resolving an unknown id returned %d", rec.Code)
	}
}

// Explaining an incident hours later must use the evidence captured at
// detection time — the pod is very likely gone.
func TestExplainUsesStoredEvidence(t *testing.T) {
	server, db := newTestServer(t, explanation.NewEngine(llm.NewOffline()))
	seedIncident(t, db, "abc123", detector.OOMKilled)

	rec := doRequest(t, server, http.MethodPost, "/api/incidents/abc123/explain")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var exp explanation.Explanation
	if err := json.Unmarshal(rec.Body.Bytes(), &exp); err != nil {
		t.Fatal(err)
	}
	if exp.Category != detector.OOMKilled {
		t.Errorf("category = %s", exp.Category)
	}
	if len(exp.Citations) == 0 {
		t.Error("the explanation cited nothing from the stored evidence")
	}
	// Baseline output must be labelled wherever it is served.
	if exp.Disclaimer == "" {
		t.Error("baseline output was served without its disclaimer")
	}

	// And it must have been persisted, not only returned.
	stored, _ := db.Incident("abc123")
	if stored.Explanation == nil {
		t.Error("the explanation was not saved")
	}
}

func TestExplainWithoutAProviderSaysSo(t *testing.T) {
	server, db := newTestServer(t, nil)
	seedIncident(t, db, "abc123", detector.OOMKilled)

	rec := doRequest(t, server, http.MethodPost, "/api/incidents/abc123/explain")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var apiErr apiError
	_ = json.Unmarshal(rec.Body.Bytes(), &apiErr)
	if !strings.Contains(apiErr.Hint, "--explain") {
		t.Errorf("the error does not say how to enable it: %+v", apiErr)
	}
}

func TestSettingsCarriesTheBaselineDisclaimer(t *testing.T) {
	server, _ := newTestServer(t, explanation.NewEngine(llm.NewOffline()))

	rec := doRequest(t, server, http.MethodGet, "/api/settings")
	var settings SettingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}

	if settings.Provider != llm.ProviderOffline {
		t.Errorf("provider = %q", settings.Provider)
	}
	if !strings.Contains(settings.Disclaimer, "not by an LLM") {
		t.Errorf("the settings page would show baseline output unlabelled: %+v", settings)
	}
	if len(settings.Categories) != 6 {
		t.Errorf("got %d categories, want 6", len(settings.Categories))
	}
}

func TestUnknownRouteReturnsJSON(t *testing.T) {
	server, _ := newTestServer(t, nil)

	rec := doRequest(t, server, http.MethodGet, "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type = %q, want JSON so a fetch can parse it", ct)
	}
}

// A page in an unrelated tab must not be able to read a cluster's history.
func TestCORSOnlyEchoesAllowedOrigins(t *testing.T) {
	server, _ := newTestServer(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rec := httptest.NewRecorder()
	server.Router().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("the dashboard origin was not allowed: %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	server.Router().ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("an unlisted origin was allowed: %q", got)
	}
}

// A handler panic must not take the watcher down with the HTTP server.
func TestPanicIsContained(t *testing.T) {
	server, _ := newTestServer(t, nil)

	handler := server.withRecovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestBrokerFansOutAndDropsSlowSubscribers(t *testing.T) {
	broker := NewBroker()

	first, closeFirst := broker.Subscribe()
	second, closeSecond := broker.Subscribe()
	defer closeFirst()
	defer closeSecond()

	if broker.Subscribers() != 2 {
		t.Fatalf("subscribers = %d, want 2", broker.Subscribers())
	}

	broker.Publish(Event{Type: "incident", Data: map[string]string{"id": "a"}})

	for _, ch := range []<-chan Event{first, second} {
		select {
		case event := <-ch:
			if event.Type != "incident" {
				t.Errorf("type = %q", event.Type)
			}
		case <-time.After(time.Second):
			t.Fatal("a subscriber did not receive the event")
		}
	}

	// A subscriber that never reads must not block the publisher.
	done := make(chan struct{})
	go func() {
		for i := 0; i < broker.buffer*3; i++ {
			broker.Publish(Event{Type: "incident", Data: i})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	broker := NewBroker()
	_, unsubscribe := broker.Subscribe()

	unsubscribe()
	unsubscribe() // must not panic on a double close
	if broker.Subscribers() != 0 {
		t.Errorf("subscribers = %d after unsubscribe", broker.Subscribers())
	}
}

// The stream is what makes the dashboard live.
func TestStreamPushesIncidents(t *testing.T) {
	server, _ := newTestServer(t, nil)

	httpServer := httptest.NewServer(server.Router())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connecting to the stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)

	// The client is told it is connected before anything breaks, so the UI can
	// show a live indicator immediately.
	if line, _ := reader.ReadString('\n'); !strings.Contains(line, "hello") {
		t.Errorf("first frame = %q, want the hello event", line)
	}

	// Give the subscription a moment to register before publishing.
	deadline := time.Now().Add(2 * time.Second)
	for server.broker.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	server.PublishIncident(store.Record{
		Incident: detector.Incident{ID: "live-1", Category: detector.OOMKilled},
	})

	found := false
	for i := 0; i < 12 && !found; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, "live-1") || strings.Contains(line, "incident") {
			found = true
		}
	}
	if !found {
		t.Error("the published incident never reached the stream")
	}
}
