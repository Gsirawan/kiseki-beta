package db

import (
	"database/sql"
	"fmt"
	"strconv"
)

// SetConfig stores a key-value pair in kiseki_config.
// Requires the kiseki_config table to exist (created by migration V2).
func SetConfig(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT OR REPLACE INTO kiseki_config (key, value) VALUES (?, ?)`, key, value)
	return err
}

// GetConfig reads a value from kiseki_config.
// Returns ("", nil) if the key doesn't exist.
func GetConfig(db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM kiseki_config WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read config key %q: %w", key, err)
	}
	return value, nil
}

// ValidateEmbedDimConfig checks that the configured EMBED_DIM matches what's stored in the database.
//
// On first run (no stored value): stores the current EmbedDimension.
// On subsequent runs: compares stored vs configured and returns an error on mismatch.
//
// This prevents silent database corruption when someone changes EMBED_DIM
// after already ingesting data with a different dimension.
func ValidateEmbedDimConfig(db *sql.DB) error {
	stored, err := GetConfig(db, "embed_dim")
	if err != nil {
		return fmt.Errorf("read embed_dim config: %w", err)
	}

	currentDim := strconv.Itoa(EmbedDimension)

	if stored == "" {
		// First run — record the current dimension
		return SetConfig(db, "embed_dim", currentDim)
	}

	if stored != currentDim {
		return fmt.Errorf(
			"database was created with EMBED_DIM=%s but current config is EMBED_DIM=%s. "+
				"Mismatched dimensions corrupt vector search.\n"+
				"  To keep existing data: set EMBED_DIM=%s in your .env\n"+
				"  To re-embed with new model: run 'kiseki re-embed'\n"+
				"  To start fresh: run 'kiseki forget --all' then re-ingest",
			stored, currentDim, stored,
		)
	}

	return nil
}

// StoredEmbedDim returns the embed dimension stored in the database config.
// Returns 0 if not yet stored.
func StoredEmbedDim(db *sql.DB) int {
	val, err := GetConfig(db, "embed_dim")
	if err != nil || val == "" {
		return 0
	}
	dim, err := strconv.Atoi(val)
	if err != nil {
		return 0
	}
	return dim
}
