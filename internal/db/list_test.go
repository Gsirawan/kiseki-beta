package db

import (
	"testing"
)

func TestListFilesEmpty(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	files, err := ListFiles(db)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestListFilesBasic(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	// Insert chunks from two different files
	chunks := []struct {
		text       string
		sourceFile string
		section    string
		seq        int
		validAt    string
		ingestedAt string
	}{
		{"chunk one", "notes.md", "Intro", 1, "2025-01-01", "2025-01-31T10:00:00Z"},
		{"chunk two", "notes.md", "Body", 2, "2025-06-01", "2025-01-31T10:00:00Z"},
		{"chunk three", "decisions.md", "ADR-001", 1, "2025-03-01", "2025-02-01T10:00:00Z"},
	}

	for _, c := range chunks {
		_, err := db.Exec(
			`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			c.text, c.sourceFile, c.section, c.seq, c.validAt, c.ingestedAt,
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	files, err := ListFiles(db)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	// decisions.md was ingested later, should be first
	if files[0].SourceFile != "decisions.md" {
		t.Errorf("expected decisions.md first (most recently ingested), got %s", files[0].SourceFile)
	}
	if files[0].ChunkCount != 1 {
		t.Errorf("expected 1 chunk for decisions.md, got %d", files[0].ChunkCount)
	}

	if files[1].SourceFile != "notes.md" {
		t.Errorf("expected notes.md second, got %s", files[1].SourceFile)
	}
	if files[1].ChunkCount != 2 {
		t.Errorf("expected 2 chunks for notes.md, got %d", files[1].ChunkCount)
	}
	if files[1].EarliestDate != "2025-01-01" {
		t.Errorf("expected earliest 2025-01-01, got %s", files[1].EarliestDate)
	}
	if files[1].LatestDate != "2025-06-01" {
		t.Errorf("expected latest 2025-06-01, got %s", files[1].LatestDate)
	}
}

func TestListFilesNullDates(t *testing.T) {
	db, err := InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer db.Close()

	// Insert chunk with no valid_at
	_, err = db.Exec(
		`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"timeless chunk", "timeless.md", "Section", 1, nil, "2025-01-01T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert chunk: %v", err)
	}

	files, err := ListFiles(db)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].EarliestDate != "" {
		t.Errorf("expected empty EarliestDate for timeless chunk, got %s", files[0].EarliestDate)
	}
	if files[0].LatestDate != "" {
		t.Errorf("expected empty LatestDate for timeless chunk, got %s", files[0].LatestDate)
	}
}
