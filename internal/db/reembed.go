package db

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// RunReEmbedCLI handles the "kiseki re-embed" CLI command.
// It re-embeds all chunks and messages when the embedding model/dimension changes.
func RunReEmbedCLI(args []string, kisekiDB, ollamaHost, embedModel string) {
	fs := flag.NewFlagSet("re-embed", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "show what would be re-embedded without doing it")
	workers := fs.Int("workers", 1, "number of parallel embedding workers")
	force := fs.Bool("force", false, "re-embed even if dimensions match")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	// Use InitDBForReEmbed to bypass embed dimension validation — re-embed handles mismatch itself.
	db, err := InitDBForReEmbed(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	storedDim := StoredEmbedDim(db)
	targetDim := EmbedDimension

	// Count chunks and messages for reporting
	var chunkCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunkCount); err != nil {
		fmt.Fprintf(os.Stderr, "Error: count chunks: %v\n", err)
		os.Exit(1)
	}
	var msgCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE length(text) >= 10`).Scan(&msgCount); err != nil {
		fmt.Fprintf(os.Stderr, "Error: count messages: %v\n", err)
		os.Exit(1)
	}

	if *dryRun {
		fmt.Printf("Dry run — no changes will be made.\n\n")
		fmt.Printf("  Stored dimension : %d\n", storedDim)
		fmt.Printf("  Target dimension : %d\n", targetDim)
		fmt.Printf("  Chunks to embed  : %d\n", chunkCount)
		fmt.Printf("  Messages to embed: %d\n", msgCount)
		if storedDim == targetDim && !*force {
			fmt.Printf("\nDimensions already match (%d). Use --force to re-embed anyway.\n", targetDim)
		} else {
			reason := "dimension mismatch"
			if *force {
				reason = "--force flag set"
			}
			fmt.Printf("\nWould re-embed %d chunks and %d messages (%s).\n", chunkCount, msgCount, reason)
		}
		return
	}

	if storedDim == targetDim && !*force {
		fmt.Printf("Dimensions already match (%d). Use --force to re-embed anyway.\n", targetDim)
		return
	}

	// Drop and recreate vec tables with new dimension
	fmt.Printf("Dropping vec tables...\n")
	dropSQL := `
		DROP TABLE IF EXISTS vec_chunks;
		DROP TABLE IF EXISTS vec_messages;
	`
	if _, err := db.Exec(dropSQL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: drop vec tables: %v\n", err)
		os.Exit(1)
	}

	createSQL := fmt.Sprintf(`
		CREATE VIRTUAL TABLE vec_chunks USING vec0(chunk_id INTEGER PRIMARY KEY, embedding float[%d] distance_metric=cosine);
		CREATE VIRTUAL TABLE vec_messages USING vec0(message_id TEXT PRIMARY KEY, embedding float[%d] distance_metric=cosine);
	`, targetDim, targetDim)
	if _, err := db.Exec(createSQL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: recreate vec tables: %v\n", err)
		os.Exit(1)
	}

	// Update stored dim config
	if err := SetConfig(db, "embed_dim", strconv.Itoa(targetDim)); err != nil {
		fmt.Fprintf(os.Stderr, "Error: update embed_dim config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Vec tables recreated with dimension %d.\n", targetDim)

	// Set up Ctrl+C handler
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	embedder := ollama.NewOllamaClient(ollamaHost, embedModel)

	// ── Embed chunks ──────────────────────────────────────────────────────────

	type chunkRow struct {
		id   int64
		text string
	}

	rows, err := db.QueryContext(ctx, `SELECT id, text FROM chunks ORDER BY id ASC`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query chunks: %v\n", err)
		os.Exit(1)
	}

	var chunks []chunkRow
	for rows.Next() {
		var c chunkRow
		if err := rows.Scan(&c.id, &c.text); err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	rows.Close()

	chunksDone := 0
	var chunksMu sync.Mutex
	interrupted := false

	type chunkWork struct {
		id   int64
		text string
	}
	chunkQueue := make(chan chunkWork, len(chunks))
	for _, c := range chunks {
		chunkQueue <- chunkWork{id: c.id, text: c.text}
	}
	close(chunkQueue)

	var wg sync.WaitGroup
	numWorkers := *workers
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Watch for interrupt in background
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	fmt.Printf("Embedding %d chunks with %d worker(s)...\n", len(chunks), numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for work := range chunkQueue {
				if ctx.Err() != nil {
					return
				}
				embedding, err := embedder.Embed(ctx, work.text)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					continue
				}
				serialized, err := sqlite_vec.SerializeFloat32(embedding)
				if err != nil {
					continue
				}
				_, _ = db.ExecContext(ctx,
					`INSERT OR REPLACE INTO vec_chunks (chunk_id, embedding) VALUES (?, ?)`,
					work.id, serialized,
				)
				chunksMu.Lock()
				chunksDone++
				done := chunksDone
				chunksMu.Unlock()
				if done%100 == 0 {
					fmt.Printf("Embedded %d/%d chunks...\n", done, len(chunks))
				}
			}
		}()
	}

	wg.Wait()

	if ctx.Err() != nil {
		chunksMu.Lock()
		done := chunksDone
		chunksMu.Unlock()
		fmt.Printf("\nInterrupted. Embedded %d/%d chunks before stopping.\n", done, len(chunks))
		interrupted = true
	} else {
		fmt.Printf("Embedded %d/%d chunks.\n", chunksDone, len(chunks))
	}

	// ── Embed messages ────────────────────────────────────────────────────────

	if interrupted {
		fmt.Printf("Skipping messages due to interruption.\n")
		fmt.Printf("Re-embedded %d chunks and 0 messages. New dimension: %d\n", chunksDone, targetDim)
		return
	}

	type msgRow struct {
		id   string
		text string
	}

	msgRows, err := db.QueryContext(ctx, `SELECT id, text FROM messages WHERE length(text) >= 10 ORDER BY rowid ASC`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query messages: %v\n", err)
		os.Exit(1)
	}

	var msgs []msgRow
	for msgRows.Next() {
		var m msgRow
		if err := msgRows.Scan(&m.id, &m.text); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}
	msgRows.Close()

	msgsDone := 0
	var msgsMu sync.Mutex

	type msgWork struct {
		id   string
		text string
	}
	msgQueue := make(chan msgWork, len(msgs))
	for _, m := range msgs {
		msgQueue <- msgWork{id: m.id, text: m.text}
	}
	close(msgQueue)

	fmt.Printf("Embedding %d messages with %d worker(s)...\n", len(msgs), numWorkers)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for work := range msgQueue {
				if ctx.Err() != nil {
					return
				}
				embedding, err := embedder.Embed(ctx, work.text)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					continue
				}
				serialized, err := sqlite_vec.SerializeFloat32(embedding)
				if err != nil {
					continue
				}
				_, _ = db.ExecContext(ctx,
					`INSERT OR REPLACE INTO vec_messages (message_id, embedding) VALUES (?, ?)`,
					work.id, serialized,
				)
				msgsMu.Lock()
				msgsDone++
				done := msgsDone
				msgsMu.Unlock()
				if done%100 == 0 {
					fmt.Printf("Embedded %d/%d messages...\n", done, len(msgs))
				}
			}
		}()
	}

	wg.Wait()

	if ctx.Err() != nil {
		msgsMu.Lock()
		done := msgsDone
		msgsMu.Unlock()
		fmt.Printf("\nInterrupted. Embedded %d/%d messages before stopping.\n", done, len(msgs))
		fmt.Printf("Re-embedded %d chunks and %d messages. New dimension: %d\n", chunksDone, done, targetDim)
		return
	}

	fmt.Printf("Embedded %d/%d messages.\n", msgsDone, len(msgs))
	fmt.Printf("Re-embedded %d chunks and %d messages. New dimension: %d\n", chunksDone, msgsDone, targetDim)
}
