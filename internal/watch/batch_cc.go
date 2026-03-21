package watch

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	dbpkg "github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/ui"
)

// RunBatchCC exports Claude Code sessions to markdown files and optionally
// inserts messages into the Kiseki database with embeddings.
func RunBatchCC(args []string, kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias string) {
	fs := flag.NewFlagSet("batch-cc", flag.ExitOnError)
	outDir := fs.String("out", ".", "output directory for markdown files")
	project := fs.String("project", "", "process specific project (default: all projects)")
	sessionID := fs.String("session", "", "process specific session ID (default: all sessions in project)")
	doNormalize := fs.Bool("normalize", false, "normalize text (fix typos) before export")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	basePath := claudeCodeBasePath()

	projects, err := discoverCCProjects(basePath)
	if err != nil {
		log.Fatalf("discover projects: %v", err)
	}
	if len(projects) == 0 {
		log.Fatal("no Claude Code projects found")
	}

	// Filter to specific project if requested
	if *project != "" {
		found := false
		for _, p := range projects {
			if p == *project {
				found = true
				break
			}
		}
		if !found {
			log.Fatalf("project not found: %s", *project)
		}
		projects = []string{*project}
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

	// Collect all sessions across selected projects
	type projectSession struct {
		projectDir string
		session    ccSessionEntry
	}
	var allSessions []projectSession

	for _, projectDir := range projects {
		sessions, err := discoverCCSessions(basePath, projectDir)
		if err != nil {
			log.Printf("  ⚠ discover sessions for %s: %v", projectDir, err)
			continue
		}
		for _, s := range sessions {
			allSessions = append(allSessions, projectSession{projectDir: projectDir, session: s})
		}
	}

	if len(allSessions) == 0 {
		log.Fatal("no Claude Code sessions found")
	}

	// Filter to specific session if requested
	if *sessionID != "" {
		var filtered []projectSession
		for _, ps := range allSessions {
			if ps.session.SessionID == *sessionID {
				filtered = append(filtered, ps)
				break
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("session not found: %s", *sessionID)
		}
		allSessions = filtered
	}

	fmt.Println()
	fmt.Println(ui.RenderHeader())
	fmt.Println()
	fmt.Printf("  Mode: Batch export (Claude Code JSONL → markdown)\n")
	fmt.Printf("  Projects: %d project(s)\n", len(projects))
	fmt.Printf("  Sessions: %d session(s)\n", len(allSessions))
	fmt.Printf("  Output: %s\n\n", *outDir)

	for _, ps := range allSessions {
		processCCBatchSession(basePath, ps.projectDir, ps.session, *outDir, db, ollamaClient, userAlias, assistantAlias, *doNormalize)
	}

	fmt.Println()
	fmt.Println(ui.InfoStyle.Render("  Done. All sessions exported."))
}

func processCCBatchSession(basePath, projectDir string, session ccSessionEntry, outDir string, db *sql.DB, ollamaClient *ollama.OllamaClient, userAlias, assistantAlias string, doNormalize bool) {
	modTime := session.Modified
	if modTime == "" {
		modTime = session.Created
	}

	fmt.Println(ui.RenderSessionItem(0, session.Summary, session.SessionID, modTime))

	// Determine JSONL path
	var jsonlPath string
	if session.FullPath != "" {
		jsonlPath = session.FullPath
	} else {
		var projectPath string
		if projectDir == "transcripts" {
			projectPath = filepath.Join(basePath, "transcripts")
		} else {
			projectPath = filepath.Join(basePath, "projects", projectDir)
		}
		jsonlPath = filepath.Join(projectPath, session.SessionID+".jsonl")
	}

	messages, ccThoughts, err := readCCJSONL(jsonlPath, session.SessionID, userAlias, assistantAlias)
	if err != nil {
		fmt.Printf("    ⚠ Read error: %v\n", err)
		return
	}

	fmt.Printf("    Found %d text messages\n", len(messages))

	if len(messages) == 0 {
		fmt.Println("    ⚠ No text content, skipping")
		return
	}

	if doNormalize {
		for i := range messages {
			messages[i].Text = ingest.NormalizeText(messages[i].Text)
		}
	}

	if db != nil && ollamaClient != nil {
		inserted, err := dbpkg.InsertMessages(db, ollamaClient, messages)
		if err != nil {
			fmt.Printf("    ⚠ Messages table: %v\n", err)
		} else {
			fmt.Printf("    ✓ Messages table: %d new rows\n", inserted)
		}

		// Insert thoughts if capture is enabled and we have any
		if len(ccThoughts) > 0 && captureThoughts() {
			if tInserted, tErr := dbpkg.InsertThoughts(db, ccThoughts); tErr != nil {
				fmt.Printf("    ⚠ Thoughts table: %v\n", tErr)
			} else if tInserted > 0 {
				fmt.Printf("    ✓ Thoughts table: %d new rows\n", tInserted)
			}
		}
	}

	title := session.Summary
	if title == "" {
		title = "Claude Code Session"
	}
	md := buildWatchMarkdown(messages, title)

	var sessionStart time.Time
	if len(messages) > 0 {
		sessionStart = messages[0].Timestamp
	} else {
		sessionStart = time.Now()
	}

	slug := sanitizeSlug(session.Summary)
	if slug == "" {
		slug = sanitizeSlug(session.FirstPrompt)
	}
	if slug == "" {
		slug = "untitled"
	}

	filename := fmt.Sprintf("session_%s_%s.md",
		sessionStart.Format("2006-01-02_15-04"),
		slug,
	)

	outPath := filepath.Join(outDir, filename)
	if err := os.WriteFile(outPath, []byte(md), 0644); err != nil {
		fmt.Printf("    ✗ Write error: %v\n", err)
		return
	}

	fmt.Printf("    ✓ Wrote %s (%d bytes, %d messages)\n\n", filename, len(md), len(messages))
}
