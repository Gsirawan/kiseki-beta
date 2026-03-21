package db

import (
	"database/sql"
)

// SourceFileInfo holds aggregated information about a single ingested source file.
type SourceFileInfo struct {
	SourceFile     string `json:"source_file"`
	ChunkCount     int    `json:"chunk_count"`
	EarliestDate   string `json:"earliest_date"`
	LatestDate     string `json:"latest_date"`
	LastIngestedAt string `json:"last_ingested_at"`
}

// ListFiles returns all ingested source files with their chunk counts and date ranges,
// sorted by last ingested timestamp descending (most recent first).
func ListFiles(db *sql.DB) ([]SourceFileInfo, error) {
	rows, err := db.Query(`
		SELECT
			source_file,
			COUNT(*) AS chunk_count,
			MIN(CASE WHEN valid_at IS NOT NULL AND valid_at != '' THEN valid_at END) AS earliest_date,
			MAX(CASE WHEN valid_at IS NOT NULL AND valid_at != '' THEN valid_at END) AS latest_date,
			MAX(ingested_at) AS last_ingested_at
		FROM chunks
		GROUP BY source_file
		ORDER BY MAX(ingested_at) DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []SourceFileInfo
	for rows.Next() {
		var f SourceFileInfo
		var earliestDate sql.NullString
		var latestDate sql.NullString
		if err := rows.Scan(
			&f.SourceFile,
			&f.ChunkCount,
			&earliestDate,
			&latestDate,
			&f.LastIngestedAt,
		); err != nil {
			return nil, err
		}
		if earliestDate.Valid {
			f.EarliestDate = earliestDate.String
		}
		if latestDate.Valid {
			f.LatestDate = latestDate.String
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if files == nil {
		files = []SourceFileInfo{}
	}
	return files, nil
}
