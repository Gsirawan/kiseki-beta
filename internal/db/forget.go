package db

import (
	"database/sql"
	"fmt"
)

// ForgetResult describes what was deleted.
type ForgetResult struct {
	ChunksDeleted int    `json:"chunks_deleted"`
	Description   string `json:"description"`
}

// ForgetByFile deletes all chunks from a specific source file.
// Returns the number of chunks deleted.
func ForgetByFile(db *sql.DB, filePath string) (ForgetResult, error) {
	// Count first
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_file = ?`, filePath).Scan(&count)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("count chunks: %w", err)
	}

	if count == 0 {
		return ForgetResult{
			ChunksDeleted: 0,
			Description:   fmt.Sprintf("no chunks found for file: %s", filePath),
		}, nil
	}

	// Delete from vec_chunks first (foreign key order)
	_, err = db.Exec(`
		DELETE FROM vec_chunks
		WHERE chunk_id IN (SELECT id FROM chunks WHERE source_file = ?)
	`, filePath)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("delete vec_chunks: %w", err)
	}

	// Delete from chunks
	res, err := db.Exec(`DELETE FROM chunks WHERE source_file = ?`, filePath)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("delete chunks: %w", err)
	}

	deleted, _ := res.RowsAffected()
	return ForgetResult{
		ChunksDeleted: int(deleted),
		Description:   fmt.Sprintf("deleted %d chunks from %s", deleted, filePath),
	}, nil
}

// ForgetBefore deletes all chunks with valid_at before the given date (YYYY-MM-DD).
func ForgetBefore(db *sql.DB, date string) (ForgetResult, error) {
	// Count first
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM chunks WHERE valid_at IS NOT NULL AND valid_at < ?`, date,
	).Scan(&count)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("count chunks: %w", err)
	}

	if count == 0 {
		return ForgetResult{
			ChunksDeleted: 0,
			Description:   fmt.Sprintf("no chunks found with valid_at before %s", date),
		}, nil
	}

	// Delete from vec_chunks first
	_, err = db.Exec(`
		DELETE FROM vec_chunks
		WHERE chunk_id IN (
			SELECT id FROM chunks WHERE valid_at IS NOT NULL AND valid_at < ?
		)
	`, date)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("delete vec_chunks: %w", err)
	}

	// Delete from chunks
	res, err := db.Exec(`DELETE FROM chunks WHERE valid_at IS NOT NULL AND valid_at < ?`, date)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("delete chunks: %w", err)
	}

	deleted, _ := res.RowsAffected()
	return ForgetResult{
		ChunksDeleted: int(deleted),
		Description:   fmt.Sprintf("deleted %d chunks with valid_at before %s", deleted, date),
	}, nil
}

// ForgetAll deletes all chunks from the database.
func ForgetAll(db *sql.DB) (ForgetResult, error) {
	// Count first
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("count chunks: %w", err)
	}

	if count == 0 {
		return ForgetResult{
			ChunksDeleted: 0,
			Description:   "database is already empty",
		}, nil
	}

	// Delete all vec_chunks
	if _, err := db.Exec(`DELETE FROM vec_chunks`); err != nil {
		return ForgetResult{}, fmt.Errorf("delete vec_chunks: %w", err)
	}

	// Delete all chunks
	res, err := db.Exec(`DELETE FROM chunks`)
	if err != nil {
		return ForgetResult{}, fmt.Errorf("delete chunks: %w", err)
	}

	deleted, _ := res.RowsAffected()
	return ForgetResult{
		ChunksDeleted: int(deleted),
		Description:   fmt.Sprintf("deleted all %d chunks from database", deleted),
	}, nil
}

// CountForgetByFile returns how many chunks would be deleted for a given file.
func CountForgetByFile(db *sql.DB, filePath string) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_file = ?`, filePath).Scan(&count)
	return count, err
}

// CountForgetBefore returns how many chunks would be deleted before a given date.
func CountForgetBefore(db *sql.DB, date string) (int, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM chunks WHERE valid_at IS NOT NULL AND valid_at < ?`, date,
	).Scan(&count)
	return count, err
}
