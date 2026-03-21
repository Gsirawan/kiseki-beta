package db

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newTestDB creates a fresh in-memory DB with sqlite-vec loaded but no schema.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func TestEnsureSchemaVersion_FreshDB(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	// Create the base schema first (simulates what InitDB does before migrations)
	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		t.Fatalf("build schema: %v", err)
	}

	version, err := ensureSchemaVersion(db)
	if err != nil {
		t.Fatalf("ensureSchemaVersion: %v", err)
	}

	// After buildSchema, chunks table exists → detected as V1
	if version != 1 {
		t.Errorf("expected version 1 for fresh DB after buildSchema, got %d", version)
	}
}

func TestEnsureSchemaVersion_Idempotent(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		t.Fatalf("build schema: %v", err)
	}

	// Call twice — second call should return same version
	v1, err := ensureSchemaVersion(db)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	v2, err := ensureSchemaVersion(db)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if v1 != v2 {
		t.Errorf("version changed between calls: %d → %d", v1, v2)
	}
}

func TestRunMigrations_FreshDB(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		t.Fatalf("build schema: %v", err)
	}

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Should be at latest version
	version := SchemaVersion(db)
	if version != LatestVersion() {
		t.Errorf("expected schema version %d, got %d", LatestVersion(), version)
	}

	// kiseki_config table should exist
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='kiseki_config'`).Scan(&name)
	if err != nil {
		t.Errorf("kiseki_config table should exist after migration: %v", err)
	}
}

func TestRunMigrations_ExistingDBAtV1(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		t.Fatalf("build schema: %v", err)
	}

	// Simulate a pre-migration DB: manually set schema_version to V1
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (1)`); err != nil {
		t.Fatalf("insert v1: %v", err)
	}

	// Run migrations — should only apply V2+
	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations from V1: %v", err)
	}

	version := SchemaVersion(db)
	if version != LatestVersion() {
		t.Errorf("expected version %d after migration from V1, got %d", LatestVersion(), version)
	}
}

func TestRunMigrations_AlreadyCurrent(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		t.Fatalf("build schema: %v", err)
	}

	// Run migrations twice — second run should be a no-op
	if err := RunMigrations(db); err != nil {
		t.Fatalf("first RunMigrations: %v", err)
	}

	v1 := SchemaVersion(db)

	if err := RunMigrations(db); err != nil {
		t.Fatalf("second RunMigrations: %v", err)
	}

	v2 := SchemaVersion(db)
	if v1 != v2 {
		t.Errorf("version changed on second run: %d → %d", v1, v2)
	}
}

func TestSchemaVersion_NoTable(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	// No schema_version table → should return 0, not panic
	version := SchemaVersion(db)
	if version != 0 {
		t.Errorf("expected 0 for DB with no schema_version table, got %d", version)
	}
}

func TestInitDB_CreatesAllTables(t *testing.T) {
	database, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer database.Close()

	expectedTables := []string{"chunks", "vec_chunks", "messages", "vec_messages", "stones", "schema_version", "kiseki_config"}
	for _, table := range expectedTables {
		var name string
		err := database.QueryRow(`SELECT name FROM sqlite_master WHERE type IN ('table') AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("expected table %q to exist: %v", table, err)
		}
	}
}
