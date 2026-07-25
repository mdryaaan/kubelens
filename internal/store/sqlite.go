package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	// Pure-Go SQLite: no cgo, so `go build` works on any machine with a Go
	// toolchain and cross-compiles without a C cross-compiler.
	_ "modernc.org/sqlite"

	"github.com/mdryaaan/kubelens/internal/detector"
	"github.com/mdryaaan/kubelens/internal/explanation"
)

// ErrNotFound marks a record that does not exist.
var ErrNotFound = errors.New("not found")

// SQLite is the incident store.
type SQLite struct {
	db *sql.DB
}

// Open creates or opens the incident database and applies migrations.
func Open(path string) (*SQLite, error) {
	if path == "" {
		path = ":memory:"
	}

	// WAL keeps the SSE writer from blocking dashboard reads; busy_timeout
	// turns the rare concurrent-write collision into a short wait rather than
	// an immediate SQLITE_BUSY error surfacing in the UI.
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	// SQLite takes one writer at a time. Letting database/sql open a pool of
	// connections against it produces lock contention rather than throughput.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &SQLite{db: db}, nil
}

// DB exposes the handle, for migrations tests.
func (s *SQLite) DB() *sql.DB { return s.db }

// Close releases the database.
func (s *SQLite) Close() error { return s.db.Close() }

// SaveIncident inserts an incident, or updates the count and timestamps of one
// already recorded.
//
// Upsert rather than insert: the same fingerprint recurring is the same
// incident seen again, and the history is more useful as "this happened 40
// times over an hour" than as forty rows.
func (s *SQLite) SaveIncident(record Record) error {
	evidence, err := json.Marshal(record.Evidence)
	if err != nil {
		return fmt.Errorf("encoding evidence: %w", err)
	}

	incident := record.Incident
	_, err = s.db.Exec(`
		INSERT INTO incidents (
			id, fingerprint, category, severity, namespace, resource, container,
			title, detail, detected_at, first_seen, count, resolved, evidence
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			count       = excluded.count,
			detected_at = excluded.detected_at,
			severity    = excluded.severity,
			detail      = excluded.detail,
			resolved    = excluded.resolved,
			evidence    = excluded.evidence`,
		incident.ID, incident.Fingerprint, string(incident.Category), string(incident.Severity),
		incident.Namespace, incident.Resource, incident.Container,
		incident.Title, incident.Detail,
		incident.DetectedAt.UTC(), incident.FirstSeen.UTC(),
		incident.Count, boolToInt(incident.Resolved), string(evidence))
	if err != nil {
		return fmt.Errorf("saving incident %s: %w", incident.ID, err)
	}
	return nil
}

// SaveExplanation attaches a verified explanation to an incident.
func (s *SQLite) SaveExplanation(exp explanation.Explanation) error {
	citations, err := json.Marshal(exp.Citations)
	if err != nil {
		return fmt.Errorf("encoding citations: %w", err)
	}
	rejected, err := json.Marshal(exp.Rejected)
	if err != nil {
		return fmt.Errorf("encoding rejected citations: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO explanations (
			incident_id, category, rule_category, agrees, confidence, summary,
			suggested_fix, citations, rejected, citation_accuracy,
			provider, model, disclaimer, generated_at, duration_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (incident_id) DO UPDATE SET
			category          = excluded.category,
			rule_category     = excluded.rule_category,
			agrees            = excluded.agrees,
			confidence        = excluded.confidence,
			summary           = excluded.summary,
			suggested_fix     = excluded.suggested_fix,
			citations         = excluded.citations,
			rejected          = excluded.rejected,
			citation_accuracy = excluded.citation_accuracy,
			provider          = excluded.provider,
			model             = excluded.model,
			disclaimer        = excluded.disclaimer,
			generated_at      = excluded.generated_at,
			duration_ms       = excluded.duration_ms`,
		exp.IncidentID, string(exp.Category), string(exp.RuleCategory), boolToInt(exp.Agrees),
		exp.Confidence, exp.Summary, exp.Fix, string(citations), string(rejected),
		exp.CitationAccuracy, exp.Provider, exp.Model, exp.Disclaimer,
		exp.GeneratedAt.UTC(), exp.Duration.Milliseconds())
	if err != nil {
		return fmt.Errorf("saving explanation for %s: %w", exp.IncidentID, err)
	}
	return nil
}

const incidentColumns = `
	i.id, i.fingerprint, i.category, i.severity, i.namespace, i.resource, i.container,
	i.title, i.detail, i.detected_at, i.first_seen, i.count, i.resolved, i.evidence`

// Incident returns one record by id.
func (s *SQLite) Incident(id string) (Record, error) {
	row := s.db.QueryRow(`SELECT `+incidentColumns+` FROM incidents i WHERE i.id = ?`, id)

	record, err := scanIncident(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, fmt.Errorf("incident %s: %w", id, ErrNotFound)
		}
		return Record{}, err
	}

	exp, err := s.explanationFor(id)
	if err != nil {
		return Record{}, err
	}
	record.Explanation = exp
	return record, nil
}

// Incidents lists records newest first.
func (s *SQLite) Incidents(filter Filter) ([]Record, error) {
	filter = filter.normalise()

	var where []string
	var args []any

	if filter.Category != "" {
		where = append(where, "i.category = ?")
		args = append(args, filter.Category)
	}
	if filter.Severity != "" {
		where = append(where, "i.severity = ?")
		args = append(args, filter.Severity)
	}
	if filter.Namespace != "" {
		where = append(where, "i.namespace = ?")
		args = append(args, filter.Namespace)
	}
	if filter.UnresolvedOnly {
		where = append(where, "i.resolved = 0")
	}
	if !filter.Since.IsZero() {
		where = append(where, "i.detected_at >= ?")
		args = append(args, filter.Since.UTC())
	}

	query := `SELECT ` + incidentColumns + ` FROM incidents i`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY i.detected_at DESC LIMIT ? OFFSET ?"
	args = append(args, filter.Limit, filter.Offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing incidents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Record
	for rows.Next() {
		record, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading incidents: %w", err)
	}

	// Explanations are loaded after the rows are closed: SQLite is limited to
	// one connection here, so querying inside an open cursor would deadlock.
	for i := range out {
		exp, err := s.explanationFor(out[i].Incident.ID)
		if err != nil {
			return nil, err
		}
		out[i].Explanation = exp
	}

	return out, nil
}

// Resolve marks an incident closed.
func (s *SQLite) Resolve(id string, at time.Time) error {
	result, err := s.db.Exec(
		`UPDATE incidents SET resolved = 1, resolved_at = ? WHERE id = ?`, at.UTC(), id)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", id, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolving %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("incident %s: %w", id, ErrNotFound)
	}
	return nil
}

// SaveHealth records one health sample.
func (s *SQLite) SaveHealth(sample HealthSample) error {
	_, err := s.db.Exec(`
		INSERT INTO health_samples (sampled_at, total_pods, unhealthy_pods, open_incidents, nodes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (sampled_at) DO UPDATE SET
			total_pods     = excluded.total_pods,
			unhealthy_pods = excluded.unhealthy_pods,
			open_incidents = excluded.open_incidents,
			nodes          = excluded.nodes`,
		sample.SampledAt.UTC(), sample.TotalPods, sample.UnhealthyPods,
		sample.OpenIncidents, sample.Nodes)
	if err != nil {
		return fmt.Errorf("saving health sample: %w", err)
	}
	return nil
}

// Health returns samples since a cutoff, oldest first so a chart can plot them
// left to right without reversing.
func (s *SQLite) Health(since time.Time, limit int) ([]HealthSample, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}

	rows, err := s.db.Query(`
		SELECT sampled_at, total_pods, unhealthy_pods, open_incidents, nodes
		FROM health_samples WHERE sampled_at >= ?
		ORDER BY sampled_at ASC LIMIT ?`, since.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("reading health samples: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []HealthSample
	for rows.Next() {
		var sample HealthSample
		if err := rows.Scan(&sample.SampledAt, &sample.TotalPods, &sample.UnhealthyPods,
			&sample.OpenIncidents, &sample.Nodes); err != nil {
			return nil, fmt.Errorf("scanning health sample: %w", err)
		}
		sample.SampledAt = sample.SampledAt.UTC()
		out = append(out, sample)
	}
	return out, rows.Err()
}

// Stats summarises the history since a cutoff.
func (s *SQLite) Stats(since time.Time) (Stats, error) {
	stats := Stats{ByCategory: map[string]int{}, BySeverity: map[string]int{}}

	rows, err := s.db.Query(`
		SELECT category, severity, resolved, detected_at, first_seen
		FROM incidents WHERE detected_at >= ?`, since.UTC())
	if err != nil {
		return stats, fmt.Errorf("reading incident stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	startOfToday := time.Now().UTC().Truncate(24 * time.Hour)
	var detectionTotal time.Duration
	var detectionCount int

	for rows.Next() {
		var category, severity string
		var resolved int
		var detectedAt, firstSeen time.Time

		if err := rows.Scan(&category, &severity, &resolved, &detectedAt, &firstSeen); err != nil {
			return stats, fmt.Errorf("scanning incident stats: %w", err)
		}

		stats.TotalIncidents++
		stats.ByCategory[category]++
		stats.BySeverity[severity]++
		if resolved == 0 {
			stats.OpenIncidents++
		}
		if detectedAt.UTC().After(startOfToday) {
			stats.IncidentsToday++
		}
		if delta := detectedAt.Sub(firstSeen); delta > 0 {
			detectionTotal += delta
			detectionCount++
		}
	}
	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("reading incident stats: %w", err)
	}

	if detectionCount > 0 {
		stats.MeanTimeToDetectMS = (detectionTotal / time.Duration(detectionCount)).Milliseconds()
	}

	if err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN rejected != '[]' AND rejected != 'null' THEN 1 ELSE 0 END), 0)
		FROM explanations`).Scan(&stats.Explained, &stats.FabricatedCitations); err != nil {
		return stats, fmt.Errorf("reading explanation stats: %w", err)
	}

	return stats, nil
}

// rowScanner covers both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanIncident(row rowScanner) (Record, error) {
	var record Record
	var category, severity, evidence string
	var resolved int

	err := row.Scan(
		&record.Incident.ID, &record.Incident.Fingerprint, &category, &severity,
		&record.Incident.Namespace, &record.Incident.Resource, &record.Incident.Container,
		&record.Incident.Title, &record.Incident.Detail,
		&record.Incident.DetectedAt, &record.Incident.FirstSeen,
		&record.Incident.Count, &resolved, &evidence)
	if err != nil {
		return Record{}, err
	}

	record.Incident.Category = detector.Category(category)
	record.Incident.Severity = detector.Severity(severity)
	record.Incident.Resolved = resolved == 1
	record.Incident.DetectedAt = record.Incident.DetectedAt.UTC()
	record.Incident.FirstSeen = record.Incident.FirstSeen.UTC()

	if evidence != "" && evidence != "{}" {
		if err := json.Unmarshal([]byte(evidence), &record.Evidence); err != nil {
			return Record{}, fmt.Errorf("decoding evidence for %s: %w", record.Incident.ID, err)
		}
	}

	return record, nil
}

func (s *SQLite) explanationFor(incidentID string) (*explanation.Explanation, error) {
	row := s.db.QueryRow(`
		SELECT category, rule_category, agrees, confidence, summary, suggested_fix,
		       citations, rejected, citation_accuracy, provider, model, disclaimer,
		       generated_at, duration_ms
		FROM explanations WHERE incident_id = ?`, incidentID)

	var exp explanation.Explanation
	var category, ruleCategory, citations, rejected string
	var agrees int
	var durationMS int64

	err := row.Scan(&category, &ruleCategory, &agrees, &exp.Confidence, &exp.Summary, &exp.Fix,
		&citations, &rejected, &exp.CitationAccuracy, &exp.Provider, &exp.Model, &exp.Disclaimer,
		&exp.GeneratedAt, &durationMS)
	if errors.Is(err, sql.ErrNoRows) {
		// Not every incident has been explained. That is normal — explanation
		// is opt-in and can fail — so it is not an error.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading explanation for %s: %w", incidentID, err)
	}

	exp.IncidentID = incidentID
	exp.Category = detector.Category(category)
	exp.RuleCategory = detector.Category(ruleCategory)
	exp.Agrees = agrees == 1
	exp.GeneratedAt = exp.GeneratedAt.UTC()
	exp.Duration = time.Duration(durationMS) * time.Millisecond

	if err := json.Unmarshal([]byte(citations), &exp.Citations); err != nil {
		return nil, fmt.Errorf("decoding citations for %s: %w", incidentID, err)
	}
	if err := json.Unmarshal([]byte(rejected), &exp.Rejected); err != nil {
		return nil, fmt.Errorf("decoding rejected citations for %s: %w", incidentID, err)
	}

	return &exp, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
