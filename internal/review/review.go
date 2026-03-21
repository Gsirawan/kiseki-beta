package review

import (
	"bufio"
	"database/sql"
	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/stone"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var filePathRegex = regexp.MustCompile(`(?:[A-Za-z]:\\[^\s"']+|/[^\s"']+/[^\s"']+|(?:\./|\.\./)?(?:src|cmd|internal|pkg|api|web|ui)/[^\s"']+)`)

type ReviewMessage struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Role      string `json:"role"`
	Timestamp int64  `json:"timestamp"`
	Text      string `json:"text"`
}

type reviewCandidate struct {
	Message    ReviewMessage `json:"message"`
	Score      int           `json:"score"`
	Importance string        `json:"importance"`
}

type reviewOutput struct {
	Week               string            `json:"week,omitempty"`
	From               string            `json:"from"`
	To                 string            `json:"to"`
	SessionsAnalyzed   int               `json:"sessions_analyzed"`
	MessagesScanned    int               `json:"messages_scanned"`
	SolutionCandidates []reviewCandidate `json:"solution_candidates"`
	KeyCandidates      []reviewCandidate `json:"key_candidates"`
	MarkedSolutions    []string          `json:"marked_solutions,omitempty"`
	MarkedKeys         []string          `json:"marked_keys,omitempty"`
	DryRun             bool              `json:"dry_run"`
}

func ReviewCmd(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	weekFlag := fs.String("week", "", "ISO week to review (YYYY-Wnn)")
	fromFlag := fs.String("from", "", "start date (YYYY-MM-DD)")
	toFlag := fs.String("to", "", "end date (YYYY-MM-DD)")
	autoFlag := fs.Bool("auto", false, "auto-mark all candidates without prompting")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	createStonesFlag := fs.Bool("create-stones", false, "prompt to create stones for marked solutions")
	dryRunFlag := fs.Bool("dry-run", false, "show candidates without marking")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	weekLabel, fromDate, toDate, startTS, endTS, err := ResolveReviewRange(*weekFlag, *fromFlag, *toFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	db, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	messages, err := FetchMessagesInRange(db, startTS, endTS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: query messages: %v\n", err)
		os.Exit(1)
	}

	sessions := GroupBySession(messages)
	candidates := scoreSessions(sessions)
	solutionCandidates, keyCandidates := categorizeCandidates(candidates)

	out := reviewOutput{
		Week:               weekLabel,
		From:               fromDate,
		To:                 toDate,
		SessionsAnalyzed:   len(sessions),
		MessagesScanned:    len(messages),
		SolutionCandidates: solutionCandidates,
		KeyCandidates:      keyCandidates,
		DryRun:             *dryRunFlag,
	}

	if *jsonFlag {
		if *autoFlag && !*dryRunFlag {
			markedSolutions, markedKeys, err := markCandidates(db, solutionCandidates, keyCandidates)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: mark candidates: %v\n", err)
				os.Exit(1)
			}
			out.MarkedSolutions = markedSolutions
			out.MarkedKeys = markedKeys
		}

		payload, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		if *createStonesFlag && !*dryRunFlag && len(out.MarkedSolutions) > 0 {
			maybeCreateStones(db, candidateMap(solutionCandidates), out.MarkedSolutions)
		}
		return
	}

	printReviewHuman(out)

	if *dryRunFlag {
		return
	}

	allCandidates := append([]reviewCandidate{}, solutionCandidates...)
	allCandidates = append(allCandidates, keyCandidates...)
	if len(allCandidates) == 0 {
		return
	}

	selectedSolutions := []reviewCandidate{}
	selectedKeys := []reviewCandidate{}

	if *autoFlag {
		selectedSolutions = append(selectedSolutions, solutionCandidates...)
		selectedKeys = append(selectedKeys, keyCandidates...)
	} else {
		selSolutions, selKeys, cancelled, err := promptReviewSelection(solutionCandidates, keyCandidates)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: read choice: %v\n", err)
			os.Exit(1)
		}
		if cancelled {
			fmt.Println("Cancelled.")
			return
		}
		selectedSolutions = selSolutions
		selectedKeys = selKeys
	}

	markedSolutions, markedKeys, err := markCandidates(db, selectedSolutions, selectedKeys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: mark candidates: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Marked %d solutions and %d keys\n", len(markedSolutions), len(markedKeys))

	if *createStonesFlag && len(markedSolutions) > 0 {
		maybeCreateStones(db, candidateMap(selectedSolutions), markedSolutions)
	}
}

func ResolveReviewRange(weekFlag, fromFlag, toFlag string) (string, string, string, int64, int64, error) {
	weekFlag = strings.TrimSpace(weekFlag)
	fromFlag = strings.TrimSpace(fromFlag)
	toFlag = strings.TrimSpace(toFlag)

	hasWeek := weekFlag != ""
	hasFromOrTo := fromFlag != "" || toFlag != ""

	if hasWeek && hasFromOrTo {
		return "", "", "", 0, 0, fmt.Errorf("use either --week or --from/--to, not both")
	}
	if !hasWeek && !hasFromOrTo {
		return "", "", "", 0, 0, fmt.Errorf("provide --week YYYY-Wnn or --from YYYY-MM-DD --to YYYY-MM-DD")
	}

	if hasWeek {
		start, endInclusive, err := parseISOWeekRange(weekFlag)
		if err != nil {
			return "", "", "", 0, 0, err
		}
		endExclusive := endInclusive.AddDate(0, 0, 1)
		return weekFlag,
			start.Format("2006-01-02"),
			endInclusive.Format("2006-01-02"),
			start.UnixMilli(),
			endExclusive.UnixMilli(),
			nil
	}

	if fromFlag == "" || toFlag == "" {
		return "", "", "", 0, 0, fmt.Errorf("both --from and --to are required together")
	}

	start, err := time.ParseInLocation("2006-01-02", fromFlag, time.UTC)
	if err != nil {
		return "", "", "", 0, 0, fmt.Errorf("invalid --from date: %s", fromFlag)
	}
	toDate, err := time.ParseInLocation("2006-01-02", toFlag, time.UTC)
	if err != nil {
		return "", "", "", 0, 0, fmt.Errorf("invalid --to date: %s", toFlag)
	}
	if toDate.Before(start) {
		return "", "", "", 0, 0, fmt.Errorf("--to must be on or after --from")
	}

	endExclusive := toDate.AddDate(0, 0, 1)
	return "", fromFlag, toFlag, start.UnixMilli(), endExclusive.UnixMilli(), nil
}

func parseISOWeekRange(week string) (time.Time, time.Time, error) {
	parts := strings.Split(week, "-W")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --week format: expected YYYY-Wnn")
	}

	year, err := strconv.Atoi(parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --week year: %s", parts[0])
	}
	weekNum, err := strconv.Atoi(parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --week number: %s", parts[1])
	}
	if weekNum < 1 || weekNum > 53 {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --week number: %d", weekNum)
	}

	start := isoWeekStart(year, weekNum)
	if y, w := start.ISOWeek(); y != year || w != weekNum {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid ISO week: %s", week)
	}

	endInclusive := start.AddDate(0, 0, 6)
	return start, endInclusive, nil
}

func isoWeekStart(year, week int) time.Time {
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	weekday := int(jan4.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	monday := jan4.AddDate(0, 0, -(weekday - 1))
	return monday.AddDate(0, 0, (week-1)*7)
}

func FetchMessagesInRange(db *sql.DB, startTS, endTS int64) ([]ReviewMessage, error) {
	rows, err := db.Query(`
		SELECT id, session_id, role, timestamp, text
		FROM messages
		WHERE timestamp >= ? AND timestamp < ?
		ORDER BY session_id ASC, timestamp ASC
	`, startTS, endTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []ReviewMessage{}
	for rows.Next() {
		var msg ReviewMessage
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Timestamp, &msg.Text); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return messages, nil
}

func GroupBySession(messages []ReviewMessage) map[string][]ReviewMessage {
	sessions := make(map[string][]ReviewMessage)
	for _, msg := range messages {
		sessions[msg.SessionID] = append(sessions[msg.SessionID], msg)
	}

	for sid := range sessions {
		sort.Slice(sessions[sid], func(i, j int) bool {
			if sessions[sid][i].Timestamp == sessions[sid][j].Timestamp {
				return sessions[sid][i].ID < sessions[sid][j].ID
			}
			return sessions[sid][i].Timestamp < sessions[sid][j].Timestamp
		})
	}

	return sessions
}

func scoreSessions(sessions map[string][]ReviewMessage) []reviewCandidate {
	all := []reviewCandidate{}
	for _, msgs := range sessions {
		for i, msg := range msgs {
			score := scoreMessage(msg, msgs, i)
			if score < 2 {
				continue
			}

			importance := "key"
			if score >= 4 {
				importance = "solution"
			}

			all = append(all, reviewCandidate{
				Message:    msg,
				Score:      score,
				Importance: importance,
			})
		}
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Score == all[j].Score {
			if all[i].Message.Timestamp == all[j].Message.Timestamp {
				return all[i].Message.ID < all[j].Message.ID
			}
			return all[i].Message.Timestamp > all[j].Message.Timestamp
		}
		return all[i].Score > all[j].Score
	})

	return all
}

func scoreMessage(msg ReviewMessage, session []ReviewMessage, idx int) int {
	score := 0

	if strings.Contains(msg.Text, "```") {
		score += 2
	}

	solutionWords := []string{"fixed", "worked", "solution", "done", "that's it", "✅"}
	lowerText := strings.ToLower(msg.Text)
	for _, word := range solutionWords {
		if strings.Contains(lowerText, word) {
			score += 1
			break
		}
	}

	if isInLastN(msg, session, 5) {
		score += 2
	}

	avgPreviousLength := averagePreviousLength(session, idx)
	if len(msg.Text) < 500 && avgPreviousLength > 1000 {
		score += 1
	}

	if ContainsFilePath(msg.Text) {
		score += 1
	}

	return score
}

func averagePreviousLength(session []ReviewMessage, idx int) float64 {
	if idx <= 0 {
		return 0
	}

	total := 0
	for i := 0; i < idx; i++ {
		total += len(session[i].Text)
	}

	return float64(total) / float64(idx)
}

func isInLastN(msg ReviewMessage, session []ReviewMessage, n int) bool {
	if n <= 0 || len(session) == 0 {
		return false
	}

	start := len(session) - n
	if start < 0 {
		start = 0
	}

	for i := start; i < len(session); i++ {
		if session[i].ID == msg.ID {
			return true
		}
	}

	return false
}

func ContainsFilePath(text string) bool {
	return filePathRegex.FindStringIndex(text) != nil
}

func categorizeCandidates(all []reviewCandidate) ([]reviewCandidate, []reviewCandidate) {
	solutions := []reviewCandidate{}
	keys := []reviewCandidate{}
	for _, c := range all {
		if c.Score >= 4 {
			solutions = append(solutions, c)
			continue
		}
		if c.Score >= 2 {
			keys = append(keys, c)
		}
	}
	return solutions, keys
}

func printReviewHuman(out reviewOutput) {
	title := out.Week
	if title == "" {
		title = fmt.Sprintf("%s to %s", out.From, out.To)
	}

	fmt.Printf("Weekly Review: %s\n", title)
	fmt.Printf("Sessions analyzed: %d\n", out.SessionsAnalyzed)
	fmt.Printf("Messages scanned: %d\n\n", out.MessagesScanned)

	line := strings.Repeat("═", 55)
	fmt.Println(line)
	fmt.Println("SOLUTION CANDIDATES (score >= 4)")
	fmt.Println(line)
	fmt.Println()

	index := 1
	if len(out.SolutionCandidates) == 0 {
		fmt.Println("(none)")
		fmt.Println()
	} else {
		for _, c := range out.SolutionCandidates {
			fmt.Printf("[%d] %s (score: %d, session: %s)\n", index, c.Message.ID, c.Score, c.Message.SessionID)
			fmt.Printf("    %q\n\n", truncate(c.Message.Text, 180))
			index++
		}
	}

	fmt.Println(line)
	fmt.Println("KEY CANDIDATES (score 2-3)")
	fmt.Println(line)
	fmt.Println()

	if len(out.KeyCandidates) == 0 {
		fmt.Println("(none)")
		fmt.Println()
	} else {
		for _, c := range out.KeyCandidates {
			fmt.Printf("[%d] %s (score: %d, session: %s)\n", index, c.Message.ID, c.Score, c.Message.SessionID)
			fmt.Printf("    %q\n\n", truncate(c.Message.Text, 180))
			index++
		}
	}

	fmt.Println(line)
	fmt.Println()
	fmt.Printf("Found: %d solution candidates, %d key candidates\n", len(out.SolutionCandidates), len(out.KeyCandidates))
}

func promptReviewSelection(solutions, keys []reviewCandidate) ([]reviewCandidate, []reviewCandidate, bool, error) {
	all := append([]reviewCandidate{}, solutions...)
	all = append(all, keys...)

	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  [a]     Mark all candidates (solutions + keys)")
	fmt.Println("  [s]     Mark solutions only")
	fmt.Println("  [n]     Mark none (review only)")
	fmt.Println("  [1,2]   Mark specific items by number")
	fmt.Print("\nChoice: ")

	reader := bufio.NewReader(os.Stdin)
	choice, err := reader.ReadString('\n')
	if err != nil {
		return nil, nil, false, err
	}

	choice = strings.TrimSpace(strings.ToLower(choice))
	switch choice {
	case "a":
		return solutions, keys, false, nil
	case "s":
		return solutions, []reviewCandidate{}, false, nil
	case "n", "":
		return []reviewCandidate{}, []reviewCandidate{}, false, nil
	}

	selectionMap := map[int]reviewCandidate{}
	for i, c := range all {
		selectionMap[i+1] = c
	}

	parts := strings.Split(choice, ",")
	selSolutions := []reviewCandidate{}
	selKeys := []reviewCandidate{}
	seen := map[string]bool{}

	for _, part := range parts {
		numText := strings.TrimSpace(part)
		if numText == "" {
			continue
		}
		n, err := strconv.Atoi(numText)
		if err != nil {
			return nil, nil, false, fmt.Errorf("invalid selection %q", numText)
		}
		candidate, ok := selectionMap[n]
		if !ok {
			return nil, nil, false, fmt.Errorf("selection out of range: %d", n)
		}
		if seen[candidate.Message.ID] {
			continue
		}
		seen[candidate.Message.ID] = true
		if candidate.Importance == "solution" {
			selSolutions = append(selSolutions, candidate)
		} else {
			selKeys = append(selKeys, candidate)
		}
	}

	return selSolutions, selKeys, false, nil
}

func markCandidates(db *sql.DB, solutions, keys []reviewCandidate) ([]string, []string, error) {
	markedSolutions := make([]string, 0, len(solutions))
	for _, c := range solutions {
		if _, err := db.Exec(`UPDATE messages SET importance = 'solution' WHERE id = ?`, c.Message.ID); err != nil {
			return nil, nil, err
		}
		markedSolutions = append(markedSolutions, c.Message.ID)
	}

	markedKeys := make([]string, 0, len(keys))
	for _, c := range keys {
		if _, err := db.Exec(`UPDATE messages SET importance = 'key' WHERE id = ?`, c.Message.ID); err != nil {
			return nil, nil, err
		}
		markedKeys = append(markedKeys, c.Message.ID)
	}

	return markedSolutions, markedKeys, nil
}

func candidateMap(candidates []reviewCandidate) map[string]reviewCandidate {
	out := make(map[string]reviewCandidate, len(candidates))
	for _, c := range candidates {
		out[c.Message.ID] = c
	}
	return out
}

func maybeCreateStones(db *sql.DB, byID map[string]reviewCandidate, solutionIDs []string) {
	reader := bufio.NewReader(os.Stdin)
	skipAll := false

	for _, msgID := range solutionIDs {
		candidate, ok := byID[msgID]
		if !ok {
			continue
		}

		if skipAll {
			return
		}

		fmt.Printf("Create stone for %s? [y/n/skip all]: ", msgID)
		answer, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: read input: %v\n", err)
			return
		}
		answer = strings.TrimSpace(strings.ToLower(answer))

		if answer == "skip all" {
			skipAll = true
			return
		}
		if answer != "y" && answer != "yes" {
			continue
		}

		title := readPrompt(reader, "Title: ")
		if strings.TrimSpace(title) == "" {
			fmt.Println("Skipped: title is required")
			continue
		}

		category := readPrompt(reader, "Category [fix/decision/config/learning]: ")
		tags := readPrompt(reader, "Tags (comma-separated): ")

		stone, err := stone.CreateStone(db, stone.StoneInput{
			Title:         title,
			Category:      category,
			Solution:      candidate.Message.Text,
			Tags:          tags,
			ChunkIDs:      candidate.Message.ID,
			SourceSession: candidate.Message.SessionID,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: create stone: %v\n", err)
			continue
		}

		fmt.Printf("Stone created: %s\n", stone.ID)
	}
}

func readPrompt(reader *bufio.Reader, label string) string {
	fmt.Print(label)
	value, err := reader.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// truncate truncates a string to max length
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
