package mark

import (
	"database/sql"
	"github.com/Gsirawan/kiseki-beta/internal/db"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func NormalizeImportance(value string) (string, error) {
	importance := strings.ToLower(strings.TrimSpace(value))
	switch importance {
	case "solution", "key", "normal":
		return importance, nil
	default:
		return "", fmt.Errorf("importance must be one of: solution, key, normal")
	}
}

func MarkImportance(db *sql.DB, targetType, id, importance string) (bool, error) {
	normalizedType := strings.ToLower(strings.TrimSpace(targetType))
	importance, err := NormalizeImportance(importance)
	if err != nil {
		return false, err
	}

	var result sql.Result
	switch normalizedType {
	case "chunk":
		chunkID, err := strconv.Atoi(id)
		if err != nil {
			return false, fmt.Errorf("chunk id must be an integer")
		}
		result, err = db.Exec(`UPDATE chunks SET importance = ? WHERE id = ?`, importance, chunkID)
		if err != nil {
			return false, fmt.Errorf("update chunk: %w", err)
		}
	case "message":
		result, err = db.Exec(`UPDATE messages SET importance = ? WHERE id = ?`, importance, id)
		if err != nil {
			return false, fmt.Errorf("update message: %w", err)
		}
	default:
		return false, fmt.Errorf("type must be one of: chunk, message")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check rows affected: %w", err)
	}

	return affected > 0, nil
}

func RunMark(args []string, kisekiDB string) {
	if len(args) != 3 {
		fmt.Fprintf(os.Stderr, "Usage: kiseki mark <chunk|message> <id> <solution|key|normal>\n")
		os.Exit(1)
	}

	db, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	updated, err := MarkImportance(db, args[0], args[1], args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: mark: %v\n", err)
		os.Exit(1)
	}
	if !updated {
		fmt.Fprintf(os.Stderr, "Error: %s with id %s not found\n", args[0], args[1])
		os.Exit(1)
	}

	importance, _ := NormalizeImportance(args[2])
	fmt.Printf("Updated %s %s importance to %s\n", strings.ToLower(args[0]), args[1], importance)
}
