package db

import (
	"testing"
)

func insertTestChunkSimple(t *testing.T, db interface {
	Exec(string, ...any) (interface{ RowsAffected() (int64, error) }, error)
}, text, sourceFile, validAt string, seq int) {
	t.Helper()
}

// setupForgetDB creates an in-memory DB with test chunks for forget tests.
func setupForgetDB(t *testing.T) interface{ Close() error } {
	t.Helper()
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	return db
}

func TestForgetByFile(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	// Insert chunks from two files
	for i, row := range []struct {
		text, file, section, validAt string
	}{
		{"chunk a", "notes.md", "Intro", "2025-01-01"},
		{"chunk b", "notes.md", "Body", "2025-02-01"},
		{"chunk c", "decisions.md", "ADR", "2025-03-01"},
	} {
		_, err := db.Exec(
			`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			row.text, row.file, row.section, i+1, row.validAt, "2025-01-31",
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	// Verify initial count
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total)
	if total != 3 {
		t.Fatalf("expected 3 chunks before forget, got %d", total)
	}

	// Forget notes.md
	result, err := ForgetByFile(db, "notes.md")
	if err != nil {
		t.Fatalf("ForgetByFile: %v", err)
	}
	if result.ChunksDeleted != 2 {
		t.Errorf("expected 2 chunks deleted, got %d", result.ChunksDeleted)
	}

	// Verify remaining
	db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total)
	if total != 1 {
		t.Errorf("expected 1 chunk remaining, got %d", total)
	}

	// Verify it's the decisions.md chunk
	var remaining string
	db.QueryRow(`SELECT source_file FROM chunks`).Scan(&remaining)
	if remaining != "decisions.md" {
		t.Errorf("expected decisions.md to remain, got %s", remaining)
	}
}

func TestForgetByFileNotFound(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	result, err := ForgetByFile(db, "nonexistent.md")
	if err != nil {
		t.Fatalf("ForgetByFile: %v", err)
	}
	if result.ChunksDeleted != 0 {
		t.Errorf("expected 0 chunks deleted, got %d", result.ChunksDeleted)
	}
}

func TestForgetBefore(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	for i, row := range []struct {
		text, file, validAt string
	}{
		{"old chunk", "notes.md", "2024-01-01"},
		{"older chunk", "notes.md", "2023-06-01"},
		{"new chunk", "notes.md", "2025-06-01"},
		{"timeless", "notes.md", ""},
	} {
		var validAt interface{} = row.validAt
		if row.validAt == "" {
			validAt = nil
		}
		_, err := db.Exec(
			`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			row.text, row.file, "Section", i+1, validAt, "2025-01-31",
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	// Forget before 2025-01-01 — should delete 2024-01-01 and 2023-06-01
	result, err := ForgetBefore(db, "2025-01-01")
	if err != nil {
		t.Fatalf("ForgetBefore: %v", err)
	}
	if result.ChunksDeleted != 2 {
		t.Errorf("expected 2 chunks deleted, got %d", result.ChunksDeleted)
	}

	// Verify remaining: new chunk + timeless = 2
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total)
	if total != 2 {
		t.Errorf("expected 2 chunks remaining, got %d", total)
	}
}

func TestForgetAll(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	for i := 0; i < 5; i++ {
		_, err := db.Exec(
			`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			"chunk", "file.md", "Section", i+1, nil, "2025-01-31",
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	result, err := ForgetAll(db)
	if err != nil {
		t.Fatalf("ForgetAll: %v", err)
	}
	if result.ChunksDeleted != 5 {
		t.Errorf("expected 5 chunks deleted, got %d", result.ChunksDeleted)
	}

	var total int
	db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&total)
	if total != 0 {
		t.Errorf("expected 0 chunks remaining, got %d", total)
	}
}

func TestForgetAllEmpty(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	result, err := ForgetAll(db)
	if err != nil {
		t.Fatalf("ForgetAll on empty db: %v", err)
	}
	if result.ChunksDeleted != 0 {
		t.Errorf("expected 0 chunks deleted on empty db, got %d", result.ChunksDeleted)
	}
}

func TestCountForgetByFile(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	for i := 0; i < 3; i++ {
		_, err := db.Exec(
			`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			"chunk", "target.md", "Section", i+1, nil, "2025-01-31",
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	count, err := CountForgetByFile(db, "target.md")
	if err != nil {
		t.Fatalf("CountForgetByFile: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}

	count, err = CountForgetByFile(db, "other.md")
	if err != nil {
		t.Fatalf("CountForgetByFile: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for nonexistent file, got %d", count)
	}
}

func TestCountForgetBefore(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	for i, validAt := range []string{"2024-01-01", "2024-06-01", "2025-06-01", ""} {
		var v interface{} = validAt
		if validAt == "" {
			v = nil
		}
		_, err := db.Exec(
			`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			"chunk", "file.md", "Section", i+1, v, "2025-01-31",
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	count, err := CountForgetBefore(db, "2025-01-01")
	if err != nil {
		t.Fatalf("CountForgetBefore: %v", err)
	}
	// Should count 2024-01-01 and 2024-06-01 (not timeless, not 2025-06-01)
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}
