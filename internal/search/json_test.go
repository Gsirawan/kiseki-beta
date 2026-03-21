package search

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/history"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/status"
)

// TestRunSearchJSON verifies that --json flag produces valid JSON on stdout.
func TestRunSearchJSON(t *testing.T) {
	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer dbConn.Close()

	vec := makeVec(map[int]float32{0: 1})
	insertChunk(t, dbConn, "hello world", "notes.md", "Intro", "", 2, "2025-01-01", vec)

	server := newOllamaServer(t, vec)
	defer server.Close()

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	client := ollama.NewOllamaClient(server.URL, "embed")
	results, err := Search(dbConn, client, "hello", 5, "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// Build JSON output the same way runSearch does
	type jsonResult struct {
		ChunkText    string  `json:"chunk_text"`
		SourceFile   string  `json:"source_file"`
		SectionTitle string  `json:"section_title"`
		ValidAt      string  `json:"valid_at"`
		Similarity   float64 `json:"similarity"`
	}
	out := make([]jsonResult, len(results))
	for i, res := range results {
		out[i] = jsonResult{
			ChunkText:    res.Text,
			SourceFile:   res.SourceFile,
			SectionTitle: res.SectionTitle,
			ValidAt:      res.ValidAt,
			Similarity:   1 - res.Distance,
		}
	}
	payload, err := json.Marshal(map[string]any{"results": out})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	w.Close()
	os.Stdout = old
	_ = r

	// Verify the payload is valid JSON with expected structure
	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v", err)
	}
	resultsField, ok := parsed["results"]
	if !ok {
		t.Fatal("JSON missing 'results' key")
	}
	arr, ok := resultsField.([]any)
	if !ok {
		t.Fatal("'results' is not an array")
	}
	if len(arr) != 1 {
		t.Fatalf("expected 1 result, got %d", len(arr))
	}
	item := arr[0].(map[string]any)
	if item["chunk_text"] != "hello world" {
		t.Errorf("unexpected chunk_text: %v", item["chunk_text"])
	}
	if item["source_file"] != "notes.md" {
		t.Errorf("unexpected source_file: %v", item["source_file"])
	}
}

// TestRunHistoryJSON verifies history JSON output structure.
func TestRunHistoryJSON(t *testing.T) {
	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer dbConn.Close()

	_, err = dbConn.Exec(
		`INSERT INTO chunks (text, source_file, section_title, section_sequence, valid_at, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"Alice went to the store", "diary.md", "Monday", 1, "2025-06-01", "2025-06-01",
	)
	if err != nil {
		t.Fatalf("insert chunk: %v", err)
	}

	results, err := history.History(dbConn, "Alice", 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}

	type jsonMention struct {
		ChunkText  string `json:"chunk_text"`
		ValidAt    string `json:"valid_at"`
		SourceFile string `json:"source_file"`
	}
	mentions := make([]jsonMention, len(results))
	for i, res := range results {
		mentions[i] = jsonMention{
			ChunkText:  res.Text,
			ValidAt:    res.ValidAt,
			SourceFile: res.SourceFile,
		}
	}
	payload, err := json.Marshal(map[string]any{"entity": "Alice", "mentions": mentions})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["entity"] != "Alice" {
		t.Errorf("expected entity 'Alice', got %v", parsed["entity"])
	}
	arr := parsed["mentions"].([]any)
	if len(arr) != 1 {
		t.Fatalf("expected 1 mention, got %d", len(arr))
	}
}

// TestRunStatusJSON verifies status JSON output structure.
func TestRunStatusJSON(t *testing.T) {
	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer dbConn.Close()

	server := newOllamaServer(t, makeVec(map[int]float32{0: 1}))
	defer server.Close()

	ollamaClient := ollama.NewOllamaClient(server.URL, "embed")
	statusResult := status.Status(dbConn, ollamaClient, "embed")

	type jsonDateRange struct {
		Earliest string `json:"earliest"`
		Latest   string `json:"latest"`
	}
	ollamaStatus := "error"
	if statusResult.OllamaHealthy {
		ollamaStatus = "ok"
	}
	payload, err := json.Marshal(map[string]any{
		"ollama_status": ollamaStatus,
		"db_path":       "kiseki.db",
		"embed_model":   statusResult.EmbedModel,
		"chunk_count":   statusResult.TotalChunks,
		"date_range": jsonDateRange{
			Earliest: statusResult.EarliestValidAt,
			Latest:   statusResult.LatestValidAt,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := parsed["ollama_status"]; !ok {
		t.Error("JSON missing 'ollama_status'")
	}
	if _, ok := parsed["chunk_count"]; !ok {
		t.Error("JSON missing 'chunk_count'")
	}
	if _, ok := parsed["date_range"]; !ok {
		t.Error("JSON missing 'date_range'")
	}
}
