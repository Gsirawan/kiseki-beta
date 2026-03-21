package ingest

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		filename string
		format   string
		warn     bool
	}{
		{"notes.md", "markdown", false},
		{"notes.markdown", "markdown", false},
		{"notes.MD", "markdown", false},
		{"notes.txt", "text", false},
		{"notes.TXT", "text", false},
		{"notes.log", "text", true},
		{"notes.csv", "text", true},
		{"notes", "text", true},
		{"path/to/notes.md", "markdown", false},
		{"path/to/notes.txt", "text", false},
	}

	for _, tt := range tests {
		format, warn := DetectFormat(tt.filename)
		if format != tt.format || warn != tt.warn {
			t.Errorf("DetectFormat(%q) = (%q, %v), want (%q, %v)", tt.filename, format, warn, tt.format, tt.warn)
		}
	}
}

func TestParsePlainText(t *testing.T) {
	content := "This is a plain text document.\nIt has multiple lines.\n\nAnd paragraphs."
	sections := ParsePlainText(content, "meeting-notes.txt")

	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}

	s := sections[0]
	if s.Title != "meeting-notes" {
		t.Fatalf("expected title %q, got %q", "meeting-notes", s.Title)
	}
	if s.HeaderLevel != 2 {
		t.Fatalf("expected header level 2, got %d", s.HeaderLevel)
	}
	if s.Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", s.Sequence)
	}
	if !strings.Contains(s.Content, "plain text document") {
		t.Fatalf("content missing expected text: %q", s.Content)
	}
}

func TestParsePlainTextEmpty(t *testing.T) {
	sections := ParsePlainText("", "empty.txt")
	if len(sections) != 0 {
		t.Fatalf("expected 0 sections for empty content, got %d", len(sections))
	}

	sections = ParsePlainText("   \n\n  \n", "whitespace.txt")
	if len(sections) != 0 {
		t.Fatalf("expected 0 sections for whitespace-only content, got %d", len(sections))
	}
}

func TestParsePlainTextStripExtension(t *testing.T) {
	sections := ParsePlainText("content", "my-notes.txt")
	if sections[0].Title != "my-notes" {
		t.Fatalf("expected title %q, got %q", "my-notes", sections[0].Title)
	}

	sections = ParsePlainText("content", "no-extension")
	if sections[0].Title != "no-extension" {
		t.Fatalf("expected title %q, got %q", "no-extension", sections[0].Title)
	}
}

func TestParseContent(t *testing.T) {
	mdContent := "## Section One\nContent one.\n\n## Section Two\nContent two."
	txtContent := "Just plain text content here."

	// Markdown format
	mdSections := ParseContent(mdContent, "notes.md", "markdown")
	if len(mdSections) != 2 {
		t.Fatalf("expected 2 sections for markdown, got %d", len(mdSections))
	}

	// Text format
	txtSections := ParseContent(txtContent, "notes.txt", "text")
	if len(txtSections) != 1 {
		t.Fatalf("expected 1 section for text, got %d", len(txtSections))
	}

	// Default (unknown format) falls back to markdown
	defaultSections := ParseContent(mdContent, "notes.md", "")
	if len(defaultSections) != 2 {
		t.Fatalf("expected 2 sections for default format, got %d", len(defaultSections))
	}
}

func TestIngestPlainTextFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		embedding := make([]float64, db.EmbedDimension)
		embedding[0] = 0.42
		resp := struct {
			Embeddings [][]float64 `json:"embeddings"`
		}{Embeddings: [][]float64{embedding}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "sample.txt")
	content := "This is a plain text file.\nIt should be ingested as a single section."
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer dbConn.Close()

	client := ollama.NewOllamaClient(server.URL, "test-embed-model")
	result, err := IngestFile(dbConn, client, filePath, "2026-01-15", "text")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if result.SectionsFound != 1 {
		t.Fatalf("expected 1 section, got %d", result.SectionsFound)
	}
	if result.ChunksCreated != 1 {
		t.Fatalf("expected 1 chunk, got %d", result.ChunksCreated)
	}

	// Verify the stored chunk
	var storedTitle string
	var storedSource string
	err = dbConn.QueryRow("SELECT section_title, source_file FROM chunks LIMIT 1").Scan(&storedTitle, &storedSource)
	if err != nil {
		t.Fatalf("query chunk: %v", err)
	}
	if storedTitle != "sample" {
		t.Fatalf("expected section_title %q, got %q", "sample", storedTitle)
	}
	if storedSource != filePath {
		t.Fatalf("expected source_file %q, got %q", filePath, storedSource)
	}

	// Verify vec_chunks
	var vecCount int
	if err := dbConn.QueryRow("SELECT COUNT(*) FROM vec_chunks").Scan(&vecCount); err != nil {
		t.Fatalf("count vec_chunks: %v", err)
	}
	if vecCount != 1 {
		t.Fatalf("expected 1 vec_chunk, got %d", vecCount)
	}
}

func TestIngestContentFromStdin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		embedding := make([]float64, db.EmbedDimension)
		embedding[0] = 0.42
		resp := struct {
			Embeddings [][]float64 `json:"embeddings"`
		}{Embeddings: [][]float64{embedding}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer dbConn.Close()

	client := ollama.NewOllamaClient(server.URL, "test-embed-model")
	content := "Meeting notes from standup.\nDiscussed deployment timeline."
	result, err := IngestContent(dbConn, client, content, "standup-2026-02-24", "2026-02-24", "text")
	if err != nil {
		t.Fatalf("IngestContent: %v", err)
	}
	if result.SectionsFound != 1 {
		t.Fatalf("expected 1 section, got %d", result.SectionsFound)
	}
	if result.ChunksCreated != 1 {
		t.Fatalf("expected 1 chunk, got %d", result.ChunksCreated)
	}

	// Verify source_file is the label, not a real path
	var storedSource string
	err = dbConn.QueryRow("SELECT source_file FROM chunks LIMIT 1").Scan(&storedSource)
	if err != nil {
		t.Fatalf("query chunk: %v", err)
	}
	if storedSource != "standup-2026-02-24" {
		t.Fatalf("expected source_file %q, got %q", "standup-2026-02-24", storedSource)
	}

	// Verify valid_at
	var validAt sql.NullString
	err = dbConn.QueryRow("SELECT valid_at FROM chunks LIMIT 1").Scan(&validAt)
	if err != nil {
		t.Fatalf("query valid_at: %v", err)
	}
	if !validAt.Valid || validAt.String != "2026-02-24" {
		t.Fatalf("expected valid_at %q, got %+v", "2026-02-24", validAt)
	}
}

func TestIngestFileDefaultsToMarkdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		embedding := make([]float64, db.EmbedDimension)
		embedding[0] = 0.42
		resp := struct {
			Embeddings [][]float64 `json:"embeddings"`
		}{Embeddings: [][]float64{embedding}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "sample.md")
	content := "## Section One\nContent one.\n\n## Section Two\nContent two."
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	defer dbConn.Close()

	client := ollama.NewOllamaClient(server.URL, "test-embed-model")
	// Call without format — should default to markdown
	result, err := IngestFile(dbConn, client, filePath, "2026-01-15")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}
	if result.SectionsFound != 2 {
		t.Fatalf("expected 2 sections (markdown), got %d", result.SectionsFound)
	}
}
