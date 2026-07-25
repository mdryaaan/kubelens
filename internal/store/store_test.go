package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	kcontext "github.com/mdryaaan/kubelens/internal/context"
	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
)

var now = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) *SQLite {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "kubelens.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sampleRecord(id string, category detector.Category, at time.Time) Record {
	return Record{
		Incident: detector.Incident{
			ID: id, Fingerprint: id, Category: category, Severity: detector.Critical,
			Namespace: "payments", Resource: "pod/payment-api-7d9f", Container: "api",
			Title: "Container api was OOMKilled", Detail: "exit code 137",
			DetectedAt: at, FirstSeen: at.Add(-90 * time.Second), Count: 1,
		},
		Evidence: Evidence{
			Logs: []kcontext.LogLine{
				{Number: 1, Text: "FATAL java.lang.OutOfMemoryError: Java heap space"},
			},
			Events: []kcontext.EventRecord{
				{Type: "Warning", Reason: "BackOff", Message: "Back-off restarting", Timestamp: at},
			},
			Spec: kcontext.ResourceSpec{MemoryLimit: "512Mi"},
		},
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubelens.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open failed: %v", err)
	}
	version, err := SchemaVersion(first.DB())
	if err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Errorf("schema version = %d, want %d", version, len(migrations))
	}
	_ = first.Close()

	// Re-opening must not re-apply anything, which is what the version table
	// exists to guarantee.
	second, err := Open(path)
	if err != nil {
		t.Fatalf("re-opening a migrated database failed: %v", err)
	}
	defer func() { _ = second.Close() }()

	again, err := SchemaVersion(second.DB())
	if err != nil {
		t.Fatal(err)
	}
	if again != version {
		t.Errorf("schema version moved on reopen: %d -> %d", version, again)
	}
}

// The evidence has to outlive the pod it came from, or an explanation's
// citations become unresolvable as soon as Kubernetes garbage-collects.
func TestSaveAndLoadIncidentKeepsEvidence(t *testing.T) {
	store := openTestStore(t)

	if err := store.SaveIncident(sampleRecord("abc123", detector.OOMKilled, now)); err != nil {
		t.Fatalf("SaveIncident failed: %v", err)
	}

	got, err := store.Incident("abc123")
	if err != nil {
		t.Fatalf("Incident failed: %v", err)
	}

	if got.Incident.Category != detector.OOMKilled {
		t.Errorf("category = %s", got.Incident.Category)
	}
	if !got.Incident.DetectedAt.Equal(now) {
		t.Errorf("detected_at = %v, want %v", got.Incident.DetectedAt, now)
	}
	if len(got.Evidence.Logs) != 1 || got.Evidence.Logs[0].Number != 1 {
		t.Errorf("log evidence did not survive: %+v", got.Evidence.Logs)
	}
	if got.Evidence.Spec.MemoryLimit != "512Mi" {
		t.Errorf("spec evidence did not survive: %+v", got.Evidence.Spec)
	}
	if got.Explanation != nil {
		t.Error("an unexplained incident came back with an explanation")
	}
}

// The same fingerprint recurring is one incident seen again, not forty rows.
func TestSaveIncidentUpsertsOnRepeat(t *testing.T) {
	store := openTestStore(t)

	record := sampleRecord("abc123", detector.OOMKilled, now)
	if err := store.SaveIncident(record); err != nil {
		t.Fatal(err)
	}

	record.Incident.Count = 40
	record.Incident.DetectedAt = now.Add(time.Hour)
	if err := store.SaveIncident(record); err != nil {
		t.Fatal(err)
	}

	all, err := store.Incidents(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows, want the repeat folded into one", len(all))
	}
	if all[0].Incident.Count != 40 {
		t.Errorf("count = %d, want 40", all[0].Incident.Count)
	}
	// FirstSeen must not move: it is when the condition started.
	if !all[0].Incident.FirstSeen.Equal(now.Add(-90 * time.Second)) {
		t.Errorf("first_seen moved on update: %v", all[0].Incident.FirstSeen)
	}
}

func TestSaveExplanationRoundTrips(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveIncident(sampleRecord("abc123", detector.OOMKilled, now)); err != nil {
		t.Fatal(err)
	}

	exp := explanation.Explanation{
		IncidentID: "abc123",
		Category:   detector.OOMKilled, RuleCategory: detector.OOMKilled, Agrees: true,
		Confidence: 0.88,
		Summary:    "The JVM heap exceeded the 512Mi limit.",
		Fix:        "Raise the limit to 1Gi.",
		Citations: []explanation.Citation{
			{Text: "FATAL java.lang.OutOfMemoryError", LineNumber: 1, Source: explanation.SourceLog},
		},
		Rejected:         []string{"ERROR connection pool exhausted"},
		CitationAccuracy: 0.5,
		Provider:         "ollama", Model: "llama3",
		GeneratedAt: now, Duration: 1500 * time.Millisecond,
	}
	if err := store.SaveExplanation(exp); err != nil {
		t.Fatalf("SaveExplanation failed: %v", err)
	}

	got, err := store.Incident("abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Explanation == nil {
		t.Fatal("the explanation was not attached")
	}
	if got.Explanation.Confidence != 0.88 {
		t.Errorf("confidence = %v", got.Explanation.Confidence)
	}
	if len(got.Explanation.Citations) != 1 || got.Explanation.Citations[0].LineNumber != 1 {
		t.Errorf("citations did not survive: %+v", got.Explanation.Citations)
	}
	// The rejected quotes are kept deliberately: a reader deserves to know the
	// model invented something.
	if len(got.Explanation.Rejected) != 1 {
		t.Errorf("rejected citations were dropped: %+v", got.Explanation.Rejected)
	}
	if got.Explanation.Duration != 1500*time.Millisecond {
		t.Errorf("duration = %v", got.Explanation.Duration)
	}
}

func TestIncidentsFilters(t *testing.T) {
	store := openTestStore(t)

	oom := sampleRecord("a", detector.OOMKilled, now)
	crash := sampleRecord("b", detector.CrashLoopBackOff, now.Add(-time.Hour))
	crash.Incident.Severity = detector.Warning
	crash.Incident.Namespace = "shop"
	pending := sampleRecord("c", detector.PendingTimeout, now.Add(-48*time.Hour))
	pending.Incident.Resolved = true

	for _, record := range []Record{oom, crash, pending} {
		if err := store.SaveIncident(record); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name   string
		filter Filter
		want   int
	}{
		{"all", Filter{}, 3},
		{"by category", Filter{Category: "OOMKilled"}, 1},
		{"by severity", Filter{Severity: "warning"}, 1},
		{"by namespace", Filter{Namespace: "shop"}, 1},
		{"unresolved only", Filter{UnresolvedOnly: true}, 2},
		{"since a cutoff", Filter{Since: now.Add(-2 * time.Hour)}, 2},
		{"limit", Filter{Limit: 1}, 1},
		{"offset", Filter{Limit: 10, Offset: 2}, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.Incidents(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.want {
				t.Errorf("got %d records, want %d", len(got), tc.want)
			}
		})
	}

	// Newest first, so the dashboard's default view needs no client-side sort.
	all, _ := store.Incidents(Filter{})
	for i := 1; i < len(all); i++ {
		if all[i-1].Incident.DetectedAt.Before(all[i].Incident.DetectedAt) {
			t.Error("incidents are not ordered newest first")
		}
	}
}

// A dashboard bug must not be able to ask for a million rows.
func TestIncidentsClampsTheLimit(t *testing.T) {
	store := openTestStore(t)
	if got := (Filter{Limit: 999999}).normalise().Limit; got != DefaultLimit {
		t.Errorf("an absurd limit was not clamped: %d", got)
	}
	if got := (Filter{Limit: -5}).normalise().Limit; got != DefaultLimit {
		t.Errorf("a negative limit was not clamped: %d", got)
	}
	_ = store
}

func TestResolve(t *testing.T) {
	store := openTestStore(t)
	if err := store.SaveIncident(sampleRecord("abc123", detector.OOMKilled, now)); err != nil {
		t.Fatal(err)
	}

	if err := store.Resolve("abc123", now.Add(time.Minute)); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	got, _ := store.Incident("abc123")
	if !got.Incident.Resolved {
		t.Error("the incident was not marked resolved")
	}

	if err := store.Resolve("nonexistent", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolving an unknown id returned %v, want ErrNotFound", err)
	}
}

func TestIncidentNotFound(t *testing.T) {
	if _, err := openTestStore(t).Incident("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestHealthSamplesAreChronological(t *testing.T) {
	store := openTestStore(t)

	for i := 0; i < 5; i++ {
		if err := store.SaveHealth(HealthSample{
			SampledAt: now.Add(time.Duration(i) * time.Minute),
			TotalPods: 40, UnhealthyPods: i, OpenIncidents: i, Nodes: 4,
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Health(now.Add(-time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d samples, want 5", len(got))
	}
	// Oldest first, so a chart plots left to right without reversing.
	for i := 1; i < len(got); i++ {
		if got[i-1].SampledAt.After(got[i].SampledAt) {
			t.Error("health samples are not oldest-first")
		}
	}
}

func TestStats(t *testing.T) {
	store := openTestStore(t)

	if err := store.SaveIncident(sampleRecord("a", detector.OOMKilled, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	crash := sampleRecord("b", detector.CrashLoopBackOff, time.Now().UTC())
	crash.Incident.Severity = detector.Warning
	crash.Incident.Resolved = true
	if err := store.SaveIncident(crash); err != nil {
		t.Fatal(err)
	}

	if err := store.SaveExplanation(explanation.Explanation{
		IncidentID: "a", Category: detector.OOMKilled, RuleCategory: detector.OOMKilled,
		Confidence: 0.9, Summary: "x", Provider: "ollama", Model: "llama3",
		Rejected: []string{"invented line"}, GeneratedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.Stats(time.Now().UTC().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if stats.TotalIncidents != 2 {
		t.Errorf("total = %d, want 2", stats.TotalIncidents)
	}
	if stats.OpenIncidents != 1 {
		t.Errorf("open = %d, want 1", stats.OpenIncidents)
	}
	if stats.ByCategory["OOMKilled"] != 1 || stats.BySeverity["warning"] != 1 {
		t.Errorf("breakdowns are wrong: %+v / %+v", stats.ByCategory, stats.BySeverity)
	}
	if stats.Explained != 1 {
		t.Errorf("explained = %d, want 1", stats.Explained)
	}
	// The number that says whether the model can be trusted here.
	if stats.FabricatedCitations != 1 {
		t.Errorf("fabricated citations = %d, want 1", stats.FabricatedCitations)
	}
	// Detection latency is measured from when the condition started.
	if stats.MeanTimeToDetectMS != 90000 {
		t.Errorf("mean time to detect = %dms, want 90000", stats.MeanTimeToDetectMS)
	}
}
