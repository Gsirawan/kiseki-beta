package db

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// migration represents a single database schema migration.
type migration struct {
	Version     int
	Description string
	Up          func(*sql.DB) error
}

// migrations is the ordered list of all schema migrations.
// V1 = "everything before the migration system existed" (implicit).
// New migrations start at V2.
var migrations = []migration{
	{
		Version:     2,
		Description: "add kiseki_config table",
		Up: func(db *sql.DB) error {
			_, err := db.Exec(`CREATE TABLE IF NOT EXISTS kiseki_config (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL
			)`)
			return err
		},
	},
	{
		Version:     3,
		Description: "add entity graph tables (entities, entity_aliases, relationships)",
		Up: func(db *sql.DB) error {
			_, err := db.Exec(`CREATE TABLE IF NOT EXISTS entities (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL UNIQUE,
				type TEXT NOT NULL DEFAULT 'person',
				description TEXT,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);

			CREATE TABLE IF NOT EXISTS entity_aliases (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				entity_id INTEGER NOT NULL,
				alias TEXT NOT NULL,
				note TEXT,
				FOREIGN KEY (entity_id) REFERENCES entities(id)
			);

			CREATE TABLE IF NOT EXISTS relationships (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				entity_a INTEGER NOT NULL,
				entity_b INTEGER NOT NULL,
				relation_type TEXT NOT NULL,
				description TEXT,
				created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				FOREIGN KEY (entity_a) REFERENCES entities(id),
				FOREIGN KEY (entity_b) REFERENCES entities(id)
			)`)
			return err
		},
	},
	{
		Version:     4,
		Description: "add keywords column to messages and update FTS5 schema",
		Up: func(db *sql.DB) error {
			// Add keywords column
			_, err := db.Exec(`ALTER TABLE messages ADD COLUMN keywords TEXT DEFAULT ''`)
			if err != nil {
				// Column might already exist — check error
				if !strings.Contains(err.Error(), "duplicate column") {
					return err
				}
			}

			// Rebuild FTS5 with keywords column included.
			// Drop old FTS5 table and recreate with new schema.
			// content-sync means FTS5 mirrors the messages table.
			db.Exec(`DROP TABLE IF EXISTS messages_fts`)
			_, err = db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
				message_id UNINDEXED,
				role,
				text,
				keywords,
				content=messages,
				content_rowid=rowid
			)`)
			if err != nil {
				// FTS5 not available in this SQLite build — skip rebuild, non-fatal
				if strings.Contains(err.Error(), "no such module") {
					log.Printf("FTS5 not available, skipping FTS5 rebuild in V4 migration: %v", err)
					return nil
				}
				return fmt.Errorf("create FTS5 table: %w", err)
			}

			// Re-populate FTS5 from existing messages
			_, err = db.Exec(`INSERT INTO messages_fts(message_id, role, text, keywords)
				SELECT id, role, text, keywords FROM messages`)
			if err != nil {
				// Non-fatal — FTS5 will be populated on next insert
				log.Printf("Warning: FTS5 re-population failed (will rebuild on next insert): %v", err)
			}

			return nil
		},
	},
	{
		Version:     5,
		Description: "rebuild FTS5 without content-sync (fixes column name mismatch)",
		Up: func(db *sql.DB) error {
			// The V4 FTS5 table used content=messages, content_rowid=rowid.
			// This caused column name mismatch: FTS5 column "message_id" vs
			// messages table column "id". FTS5 reads from the content table
			// by column name, so SELECT f.message_id failed with
			// "no such column: T.message_id".
			//
			// Fix: recreate without content-sync. FTS5 stores its own data.
			db.Exec(`DROP TABLE IF EXISTS messages_fts`)
			_, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
				message_id UNINDEXED,
				role,
				text,
				keywords
			)`)
			if err != nil {
				if strings.Contains(err.Error(), "no such module") {
					log.Printf("FTS5 not available, skipping FTS5 rebuild in V5 migration: %v", err)
					return nil
				}
				return fmt.Errorf("create FTS5 table: %w", err)
			}

			// Re-populate FTS5 from existing messages
			_, err = db.Exec(`INSERT INTO messages_fts(message_id, role, text, keywords)
				SELECT id, role, text, keywords FROM messages`)
			if err != nil {
				log.Printf("Warning: FTS5 re-population failed (will rebuild on next insert): %v", err)
			}

			return nil
		},
	},
	{
		Version:     6,
		Description: "add thoughts table for extended thinking blocks",
		Up: func(db *sql.DB) error {
			_, err := db.Exec(`CREATE TABLE IF NOT EXISTS thoughts (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				message_id TEXT NOT NULL,
				session_id TEXT NOT NULL,
				text TEXT NOT NULL,
				timestamp INTEGER NOT NULL,
				source TEXT NOT NULL,
				created_at TEXT NOT NULL
			);
			CREATE INDEX IF NOT EXISTS idx_thoughts_session ON thoughts(session_id);
			CREATE INDEX IF NOT EXISTS idx_thoughts_message ON thoughts(message_id)`)
			return err
		},
	},
}

// ensureSchemaVersion creates the schema_version table if it doesn't exist
// and returns the current schema version.
//
// Detection logic:
//   - schema_version table exists with a row → return stored version
//   - schema_version table missing, but chunks table exists → pre-migration DB, set V1
//   - schema_version table missing, no chunks table → impossible after buildSchema, but treat as V1
func ensureSchemaVersion(db *sql.DB) (int, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER NOT NULL
	)`)
	if err != nil {
		return 0, fmt.Errorf("create schema_version table: %w", err)
	}

	var version int
	err = db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if err == sql.ErrNoRows {
		// No row yet. This is either a fresh DB or a pre-migration DB.
		// Either way, buildSchema has already run, so base tables exist.
		// Treat as V1 (base schema established).
		version = 1
		if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
			return 0, fmt.Errorf("insert initial schema version: %w", err)
		}
	} else if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}

	return version, nil
}

// LatestVersion returns the highest migration version available.
func LatestVersion() int {
	if len(migrations) == 0 {
		return 1
	}
	return migrations[len(migrations)-1].Version
}

// RunMigrations detects the current schema version and runs any pending migrations.
// Each migration updates the version row after success.
func RunMigrations(db *sql.DB) error {
	current, err := ensureSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("check schema version: %w", err)
	}

	ran := 0
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}

		log.Printf("Migrating database: v%d → v%d (%s)", current, m.Version, m.Description)

		if err := m.Up(db); err != nil {
			return fmt.Errorf("migration v%d (%s) failed: %w", m.Version, m.Description, err)
		}

		if _, err := db.Exec(`UPDATE schema_version SET version = ?`, m.Version); err != nil {
			return fmt.Errorf("update schema version to v%d: %w", m.Version, err)
		}

		current = m.Version
		ran++
	}

	if ran > 0 {
		log.Printf("Database migrated to v%d (%d migration(s) applied)", current, ran)
	}

	return nil
}

// SchemaVersion reads the current schema version from the database.
// Returns 0 if schema_version table doesn't exist yet.
func SchemaVersion(db *sql.DB) int {
	var version int
	err := db.QueryRow(`SELECT version FROM schema_version LIMIT 1`).Scan(&version)
	if err != nil {
		return 0
	}
	return version
}
