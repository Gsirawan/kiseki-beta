package search

import (
	"context"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/stone"
	"database/sql"
	"sort"
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

type SearchResult struct {
	ID             int
	Text           string
	SourceFile     string
	SectionTitle   string
	ParentTitle    string
	HeaderLevel    int
	ValidAt        string
	Importance     string
	Distance       float64
	IsStone        bool
	StoneID        string
	StoneTitle     string
	StoneCategory  string
	StoneCreatedAt string
}

func Search(db *sql.DB, embedder ollama.Embedder, query string, limit int, asOf string) ([]SearchResult, error) {
	stoneLimit := limit
	if stoneLimit <= 0 {
		stoneLimit = 10
	}
	stoneMatches, err := stone.SearchStonesExact(db, query, stoneLimit)
	if err != nil {
		return nil, err
	}

	stoneResults := make([]SearchResult, 0, len(stoneMatches))
	for _, stone := range stoneMatches {
		text := strings.TrimSpace(stone.Solution)
		if text == "" {
			text = strings.TrimSpace(stone.Problem)
		}
		if text == "" {
			text = stone.Title
		}
		stoneResults = append(stoneResults, SearchResult{
			Text:           text,
			SourceFile:     "stones",
			SectionTitle:   stone.Title,
			ValidAt:        stone.CreatedAt,
			Importance:     "solution",
			Distance:       0,
			IsStone:        true,
			StoneID:        stone.ID,
			StoneTitle:     stone.Title,
			StoneCategory:  stone.Category,
			StoneCreatedAt: stone.CreatedAt,
		})
	}

	chunkResults, err := searchChunkResults(db, embedder, query, limit, asOf)
	if err != nil {
		return nil, err
	}

	combined := make([]SearchResult, 0, len(stoneResults)+len(chunkResults))
	combined = append(combined, stoneResults...)
	combined = append(combined, chunkResults...)
	return combined, nil
}

func searchChunkResults(db *sql.DB, embedder ollama.Embedder, query string, limit int, asOf string) ([]SearchResult, error) {
	ctx := context.Background()
	embedding, err := embedder.Embed(ctx, query)
	if err != nil {
		return nil, err
	}

	serialized, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, err
	}

	fetchLimit := limit
	if asOf != "" {
		fetchLimit = limit * 3
	}

	rows, err := db.Query(
		`SELECT v.chunk_id, v.distance, c.text, c.source_file, c.section_title, c.parent_title, c.header_level, c.valid_at, c.importance
		 FROM vec_chunks v
		 JOIN chunks c ON c.id = v.chunk_id
		 WHERE v.embedding MATCH ? AND v.k = ?
		 ORDER BY
			CASE c.importance
				WHEN 'solution' THEN 1
				WHEN 'key' THEN 2
				ELSE 3
			END,
			v.distance
		 LIMIT ?`,
		serialized,
		fetchLimit,
		fetchLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []SearchResult{}
	for rows.Next() {
		var result SearchResult
		var parentTitle sql.NullString
		var validAt sql.NullString
		if err := rows.Scan(
			&result.ID,
			&result.Distance,
			&result.Text,
			&result.SourceFile,
			&result.SectionTitle,
			&parentTitle,
			&result.HeaderLevel,
			&validAt,
			&result.Importance,
		); err != nil {
			return nil, err
		}
		if parentTitle.Valid {
			result.ParentTitle = parentTitle.String
		}
		if validAt.Valid {
			result.ValidAt = validAt.String
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if asOf != "" {
		filtered := make([]SearchResult, 0, len(results))
		for _, result := range results {
			if result.ValidAt == "" || result.ValidAt <= asOf {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}

	if len(results) > limit {
		results = results[:limit]
	}

	sort.SliceStable(results, func(i, j int) bool {
		leftRank := importanceRank(results[i].Importance)
		rightRank := importanceRank(results[j].Importance)
		if leftRank != rightRank {
			return leftRank < rightRank
		}

		left := results[i].ValidAt
		right := results[j].ValidAt
		if left == "" && right == "" {
			return false
		}
		if left == "" {
			return true
		}
		if right == "" {
			return false
		}
		return left < right
	})

	return results, nil
}

func importanceRank(importance string) int {
	switch importance {
	case "solution":
		return 1
	case "key":
		return 2
	default:
		return 3
	}
}
