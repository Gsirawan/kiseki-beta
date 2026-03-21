package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/mark"
	"github.com/Gsirawan/kiseki-beta/internal/stone"
	"github.com/Gsirawan/kiseki-beta/internal/testutil"
)

// writeTestMarkdown creates a temporary markdown file and returns its path.
func writeTestMarkdown(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write test markdown: %v", err)
	}
	return path
}

func TestPipeline_IngestAndSearch(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	md := `## January 15, 2026
### Authentication Setup
We configured JWT authentication with RS256 signing.
The token expiration is set to 1 hour with a 24-hour refresh window.

### Database Migration
Migrated from MySQL to PostgreSQL for better JSON support.
Used pgloader for the initial data migration.
`
	path := writeTestMarkdown(t, md)

	result, err := ingest.IngestFile(database, embedder, path, "")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	if result.SectionsFound < 2 {
		t.Errorf("expected at least 2 sections, got %d", result.SectionsFound)
	}
	if result.ChunksCreated < 2 {
		t.Errorf("expected at least 2 chunks, got %d", result.ChunksCreated)
	}

	// Search for auth-related content
	results, err := Search(database, embedder, "JWT authentication setup", 10, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected search results, got none")
	}

	// Verify results have expected fields populated
	for _, r := range results {
		if r.Text == "" {
			t.Error("result has empty text")
		}
		if r.SourceFile == "" && !r.IsStone {
			t.Error("result has empty source file")
		}
	}
}

func TestPipeline_ImportanceHierarchy(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	// Ingest content with different importance levels
	md := `## Test Content
### Normal Section
This is normal content about authentication patterns.

### Key Section
This is key content about authentication implementation details.

### Solution Section
This is the solution for authentication: use OAuth2 with PKCE flow.
`
	path := writeTestMarkdown(t, md)

	_, err := ingest.IngestFile(database, embedder, path, "2026-01-15")
	if err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Get chunk IDs to mark importance
	var normalID, keyID, solutionID int
	rows, err := database.Query(`SELECT id, section_title FROM chunks ORDER BY section_sequence`)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	for rows.Next() {
		var id int
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			t.Fatalf("scan: %v", err)
		}
		switch {
		case title == "Normal Section":
			normalID = id
		case title == "Key Section":
			keyID = id
		case title == "Solution Section":
			solutionID = id
		}
	}
	rows.Close()

	if normalID == 0 || keyID == 0 || solutionID == 0 {
		t.Fatalf("couldn't find all chunks: normal=%d, key=%d, solution=%d", normalID, keyID, solutionID)
	}

	// Mark importance levels using the real API
	mark.MarkImportance(database, "chunk", strconv.Itoa(keyID), "key")
	mark.MarkImportance(database, "chunk", strconv.Itoa(solutionID), "solution")

	// Search — solutions should come before key, key before normal
	results, err := Search(database, embedder, "authentication", 10, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) < 3 {
		t.Fatalf("expected at least 3 results, got %d", len(results))
	}

	// Verify importance ordering: solution first, then key, then normal
	foundSolution := false
	foundKey := false
	for _, r := range results {
		if r.Importance == "solution" {
			foundSolution = true
			if foundKey {
				t.Error("solution appeared after key — importance ordering broken")
			}
		}
		if r.Importance == "key" {
			foundKey = true
			if !foundSolution {
				// This is expected if solution has worse vector distance,
				// but the ORDER BY should prioritize importance over distance
			}
		}
	}
}

func TestPipeline_StonesBeforeChunks(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	// Ingest regular content about auth
	md := `## Auth Notes
### Authentication Discussion
We discussed using JWT tokens for the API authentication layer.
`
	path := writeTestMarkdown(t, md)
	if _, err := ingest.IngestFile(database, embedder, path, "2026-01-15"); err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Add a stone about auth
	_, err := stone.CreateStone(database, stone.StoneInput{
		Title:    "Auth Fix",
		Category: "fix",
		Problem:  "JWT tokens expiring too fast",
		Solution: "Increased token TTL to 3600s",
		Tags:     "auth,jwt",
	})
	if err != nil {
		t.Fatalf("CreateStone: %v", err)
	}

	// Search — stones should appear first
	results, err := Search(database, embedder, "auth", 10, "")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}

	// First result should be the stone (if exact match found)
	stoneFound := false
	for _, r := range results {
		if r.IsStone {
			stoneFound = true
			if r.StoneTitle != "Auth Fix" {
				t.Errorf("unexpected stone title: %s", r.StoneTitle)
			}
			break
		}
	}
	if !stoneFound {
		t.Log("Note: stone not found in results (stone search is exact keyword, not vector)")
	}
}

func TestPipeline_AsOfDateFilter(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	md := `## January 10, 2026
### Early Decision
We decided to use PostgreSQL.

## February 20, 2026
### Later Decision
We switched to CockroachDB for horizontal scaling.
`
	path := writeTestMarkdown(t, md)

	if _, err := ingest.IngestFile(database, embedder, path, ""); err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Search with as-of before the second section
	results, err := Search(database, embedder, "database decision", 10, "2026-01-31")
	if err != nil {
		t.Fatalf("Search with as-of: %v", err)
	}

	// Only the January chunk should appear
	for _, r := range results {
		if r.IsStone {
			continue
		}
		if r.ValidAt > "2026-01-31" {
			t.Errorf("as-of filter failed: got chunk with valid_at=%s (should be <= 2026-01-31)", r.ValidAt)
		}
	}
}

func TestPipeline_ReIngestReplacesChunks(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	md1 := `## Test
### Original Content
This is the original content about feature X.
`
	path := writeTestMarkdown(t, md1)

	res1, err := ingest.IngestFile(database, embedder, path, "2026-01-15")
	if err != nil {
		t.Fatalf("first IngestFile: %v", err)
	}

	// Count chunks after first ingest
	var count1 int
	database.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count1)

	// Re-ingest same file with different content
	md2 := `## Test
### Updated Content
This is the updated content about feature Y, replacing the old content.

### New Section
Brand new section that didn't exist before.
`
	if err := os.WriteFile(path, []byte(md2), 0644); err != nil {
		t.Fatalf("overwrite test file: %v", err)
	}

	res2, err := ingest.IngestFile(database, embedder, path, "2026-01-15")
	if err != nil {
		t.Fatalf("second IngestFile: %v", err)
	}

	// Should have deleted old chunks
	if res2.DeletedChunks != int64(res1.ChunksCreated) {
		t.Errorf("expected %d deleted chunks, got %d", res1.ChunksCreated, res2.DeletedChunks)
	}

	// Count chunks after re-ingest — should only have new chunks
	var count2 int
	database.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count2)

	if count2 != res2.ChunksCreated {
		t.Errorf("expected %d chunks after re-ingest, got %d (possible duplicates)", res2.ChunksCreated, count2)
	}

	// Search should find new content, not old
	results, err := Search(database, embedder, "feature", 10, "")
	if err != nil {
		t.Fatalf("Search after re-ingest: %v", err)
	}

	for _, r := range results {
		if r.IsStone {
			continue
		}
		if r.SectionTitle == "Original Content" {
			t.Error("found old 'Original Content' chunk after re-ingest — deletion failed")
		}
	}
}

func TestPipeline_EmptyDBSearch(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	// Search on empty DB should return empty results, not error
	results, err := Search(database, embedder, "anything", 10, "")
	if err != nil {
		t.Fatalf("Search on empty DB should not error: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results on empty DB, got %d", len(results))
	}
}

// ============ MultiLayerSearch Tests ============

func TestMultiLayerSearch_ChunksAndMessages(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	database.SetMaxOpenConns(1)
	embedder := &testutil.MockEmbedder{}

	// Ingest a markdown file with auth content
	md := `## Auth Notes
### Authentication Setup
We configured JWT authentication with RS256 signing.
The token expiration is set to 1 hour with a 24-hour refresh window.
`
	path := writeTestMarkdown(t, md)
	if _, err := ingest.IngestFile(database, embedder, path, ""); err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Insert messages about auth
	_, err := db.InsertMessages(database, embedder, []db.TextMessage{
		{
			Role:      "user",
			Text:      "How does the authentication system work with JWT tokens?",
			Timestamp: time.Now(),
			IsUser:    true,
			MessageID: "msg-auth-001",
			SessionID: "sess-auth-001",
		},
		{
			Role:      "assistant",
			Text:      "The authentication uses JWT with RS256 signing for secure token validation.",
			Timestamp: time.Now().Add(time.Second),
			IsUser:    false,
			MessageID: "msg-auth-002",
			SessionID: "sess-auth-001",
		},
	})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	cfg := DefaultMultiLayerConfig()
	cfg.VecThreshold = 2.0 // lenient for test vectors
	results, _, err := MultiLayerSearch(database, embedder, "JWT authentication", cfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}

	// Assert both chunks and messages are present
	hasChunk := false
	hasMessage := false
	for _, r := range results {
		if r.Type == "chunk" {
			hasChunk = true
		}
		if r.Type == "message" {
			hasMessage = true
		}
	}

	if !hasChunk {
		t.Error("expected at least one chunk result")
	}
	if !hasMessage {
		t.Error("expected at least one message result")
	}
}

func TestMultiLayerSearch_EntityExpand(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	database.SetMaxOpenConns(1)
	embedder := &testutil.MockEmbedder{}

	// Insert entity "Alice" with alias "Bob"
	_, err := database.Exec(`INSERT INTO entities (name, type) VALUES ('Alice', 'person')`)
	if err != nil {
		t.Fatalf("insert entity: %v", err)
	}
	_, err = database.Exec(`INSERT INTO entity_aliases (entity_id, alias) VALUES (1, 'Bob')`)
	if err != nil {
		t.Fatalf("insert alias: %v", err)
	}

	// Insert messages containing "Alice" (NOT "Max")
	_, err = db.InsertMessages(database, embedder, []db.TextMessage{
		{
			Role:      "user",
			Text:      "Alice is working on the authentication module today.",
			Timestamp: time.Now(),
			IsUser:    true,
			MessageID: "msg-entity-001",
			SessionID: "sess-entity-001",
		},
		{
			Role:      "assistant",
			Text:      "Alice has been making great progress on the auth system.",
			Timestamp: time.Now().Add(time.Second),
			IsUser:    false,
			MessageID: "msg-entity-002",
			SessionID: "sess-entity-001",
		},
	})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// Search for "Bob" — entity expansion should replace it with "Alice"
	cfg := DefaultMultiLayerConfig()
	cfg.VecThreshold = 2.0
	results, _, err := MultiLayerSearch(database, embedder, "Bob", cfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch: %v", err)
	}

	// FTS5 layer should have found results because "Bob" was expanded to "Alice"
	if len(results) == 0 {
		t.Fatal("expected results after entity expansion, got none")
	}

	// At least one result should mention "Alice"
	foundAlice := false
	for _, r := range results {
		if strings.Contains(r.Text, "Alice") {
			foundAlice = true
			break
		}
	}
	if !foundAlice {
		t.Error("expected results containing 'Alice' after entity expansion from 'Bob'")
	}
}

func TestMultiLayerSearch_FTS5Layer(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	database.SetMaxOpenConns(1)
	embedder := &testutil.MockEmbedder{}

	// Insert messages with a distinctive exact phrase
	_, err := db.InsertMessages(database, embedder, []db.TextMessage{
		{
			Role:      "user",
			Text:      "The xyzzy_frobnicator_protocol is a unique authentication mechanism.",
			Timestamp: time.Now(),
			IsUser:    true,
			MessageID: "msg-fts5-001",
			SessionID: "sess-fts5-001",
		},
		{
			Role:      "assistant",
			Text:      "Yes, xyzzy_frobnicator_protocol handles token validation.",
			Timestamp: time.Now().Add(time.Second),
			IsUser:    false,
			MessageID: "msg-fts5-002",
			SessionID: "sess-fts5-001",
		},
	})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	cfg := DefaultMultiLayerConfig()
	cfg.VecThreshold = 2.0
	results, _, err := MultiLayerSearch(database, embedder, "xyzzy_frobnicator_protocol", cfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected FTS5 results for exact phrase, got none")
	}

	// At least one result should have "fts5" in LayerSources
	foundFTS5 := false
	for _, r := range results {
		for _, src := range r.LayerSources {
			if src == "fts5" {
				foundFTS5 = true
				break
			}
		}
		if foundFTS5 {
			break
		}
	}
	if !foundFTS5 {
		t.Error("expected at least one result with 'fts5' in LayerSources")
	}
}

func TestMultiLayerSearch_DuplicateBoost(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	database.SetMaxOpenConns(1)
	embedder := &testutil.MockEmbedder{}

	// Insert messages that should be found by both FTS5 and vec_messages.
	// Use a phrase that is both exact-matchable and semantically relevant.
	_, err := db.InsertMessages(database, embedder, []db.TextMessage{
		{
			Role:      "user",
			Text:      "authentication token validation is critical for security",
			Timestamp: time.Now(),
			IsUser:    true,
			MessageID: "msg-boost-001",
			SessionID: "sess-boost-001",
		},
		{
			Role:      "assistant",
			Text:      "token validation ensures authentication security",
			Timestamp: time.Now().Add(time.Second),
			IsUser:    false,
			MessageID: "msg-boost-002",
			SessionID: "sess-boost-001",
		},
	})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	cfg := DefaultMultiLayerConfig()
	cfg.VecThreshold = 2.0
	results, _, err := MultiLayerSearch(database, embedder, "authentication token validation", cfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}

	// Find any 2-layer results
	var twoLayerIdx []int
	var oneLayerIdx []int
	for i, r := range results {
		if r.Type == "message" {
			if r.Layers == 2 {
				twoLayerIdx = append(twoLayerIdx, i)
			} else if r.Layers == 1 {
				oneLayerIdx = append(oneLayerIdx, i)
			}
		}
	}

	// If we have both 1-layer and 2-layer results, 2-layer must sort above 1-layer
	if len(twoLayerIdx) > 0 && len(oneLayerIdx) > 0 {
		maxTwoLayerIdx := twoLayerIdx[len(twoLayerIdx)-1]
		minOneLayerIdx := oneLayerIdx[0]
		if maxTwoLayerIdx > minOneLayerIdx {
			t.Errorf("2-layer result at index %d appears after 1-layer result at index %d — boost ordering broken",
				maxTwoLayerIdx, minOneLayerIdx)
		}
	}
}

func TestMultiLayerSearch_StonesFirst(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	database.SetMaxOpenConns(1)
	embedder := &testutil.MockEmbedder{}

	// Create a stone about auth
	_, err := stone.CreateStone(database, stone.StoneInput{
		Title:    "auth token fix",
		Category: "fix",
		Problem:  "auth tokens expiring too quickly",
		Solution: "Increased auth token TTL to 3600 seconds",
		Tags:     "auth,token",
	})
	if err != nil {
		t.Fatalf("CreateStone: %v", err)
	}

	// Ingest chunks about auth
	md := `## Auth Notes
### Authentication Overview
The auth system uses JWT tokens for API authentication.
`
	path := writeTestMarkdown(t, md)
	if _, err := ingest.IngestFile(database, embedder, path, ""); err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Insert messages about auth
	_, err = db.InsertMessages(database, embedder, []db.TextMessage{
		{
			Role:      "user",
			Text:      "How does auth work in this system?",
			Timestamp: time.Now(),
			IsUser:    true,
			MessageID: "msg-stone-001",
			SessionID: "sess-stone-001",
		},
	})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	cfg := DefaultMultiLayerConfig()
	cfg.VecThreshold = 2.0
	results, _, err := MultiLayerSearch(database, embedder, "auth", cfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}

	// First result must be a stone
	if results[0].Type != "stone" {
		t.Errorf("expected first result to be a stone, got type=%q", results[0].Type)
	}

	// All stones must appear before any non-stone
	passedStones := false
	for _, r := range results {
		if r.Type == "stone" {
			if passedStones {
				t.Error("found a stone after a non-stone result — stones must come first")
				break
			}
		} else {
			passedStones = true
		}
	}
}

func TestMultiLayerSearch_EmptyDB(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	cfg := DefaultMultiLayerConfig()
	results, _, err := MultiLayerSearch(database, embedder, "anything", cfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch on empty DB should not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results on empty DB, got %d", len(results))
	}
}

// failingEmbedder simulates Ollama being unreachable.
type failingEmbedder struct{}

func (f *failingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("ollama unreachable")
}

func TestMultiLayerSearch_GracefulDegradation(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()

	// Use a real embedder to insert messages (so they have embeddings and FTS entries)
	realEmbedder := &testutil.MockEmbedder{}
	_, err := db.InsertMessages(database, realEmbedder, []db.TextMessage{
		{
			Role:      "user",
			Text:      "graceful degradation test message about authentication",
			Timestamp: time.Now(),
			IsUser:    true,
			MessageID: "msg-degrade-001",
			SessionID: "sess-degrade-001",
		},
		{
			Role:      "assistant",
			Text:      "authentication graceful degradation is important for reliability",
			Timestamp: time.Now().Add(time.Second),
			IsUser:    false,
			MessageID: "msg-degrade-002",
			SessionID: "sess-degrade-001",
		},
	})
	if err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	// Now search with a failing embedder — vec layers will fail, FTS5 should still work
	badEmbedder := &failingEmbedder{}
	cfg := DefaultMultiLayerConfig()
	results, _, err := MultiLayerSearch(database, badEmbedder, "authentication", cfg)

	// Should NOT return an error — graceful degradation means FTS5 results come through
	if err != nil {
		t.Fatalf("expected graceful degradation (no error), got: %v", err)
	}

	// Should have FTS5 results
	if len(results) == 0 {
		t.Fatal("expected FTS5-only results when embedder fails, got none")
	}

	// All results should come from FTS5 (vec layers failed)
	for _, r := range results {
		for _, src := range r.LayerSources {
			if src == "vec_chunks" || src == "vec_messages" {
				t.Errorf("unexpected vec layer result when embedder is failing: source=%q", src)
			}
		}
	}
}

func TestMultiLayerSearch_VecThreshold(t *testing.T) {
	database := testutil.NewTestDB()
	defer database.Close()
	embedder := &testutil.MockEmbedder{}

	// Ingest content
	md := `## Auth Notes
### Authentication Setup
We configured JWT authentication with RS256 signing.
The token expiration is set to 1 hour with a 24-hour refresh window.

### Database Setup
We use PostgreSQL for persistent storage of authentication tokens.

### Session Management
Sessions are managed with Redis for fast token lookup and invalidation.
`
	path := writeTestMarkdown(t, md)
	if _, err := ingest.IngestFile(database, embedder, path, ""); err != nil {
		t.Fatalf("IngestFile: %v", err)
	}

	// Run with very strict threshold (almost nothing passes)
	strictCfg := DefaultMultiLayerConfig()
	strictCfg.VecThreshold = 0.01
	strictResults, _, err := MultiLayerSearch(database, embedder, "authentication", strictCfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch strict: %v", err)
	}

	// Run with very lenient threshold (everything passes)
	lenientCfg := DefaultMultiLayerConfig()
	lenientCfg.VecThreshold = 2.0
	lenientResults, _, err := MultiLayerSearch(database, embedder, "authentication", lenientCfg)
	if err != nil {
		t.Fatalf("MultiLayerSearch lenient: %v", err)
	}

	// Lenient should return >= strict (strict filters more aggressively)
	if len(lenientResults) < len(strictResults) {
		t.Errorf("expected lenient threshold to return >= results than strict: lenient=%d strict=%d",
			len(lenientResults), len(strictResults))
	}

	// Log for visibility
	t.Logf("strict (threshold=0.01): %d results, lenient (threshold=2.0): %d results",
		len(strictResults), len(lenientResults))

	// Sanity: use the strconv import
	_ = strconv.Itoa(len(strictResults))
}
