package store

import (
	"database/sql"
	"fmt"
)

// migration is one forward schema change.
//
// Migrations are numbered and applied in order, with the applied version
// recorded in the database. There is no down path: rolling a schema backwards
// on a local incident history is not worth the code it would take, and the
// honest recovery for a bad migration is to delete the file and re-watch.
type migration struct {
	version int
	name    string
	stmts   []string
}

var migrations = []migration{
	{
		version: 1,
		name:    "incidents and explanations",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS incidents (
				id           TEXT PRIMARY KEY,
				fingerprint  TEXT NOT NULL,
				category     TEXT NOT NULL,
				severity     TEXT NOT NULL,
				namespace    TEXT NOT NULL,
				resource     TEXT NOT NULL,
				container    TEXT NOT NULL DEFAULT '',
				title        TEXT NOT NULL,
				detail       TEXT NOT NULL,
				detected_at  TIMESTAMP NOT NULL,
				first_seen   TIMESTAMP NOT NULL,
				count        INTEGER NOT NULL DEFAULT 1,
				resolved     INTEGER NOT NULL DEFAULT 0,
				resolved_at  TIMESTAMP
			)`,
			// The dashboard's default view is "newest first, unresolved only",
			// so that is the index that exists.
			`CREATE INDEX IF NOT EXISTS idx_incidents_detected ON incidents (detected_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_incidents_resolved ON incidents (resolved, detected_at DESC)`,
			`CREATE INDEX IF NOT EXISTS idx_incidents_category ON incidents (category)`,

			`CREATE TABLE IF NOT EXISTS explanations (
				incident_id       TEXT PRIMARY KEY REFERENCES incidents (id) ON DELETE CASCADE,
				category          TEXT NOT NULL,
				rule_category     TEXT NOT NULL,
				agrees            INTEGER NOT NULL,
				confidence        REAL NOT NULL,
				summary           TEXT NOT NULL,
				suggested_fix     TEXT NOT NULL DEFAULT '',
				citations         TEXT NOT NULL DEFAULT '[]',
				rejected          TEXT NOT NULL DEFAULT '[]',
				citation_accuracy REAL NOT NULL DEFAULT 0,
				provider          TEXT NOT NULL,
				model             TEXT NOT NULL,
				disclaimer        TEXT NOT NULL DEFAULT '',
				generated_at      TIMESTAMP NOT NULL,
				duration_ms       INTEGER NOT NULL DEFAULT 0
			)`,
		},
	},
	{
		version: 2,
		name:    "cluster health samples",
		stmts: []string{
			`CREATE TABLE IF NOT EXISTS health_samples (
				sampled_at     TIMESTAMP PRIMARY KEY,
				total_pods     INTEGER NOT NULL,
				unhealthy_pods INTEGER NOT NULL,
				open_incidents INTEGER NOT NULL,
				nodes          INTEGER NOT NULL DEFAULT 0
			)`,
			`CREATE INDEX IF NOT EXISTS idx_health_sampled ON health_samples (sampled_at DESC)`,
		},
	},
	{
		version: 3,
		name:    "evidence retained with the incident",
		stmts: []string{
			// The logs and events an explanation cites have to outlive the pod
			// they came from, or an incident from yesterday becomes unciteable
			// the moment Kubernetes garbage-collects the object.
			`ALTER TABLE incidents ADD COLUMN evidence TEXT NOT NULL DEFAULT '{}'`,
		},
	},
	{
		version: 4,
		name:    "pre-existing conditions excluded from detection latency",
		stmts: []string{
			`ALTER TABLE incidents ADD COLUMN pre_existing INTEGER NOT NULL DEFAULT 0`,
		},
	},
}

// migrate applies every migration the database has not yet seen.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("reading schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		// Each migration is one transaction: a half-applied schema is worse
		// than an unapplied one, because the version counter would then lie.
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("starting migration %d: %w", m.version, err)
		}

		for _, stmt := range m.stmts {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
		}

		if _, err := tx.Exec(`INSERT INTO schema_version (version, name) VALUES (?, ?)`,
			m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration %d: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %d: %w", m.version, err)
		}
	}

	return nil
}

// SchemaVersion returns the highest migration applied.
func SchemaVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	return version, err
}
