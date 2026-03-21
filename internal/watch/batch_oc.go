package watch

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	dbpkg "github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/ui"
)

// RunBatchOC exports OpenCode sessions to markdown files and optionally
// inserts messages into the Kiseki database with embeddings.
func RunBatchOC(args []string, kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias string) {
	fs := flag.NewFlagSet("batch-oc", flag.ExitOnError)
	outDir := fs.String("out", ".", "output directory for markdown files")
	sessionFilter := fs.String("session", "", "process specific session ID (default: all parent sessions)")
	doNormalize := fs.Bool("normalize", false, "normalize text (fix typos) before export")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	ocDBPath := openCodeDBPath()
	ocDB, err := sql.Open("sqlite3", ocDBPath+"?mode=ro")
	if err != nil {
		log.Fatalf("open opencode db: %v", err)
	}
	defer ocDB.Close()

	sessions, err := discoverSessions(ocDB)
	if err != nil {
		log.Fatalf("discover sessions: %v", err)
	}
	if len(sessions) == 0 {
		log.Fatal("no OpenCode sessions found")
	}

	if *sessionFilter != "" {
		var filtered []ocSession
		for _, s := range sessions {
			if s.ID == *sessionFilter {
				filtered = append(filtered, s)
				break
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("session not found: %s", *sessionFilter)
		}
		sessions = filtered
	}

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	var db *sql.DB
	var ollamaClient *ollama.OllamaClient
	if kisekiDB != "" {
		fmt.Println()
		if err := watchPreflight(ollamaHost, embedModel); err != nil {
			log.Fatalf("preflight: %v", err)
		}
		db, err = dbpkg.InitDB(kisekiDB)
		if err != nil {
			log.Fatalf("init db: %v", err)
		}
		defer db.Close()
		ollamaClient = ollama.NewOllamaClient("http://"+ollamaHost, embedModel)
	}

	fmt.Println()
	fmt.Println(ui.RenderHeader())
	fmt.Println()
	fmt.Printf("  Mode: Batch export (OpenCode → markdown)\n")
	fmt.Printf("  Sessions: %d parent session(s)\n", len(sessions))
	fmt.Printf("  Output: %s\n\n", *outDir)

	for _, session := range sessions {
		processOCBatchSession(ocDB, session, *outDir, db, ollamaClient, userAlias, assistantAlias, *doNormalize)
	}

	fmt.Println()
	fmt.Println(ui.InfoStyle.Render("  Done. All sessions exported."))
}

func processOCBatchSession(ocDB *sql.DB, session ocSession, outDir string, db *sql.DB, ollamaClient *ollama.OllamaClient, userAlias, assistantAlias string, doNormalize bool) {
	fmt.Println(ui.RenderSessionItem(0, session.Title, session.ID, time.UnixMilli(session.Updated).Format("Jan 02, 2006 15:04")))

	msgIDs, err := getAllMessageIDs(ocDB, session.ID)
	if err != nil {
		fmt.Printf("    ⚠ No messages: %v\n", err)
		return
	}

	fmt.Printf("    Found %d messages\n", len(msgIDs))

	var messages []dbpkg.TextMessage
	var allThoughts []dbpkg.ThoughtBlock
	skipped := 0
	errors := 0
	for _, msgID := range msgIDs {
		tm, msgThoughts, err := readTextFromDB(ocDB, session.ID, msgID, userAlias, assistantAlias)
		if err != nil {
			errors++
			continue
		}
		if len(msgThoughts) > 0 {
			allThoughts = append(allThoughts, msgThoughts...)
		}
		if tm == nil {
			skipped++
			continue
		}
		messages = append(messages, *tm)
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	if doNormalize {
		for i := range messages {
			messages[i].Text = ingest.NormalizeText(messages[i].Text)
		}
	}

	fmt.Printf("    Text messages: %d  |  Skipped: %d  |  Errors: %d\n", len(messages), skipped, errors)

	if len(messages) == 0 {
		fmt.Println("    ⚠ No text content, skipping")
		return
	}

	if db != nil && ollamaClient != nil {
		inserted, err := dbpkg.InsertMessages(db, ollamaClient, messages)
		if err != nil {
			fmt.Printf("    ⚠ Messages table: %v\n", err)
		} else {
			fmt.Printf("    ✓ Messages table: %d new rows\n", inserted)
		}

		// Insert thoughts if capture is enabled and we have any
		if len(allThoughts) > 0 && captureThoughts() {
			if inserted, err := dbpkg.InsertThoughts(db, allThoughts); err != nil {
				fmt.Printf("    ⚠ Thoughts table: %v\n", err)
			} else if inserted > 0 {
				fmt.Printf("    ✓ Thoughts table: %d new rows\n", inserted)
			}
		}
	}

	md := buildWatchMarkdown(messages, session.Title)

	created := time.UnixMilli(session.Updated)
	slug := sanitizeSlug(session.Slug)
	if slug == "" {
		slug = sanitizeSlug(session.Title)
	}
	if slug == "" {
		slug = "untitled"
	}

	filename := fmt.Sprintf("session_%s_%s.md",
		created.Format("2006-01-02_15-04"),
		slug,
	)

	outPath := filepath.Join(outDir, filename)
	if err := os.WriteFile(outPath, []byte(md), 0644); err != nil {
		fmt.Printf("    ✗ Write error: %v\n", err)
		return
	}

	fmt.Printf("    ✓ Wrote %s (%d bytes, %d messages)\n\n", filename, len(md), len(messages))
}

// getAllMessageIDs returns all message IDs for a session ordered by creation time.
func getAllMessageIDs(ocDB *sql.DB, sessionID string) ([]string, error) {
	rows, err := ocDB.Query(`
		SELECT id FROM message 
		WHERE session_id = ? 
		ORDER BY time_created
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// sanitizeSlug converts a title to a filesystem-safe slug.
func sanitizeSlug(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r == ' ' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	return s
}
