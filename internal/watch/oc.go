package watch

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	dbpkg "github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/ui"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

type ocSession struct {
	ID        string
	Slug      string
	Title     string
	ParentID  sql.NullString
	Updated   int64
	AgentName string
}

type ocPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var noisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\[search-mode\].*?---\s*\n`),
	regexp.MustCompile(`(?s)\[analyze-mode\].*?---\s*\n`),
	regexp.MustCompile(`(?s)\[SYSTEM DIRECTIVE[^\]]*\].*?(?:\[Status:[^\]]*\])`),
	regexp.MustCompile(`(?s)# Continuation Prompt.*`),
	regexp.MustCompile(`\(sisyphus\)\s*`),
	regexp.MustCompile(`\(prometheus\)\s*`),
	regexp.MustCompile(`\(oracle\)\s*`),
	regexp.MustCompile(`(?s)\[BACKGROUND TASK COMPLETED\].*?\n`),
	regexp.MustCompile(`(?s)\[Agent Usage Reminder\].*?(?:\n\n|\z)`),
	regexp.MustCompile(`(?s)\[Category\+Skill Reminder\].*?(?:\n\n|\z)`),
	regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`),
	regexp.MustCompile(`(?s)\[ALL BACKGROUND TASKS COMPLETE\].*?(?:\n\n|\z)`),
	regexp.MustCompile(`(?s)\[SYSTEM REMINDER[^\]]*\].*?(?:\n\n|\z)`),
	regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>`),
	regexp.MustCompile(`(?s)<command-name>.*?</command-name>`),
	regexp.MustCompile(`(?s)<command-message>.*?</command-message>`),
	regexp.MustCompile(`(?s)<command-args>.*?</command-args>`),
	regexp.MustCompile(`(?s)<local-command-stdout>.*?</local-command-stdout>`),
	regexp.MustCompile(`(?s)<task-notification>.*?</task-notification>`),
}

func openCodeDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

func discoverSessions(ocDB *sql.DB) ([]ocSession, error) {
	rows, err := ocDB.Query(`
		SELECT id, slug, title, parent_id, time_updated 
		FROM session 
		WHERE parent_id IS NULL 
		ORDER BY time_updated DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []ocSession
	for rows.Next() {
		var s ocSession
		if err := rows.Scan(&s.ID, &s.Slug, &s.Title, &s.ParentID, &s.Updated); err != nil {
			continue
		}
		sessions = append(sessions, s)
	}

	return sessions, nil
}

func pickSession(sessions []ocSession) (ocSession, error) {
	fmt.Println()
	fmt.Println(ui.RenderHeader())
	fmt.Println()

	limit := 10
	if len(sessions) < limit {
		limit = len(sessions)
	}

	for i, s := range sessions[:limit] {
		updated := time.UnixMilli(s.Updated).Format("Jan 02, 2006 15:04")
		slug := s.Slug
		if slug == "" {
			slug = "(no slug)"
		}
		fmt.Println(ui.RenderSessionItem(i+1, s.Title, slug, updated))
	}

	fmt.Println()
	fmt.Print(ui.PromptStyle.Render("  Select session [1]: "))
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return ocSession{}, fmt.Errorf("read input: %w", err)
	}

	input = strings.TrimSpace(input)
	if input == "" {
		input = "1"
	}

	var choice int
	if _, err := fmt.Sscanf(input, "%d", &choice); err != nil || choice < 1 || choice > limit {
		return ocSession{}, fmt.Errorf("invalid choice: %s", input)
	}

	return sessions[choice-1], nil
}

func stripNoise(text string) string {
	for _, p := range noisePatterns {
		text = p.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

func getExistingMessageIDs(ocDB *sql.DB, sessionID string) (map[string]bool, error) {
	rows, err := ocDB.Query(`SELECT id FROM message WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids[id] = true
	}
	return ids, nil
}

// captureThoughts returns true when KISEKI_CAPTURE_THOUGHTS env var is "true".
func captureThoughts() bool {
	return os.Getenv("KISEKI_CAPTURE_THOUGHTS") == "true"
}

func readTextFromDB(ocDB *sql.DB, sessionID, msgID, userAlias, assistantAlias string) (*dbpkg.TextMessage, []dbpkg.ThoughtBlock, error) {
	var data string
	var timeCreated int64
	err := ocDB.QueryRow(`
		SELECT data, time_created FROM message WHERE id = ? AND session_id = ?
	`, msgID, sessionID).Scan(&data, &timeCreated)
	if err != nil {
		return nil, nil, err
	}

	var msgData struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal([]byte(data), &msgData); err != nil {
		return nil, nil, err
	}

	rows, err := ocDB.Query(`
		SELECT data FROM part 
		WHERE message_id = ? AND session_id = ?
		ORDER BY time_created
	`, msgID, sessionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	wantThoughts := captureThoughts()
	var texts []string
	var thoughts []dbpkg.ThoughtBlock
	for rows.Next() {
		var partData string
		if err := rows.Scan(&partData); err != nil {
			continue
		}
		var part ocPart
		if err := json.Unmarshal([]byte(partData), &part); err != nil {
			continue
		}
		if part.Type == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
		if wantThoughts && part.Type == "reasoning" && part.Text != "" {
			thoughts = append(thoughts, dbpkg.ThoughtBlock{
				MessageID: msgID,
				SessionID: sessionID,
				Text:      part.Text,
				Timestamp: time.UnixMilli(timeCreated),
				Source:    "opencode",
			})
		}
	}

	if len(texts) == 0 {
		return nil, thoughts, nil
	}

	cleaned := stripNoise(strings.Join(texts, "\n"))
	if len(cleaned) < 3 {
		return nil, thoughts, nil
	}

	isUser := msgData.Role != "assistant"
	role := userAlias
	if !isUser {
		role = assistantAlias
	}

	return &dbpkg.TextMessage{
		Role:      role,
		Text:      cleaned,
		Timestamp: time.UnixMilli(timeCreated),
		IsUser:    isUser,
		MessageID: msgID,
		SessionID: sessionID,
	}, thoughts, nil
}

func getNewMessages(ocDB *sql.DB, sessionID string, done map[string]bool) ([]string, error) {
	rows, err := ocDB.Query(`
		SELECT id FROM message 
		WHERE session_id = ? 
		ORDER BY time_created
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var newMsgs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		if !done[id] {
			newMsgs = append(newMsgs, id)
		}
	}
	return newMsgs, nil
}

func buildWatchMarkdown(messages []dbpkg.TextMessage, sessionTitle string) string {
	if len(messages) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", sessionTitle)

	date := messages[0].Timestamp.Format("January 2, 2006")
	fmt.Fprintf(&b, "## %s\n\n", date)

	for _, m := range messages {
		msgDate := m.Timestamp.Format("January 2, 2006")
		if msgDate != date {
			date = msgDate
			fmt.Fprintf(&b, "\n## %s\n\n", date)
		}
		fmt.Fprintf(&b, "**%s** [%s]:\n<!-- msg:%s ses:%s -->\n%s\n\n", m.Role, m.Timestamp.Format("2006-01-02 15:04"), m.MessageID, m.SessionID, m.Text)
	}

	return b.String()
}

type preparedChunk struct {
	chunk      ingest.ChunkData
	validAt    sql.NullString
	serialized []byte
}

func ingestBatch(db *sql.DB, ollama *ollama.OllamaClient, sourceFile string, messages []dbpkg.TextMessage, sessionTitle string) error {
	// Phase 2: Store individual messages with embeddings for direct search
	if inserted, err := dbpkg.InsertMessages(db, ollama, messages); err != nil {
		log.Printf("Warning: message insert failed: %v", err)
	} else if inserted > 0 {
		fmt.Println(ui.RenderPreflightStep("ok", fmt.Sprintf("Stored %d messages", inserted)))
	}

	md := buildWatchMarkdown(messages, sessionTitle)
	sections := ingest.ParseMarkdown(md)
	if len(sections) == 0 {
		return nil
	}

	ctx := context.Background()
	ingestedAt := time.Now().UTC().Format(time.RFC3339)

	// Phase 1: embed everything BEFORE touching the DB — safe to fail here
	var prepared []preparedChunk
	for _, section := range sections {
		if strings.TrimSpace(section.Content) == "" {
			continue
		}

		var validAtValue sql.NullString
		if section.ValidAt != "" {
			validAtValue = sql.NullString{String: section.ValidAt, Valid: true}
		}

		chunks := ingest.ChunkSection(section, 600)
		for _, chunk := range chunks {
			if strings.TrimSpace(chunk.Text) == "" {
				continue
			}

			embedding, err := ollama.Embed(ctx, chunk.Text)
			if err != nil {
				return fmt.Errorf("embed: %w", err)
			}
			serialized, err := sqlite_vec.SerializeFloat32(embedding)
			if err != nil {
				return fmt.Errorf("serialize: %w", err)
			}

			prepared = append(prepared, preparedChunk{
				chunk:      chunk,
				validAt:    validAtValue,
				serialized: serialized,
			})
		}
	}

	if len(prepared) == 0 {
		return nil
	}

	db.Exec(`DELETE FROM vec_chunks WHERE chunk_id IN (SELECT id FROM chunks WHERE source_file = ?)`, sourceFile)

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	tx.Exec(`DELETE FROM chunks WHERE source_file = ?`, sourceFile)

	chunkIDs := make([]int64, 0, len(prepared))
	for _, pc := range prepared {
		res, err := tx.Exec(
			`INSERT INTO chunks (text, source_file, section_title, header_level, parent_title, section_sequence, chunk_sequence, chunk_total, valid_at, ingested_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pc.chunk.Text, sourceFile, pc.chunk.SectionTitle, pc.chunk.HeaderLevel, pc.chunk.ParentTitle,
			pc.chunk.SectionSequence, pc.chunk.ChunkSequence, pc.chunk.ChunkTotal, pc.validAt, ingestedAt,
		)
		if err != nil {
			return fmt.Errorf("insert chunk: %w", err)
		}
		chunkID, _ := res.LastInsertId()
		chunkIDs = append(chunkIDs, chunkID)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	for i, pc := range prepared {
		if _, err := db.Exec(
			"INSERT INTO vec_chunks (chunk_id, embedding) VALUES (?, ?)",
			chunkIDs[i], pc.serialized,
		); err != nil {
			return fmt.Errorf("insert vec: %w", err)
		}
	}

	return nil
}

type tagsResponse struct {
	Models []struct {
		Name string `json:"name"`
	} `json:"models"`
}

func watchPreflight(ollamaHost, embedModel string) error {
	ctx := context.Background()
	baseURL := "http://" + ollamaHost
	client := ollama.NewOllamaClientWithTimeout(baseURL, embedModel, 5*time.Second)
	fmt.Print(ui.RenderPreflightStep("wait", "Ollama"))
	if !client.IsHealthy(ctx) {
		fmt.Print("\r" + ui.RenderPreflightStep("wait", "Ollama  starting...") + "\n")
		cmd := exec.Command("ollama", "serve")
		cmd.Stdout = nil
		cmd.Stderr = nil
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // Own process group, survives watcher Ctrl+C
		if err := cmd.Start(); err != nil {
			fmt.Print("\r" + ui.RenderPreflightStep("fail", "Ollama  could not start") + "\n")
			return fmt.Errorf("start ollama: %w", err)
		}
		go func() { _ = cmd.Wait() }()

		deadline := time.Now().Add(15 * time.Second)
		started := false
		for time.Now().Before(deadline) {
			if client.IsHealthy(ctx) {
				started = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !started {
			fmt.Print("\r" + ui.RenderPreflightStep("fail", "Ollama  timeout") + "\n")
			return fmt.Errorf("ollama did not start within 15s")
		}
		fmt.Print("\r" + ui.RenderPreflightStep("ok", "Ollama  started") + "\n")
	} else {
		fmt.Print("\r" + ui.RenderPreflightStep("ok", "Ollama  running") + "\n")
	}

	fmt.Print(ui.RenderPreflightStep("wait", "Model   "+embedModel))
	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/tags", nil)
	resp, err := httpClient.Do(req)
	modelFound := false
	if err == nil {
		var tags tagsResponse
		if json.NewDecoder(resp.Body).Decode(&tags) == nil {
			for _, m := range tags.Models {
				if m.Name == embedModel {
					modelFound = true
					break
				}
			}
		}
		resp.Body.Close()
	}

	if !modelFound {
		fmt.Print("\r" + ui.RenderPreflightStep("wait", "Model   pulling "+embedModel+"...") + "\n")
		cmd := exec.Command("ollama", "pull", embedModel)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Print("\r" + ui.RenderPreflightStep("fail", "Model   pull failed") + "\n")
			return fmt.Errorf("pull model: %w", err)
		}
		fmt.Print("\r" + ui.RenderPreflightStep("ok", "Model   "+embedModel+" pulled") + "\n")
	} else {
		fmt.Print("\r" + ui.RenderPreflightStep("ok", "Model   "+embedModel) + "\n")
	}

	fmt.Print(ui.RenderPreflightStep("wait", "Warmup  loading into VRAM"))
	warmupClient := ollama.NewOllamaClient(baseURL, embedModel)
	if err := dbpkg.ValidateEmbedDimension(warmupClient); err != nil {
		fmt.Print("\r" + ui.RenderPreflightStep("fail", "Warmup  "+err.Error()) + "\n")
		return fmt.Errorf("warmup: %w", err)
	}
	fmt.Print("\r" + ui.RenderPreflightStep("ok", fmt.Sprintf("Warmup  model loaded (%d dims)", dbpkg.EmbedDimension)) + "\n")

	return nil
}

// --- Note Extractor: Auto-save subagent syntheses ---

func discoverCompletedSubagentSessions(ocDB *sql.DB, extracted map[string]bool, startTime int64, projectDirs []string, agentNames []string) ([]ocSession, error) {
	if len(projectDirs) == 0 {
		return nil, fmt.Errorf("no project directories specified for note filtering")
	}

	// Build IN clause for parent directory filter
	placeholders := make([]string, len(projectDirs))
	args := make([]any, len(projectDirs))
	for i, d := range projectDirs {
		placeholders[i] = "?"
		args[i] = d
	}

	// Build IN clause for agent name filter
	agentPlaceholders := make([]string, len(agentNames))
	for i := range agentNames {
		agentPlaceholders[i] = "?"
	}
	for _, name := range agentNames {
		args = append(args, name)
	}
	args = append(args, startTime)

	query := fmt.Sprintf(`
		SELECT DISTINCT s.id, s.slug, s.title, s.parent_id, s.time_updated,
		       json_extract(m.data, '$.agent') as agent_name
		FROM session s
		JOIN session p ON s.parent_id = p.id
		JOIN message m ON m.session_id = s.id
		WHERE s.parent_id IS NOT NULL
		  AND p.directory IN (%s)
		  AND json_extract(m.data, '$.role') = 'assistant'
		  AND json_extract(m.data, '$.finish') IN ('stop', 'length')
		  AND json_extract(m.data, '$.agent') IN (%s)
		  AND s.time_updated > ?
		ORDER BY s.time_updated
	`, strings.Join(placeholders, ", "), strings.Join(agentPlaceholders, ", "))

	rows, err := ocDB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("discover subagent sessions: %w", err)
	}
	defer rows.Close()

	var sessions []ocSession
	for rows.Next() {
		var s ocSession
		if err := rows.Scan(&s.ID, &s.Slug, &s.Title, &s.ParentID, &s.Updated, &s.AgentName); err != nil {
			continue
		}
		if !extracted[s.ID] {
			sessions = append(sessions, s)
		}
	}
	return sessions, nil
}

func extractFinalSynthesis(ocDB *sql.DB, sessionID string) (string, string, error) {
	// Find last assistant message with finish stop/length
	var lastMsgID string
	err := ocDB.QueryRow(`
		SELECT m.id FROM message m
		WHERE m.session_id = ?
		  AND json_extract(m.data, '$.role') = 'assistant'
		  AND json_extract(m.data, '$.finish') IN ('stop', 'length')
		ORDER BY m.time_created DESC LIMIT 1
	`, sessionID).Scan(&lastMsgID)
	if err != nil {
		return "", "", fmt.Errorf("find last assistant msg: %w", err)
	}

	// Extract text parts from final message
	rows, err := ocDB.Query(`
		SELECT data FROM part
		WHERE message_id = ? AND session_id = ?
		ORDER BY time_created
	`, lastMsgID, sessionID)
	if err != nil {
		return "", "", fmt.Errorf("query parts: %w", err)
	}
	defer rows.Close()

	var texts []string
	for rows.Next() {
		var partData string
		if err := rows.Scan(&partData); err != nil {
			continue
		}
		var part ocPart
		if err := json.Unmarshal([]byte(partData), &part); err != nil {
			continue
		}
		if part.Type == "text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}

	synthesis := stripNoise(strings.Join(texts, "\n"))
	if len(synthesis) < 500 {
		return "", "", nil // not substantial
	}

	// Extract original prompt (first user message)
	var prompt string
	var promptText sql.NullString
	ocDB.QueryRow(`
		SELECT json_extract(p.data, '$.text') FROM message m
		JOIN part p ON p.message_id = m.id AND p.session_id = m.session_id
		WHERE m.session_id = ?
		  AND json_extract(m.data, '$.role') = 'user'
		  AND json_extract(p.data, '$.type') = 'text'
		ORDER BY m.time_created LIMIT 1
	`, sessionID).Scan(&promptText)
	if promptText.Valid {
		prompt = promptText.String
		if len(prompt) > 500 {
			prompt = prompt[:500] + "..."
		}
	}

	return synthesis, prompt, nil
}

func saveNote(notesDir string, session ocSession, synthesis, prompt, agentName string) (string, error) {
	date := time.UnixMilli(session.Updated).Format("2006-01-02")
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		return "", fmt.Errorf("create notes dir: %w", err)
	}

	// Strip noise from title: agent tags like (@agent subagent), date prefixes
	cleanTitle := session.Title
	cleanTitle = strings.TrimSpace(regexp.MustCompile(`\(@\w+\s+subagent\)`).ReplaceAllString(cleanTitle, ""))
	slug := sanitizeSlug(cleanTitle)
	if slug == "" {
		slug = "untitled"
	}
	filePath := filepath.Join(notesDir, slug+".md")

	for i := 2; fileExists(filePath); i++ {
		filePath = filepath.Join(notesDir, fmt.Sprintf("%s-%d.md", slug, i))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", session.Title)
	fmt.Fprintf(&b, "**Date:** %s\n", date)
	fmt.Fprintf(&b, "**Agent:** %s\n", agentName)
	fmt.Fprintf(&b, "**Session:** %s\n", session.ID)
	if session.ParentID.Valid {
		fmt.Fprintf(&b, "**Parent:** %s\n", session.ParentID.String)
	}
	if prompt != "" {
		fmt.Fprintf(&b, "**Prompt:** %s\n", prompt)
	}
	b.WriteString("\n---\n\n")
	b.WriteString(synthesis)
	b.WriteString("\n")

	if err := os.WriteFile(filePath, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("write note: %w", err)
	}
	return filePath, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadExtracted(path string) map[string]bool {
	extracted := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return extracted
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if id := strings.TrimSpace(scanner.Text()); id != "" {
			extracted[id] = true
		}
	}
	return extracted
}

func appendExtracted(path, sessionID string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Warning: could not append to extracted file: %v", err)
		return
	}
	fmt.Fprintln(f, sessionID)
	f.Close()
}

func extractNotes(ocDB *sql.DB, extracted map[string]bool, notesDir string, startTime int64, extractedPath string, projectDirs []string, agentNames []string) {
	completed, _ := discoverCompletedSubagentSessions(ocDB, extracted, startTime, projectDirs, agentNames)
	for _, sub := range completed {
		synthesis, prompt, err := extractFinalSynthesis(ocDB, sub.ID)
		if err != nil || synthesis == "" {
			extracted[sub.ID] = true
			appendExtracted(extractedPath, sub.ID)
			continue
		}
		filePath, err := saveNote(notesDir, sub, synthesis, prompt, sub.AgentName)
		if err != nil {
			log.Printf("note extract failed for %s: %v", sub.ID, err)
			continue
		}
		extracted[sub.ID] = true
		appendExtracted(extractedPath, sub.ID)
		fmt.Println(ui.RenderPreflightStep("ok", fmt.Sprintf("Note: %s", filepath.Base(filePath))))
	}
}

func RunWatch(args []string, kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias string) {
	fs := flag.NewFlagSet("watch-oc", flag.ExitOnError)
	batchSize := fs.Int("batch", 6, "text messages before ingesting")
	pollSec := fs.Int("poll", 3, "poll interval in seconds")
	notesDir := fs.String("notes", "", "directory for extracted subagent notes (empty = disabled)")
	notesAgent := fs.String("notes-agent", "", "agent name to filter for note extraction (required with --notes)")
	projectDir := fs.String("project-dir", "", "comma-separated parent session directories to filter notes (required with --notes)")
	backfill := fs.Bool("backfill", false, "extract all existing sessions for the agent, not just new ones")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	ocDBPath := openCodeDBPath()
	ocDB, err := sql.Open("sqlite3", ocDBPath+"?mode=ro")
	if err != nil {
		log.Fatalf("open opencode db: %v", err)
	}
	defer ocDB.Close()

	// Resolve agent name: flag > env > fatal
	agent := *notesAgent
	if agent == "" {
		agent = os.Getenv("KISEKI_NOTES_AGENT")
	}

	// Split comma-separated agent names into a slice
	var agentNames []string
	if agent != "" {
		for _, name := range strings.Split(agent, ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				agentNames = append(agentNames, trimmed)
			}
		}
	}

	// Backfill mode: extract notes and exit (no watcher, no session picker)
	if *backfill {
		if *notesDir == "" {
			log.Fatal("--backfill requires --notes <dir>")
		}
		if *projectDir == "" {
			log.Fatal("--backfill requires --project-dir <dirs>")
		}
		if len(agentNames) == 0 {
			log.Fatal("--backfill requires --notes-agent or KISEKI_NOTES_AGENT")
		}
		dirs := strings.Split(*projectDir, ",")
		extractedPath := filepath.Join(*notesDir, ".extracted")
		extracted := loadExtracted(extractedPath)
		fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Backfill: extracting %s notes to %s (dirs: %s, %d already extracted)", agent, *notesDir, *projectDir, len(extracted))))
		extractNotes(ocDB, extracted, *notesDir, 0, extractedPath, dirs, agentNames)
		fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Done. %d total extracted.", len(loadExtracted(extractedPath)))))
		return
	}

	sessions, err := discoverSessions(ocDB)
	if err != nil {
		log.Fatalf("discover sessions: %v", err)
	}
	if len(sessions) == 0 {
		log.Fatal("no OpenCode sessions found")
	}

	session, err := pickSession(sessions)
	if err != nil {
		log.Fatalf("pick session: %v", err)
	}

	fmt.Println()
	if err := watchPreflight(ollamaHost, embedModel); err != nil {
		log.Fatalf("preflight: %v", err)
	}

	fmt.Println()
	fmt.Println(ui.RenderWatchStatus(session.Title, session.ID, *batchSize, *pollSec, kisekiDB))
	fmt.Println()

	db, err := dbpkg.InitDB(kisekiDB)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	ollama := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	db.Exec(`DELETE FROM vec_chunks WHERE chunk_id NOT IN (SELECT id FROM chunks)`)

	done := make(map[string]bool)
	retry := make(map[string]int)
	var pending []dbpkg.TextMessage
	var pendingThoughts []dbpkg.ThoughtBlock

	batchNum := 0
	watchPrefix := fmt.Sprintf("watch://%s/batch-", session.ID)
	var maxBatch sql.NullInt64
	_ = db.QueryRow(
		`SELECT MAX(CAST(REPLACE(source_file, ?, '') AS INTEGER)) FROM chunks WHERE source_file LIKE ?`,
		watchPrefix, watchPrefix+"%",
	).Scan(&maxBatch)
	if maxBatch.Valid {
		batchNum = int(maxBatch.Int64) + 1
	}

	done, err = getExistingMessageIDs(ocDB, session.ID)
	if err != nil {
		log.Fatalf("get existing messages: %v", err)
	}
	fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Skipping %d existing messages. Watching for new...", len(done))))
	fmt.Println()

	// Note extraction setup (live mode)
	var extracted map[string]bool
	var extractedPath string
	var noteStartTime int64
	var projectDirs []string
	if *notesDir != "" {
		if *projectDir == "" {
			log.Fatal("--notes requires --project-dir <dirs>")
		}
		if len(agentNames) == 0 {
			log.Fatal("--notes requires --notes-agent or KISEKI_NOTES_AGENT")
		}
		projectDirs = strings.Split(*projectDir, ",")
		noteStartTime = 0 // Check all sessions — .extracted file handles dedup
		extractedPath = filepath.Join(*notesDir, ".extracted")
		extracted = loadExtracted(extractedPath)
		fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Note extraction: %s (agents: %s, dirs: %s, %d already extracted)", *notesDir, agent, *projectDir, len(extracted))))
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	ticker := time.NewTicker(time.Duration(*pollSec) * time.Second)
	defer ticker.Stop()

	flushPending := func() {
		if len(pending) == 0 && len(pendingThoughts) == 0 {
			return
		}
		if len(pending) > 0 {
			fmt.Println()
			fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Flushing %d pending messages...", len(pending))))
			for i := range pending {
				pending[i].Text = ingest.NormalizeText(pending[i].Text)
			}
			typos := ingest.FindTyposInMessages(pending)
			if err := ingest.UpdateTyposFile(typos); err != nil {
				log.Printf("Warning: typo update failed: %v", err)
			}
			sourceFile := fmt.Sprintf("watch://%s/batch-%d", session.ID, batchNum)
			if err := ingestBatch(db, ollama, sourceFile, pending, session.Title); err != nil {
				fmt.Println(ui.RenderPreflightStep("fail", fmt.Sprintf("Flush error: %v", err)))
				return
			}
			batchNum++
			fmt.Println(ui.RenderIngest(len(pending), batchNum))
			pending = nil
		}
		if len(pendingThoughts) > 0 && captureThoughts() {
			if inserted, err := dbpkg.InsertThoughts(db, pendingThoughts); err != nil {
				log.Printf("Warning: thought insert failed: %v", err)
			} else if inserted > 0 {
				fmt.Println(ui.RenderPreflightStep("ok", fmt.Sprintf("Stored %d thoughts", inserted)))
			}
			pendingThoughts = nil
		}
	}

	for {
		select {
		case <-sigCh:
			flushPending()
			if *notesDir != "" {
				extractNotes(ocDB, extracted, *notesDir, noteStartTime, extractedPath, projectDirs, agentNames)
			}
			fmt.Println()
			fmt.Println(ui.InfoStyle.Render("  Stopped."))
			return
		case <-ticker.C:
		}

		newMsgs, err := getNewMessages(ocDB, session.ID, done)
		if err != nil {
			continue
		}

		for _, msgID := range newMsgs {
			tm, msgThoughts, err := readTextFromDB(ocDB, session.ID, msgID, userAlias, assistantAlias)
			if err != nil || tm == nil {
				// Even if text is nil, we may have captured thoughts from this message
				if len(msgThoughts) > 0 {
					pendingThoughts = append(pendingThoughts, msgThoughts...)
				}
				retry[msgID]++
				if retry[msgID] > 60 {
					done[msgID] = true
					delete(retry, msgID)
				}
				continue
			}

			done[msgID] = true
			delete(retry, msgID)
			pending = append(pending, *tm)
			if len(msgThoughts) > 0 {
				pendingThoughts = append(pendingThoughts, msgThoughts...)
			}

			fmt.Println(ui.RenderMessage(tm.Role, tm.Timestamp.Format("15:04:05"), tm.Text, tm.IsUser))
		}

		if len(pending) >= *batchSize {
			// Normalize text before ingestion
			for i := range pending {
				pending[i].Text = ingest.NormalizeText(pending[i].Text)
			}
			typos := ingest.FindTyposInMessages(pending)
			if err := ingest.UpdateTyposFile(typos); err != nil {
				log.Printf("Warning: typo update failed: %v", err)
			}

			sourceFile := fmt.Sprintf("watch://%s/batch-%d", session.ID, batchNum)
			if err := ingestBatch(db, ollama, sourceFile, pending, session.Title); err != nil {
				fmt.Println(ui.RenderPreflightStep("fail", fmt.Sprintf("Ingest error: %v", err)))
				continue
			}
			batchNum++
			fmt.Println()
			fmt.Println(ui.RenderIngest(len(pending), batchNum))
			fmt.Println()
			pending = nil

			// Flush accumulated thoughts alongside the text batch
			if len(pendingThoughts) > 0 && captureThoughts() {
				if inserted, err := dbpkg.InsertThoughts(db, pendingThoughts); err != nil {
					log.Printf("Warning: thought insert failed: %v", err)
				} else if inserted > 0 {
					fmt.Println(ui.RenderPreflightStep("ok", fmt.Sprintf("Stored %d thoughts", inserted)))
				}
				pendingThoughts = nil
			}
		}

		// Extract completed subagent notes
		if *notesDir != "" {
			extractNotes(ocDB, extracted, *notesDir, noteStartTime, extractedPath, projectDirs, agentNames)
		}
	}
}
