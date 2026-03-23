package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gsirawan/kiseki-beta/internal/keywords"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

var EmbedDimension = 1024

// dbEncryptionKey holds the encryption key loaded from KISEKI_DB_KEY.
// Empty string means plaintext mode (no encryption).
var dbEncryptionKey string

func init() {
	sqlite_vec.Auto()
}

// LoadEncryptionKey reads KISEKI_DB_KEY from the environment.
// Must be called after godotenv.Load() in main.
func LoadEncryptionKey() {
	dbEncryptionKey = os.Getenv("KISEKI_DB_KEY")
}

// IsEncryptionEnabled returns true if an encryption key is configured.
func IsEncryptionEnabled() bool {
	return dbEncryptionKey != ""
}

// applyEncryptionKey issues PRAGMA key on the connection if an encryption key is set.
// This MUST be the very first statement after sql.Open(), before any other PRAGMA or query.
// Returns nil if no key is configured (plaintext mode).
func applyEncryptionKey(db *sql.DB) error {
	if dbEncryptionKey == "" {
		return nil
	}

	// PRAGMA key must be the first statement on an encrypted database.
	// SQLCipher requires this before any other operation.
	// Note: PRAGMA does not support parameter binding — the key must be a string literal.
	// The key comes from a trusted environment variable, not user input.
	pragmaSQL := fmt.Sprintf("PRAGMA key = '%s'", strings.ReplaceAll(dbEncryptionKey, "'", "''"))
	if _, err := db.Exec(pragmaSQL); err != nil {
		return fmt.Errorf("encryption: wrong key or database is not encrypted: %w", err)
	}

	// Validate the key by reading sqlite_master.
	// If the key is wrong, this will fail with "file is not a database".
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&count); err != nil {
		return fmt.Errorf("encryption: wrong key or database is not encrypted: %w", err)
	}

	return nil
}

func LoadEmbedDimension() {
	if dim := os.Getenv("EMBED_DIM"); dim != "" {
		if d, err := strconv.Atoi(dim); err == nil && d > 0 {
			EmbedDimension = d
		}
	}
}

func buildSchema(dim int) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS chunks (
    id INTEGER PRIMARY KEY,
    text TEXT NOT NULL,
    source_file TEXT NOT NULL,
    section_title TEXT NOT NULL,
    importance TEXT NOT NULL DEFAULT 'normal',
    header_level INTEGER NOT NULL DEFAULT 2,
    parent_title TEXT,
    section_sequence INTEGER,
    chunk_sequence INTEGER,
    chunk_total INTEGER,
    valid_at TEXT,
    ingested_at TEXT NOT NULL,
    UNIQUE(source_file, section_sequence, chunk_sequence)
);

CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
    chunk_id INTEGER PRIMARY KEY,
    embedding float[%d] distance_metric=cosine
);

-- Phase 2: Messages table for raw conversation storage
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    role TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    text TEXT NOT NULL,
    importance TEXT NOT NULL DEFAULT 'normal'
);

CREATE INDEX IF NOT EXISTS idx_messages_session_ts ON messages(session_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id);

CREATE TABLE IF NOT EXISTS stones (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    category TEXT,
    problem TEXT,
    solution TEXT,
    tags TEXT,
    chunk_ids TEXT,
    key_chunk_ids TEXT,
    source_session TEXT,
    created_at TEXT NOT NULL,
    week TEXT
);

CREATE INDEX IF NOT EXISTS idx_stones_title ON stones(title);
CREATE INDEX IF NOT EXISTS idx_stones_tags ON stones(tags);
CREATE INDEX IF NOT EXISTS idx_stones_week ON stones(week);

-- Phase 2: Vector search on messages (search actual words, not compressed topics)
CREATE VIRTUAL TABLE IF NOT EXISTS vec_messages USING vec0(
    message_id TEXT PRIMARY KEY,
    embedding float[%d] distance_metric=cosine
);
`, dim, dim)
}

var fts5Available = false

// FTS5 schema - run separately because CREATE VIRTUAL TABLE IF NOT EXISTS
// doesn't work well with FTS5 in all SQLite versions
func ensureFTS5(db *sql.DB) error {
	// Check if FTS5 table already exists
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='messages_fts'`).Scan(&name)
	if err == nil {
		fts5Available = true
		return nil // already exists
	}

	// Try to create FTS5 table - may fail if FTS5 not compiled in.
	// NOT content-synced: FTS5 stores its own data to avoid column name
	// mismatch with the messages table (message_id vs id).
	_, err = db.Exec(`
		CREATE VIRTUAL TABLE messages_fts USING fts5(
			message_id UNINDEXED,
			role,
			text,
			keywords
		)
	`)
	if err != nil {
		// FTS5 not available - that's okay, we'll use LIKE fallback
		log.Printf("FTS5 not available (optional): %v", err)
		return nil
	}

	fts5Available = true

	// Populate from existing messages
	_, _ = db.Exec(`
		INSERT INTO messages_fts(message_id, role, text, keywords)
		SELECT id, role, text, COALESCE(keywords, '') FROM messages
	`)

	return nil
}

func ValidateEmbedDimension(embedder ollama.Embedder) error {
	ctx := context.Background()
	embedding, err := embedder.Embed(ctx, "dimension check")
	if err != nil {
		return fmt.Errorf("embed test failed: %w", err)
	}
	if len(embedding) != EmbedDimension {
		return fmt.Errorf("embedding model produces %d dimensions, config expects %d — set EMBED_DIM=%d in .env", len(embedding), EmbedDimension, len(embedding))
	}
	return nil
}

func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Encryption key MUST be the first statement after open (SQLCipher requirement).
	// If KISEKI_DB_KEY is not set, this is a no-op (plaintext mode).
	if err := applyEncryptionKey(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, err
	}

	// Base schema (idempotent — CREATE IF NOT EXISTS)
	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		_ = db.Close()
		return nil, err
	}

	// Legacy column migrations (safe to re-run, silently ignored if already present)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN importance TEXT DEFAULT 'normal'`)
	_, _ = db.Exec(`ALTER TABLE messages ADD COLUMN importance TEXT DEFAULT 'normal'`)

	// Schema migration system (v2+)
	if err := RunMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Embed dimension safety — prevent silent corruption from mismatched dimensions
	if err := ValidateEmbedDimConfig(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	// Set up FTS5
	if err := ensureFTS5(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// InitDBForReEmbed opens and initializes the database WITHOUT embed dimension validation.
// Used by the re-embed command which needs to open DBs with mismatched dimensions.
func InitDBForReEmbed(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Encryption key MUST be the first statement after open (SQLCipher requirement).
	if err := applyEncryptionKey(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, err
	}

	if _, err := db.Exec(buildSchema(EmbedDimension)); err != nil {
		_ = db.Close()
		return nil, err
	}

	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN importance TEXT DEFAULT 'normal'`)
	_, _ = db.Exec(`ALTER TABLE messages ADD COLUMN importance TEXT DEFAULT 'normal'`)

	if err := RunMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	// Skip ValidateEmbedDimConfig — re-embed handles dim mismatch itself

	if err := ensureFTS5(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

// ============ Message Functions ============

// insertMessages upserts messages and their embeddings
func InsertMessages(db *sql.DB, embedder ollama.Embedder, messages []TextMessage) (int, error) {
	if len(messages) == 0 {
		return 0, nil
	}

	ctx := context.Background()
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	msgStmt, err := tx.Prepare(`INSERT OR IGNORE INTO messages (id, session_id, role, timestamp, text, keywords) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare msg: %w", err)
	}
	defer msgStmt.Close()

	var ftsStmt *sql.Stmt
	if fts5Available {
		ftsStmt, err = tx.Prepare(`INSERT OR IGNORE INTO messages_fts (message_id, role, text, keywords) VALUES (?, ?, ?, ?)`)
		if err != nil {
			// FTS5 might have become unavailable, continue without it
			ftsStmt = nil
		} else {
			defer ftsStmt.Close()
		}
	}

	inserted := 0
	var toEmbed []TextMessage

	for _, m := range messages {
		if m.MessageID == "" {
			continue
		}
		kw := strings.Join(keywords.Extract(m.Text, 10), " ")
		res, err := msgStmt.Exec(m.MessageID, m.SessionID, m.Role, m.Timestamp.UnixMilli(), m.Text, kw)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
			toEmbed = append(toEmbed, m)
			// Also insert into FTS if available
			if ftsStmt != nil {
				_, _ = ftsStmt.Exec(m.MessageID, m.Role, m.Text, kw)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	// Embed new messages (outside transaction for performance)
	for _, m := range toEmbed {
		if len(m.Text) < 10 {
			continue // skip very short messages
		}
		embedding, err := embedder.Embed(ctx, m.Text)
		if err != nil {
			continue
		}
		serialized, err := sqlite_vec.SerializeFloat32(embedding)
		if err != nil {
			continue
		}
		_, _ = db.Exec(`INSERT OR IGNORE INTO vec_messages (message_id, embedding) VALUES (?, ?)`, m.MessageID, serialized)
	}

	return inserted, nil
}

// ContextMessage for returning message context
type ContextMessage struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Text      string `json:"text"`
}

// GetMessageContext returns messages around a given message ID within rangeMinutes
func GetMessageContext(db *sql.DB, messageID string, rangeMinutes int) ([]ContextMessage, error) {
	var sessionID string
	var ts int64
	err := db.QueryRow(`SELECT session_id, timestamp FROM messages WHERE id = ?`, messageID).Scan(&sessionID, &ts)
	if err != nil {
		return nil, fmt.Errorf("message not found: %s", messageID)
	}

	rows, err := db.Query(`
		SELECT id, session_id, role, timestamp, text FROM messages
		WHERE session_id = ? AND timestamp BETWEEN ? AND ?
		ORDER BY timestamp ASC`,
		sessionID,
		ts-int64(rangeMinutes)*60*1000,
		ts+int64(rangeMinutes)*60*1000,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []ContextMessage
	for rows.Next() {
		var m ContextMessage
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Timestamp, &m.Text); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// ============ Search Functions ============

// MessageSearchResult for returning message search results
type MessageSearchResult struct {
	MessageID  string  `json:"message_id"`
	SessionID  string  `json:"session_id"`
	Role       string  `json:"role"`
	Timestamp  int64   `json:"timestamp"`
	Text       string  `json:"text"`
	Importance string  `json:"importance"`
	Distance   float64 `json:"distance"`
}

// SearchMessages performs semantic search on messages
func SearchMessages(db *sql.DB, embedder ollama.Embedder, query string, limit int) ([]MessageSearchResult, error) {
	ctx := context.Background()
	embedding, err := embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	serialized, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serialize: %w", err)
	}

	rows, err := db.Query(`
		SELECT vm.message_id, m.session_id, m.role, m.timestamp, m.text, m.importance, vm.distance
		FROM vec_messages vm
		JOIN messages m ON m.id = vm.message_id
		WHERE vm.embedding MATCH ? AND k = ?
		ORDER BY
			CASE m.importance
				WHEN 'solution' THEN 1
				WHEN 'key' THEN 2
				ELSE 3
			END,
			vm.distance ASC`,
		serialized, limit)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var results []MessageSearchResult
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.MessageID, &r.SessionID, &r.Role, &r.Timestamp, &r.Text, &r.Importance, &r.Distance); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// searchMessagesFTS performs exact phrase search using FTS5 or LIKE fallback
func SearchMessagesFTS(db *sql.DB, query string, limit int) ([]MessageSearchResult, error) {
	var rows *sql.Rows
	var err error

	if fts5Available {
		// Use FTS5 for fast exact phrase matching
		rows, err = db.Query(`
			SELECT f.message_id, m.session_id, m.role, m.timestamp, m.text, m.importance
			FROM messages_fts f
			JOIN messages m ON m.id = f.message_id
			WHERE messages_fts MATCH ?
			ORDER BY
				CASE m.importance
					WHEN 'solution' THEN 1
					WHEN 'key' THEN 2
					ELSE 3
				END,
				m.timestamp DESC
			LIMIT ?`,
			query, limit)
	} else {
		// Fallback to LIKE for exact substring matching
		rows, err = db.Query(`
			SELECT id, session_id, role, timestamp, text, importance
			FROM messages
			WHERE text LIKE ?
			ORDER BY
				CASE importance
					WHEN 'solution' THEN 1
					WHEN 'key' THEN 2
					ELSE 3
				END,
				timestamp DESC
			LIMIT ?`,
			"%"+query+"%", limit)
	}

	if err != nil {
		return nil, fmt.Errorf("text search: %w", err)
	}
	defer rows.Close()

	var results []MessageSearchResult
	for rows.Next() {
		var r MessageSearchResult
		if err := rows.Scan(&r.MessageID, &r.SessionID, &r.Role, &r.Timestamp, &r.Text, &r.Importance); err != nil {
			continue
		}
		r.Distance = 0 // exact match
		results = append(results, r)
	}
	return results, nil
}

// searchMessagesWithContext performs semantic search and returns context window
func SearchMessagesWithContext(db *sql.DB, embedder ollama.Embedder, query string, limit, contextMinutes int) ([][]ContextMessage, error) {
	results, err := SearchMessages(db, embedder, query, limit)
	if err != nil {
		return nil, err
	}

	var contexts [][]ContextMessage
	seen := make(map[string]bool) // avoid duplicate context windows

	for _, r := range results {
		// Skip if we already have context from this session+time range
		key := fmt.Sprintf("%s:%d", r.SessionID, r.Timestamp/(60000*int64(contextMinutes)))
		if seen[key] {
			continue
		}
		seen[key] = true

		ctx, err := GetMessageContext(db, r.MessageID, contextMinutes)
		if err != nil || len(ctx) == 0 {
			continue
		}
		contexts = append(contexts, ctx)
	}
	return contexts, nil
}

// ============ Thought Functions ============

// InsertThoughts batch-inserts thought blocks into the thoughts table.
// No embedding, no FTS, no vector — just raw storage.
// Returns the number of rows inserted.
func InsertThoughts(db *sql.DB, thoughts []ThoughtBlock) (int, error) {
	if len(thoughts) == 0 {
		return 0, nil
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO thoughts (message_id, session_id, text, timestamp, source, created_at) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare thought: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	now := time.Now().UTC().Format(time.RFC3339)

	for _, t := range thoughts {
		if t.MessageID == "" || t.Text == "" {
			continue
		}
		res, err := stmt.Exec(t.MessageID, t.SessionID, t.Text, t.Timestamp.UnixMilli(), t.Source, now)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return inserted, nil
}

// ============ Utility Functions ============

// countMessages returns total message count
func countMessages(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count)
	return count, err
}

// countEmbeddedMessages returns count of messages with embeddings
func countEmbeddedMessages(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM vec_messages`).Scan(&count)
	return count, err
}

// CountThoughts returns total thought block count.
// Returns 0 if the thoughts table doesn't exist yet (pre-V6 schema).
func CountThoughts(db *sql.DB) int {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM thoughts`).Scan(&count)
	if err != nil {
		return 0
	}
	return count
}

// sessionMessages groups messages by session
type sessionMessages struct {
	sessionID string
	messages  []TextMessage
}

// readAllSessions reads all messages grouped by session
func readAllSessions(db *sql.DB) ([]sessionMessages, error) {
	rows, err := db.Query(`SELECT id, session_id, role, timestamp, text FROM messages ORDER BY session_id, timestamp ASC`)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	sessMap := make(map[string][]TextMessage)
	var order []string

	for rows.Next() {
		var cm ContextMessage
		if err := rows.Scan(&cm.ID, &cm.SessionID, &cm.Role, &cm.Timestamp, &cm.Text); err != nil {
			continue
		}
		if _, seen := sessMap[cm.SessionID]; !seen {
			order = append(order, cm.SessionID)
		}
		sessMap[cm.SessionID] = append(sessMap[cm.SessionID], TextMessage{
			Role:      cm.Role,
			Text:      cm.Text,
			Timestamp: time.UnixMilli(cm.Timestamp),
			IsUser:    !isAssistantRole(cm.Role),
			MessageID: cm.ID,
			SessionID: cm.SessionID,
		})
	}

	sessions := make([]sessionMessages, 0, len(order))
	for _, sid := range order {
		sessions = append(sessions, sessionMessages{
			sessionID: sid,
			messages:  sessMap[sid],
		})
	}
	return sessions, nil
}

// isAssistantRole returns true if the role represents an AI assistant.
// Checks against the configured ASSISTANT_ALIAS and common defaults.
func isAssistantRole(role string) bool {
	lower := strings.ToLower(role)
	if lower == "assistant" {
		return true
	}
	if alias := os.Getenv("ASSISTANT_ALIAS"); alias != "" && strings.EqualFold(role, alias) {
		return true
	}
	return false
}
