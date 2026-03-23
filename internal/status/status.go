package status

import (
	"context"
	"database/sql"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
)

type StatusInfo struct {
	OllamaHealthy      bool
	EmbedModel         string
	SqliteVecVersion   string
	TotalChunks        int
	TotalThoughts      int
	EarliestValidAt    string
	LatestValidAt      string
	SchemaVersion      int
	StoredEmbedDim     int
	ConfiguredEmbedDim int
	Encrypted          bool
}

// Status gathers system status information.
// It never returns an error — it returns whatever it can gather.
// embedModel is passed separately since OllamaClient fields are unexported.
func Status(database *sql.DB, ollamaClient *ollama.OllamaClient, embedModel string) StatusInfo {
	info := StatusInfo{
		EmbedModel: embedModel,
	}

	// Check Ollama health
	ctx := context.Background()
	info.OllamaHealthy = ollamaClient.IsHealthy(ctx)

	// Schema version
	info.SchemaVersion = db.SchemaVersion(database)

	// Embed dimension info
	info.StoredEmbedDim = db.StoredEmbedDim(database)
	info.ConfiguredEmbedDim = db.EmbedDimension

	// Encryption status
	info.Encrypted = db.IsEncryptionEnabled()

	// Get sqlite-vec version
	var vecVersion string
	err := database.QueryRow("SELECT vec_version()").Scan(&vecVersion)
	if err == nil {
		info.SqliteVecVersion = vecVersion
	}

	// Count total chunks
	var totalChunks int
	err = database.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&totalChunks)
	if err == nil {
		info.TotalChunks = totalChunks
	}

	// Count thoughts (returns 0 if table doesn't exist yet)
	info.TotalThoughts = db.CountThoughts(database)

	// Get earliest valid_at (ignoring NULLs)
	var earliestValidAt sql.NullString
	err = database.QueryRow("SELECT MIN(valid_at) FROM chunks WHERE valid_at IS NOT NULL").Scan(&earliestValidAt)
	if err == nil && earliestValidAt.Valid {
		info.EarliestValidAt = earliestValidAt.String
	}

	// Get latest valid_at (ignoring NULLs)
	var latestValidAt sql.NullString
	err = database.QueryRow("SELECT MAX(valid_at) FROM chunks WHERE valid_at IS NOT NULL").Scan(&latestValidAt)
	if err == nil && latestValidAt.Valid {
		info.LatestValidAt = latestValidAt.String
	}

	return info
}
