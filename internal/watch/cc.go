package watch

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	dbpkg "github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
	"github.com/Gsirawan/kiseki-beta/internal/ui"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Claude Code session from sessions-index.json
type ccSessionEntry struct {
	SessionID    string `json:"sessionId"`
	FullPath     string `json:"fullPath"`
	Summary      string `json:"summary"`
	FirstPrompt  string `json:"firstPrompt"`
	MessageCount int    `json:"messageCount"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	ProjectPath  string `json:"projectPath"`
	IsSidechain  bool   `json:"isSidechain"`
}

type ccSessionsIndex struct {
	Version      int              `json:"version"`
	Entries      []ccSessionEntry `json:"entries"`
	OriginalPath string           `json:"originalPath"`
}

// Claude Code JSONL line
type ccJSONLLine struct {
	Type      string    `json:"type"`
	UUID      string    `json:"uuid"`
	SessionID string    `json:"sessionId"`
	Timestamp string    `json:"timestamp"`
	Message   ccMessage `json:"message"`
}

type ccMessage struct {
	Role    string      `json:"role"`
	Content any `json:"content"` // string for user, []any for assistant
}


func claudeCodeBasePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func discoverCCProjects(basePath string) ([]string, error) {
	var projects []string

	transcriptsDir := filepath.Join(basePath, "transcripts")
	if entries, err := os.ReadDir(transcriptsDir); err == nil {
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".jsonl") {
				projects = append(projects, "transcripts")
				break
			}
		}
	}

	projectsDir := filepath.Join(basePath, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil && len(projects) == 0 {
		return nil, fmt.Errorf("read projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsDir, entry.Name())
		if _, err := os.Stat(filepath.Join(dirPath, "sessions-index.json")); err == nil {
			projects = append(projects, entry.Name())
			continue
		}
		dirEntries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, de := range dirEntries {
			if strings.HasSuffix(de.Name(), ".jsonl") {
				projects = append(projects, entry.Name())
				break
			}
		}
	}
	return projects, nil
}

func discoverCCSessions(basePath, projectDir string) ([]ccSessionEntry, error) {
	var projectPath string
	if projectDir == "transcripts" {
		projectPath = filepath.Join(basePath, "transcripts")
	} else {
		projectPath = filepath.Join(basePath, "projects", projectDir)
	}

	indexed := make(map[string]bool)
	indexPath := filepath.Join(projectPath, "sessions-index.json")
	if data, err := os.ReadFile(indexPath); err == nil {
		var index ccSessionsIndex
		if err := json.Unmarshal(data, &index); err == nil {
			for _, s := range index.Entries {
				indexed[s.SessionID] = true
			}
		}
	}

	var sessions []ccSessionEntry

	entries, err := os.ReadDir(projectPath)
	if err != nil {
		return nil, fmt.Errorf("read project dir: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(name, ".jsonl")
		fullPath := filepath.Join(projectPath, name)

		info, err := entry.Info()
		if err != nil || info.Size() == 0 {
			continue
		}

		if indexed[sessionID] {
			sessions = append(sessions, buildSessionFromIndex(basePath, projectDir, sessionID))
			continue
		}

		sessions = append(sessions, buildSessionFromJSONL(sessionID, fullPath, info))
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Modified > sessions[j].Modified
	})

	return sessions, nil
}

func buildSessionFromIndex(basePath, projectDir, sessionID string) ccSessionEntry {
	indexPath := filepath.Join(basePath, "projects", projectDir, "sessions-index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return ccSessionEntry{SessionID: sessionID}
	}
	var index ccSessionsIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return ccSessionEntry{SessionID: sessionID}
	}
	for _, s := range index.Entries {
		if s.SessionID == sessionID && !s.IsSidechain && s.MessageCount > 0 {
			// Set FullPath to actual file location (index may have stale/empty path)
			if projectDir == "transcripts" {
				s.FullPath = filepath.Join(basePath, "transcripts", sessionID+".jsonl")
			} else {
				s.FullPath = filepath.Join(basePath, "projects", projectDir, sessionID+".jsonl")
			}
			return s
		}
	}
	return ccSessionEntry{}
}

func buildSessionFromJSONL(sessionID, fullPath string, info os.FileInfo) ccSessionEntry {
	entry := ccSessionEntry{
		SessionID: sessionID,
		FullPath:  fullPath,
		Modified:  info.ModTime().UTC().Format(time.RFC3339),
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return entry
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	msgCount := 0
	for scanner.Scan() {
		var line ccJSONLLine
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}

		if line.Type == "summary" {
			var raw struct {
				Summary string `json:"summary"`
			}
			if json.Unmarshal(scanner.Bytes(), &raw) == nil && raw.Summary != "" {
				entry.Summary = raw.Summary
			}
			continue
		}

		if line.Type == "user" || line.Type == "assistant" {
			msgCount++
			if entry.FirstPrompt == "" && line.Type == "user" {
				if text, ok := line.Message.Content.(string); ok && len(text) > 0 {
					entry.FirstPrompt = text
					if len(entry.FirstPrompt) > 100 {
						entry.FirstPrompt = entry.FirstPrompt[:100]
					}
				}
			}
			if entry.Created == "" && line.Timestamp != "" {
				entry.Created = line.Timestamp
			}
		}
	}

	entry.MessageCount = msgCount
	return entry
}

func pickCCProject(basePath string, projects []string) (string, error) {
	if len(projects) == 1 {
		return projects[0], nil
	}

	fmt.Println()
	fmt.Println(ui.RenderHeader())
	fmt.Println()
	fmt.Println(ui.PromptStyle.Render("  Claude Code Projects:"))
	fmt.Println()

	for i, p := range projects {
		readable := p
		if p != "transcripts" {
			indexPath := filepath.Join(basePath, "projects", p, "sessions-index.json")
			if data, err := os.ReadFile(indexPath); err == nil {
				var index ccSessionsIndex
				if json.Unmarshal(data, &index) == nil && index.OriginalPath != "" {
					readable = index.OriginalPath
				}
			}
			if readable == p {
				readable = strings.ReplaceAll(p, "-", "/")
			}
		}
		fmt.Println(ui.RenderSessionItem(i+1, readable, "", ""))
	}

	fmt.Println()
	fmt.Print(ui.PromptStyle.Render("  Select project [1]: "))
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read input: %w", err)
	}

	input = strings.TrimSpace(input)
	if input == "" {
		input = "1"
	}

	var choice int
	if _, err := fmt.Sscanf(input, "%d", &choice); err != nil || choice < 1 || choice > len(projects) {
		return "", fmt.Errorf("invalid choice: %s", input)
	}

	return projects[choice-1], nil
}

func pickCCSession(sessions []ccSessionEntry) (ccSessionEntry, error) {
	fmt.Println()
	fmt.Println(ui.RenderHeader())
	fmt.Println()

	limit := min(10, len(sessions))

	for i, s := range sessions[:limit] {
		title := s.Summary
		if title == "" {
			title = s.FirstPrompt
			if len(title) > 60 {
				title = title[:60] + "..."
			}
		}
		modified := s.Modified
		if t, err := time.Parse(time.RFC3339, s.Modified); err == nil {
			modified = t.Format("Jan 02, 2006 15:04")
		}
		slug := fmt.Sprintf("(%d msgs)", s.MessageCount)
		fmt.Println(ui.RenderSessionItem(i+1, title, slug, modified))
	}

	fmt.Println()
	fmt.Print(ui.PromptStyle.Render("  Select session [1]: "))
	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return ccSessionEntry{}, fmt.Errorf("read input: %w", err)
	}

	input = strings.TrimSpace(input)
	if input == "" {
		input = "1"
	}

	var choice int
	if _, err := fmt.Sscanf(input, "%d", &choice); err != nil || choice < 1 || choice > limit {
		return ccSessionEntry{}, fmt.Errorf("invalid choice: %s", input)
	}

	return sessions[choice-1], nil
}

// readCCJSONL reads the JSONL file and returns all text messages and optionally thinking blocks.
// Thinking blocks are only extracted when captureThoughts() returns true (KISEKI_CAPTURE_THOUGHTS=true).
func readCCJSONL(filePath, sessionID, userAlias, assistantAlias string) ([]dbpkg.TextMessage, []dbpkg.ThoughtBlock, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	wantThoughts := captureThoughts()

	var messages []dbpkg.TextMessage
	var thoughts []dbpkg.ThoughtBlock
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		var entry ccJSONLLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		// Only process user and assistant messages
		if entry.Type != "user" && entry.Type != "assistant" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if ts.IsZero() {
			ts, _ = time.Parse(time.RFC3339, entry.Timestamp)
		}
		if ts.IsZero() {
			ts = time.Now()
		}
		ts = ts.Local()

		if entry.Type == "user" {
			// User content is a string
			text := ""
			switch v := entry.Message.Content.(type) {
			case string:
				text = v
			case []any:
				// Sometimes user content is array of blocks
				for _, block := range v {
					if m, ok := block.(map[string]any); ok {
						if m["type"] == "text" {
							if t, ok := m["text"].(string); ok {
								text += t + "\n"
							}
						}
					}
				}
			}

			cleaned := stripNoise(text)
			if len(cleaned) < 3 {
				continue
			}

			msgSessionID := entry.SessionID
			if msgSessionID == "" {
				msgSessionID = sessionID
			}
			messages = append(messages, dbpkg.TextMessage{
				Role:      userAlias,
				Text:      cleaned,
				Timestamp: ts,
				IsUser:    true,
				MessageID: entry.UUID,
				SessionID: msgSessionID,
			})
		}

		if entry.Type == "assistant" {
			// Assistant content is array of blocks
			blocks, ok := entry.Message.Content.([]any)
			if !ok {
				continue
			}

			msgSessionID := entry.SessionID
			if msgSessionID == "" {
				msgSessionID = sessionID
			}

			var texts []string
			for _, block := range blocks {
				m, ok := block.(map[string]any)
				if !ok {
					continue
				}
				// Text blocks → messages
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok && t != "" {
						texts = append(texts, t)
					}
				}
				// Thinking blocks → separate thoughts slice (only when env var is set)
				// CC thinking blocks use key "thinking" (not "text") for content
				if wantThoughts && m["type"] == "thinking" {
					if t, ok := m["thinking"].(string); ok && t != "" {
						thoughts = append(thoughts, dbpkg.ThoughtBlock{
							MessageID: entry.UUID,
							SessionID: msgSessionID,
							Text:      t,
							Timestamp: ts,
							Source:    "claudecode",
						})
					}
				}
			}

			if len(texts) == 0 {
				continue
			}

			cleaned := stripNoise(strings.Join(texts, "\n"))
			if len(cleaned) < 3 {
				continue
			}

			messages = append(messages, dbpkg.TextMessage{
				Role:      assistantAlias,
				Text:      cleaned,
				Timestamp: ts,
				IsUser:    false,
				MessageID: entry.UUID,
				SessionID: msgSessionID,
			})
		}
	}

	return messages, thoughts, scanner.Err()
}

// --- CC Note Extraction ---

type ccSubagentInfo struct {
	AgentID     string
	AgentName   string
	Description string
	FilePath    string
	Updated     time.Time
}

// buildCCAgentMap reads the parent JSONL and maps agentId → agent name + description
func buildCCAgentMap(parentJSONLPath string) (nameMap map[string]string, descMap map[string]string, err error) {
	f, err := os.Open(parentJSONLPath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	toolUseToName := make(map[string]string)
	toolUseToDesc := make(map[string]string)
	nameMap = make(map[string]string)
	descMap = make(map[string]string)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		var raw map[string]any
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		lineType, _ := raw["type"].(string)

		if lineType == "assistant" {
			msg, _ := raw["message"].(map[string]any)
			if msg == nil {
				continue
			}
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, _ := block.(map[string]any)
				if b == nil || b["type"] != "tool_use" || b["name"] != "Agent" {
					continue
				}
				toolID, _ := b["id"].(string)
				input, _ := b["input"].(map[string]any)
				if input == nil || toolID == "" {
					continue
				}
				subType, _ := input["subagent_type"].(string)
				if subType != "" {
					toolUseToName[toolID] = subType
					desc, _ := input["description"].(string)
					toolUseToDesc[toolID] = desc
				}
			}
		}

		if lineType == "progress" {
			data, _ := raw["data"].(map[string]any)
			if data == nil || data["type"] != "agent_progress" {
				continue
			}
			agentID, _ := data["agentId"].(string)
			parentToolID, _ := raw["parentToolUseID"].(string)
			if agentID != "" && parentToolID != "" {
				if name, ok := toolUseToName[parentToolID]; ok {
					nameMap[agentID] = name
					descMap[agentID] = toolUseToDesc[parentToolID]
				}
			}
		}

		// CC >= 2.1.50 stopped emitting agent_progress lines.
		// Fallback: parse tool_result text for "agentId: <id>" and link
		// back to the Agent tool_use via matching tool_use_id.
		if lineType == "user" {
			msg, _ := raw["message"].(map[string]any)
			if msg == nil {
				continue
			}
			content, _ := msg["content"].([]any)
			for _, block := range content {
				b, _ := block.(map[string]any)
				if b == nil || b["type"] != "tool_result" {
					continue
				}
				toolUseID, _ := b["tool_use_id"].(string)
				if toolUseID == "" {
					continue
				}
				if _, ok := toolUseToName[toolUseID]; !ok {
					continue
				}
				text := extractToolResultText(b)
				if _, after, ok := strings.Cut(text, "agentId:"); ok {
					rest := strings.TrimSpace(after)
					if sp := strings.IndexAny(rest, " \t\n("); sp > 0 {
						rest = rest[:sp]
					}
					if rest != "" {
						nameMap[rest] = toolUseToName[toolUseID]
						descMap[rest] = toolUseToDesc[toolUseID]
					}
				}
			}
		}
	}

	return nameMap, descMap, scanner.Err()
}

// extractToolResultText pulls the text string out of a tool_result content block.
func extractToolResultText(b map[string]any) string {
	// content can be a string or []any of typed blocks
	switch v := b["content"].(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok && m["type"] == "text" {
				if t, ok := m["text"].(string); ok {
					return t
				}
			}
		}
	}
	return ""
}

// discoverCCSubagents finds subagent sessions matching the given agent names
func discoverCCSubagents(sessionDir, parentJSONLPath string, agentNames []string, extracted map[string]bool) ([]ccSubagentInfo, error) {
	subagentsDir := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(subagentsDir)
	if err != nil {
		return nil, nil // no subagents dir = no subagents, not an error
	}

	nameMap, descMap, err := buildCCAgentMap(parentJSONLPath)
	if err != nil {
		return nil, fmt.Errorf("build agent map: %w", err)
	}

	var result []ccSubagentInfo
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		agentID := strings.TrimPrefix(strings.TrimSuffix(name, ".jsonl"), "agent-")

		if extracted[agentID] {
			continue
		}
		if len(agentNames) > 0 && !slices.Contains(agentNames, nameMap[agentID]) {
			continue
		}

		filePath := filepath.Join(subagentsDir, name)
		info, err := entry.Info()
		if err != nil || info.Size() == 0 {
			continue
		}

		result = append(result, ccSubagentInfo{
			AgentID:     agentID,
			AgentName:   nameMap[agentID],
			Description: descMap[agentID],
			FilePath:    filePath,
			Updated:     info.ModTime(),
		})
	}

	return result, nil
}

// extractCCFinalSynthesis reads a subagent JSONL and returns the last substantial assistant text + first user prompt
func extractCCFinalSynthesis(subagentJSONLPath string) (string, string, error) {
	f, err := os.Open(subagentJSONLPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)

	var lastAssistantText string
	var firstUserPrompt string

	for scanner.Scan() {
		var entry ccJSONLLine
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}

		if entry.Type == "user" && firstUserPrompt == "" {
			switch v := entry.Message.Content.(type) {
			case string:
				firstUserPrompt = v
			case []any:
				for _, block := range v {
					if m, ok := block.(map[string]any); ok {
						if m["type"] == "text" {
							if t, ok := m["text"].(string); ok {
								firstUserPrompt = t
								break
							}
						}
					}
				}
			}
			if len(firstUserPrompt) > 500 {
				firstUserPrompt = firstUserPrompt[:500] + "..."
			}
		}

		if entry.Type == "assistant" {
			blocks, ok := entry.Message.Content.([]any)
			if !ok {
				continue
			}
			var texts []string
			for _, block := range blocks {
				m, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if m["type"] == "text" {
					if t, ok := m["text"].(string); ok && t != "" {
						texts = append(texts, t)
					}
				}
			}
			if len(texts) > 0 {
				combined := stripNoise(strings.Join(texts, "\n"))
				if len(combined) >= 500 {
					lastAssistantText = combined
				}
			}
		}
	}

	return lastAssistantText, firstUserPrompt, scanner.Err()
}

// ccSynthesisNoise strips process metadata from subagent output
var ccSynthesisNoise = []*regexp.Regexp{
	regexp.MustCompile(`(?s)\*\*Query received:\*\*.*?---`),
	regexp.MustCompile(`(?s)\*\*Searches performed:\*\*.*?---`),
	regexp.MustCompile(`(?m)^\*\*Total sources consulted:\*\*.*\n`),
	regexp.MustCompile(`(?s)\n---\n\n\*\*Gaps?\*\*\n.*$`),
	regexp.MustCompile(`(?m)^I now have a comprehensive picture\..*\n`),
	regexp.MustCompile(`(?m)^Let me compile the full report\..*\n`),
}

func stripCCSynthesisNoise(text string) string {
	for _, p := range ccSynthesisNoise {
		text = p.ReplaceAllString(text, "")
	}
	// Collapse triple+ newlines to double
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// saveCCNote writes a note file for a CC subagent synthesis
func saveCCNote(notesDir string, sub ccSubagentInfo, synthesis, prompt string) (string, error) {
	date := sub.Updated.Format("2006-01-02")
	if err := os.MkdirAll(notesDir, 0755); err != nil {
		return "", fmt.Errorf("create notes dir: %w", err)
	}

	title := sub.Description
	if title == "" {
		title = prompt
		if len(title) > 80 {
			title = title[:80]
		}
	}
	if title == "" {
		title = sub.AgentName + " subagent"
	}

	slug := sanitizeSlug(title)
	if slug == "" {
		slug = "untitled"
	}
	filePath := filepath.Join(notesDir, slug+".md")

	for i := 2; fileExists(filePath); i++ {
		filePath = filepath.Join(notesDir, fmt.Sprintf("%s-%d.md", slug, i))
	}

	synthesis = stripCCSynthesisNoise(synthesis)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "**Date:** %s\n", date)
	fmt.Fprintf(&b, "**Agent:** %s\n", sub.AgentName)
	fmt.Fprintf(&b, "**AgentID:** %s\n", sub.AgentID)
	b.WriteString("\n---\n\n")
	b.WriteString(synthesis)
	b.WriteString("\n")

	if err := os.WriteFile(filePath, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("write note: %w", err)
	}
	return filePath, nil
}

// extractCCNotes discovers and extracts notes from CC subagent sessions
func extractCCNotes(sessionDir, parentJSONLPath, agentName, notesDir, extractedPath string, extracted map[string]bool) {
	agentNames := strings.Split(agentName, ",")
	for i := range agentNames {
		agentNames[i] = strings.TrimSpace(agentNames[i])
	}
	subs, err := discoverCCSubagents(sessionDir, parentJSONLPath, agentNames, extracted)
	if err != nil {
		log.Printf("discover cc subagents: %v", err)
		return
	}

	for _, sub := range subs {
		synthesis, prompt, err := extractCCFinalSynthesis(sub.FilePath)
		if err != nil {
			// Actual error — mark done to avoid infinite retry
			extracted[sub.AgentID] = true
			appendExtracted(extractedPath, sub.AgentID)
			continue
		}
		if synthesis == "" {
			// Agent still running or short output — retry next tick
			continue
		}
		filePath, err := saveCCNote(notesDir, sub, synthesis, prompt)
		if err != nil {
			log.Printf("cc note extract failed for %s: %v", sub.AgentID, err)
			continue
		}
		extracted[sub.AgentID] = true
		appendExtracted(extractedPath, sub.AgentID)
		fmt.Println(ui.RenderPreflightStep("ok", fmt.Sprintf("Note: %s", filepath.Base(filePath))))
	}
}

func RunWatchCC(args []string, kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias string) {
	fs := flag.NewFlagSet("watch-cc", flag.ExitOnError)
	batchSize := fs.Int("batch", 6, "text messages before ingesting")
	pollSec := fs.Int("poll", 3, "poll interval in seconds")
	notesDir := fs.String("notes", "", "directory for extracted subagent notes (empty = disabled)")
	notesAgent := fs.String("notes-agent", "", "agent name to filter for note extraction (required with --notes)")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	basePath := claudeCodeBasePath()

	// Discover projects
	projects, err := discoverCCProjects(basePath)
	if err != nil {
		log.Fatalf("discover projects: %v", err)
	}
	if len(projects) == 0 {
		log.Fatal("no Claude Code projects found")
	}

	projectDir, err := pickCCProject(basePath, projects)
	if err != nil {
		log.Fatalf("pick project: %v", err)
	}

	// Discover sessions in project
	sessions, err := discoverCCSessions(basePath, projectDir)
	if err != nil {
		log.Fatalf("discover sessions: %v", err)
	}
	if len(sessions) == 0 {
		log.Fatal("no Claude Code sessions found in project")
	}

	session, err := pickCCSession(sessions)
	if err != nil {
		log.Fatalf("pick session: %v", err)
	}

	fmt.Println()
	if err := watchPreflight(ollamaHost, embedModel); err != nil {
		log.Fatalf("preflight: %v", err)
	}

	fmt.Println()
	title := session.Summary
	if title == "" {
		title = session.FirstPrompt
		if len(title) > 60 {
			title = title[:60] + "..."
		}
	}
	fmt.Println(ui.RenderWatchStatus(title, session.SessionID, *batchSize, *pollSec, kisekiDB))
	fmt.Println()

	db, err := dbpkg.InitDB(kisekiDB)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	ollama := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	// Cleanup orphaned vec_chunks
	db.Exec(`DELETE FROM vec_chunks WHERE chunk_id NOT IN (SELECT id FROM chunks)`)

	// Find batch number
	batchNum := 0
	watchPrefix := fmt.Sprintf("watch-cc://%s/batch-", session.SessionID)
	var maxBatch sql.NullInt64
	_ = db.QueryRow(
		`SELECT MAX(CAST(REPLACE(source_file, ?, '') AS INTEGER)) FROM chunks WHERE source_file LIKE ?`,
		watchPrefix, watchPrefix+"%",
	).Scan(&maxBatch)
	if maxBatch.Valid {
		batchNum = int(maxBatch.Int64) + 1
	}

	// Build done map from existing messages (UUID-based dedup, not fragile count)
	existingMsgs, _, _ := readCCJSONL(session.FullPath, session.SessionID, userAlias, assistantAlias)
	done := make(map[string]bool)
	for _, m := range existingMsgs {
		if m.MessageID != "" {
			done[m.MessageID] = true
		}
	}
	fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Skipping %d existing messages. Watching for new...", len(done))))
	fmt.Println()

	// Note extraction setup
	agent := *notesAgent
	if agent == "" {
		agent = os.Getenv("KISEKI_NOTES_AGENT")
	}
	var extracted map[string]bool
	var extractedPath string
	var sessionDir string
	if *notesDir != "" {
		if agent == "" {
			log.Fatal("--notes requires --notes-agent or KISEKI_NOTES_AGENT")
		}
		sessionDir = strings.TrimSuffix(session.FullPath, ".jsonl")
		extractedPath = filepath.Join(*notesDir, ".extracted-cc")
		extracted = loadExtracted(extractedPath)
		fmt.Println(ui.InfoStyle.Render(fmt.Sprintf("  Note extraction: %s (agents: %s, %d already extracted)", *notesDir, agent, len(extracted))))
	}

	var pending []dbpkg.TextMessage
	var pendingThoughts []dbpkg.ThoughtBlock
	doneThoughts := make(map[string]bool) // tracks processed thought message UUIDs

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
			sourceFile := fmt.Sprintf("watch-cc://%s/batch-%d", session.SessionID, batchNum)
			if err := ingestBatch(db, ollama, sourceFile, pending, title); err != nil {
				fmt.Println(ui.RenderPreflightStep("fail", fmt.Sprintf("Flush error: %v", err)))
				return
			}
			batchNum++
			fmt.Println(ui.RenderIngest(len(pending), batchNum))
			pending = nil
		}
		// Flush pending thoughts if InsertThoughts is available and thoughts were captured
		if len(pendingThoughts) > 0 {
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
				extractCCNotes(sessionDir, session.FullPath, agent, *notesDir, extractedPath, extracted)
			}
			fmt.Println()
			fmt.Println(ui.InfoStyle.Render("  Stopped."))
			return
		case <-ticker.C:
		}

		allMsgs, newThoughts, err := readCCJSONL(session.FullPath, session.SessionID, userAlias, assistantAlias)
		if err != nil {
			continue
		}

		// Collect any new thoughts from this read (dedup by message UUID)
		for _, t := range newThoughts {
			if t.MessageID == "" || doneThoughts[t.MessageID] {
				continue
			}
			doneThoughts[t.MessageID] = true
			pendingThoughts = append(pendingThoughts, t)
		}

		for _, tm := range allMsgs {
			if tm.MessageID == "" || done[tm.MessageID] {
				continue
			}
			done[tm.MessageID] = true
			pending = append(pending, tm)
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
			sourceFile := fmt.Sprintf("watch-cc://%s/batch-%d", session.SessionID, batchNum)
			if err := ingestBatch(db, ollama, sourceFile, pending, title); err != nil {
				fmt.Println(ui.RenderPreflightStep("fail", fmt.Sprintf("Ingest error: %v", err)))
				continue
			}

			batchNum++
			fmt.Println()
			fmt.Println(ui.RenderIngest(len(pending), batchNum))
			fmt.Println()
			pending = nil

			// Flush thoughts alongside text batch
			if len(pendingThoughts) > 0 {
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
			extractCCNotes(sessionDir, session.FullPath, agent, *notesDir, extractedPath, extracted)
		}
	}
}
