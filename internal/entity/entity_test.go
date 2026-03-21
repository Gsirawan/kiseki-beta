package entity

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newTestDB creates an in-memory SQLite DB with the entity tables created.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS entities (
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
		);
	`)
	if err != nil {
		t.Fatalf("create entity tables: %v", err)
	}

	return db
}

// sampleYAML is a minimal entities.yaml for testing (name + aliases only).
const sampleYAML = `
entities:
  - name: Alice
    aliases:
      - Ali
      - "\u0623\u0644\u064A\u0633"
  - name: Bob
    aliases:
      - Hope
  - name: Charlie
    aliases:
      - the dog
`

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp yaml: %v", err)
	}
	return path
}

// ── LoadEntityGraph ──────────────────────────────────────────────────────────

func TestLoadEntityGraph_ParsesEntities(t *testing.T) {
	path := writeTempYAML(t, sampleYAML)

	graph, err := LoadEntityGraph(path)
	if err != nil {
		t.Fatalf("LoadEntityGraph: %v", err)
	}

	if len(graph.Entities) != 3 {
		t.Errorf("expected 3 entities, got %d", len(graph.Entities))
	}

	// Spot-check first entity
	e := graph.Entities[0]
	if e.Name != "Alice" {
		t.Errorf("expected first entity name 'Alice', got %q", e.Name)
	}
	if len(e.Aliases) != 2 {
		t.Errorf("expected 2 aliases for Alice, got %d", len(e.Aliases))
	}
}

func TestLoadEntityGraph_FileNotFound(t *testing.T) {
	_, err := LoadEntityGraph("/nonexistent/path/entities.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadEntityGraph_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("{bad: [unclosed bracket"), 0o600); err != nil {
		t.Fatalf("write bad yaml: %v", err)
	}
	_, err := LoadEntityGraph(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// ── IngestEntities ───────────────────────────────────────────────────────────

func TestIngestEntities_InsertsAll(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, err := LoadEntityGraph(path)
	if err != nil {
		t.Fatalf("LoadEntityGraph: %v", err)
	}

	if err := IngestEntities(db, graph); err != nil {
		t.Fatalf("IngestEntities: %v", err)
	}

	assertCount := func(query string, expected int) {
		t.Helper()
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil {
			t.Fatalf("count query %q: %v", query, err)
		}
		if count != expected {
			t.Errorf("query %q: expected %d, got %d", query, expected, count)
		}
	}

	assertCount("SELECT COUNT(*) FROM entities", 3)
	// Alice has 2 aliases, Bob has 1, Charlie has 1 → total 4
	assertCount("SELECT COUNT(*) FROM entity_aliases", 4)
}

func TestIngestEntities_Idempotent(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, err := LoadEntityGraph(path)
	if err != nil {
		t.Fatalf("LoadEntityGraph: %v", err)
	}

	// Ingest twice — second run should clear and re-insert, not duplicate
	if err := IngestEntities(db, graph); err != nil {
		t.Fatalf("first IngestEntities: %v", err)
	}
	if err := IngestEntities(db, graph); err != nil {
		t.Fatalf("second IngestEntities: %v", err)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&count); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 entities after double ingest, got %d", count)
	}
}

// ── LookupAliases ────────────────────────────────────────────────────────────

func TestLookupAliases_FindsMatch(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, _ := LoadEntityGraph(path)
	_ = IngestEntities(db, graph)

	matches, err := LookupAliases(db, "Ali is working on a project")
	if err != nil {
		t.Fatalf("LookupAliases: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one match for 'Ali', got none")
	}

	found := false
	for _, m := range matches {
		if m.Alias == "Ali" && m.EntityName == "Alice" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected alias 'Ali' → 'Alice', matches: %+v", matches)
	}
}

func TestLookupAliases_CaseInsensitive(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, _ := LoadEntityGraph(path)
	_ = IngestEntities(db, graph)

	// "ali" lowercase should still match alias "Ali"
	matches, err := LookupAliases(db, "ali went to the store")
	if err != nil {
		t.Fatalf("LookupAliases: %v", err)
	}

	found := false
	for _, m := range matches {
		if m.EntityName == "Alice" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected case-insensitive match for 'ali' → 'Alice', matches: %+v", matches)
	}
}

func TestLookupAliases_NoMatch(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, _ := LoadEntityGraph(path)
	_ = IngestEntities(db, graph)

	matches, err := LookupAliases(db, "completely unrelated query about nothing")
	if err != nil {
		t.Fatalf("LookupAliases: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d: %+v", len(matches), matches)
	}
}

// ── ExpandQuery ──────────────────────────────────────────────────────────────

func TestExpandQuery_ReplacesAlias(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, _ := LoadEntityGraph(path)
	_ = IngestEntities(db, graph)

	expanded, matches, err := ExpandQuery(db, "Ali is working on a project")
	if err != nil {
		t.Fatalf("ExpandQuery: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected matches, got none")
	}
	// "Ali" should be replaced with "Alice" (canonical name)
	if expanded == "Ali is working on a project" {
		t.Errorf("expected alias to be expanded, got unchanged: %q", expanded)
	}
	// The canonical name should appear in the expanded query
	if !containsString(expanded, "Alice") {
		t.Errorf("expected 'Alice' in expanded query, got: %q", expanded)
	}
}

func TestExpandQuery_NoAliasesReturnsOriginal(t *testing.T) {
	db := newTestDB(t)
	defer db.Close()

	path := writeTempYAML(t, sampleYAML)
	graph, _ := LoadEntityGraph(path)
	_ = IngestEntities(db, graph)

	original := "completely unrelated query"
	expanded, matches, err := ExpandQuery(db, original)
	if err != nil {
		t.Fatalf("ExpandQuery: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected no matches, got %d", len(matches))
	}
	if expanded != original {
		t.Errorf("expected original query unchanged, got: %q", expanded)
	}
}

// unused tests — commented out with their source functions in entity.go
// TestGetEntityStats_CorrectCounts, TestGetEntityStats_EmptyDB
// TestGetEntityContext_ReturnsDescription, TestGetEntityContext_NotFound
// TestGetRelationships_ReturnsAll, TestGetRelationships_UnknownEntity
// TestIngestEntities_UnknownRelationshipEntity

// ── helpers ──────────────────────────────────────────────────────────────────

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
