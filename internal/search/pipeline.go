package search

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"sync"

	dbpkg "github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/entity"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/stone"
	"strings"
)

// MultiLayerResult is the unified result type — can be chunk, message, or stone.
type MultiLayerResult struct {
	Type          string   `json:"type"` // "chunk", "message", "stone"
	ID            string   `json:"id"`   // chunk ID (as string) or message ID or stone ID
	Text          string   `json:"text"`
	SourceFile    string   `json:"source_file,omitempty"`
	SectionTitle  string   `json:"section_title,omitempty"`
	ParentTitle   string   `json:"parent_title,omitempty"`
	HeaderLevel   int      `json:"header_level,omitempty"`
	ValidAt       string   `json:"valid_at,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	Role          string   `json:"role,omitempty"`
	Timestamp     int64    `json:"timestamp,omitempty"`
	Importance    string   `json:"importance"`
	Distance      float64  `json:"distance"`      // lower = better; 0 for exact/stone
	Layers        int      `json:"layers"`        // how many search layers found this (1, 2, or 3)
	LayerSources  []string `json:"layer_sources"` // e.g. ["fts5","vec_messages"]
	IsStone       bool     `json:"is_stone,omitempty"`
	StoneID       string   `json:"stone_id,omitempty"`
	StoneTitle    string   `json:"stone_title,omitempty"`
	StoneCategory string   `json:"stone_category,omitempty"`
}

// MultiLayerConfig holds tuning knobs for the pipeline.
type MultiLayerConfig struct {
	MinPerLayer      int     // floor per layer (default 50)
	VecThreshold     float64 // max vec distance to include (default 1.5)
	FTSRankThreshold float64 // unused for now, reserved for future rank filtering
	AsOf             string  // optional date filter for chunks (ISO date string)
}

// DefaultMultiLayerConfig returns sensible defaults.
func DefaultMultiLayerConfig() MultiLayerConfig {
	return MultiLayerConfig{
		MinPerLayer:  50,
		VecThreshold: 1.5,
	}
}

// MultiLayerSearch runs the 3-layer parallel search pipeline:
// entity expansion → stones → (FTS5 + vec_chunks + vec_messages in parallel) → merge + rank.
// Returns results and any entity alias matches found during expansion.
func MultiLayerSearch(db *sql.DB, embedder ollama.Embedder, query string, cfg MultiLayerConfig) ([]MultiLayerResult, []entity.AliasMatch, error) {
	expandedQuery := query
	var entityMatches []entity.AliasMatch
	if eq, matches, err := entity.ExpandQuery(db, query); err != nil {
		log.Printf("[search/pipeline] entity expansion warning: %v — using original query", err)
	} else {
		expandedQuery = eq
		entityMatches = matches
	}

	// Step 2: Stones (sequential, cheap).
	stoneMatches, err := stone.SearchStonesExact(db, query, cfg.MinPerLayer)
	if err != nil {
		log.Printf("[search/pipeline] stone search warning: %v", err)
		stoneMatches = nil
	}
	stoneResults := make([]MultiLayerResult, 0, len(stoneMatches))
	for _, s := range stoneMatches {
		text := strings.TrimSpace(s.Solution)
		if text == "" {
			text = strings.TrimSpace(s.Problem)
		}
		if text == "" {
			text = s.Title
		}
		stoneResults = append(stoneResults, MultiLayerResult{
			Type:          "stone",
			ID:            s.ID,
			Text:          text,
			SourceFile:    "stones",
			SectionTitle:  s.Title,
			ValidAt:       s.CreatedAt,
			Importance:    "solution",
			Distance:      0,
			Layers:        1,
			LayerSources:  []string{"stone"},
			IsStone:       true,
			StoneID:       s.ID,
			StoneTitle:    s.Title,
			StoneCategory: s.Category,
		})
	}

	// Step 3: Three search layers in parallel.
	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		ftsResults []dbpkg.MessageSearchResult
		ftsErr     error
		chunkRes   []SearchResult
		chunkErr   error
		msgResults []dbpkg.MessageSearchResult
		msgErr     error
	)

	wg.Add(3)

	// Goroutine 1: FTS5 — receives expandedQuery (catches canonical name matches).
	go func() {
		defer wg.Done()
		res, err := dbpkg.SearchMessagesFTS(db, expandedQuery, cfg.MinPerLayer)
		mu.Lock()
		ftsResults = res
		ftsErr = err
		mu.Unlock()
	}()

	// Goroutine 2: vec_chunks — receives expandedQuery (canonical names produce better embeddings than short aliases).
	go func() {
		defer wg.Done()
		res, err := searchChunkResults(db, embedder, expandedQuery, cfg.MinPerLayer, cfg.AsOf)
		mu.Lock()
		chunkRes = res
		chunkErr = err
		mu.Unlock()
	}()

	// Goroutine 3: vec_messages — receives expandedQuery (same reason as vec_chunks).
	go func() {
		defer wg.Done()
		res, err := dbpkg.SearchMessages(db, embedder, expandedQuery, cfg.MinPerLayer)
		mu.Lock()
		msgResults = res
		msgErr = err
		mu.Unlock()
	}()

	wg.Wait()

	// Graceful degradation: log warnings, only hard-fail if ALL three layers failed.
	if ftsErr != nil {
		log.Printf("[search/pipeline] FTS5 layer warning: %v", ftsErr)
		ftsResults = nil
	}
	if chunkErr != nil {
		log.Printf("[search/pipeline] vec_chunks layer warning: %v", chunkErr)
		chunkRes = nil
	}
	if msgErr != nil {
		log.Printf("[search/pipeline] vec_messages layer warning: %v", msgErr)
		msgResults = nil
	}
	if ftsErr != nil && chunkErr != nil && msgErr != nil {
		return nil, entityMatches, fmt.Errorf("all search layers failed: fts5=%w, vec_chunks=%v, vec_messages=%v", ftsErr, chunkErr, msgErr)
	}

	// Step 4: Quality filtering — vec results only (FTS5 distance is always 0).
	filteredChunks := make([]SearchResult, 0, len(chunkRes))
	for _, c := range chunkRes {
		if c.Distance <= cfg.VecThreshold {
			filteredChunks = append(filteredChunks, c)
		}
	}
	filteredMsgs := make([]dbpkg.MessageSearchResult, 0, len(msgResults))
	for _, m := range msgResults {
		if m.Distance <= cfg.VecThreshold {
			filteredMsgs = append(filteredMsgs, m)
		}
	}
	// FTS5 results pass through unfiltered.

	// Step 5: Merge + deduplicate + tier rank.
	merged := mergeResults(stoneResults, filteredChunks, ftsResults, filteredMsgs)

	return merged, entityMatches, nil
}

// mergeResults converts all layer results to MultiLayerResult, deduplicates messages
// across FTS5 and vec_messages, then applies tier ranking.
func mergeResults(
	stones []MultiLayerResult,
	chunks []SearchResult,
	ftsMessages []dbpkg.MessageSearchResult,
	vecMessages []dbpkg.MessageSearchResult,
) []MultiLayerResult {
	// Map for message deduplication: messageID → *MultiLayerResult index in output slice.
	msgIndex := make(map[string]int)
	results := make([]MultiLayerResult, 0, len(stones)+len(chunks)+len(ftsMessages)+len(vecMessages))

	// Add stones first (they're already MultiLayerResult).
	results = append(results, stones...)

	// Convert vec_chunks → MultiLayerResult.
	for _, c := range chunks {
		results = append(results, MultiLayerResult{
			Type:          "chunk",
			ID:            strconv.Itoa(c.ID),
			Text:          c.Text,
			SourceFile:    c.SourceFile,
			SectionTitle:  c.SectionTitle,
			ParentTitle:   c.ParentTitle,
			HeaderLevel:   c.HeaderLevel,
			ValidAt:       c.ValidAt,
			Importance:    c.Importance,
			Distance:      c.Distance,
			Layers:        1,
			LayerSources:  []string{"vec_chunks"},
			IsStone:       c.IsStone,
			StoneID:       c.StoneID,
			StoneTitle:    c.StoneTitle,
			StoneCategory: c.StoneCategory,
		})
	}

	// Convert FTS5 messages → MultiLayerResult, track by message ID.
	for _, m := range ftsMessages {
		r := MultiLayerResult{
			Type:         "message",
			ID:           m.MessageID,
			Text:         m.Text,
			SessionID:    m.SessionID,
			Role:         m.Role,
			Timestamp:    m.Timestamp,
			Importance:   m.Importance,
			Distance:     0, // FTS5 distance is always 0
			Layers:       1,
			LayerSources: []string{"fts5"},
		}
		msgIndex[m.MessageID] = len(results)
		results = append(results, r)
	}

	// Convert vec_messages → MultiLayerResult, merging with FTS5 hits if same ID.
	for _, m := range vecMessages {
		if idx, exists := msgIndex[m.MessageID]; exists {
			// Same message found in both FTS5 and vec_messages → 2-layer hit.
			existing := &results[idx]
			existing.Layers = 2
			existing.LayerSources = []string{"fts5", "vec_messages"}
			// Use vec distance for meaningful ranking within tier (FTS5 distance is always 0).
			existing.Distance = m.Distance
		} else {
			r := MultiLayerResult{
				Type:         "message",
				ID:           m.MessageID,
				Text:         m.Text,
				SessionID:    m.SessionID,
				Role:         m.Role,
				Timestamp:    m.Timestamp,
				Importance:   m.Importance,
				Distance:     m.Distance,
				Layers:       1,
				LayerSources: []string{"vec_messages"},
			}
			msgIndex[m.MessageID] = len(results)
			results = append(results, r)
		}
	}

	// Tier ranking:
	// 1. Stones first (always top)
	// 2. Layers DESC (3 > 2 > 1)
	// 3. importanceRank ASC (solution=1, key=2, normal=3)
	// 4. Distance ASC (lower = better)
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := results[i], results[j]

		// Stones always first.
		iStone := ri.Type == "stone"
		jStone := rj.Type == "stone"
		if iStone != jStone {
			return iStone
		}

		// More layers = higher priority.
		if ri.Layers != rj.Layers {
			return ri.Layers > rj.Layers
		}

		// Importance rank.
		iRank := importanceRank(ri.Importance)
		jRank := importanceRank(rj.Importance)
		if iRank != jRank {
			return iRank < jRank
		}

		// Distance (lower = better).
		return ri.Distance < rj.Distance
	})

	return results
}
