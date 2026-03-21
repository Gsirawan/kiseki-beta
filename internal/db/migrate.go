package db

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Gsirawan/kiseki-beta/internal/keywords"
	_ "github.com/mattn/go-sqlite3"
)

// MigrateConfig holds migration parameters.
type MigrateConfig struct {
	SourcePath string
	DryRun     bool
}

// MigrateResult holds migration statistics.
type MigrateResult struct {
	ChunksCopied        int
	MessagesCopied      int
	KeywordsExtracted   int
	EntitiesCopied      int
	AliasesCopied       int
	RelationshipsCopied int
	FTSRebuilt          bool
	Warnings            []string

	// Cleanup stats
	ChunksSkippedTiny int
	ChunksSkippedDupe int
	MsgsSkippedTiny   int
	MsgsSkippedDupe   int
	ChunksOriginal    int
	MsgsOriginal      int
}

// RunMigrateCLI handles the "kiseki migrate" CLI command.
func RunMigrateCLI(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	sourcePath := fs.String("source", "", "path to source database (required)")
	dryRun := fs.Bool("dry-run", false, "preview migration without writing")
	confirm := fs.Bool("confirm", false, "execute migration (required for writes)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	if *sourcePath == "" {
		fmt.Fprintf(os.Stderr, "Error: --source is required\n\n")
		fs.Usage()
		os.Exit(1)
	}

	if !*dryRun && !*confirm {
		fmt.Fprintf(os.Stderr, "Error: must specify --dry-run or --confirm\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  kiseki migrate --source /path/to/nectar.db --dry-run\n")
		fmt.Fprintf(os.Stderr, "  kiseki migrate --source /path/to/nectar.db --confirm\n")
		os.Exit(1)
	}

	// Verify source exists
	if _, err := os.Stat(*sourcePath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: source database not found: %s\n", *sourcePath)
		os.Exit(1)
	}

	// Open source DB read-only
	sourceDB, err := sql.Open("sqlite3", *sourcePath+"?mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open source database: %v\n", err)
		os.Exit(1)
	}
	defer sourceDB.Close()

	// Verify source is readable
	if err := sourceDB.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot read source database: %v\n", err)
		os.Exit(1)
	}

	// Open target DB via InitDBForReEmbed (creates full Kiseki schema, skips embed dim validation)
	targetDB, err := InitDBForReEmbed(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open target database: %v\n", err)
		os.Exit(1)
	}
	defer targetDB.Close()

	result, err := Migrate(sourceDB, targetDB, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: migration failed: %v\n", err)
		os.Exit(1)
	}

	// Print warnings
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}

	if *dryRun {
		fmt.Printf("Migration preview (dry-run):\n")
		fmt.Printf("  Source: %s\n", *sourcePath)
		fmt.Printf("  Target: %s\n\n", kisekiDB)
		fmt.Printf("  Cleanup (would skip):\n")
		fmt.Printf("    Tiny chunks (<20 chars):    %s\n", formatInt(result.ChunksSkippedTiny))
		fmt.Printf("    Duplicate chunks:           %s\n", formatInt(result.ChunksSkippedDupe))
		fmt.Printf("    Empty messages (<5 chars):  %s\n", formatInt(result.MsgsSkippedTiny))
		fmt.Printf("    Duplicate messages:         %s\n", formatInt(result.MsgsSkippedDupe))
		fmt.Printf("\n")
		fmt.Printf("  Would migrate (after cleanup):\n")
		fmt.Printf("    Chunks:        %s (from %s original)\n", formatInt(result.ChunksCopied), formatInt(result.ChunksOriginal))
		fmt.Printf("    Messages:      %s (from %s original)\n", formatInt(result.MessagesCopied), formatInt(result.MsgsOriginal))
		fmt.Printf("    Entities:      %s\n", formatInt(result.EntitiesCopied))
		fmt.Printf("    Aliases:       %s\n", formatInt(result.AliasesCopied))
		fmt.Printf("    Relationships: %s\n", formatInt(result.RelationshipsCopied))
		fmt.Printf("\n")
		fmt.Printf("  Skipped (M3b): vec_chunks, vec_messages\n")
		fmt.Printf("  Skipped (dead): topics, vec_topics\n")
		fmt.Printf("\n")
		fmt.Printf("  Run with --confirm to execute.\n")
	} else {
		fmt.Printf("Migrating from %s...\n", *sourcePath)
		fmt.Printf("  Chunks copied:        %s\n", formatInt(result.ChunksCopied))
		fmt.Printf("  Messages copied:      %s (keywords extracted)\n", formatInt(result.MessagesCopied))
		fmt.Printf("  Entities copied:      %s\n", formatInt(result.EntitiesCopied))
		fmt.Printf("  Aliases copied:       %s\n", formatInt(result.AliasesCopied))
		fmt.Printf("  Relationships copied: %s\n", formatInt(result.RelationshipsCopied))
		ftsStr := "no"
		if result.FTSRebuilt {
			ftsStr = "yes"
		}
		fmt.Printf("  FTS5 rebuilt:         %s\n", ftsStr)
		fmt.Printf("\n")
		fmt.Printf("  Cleanup: skipped %s tiny chunks, %s duplicate chunks, %s empty messages, %s duplicate messages\n",
			formatInt(result.ChunksSkippedTiny), formatInt(result.ChunksSkippedDupe),
			formatInt(result.MsgsSkippedTiny), formatInt(result.MsgsSkippedDupe))
		fmt.Printf("  Migrated: %s chunks, %s messages (from original %s chunks, %s messages)\n",
			formatInt(result.ChunksCopied), formatInt(result.MessagesCopied),
			formatInt(result.ChunksOriginal), formatInt(result.MsgsOriginal))
		fmt.Printf("\n")
		fmt.Printf("  Done. Target: %s\n", kisekiDB)
		fmt.Printf("  Next step: kiseki re-embed (M3b)\n")
	}
}

// Migrate copies data from source DB to target DB.
// If dryRun is true, it counts source data and returns without writing.
func Migrate(source, target *sql.DB, dryRun bool) (*MigrateResult, error) {
	result := &MigrateResult{}

	// ── Step 1: Count source data ──────────────────────────────────────────────

	var chunkCount int
	if err := source.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunkCount); err != nil {
		// chunks table might not exist in some source DBs
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not count chunks: %v", err))
	}
	result.ChunksOriginal = chunkCount

	var msgCount int
	if err := source.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&msgCount); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("could not count messages: %v", err))
	}
	result.MsgsOriginal = msgCount

	// Count cleanup stats for chunks
	var chunksTiny int
	_ = source.QueryRow(`SELECT COUNT(*) FROM chunks WHERE LENGTH(text) < 20`).Scan(&chunksTiny)
	var chunksDupe int
	_ = source.QueryRow(`
		SELECT COUNT(*) FROM chunks
		WHERE LENGTH(text) >= 20
		AND rowid NOT IN (SELECT MIN(rowid) FROM chunks WHERE LENGTH(text) >= 20 GROUP BY text)
	`).Scan(&chunksDupe)
	result.ChunksSkippedTiny = chunksTiny
	result.ChunksSkippedDupe = chunksDupe

	// Count cleanup stats for messages
	var msgsTiny int
	_ = source.QueryRow(`SELECT COUNT(*) FROM messages WHERE LENGTH(text) < 5`).Scan(&msgsTiny)
	var msgsDupe int
	_ = source.QueryRow(`
		SELECT COUNT(*) FROM messages
		WHERE LENGTH(text) >= 5
		AND rowid NOT IN (SELECT MIN(rowid) FROM messages WHERE LENGTH(text) >= 5 GROUP BY text)
	`).Scan(&msgsDupe)
	result.MsgsSkippedTiny = msgsTiny
	result.MsgsSkippedDupe = msgsDupe

	// Clean counts after filtering
	chunkClean := chunkCount - chunksTiny - chunksDupe
	if chunkClean < 0 {
		chunkClean = 0
	}
	msgClean := msgCount - msgsTiny - msgsDupe
	if msgClean < 0 {
		msgClean = 0
	}

	// Check if entities table exists
	var entitiesTableExists int
	_ = source.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='entities'`).Scan(&entitiesTableExists)

	var entityCount, aliasCount, relCount int
	if entitiesTableExists > 0 {
		_ = source.QueryRow(`SELECT COUNT(*) FROM entities`).Scan(&entityCount)
		_ = source.QueryRow(`SELECT COUNT(*) FROM entity_aliases`).Scan(&aliasCount)
		_ = source.QueryRow(`SELECT COUNT(*) FROM relationships`).Scan(&relCount)
	}

	// Warn if nothing to migrate
	if chunkCount == 0 && msgCount == 0 {
		result.Warnings = append(result.Warnings, "source database has no chunks and no messages — nothing to migrate")
	}

	// For dry-run: populate result with counts and return
	if dryRun {
		result.ChunksCopied = chunkClean
		result.MessagesCopied = msgClean
		result.EntitiesCopied = entityCount
		result.AliasesCopied = aliasCount
		result.RelationshipsCopied = relCount
		return result, nil
	}

	// ── Step 2: Check if target already has data ───────────────────────────────

	var targetChunkCount int
	_ = target.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&targetChunkCount)
	if targetChunkCount > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("target already has %d chunks — INSERT OR IGNORE will skip duplicates", targetChunkCount))
	}

	// ── Step 3: Execute migration in a single transaction ─────────────────────

	tx, err := target.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// ── 3a: Copy chunks ────────────────────────────────────────────────────────

	chunkRows, err := source.Query(`
		SELECT text, source_file, section_title, header_level, parent_title,
		       section_sequence, chunk_sequence, chunk_total, valid_at, ingested_at
		FROM chunks
		WHERE LENGTH(text) >= 20
		AND rowid IN (SELECT MIN(rowid) FROM chunks WHERE LENGTH(text) >= 20 GROUP BY text)
	`)
	if err != nil {
		return nil, fmt.Errorf("query source chunks: %w", err)
	}
	defer chunkRows.Close()

	chunkStmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO chunks
			(text, source_file, section_title, importance, header_level, parent_title,
			 section_sequence, chunk_sequence, chunk_total, valid_at, ingested_at)
		VALUES (?, ?, ?, 'normal', ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare chunk insert: %w", err)
	}
	defer chunkStmt.Close()

	for chunkRows.Next() {
		var (
			text            string
			sourceFile      string
			sectionTitle    string
			headerLevel     int
			parentTitle     sql.NullString
			sectionSequence sql.NullInt64
			chunkSequence   sql.NullInt64
			chunkTotal      sql.NullInt64
			validAt         sql.NullString
			ingestedAt      string
		)
		if err := chunkRows.Scan(
			&text, &sourceFile, &sectionTitle, &headerLevel, &parentTitle,
			&sectionSequence, &chunkSequence, &chunkTotal, &validAt, &ingestedAt,
		); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("scan chunk row: %v", err))
			continue
		}
		res, err := chunkStmt.Exec(
			text, sourceFile, sectionTitle, headerLevel, parentTitle,
			sectionSequence, chunkSequence, chunkTotal, validAt, ingestedAt,
		)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("insert chunk: %v", err))
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.ChunksCopied++
		}
	}
	if err := chunkRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk rows: %w", err)
	}

	// ── 3b: Copy messages with keyword extraction ──────────────────────────────

	msgRows, err := source.Query(`
		SELECT id, session_id, role, timestamp, text
		FROM messages
		WHERE LENGTH(text) >= 5
		AND rowid IN (SELECT MIN(rowid) FROM messages WHERE LENGTH(text) >= 5 GROUP BY text)
	`)
	if err != nil {
		return nil, fmt.Errorf("query source messages: %w", err)
	}
	defer msgRows.Close()

	msgStmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO messages
			(id, session_id, role, timestamp, text, importance, keywords)
		VALUES (?, ?, ?, ?, ?, 'normal', ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("prepare message insert: %w", err)
	}
	defer msgStmt.Close()

	for msgRows.Next() {
		var (
			id        string
			sessionID string
			role      string
			timestamp int64
			text      string
		)
		if err := msgRows.Scan(&id, &sessionID, &role, &timestamp, &text); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("scan message row: %v", err))
			continue
		}
		kw := strings.Join(keywords.Extract(text, 10), " ")
		res, err := msgStmt.Exec(id, sessionID, role, timestamp, text, kw)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("insert message %s: %v", id, err))
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.MessagesCopied++
			if kw != "" {
				result.KeywordsExtracted++
			}
		}
	}
	if err := msgRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message rows: %w", err)
	}

	// ── 3c: Copy entities, aliases, relationships (if source has them) ─────────

	if entitiesTableExists > 0 {
		if warn := migrateEntities(source, tx, result); warn != "" {
			result.Warnings = append(result.Warnings, warn)
		}
	}


	// ── 3d: Rebuild FTS5 on target ─────────────────────────────────────────────

	var ftsExists int
	_ = target.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages_fts'`).Scan(&ftsExists)
	if ftsExists > 0 {
		_, err := tx.Exec(`INSERT OR IGNORE INTO messages_fts(message_id, role, text, keywords) SELECT id, role, text, keywords FROM messages`)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("FTS5 rebuild failed (non-fatal): %v", err))
		} else {
			result.FTSRebuilt = true
		}
	}

	// ── Step 4: Commit ─────────────────────────────────────────────────────────

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit migration transaction: %w", err)
	}

	return result, nil
}

// formatInt formats an integer with comma separators for readability.
func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	// Insert commas from right
	result := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// migrateEntities copies entities, aliases, and relationships from source to target.
// It returns a non-empty warning string if a fatal sub-step fails (entity migration is
// best-effort — alias/relationship failures are appended to result.Warnings directly).
func migrateEntities(source *sql.DB, tx *sql.Tx, result *MigrateResult) string {
	type entityRow struct {
		oldID       int64
		name        string
		entityType  string
		description sql.NullString
	}

	// Read all source entities
	entRows, err := source.Query(`SELECT id, name, type, description FROM entities`)
	if err != nil {
		return fmt.Sprintf("query source entities: %v — skipping entity migration", err)
	}
	var sourceEntities []entityRow
	for entRows.Next() {
		var e entityRow
		if err := entRows.Scan(&e.oldID, &e.name, &e.entityType, &e.description); err != nil {
			continue
		}
		sourceEntities = append(sourceEntities, e)
	}
	entRows.Close()

	// Insert entities
	entStmt, err := tx.Prepare(`INSERT OR IGNORE INTO entities (name, type, description) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Sprintf("prepare entity insert: %v — skipping entity migration", err)
	}
	defer entStmt.Close()

	for _, e := range sourceEntities {
		res, err := entStmt.Exec(e.name, e.entityType, e.description)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("insert entity %q: %v", e.name, err))
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.EntitiesCopied++
		}
	}

	// Build old→new entity ID map (read within same transaction)
	oldToNew := make(map[int64]int64)
	for _, e := range sourceEntities {
		var newID int64
		if err := tx.QueryRow(`SELECT id FROM entities WHERE name = ?`, e.name).Scan(&newID); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("lookup new entity ID for %q: %v", e.name, err))
			continue
		}
		oldToNew[e.oldID] = newID
	}

	// Copy aliases
	migrateAliases(source, tx, oldToNew, result)

	// Copy relationships
	migrateRelationships(source, tx, oldToNew, result)

	return ""
}

// migrateAliases copies entity_aliases from source to target using the old→new ID map.
func migrateAliases(source *sql.DB, tx *sql.Tx, oldToNew map[int64]int64, result *MigrateResult) {
	aliasRows, err := source.Query(`SELECT entity_id, alias, note FROM entity_aliases`)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("query source aliases: %v — skipping alias migration", err))
		return
	}
	defer aliasRows.Close()

	aliasStmt, err := tx.Prepare(`INSERT OR IGNORE INTO entity_aliases (entity_id, alias, note) VALUES (?, ?, ?)`)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("prepare alias insert: %v — skipping alias migration", err))
		return
	}
	defer aliasStmt.Close()

	for aliasRows.Next() {
		var (
			oldEntityID int64
			alias       string
			note        sql.NullString
		)
		if err := aliasRows.Scan(&oldEntityID, &alias, &note); err != nil {
			continue
		}
		newEntityID, ok := oldToNew[oldEntityID]
		if !ok {
			result.Warnings = append(result.Warnings, fmt.Sprintf("alias %q: no mapping for entity_id %d — skipping", alias, oldEntityID))
			continue
		}
		res, err := aliasStmt.Exec(newEntityID, alias, note)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("insert alias %q: %v", alias, err))
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.AliasesCopied++
		}
	}
}

// migrateRelationships copies relationships from source to target using the old→new ID map.
func migrateRelationships(source *sql.DB, tx *sql.Tx, oldToNew map[int64]int64, result *MigrateResult) {
	relRows, err := source.Query(`SELECT entity_a, entity_b, relation_type, description FROM relationships`)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("query source relationships: %v — skipping relationship migration", err))
		return
	}
	defer relRows.Close()

	relStmt, err := tx.Prepare(`INSERT OR IGNORE INTO relationships (entity_a, entity_b, relation_type, description) VALUES (?, ?, ?, ?)`)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("prepare relationship insert: %v — skipping relationship migration", err))
		return
	}
	defer relStmt.Close()

	for relRows.Next() {
		var (
			oldA        int64
			oldB        int64
			relType     string
			description sql.NullString
		)
		if err := relRows.Scan(&oldA, &oldB, &relType, &description); err != nil {
			continue
		}
		newA, okA := oldToNew[oldA]
		newB, okB := oldToNew[oldB]
		if !okA || !okB {
			result.Warnings = append(result.Warnings, fmt.Sprintf("relationship %d\u2192%d: missing entity mapping — skipping", oldA, oldB))
			continue
		}
		res, err := relStmt.Exec(newA, newB, relType, description)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("insert relationship %d\u2192%d: %v", oldA, oldB, err))
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.RelationshipsCopied++
		}
	}
}
