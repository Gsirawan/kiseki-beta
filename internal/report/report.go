package report

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/review"
)

var reportWorkPatterns = []string{
	"implemented", "added", "created", "built",
	"fixed", "resolved", "solved", "debugged",
	"updated", "modified", "changed", "refactored",
	"deployed", "released", "shipped", "pushed",
	"completed", "finished", "done",
}

var reportDecisionPatterns = []string{
	"decided", "chose", "selected", "picked",
	"agreed", "confirmed", "approved",
	"went with", "settled on",
}

var reportCommitLikeRegex = regexp.MustCompile(`\b(feat|fix|chore|docs|refactor|test|perf|build|ci)(\([^)]+\))?:`)
var reportInlineCodeRegex = regexp.MustCompile("`[^`]+`")
var reportCodeFenceRegex = regexp.MustCompile("(?s)```.*?```")
var reportTokenRegex = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_-]{2,}`)
var reportPathRegex = regexp.MustCompile(`(?:[A-Za-z]:\\[^\s"']+|(?:\./|\.\./)?(?:[a-zA-Z0-9_.-]+/)+[a-zA-Z0-9_.-]+(?:\.[a-zA-Z0-9]+)?)`)

var reportKeywordStoplist = map[string]bool{
	"implemented": true,
	"added":       true,
	"created":     true,
	"built":       true,
	"fixed":       true,
	"resolved":    true,
	"solved":      true,
	"debugged":    true,
	"updated":     true,
	"modified":    true,
	"changed":     true,
	"refactored":  true,
	"deployed":    true,
	"released":    true,
	"shipped":     true,
	"pushed":      true,
	"completed":   true,
	"finished":    true,
	"done":        true,
	"decided":     true,
	"chose":       true,
	"selected":    true,
	"picked":      true,
	"agreed":      true,
	"confirmed":   true,
	"approved":    true,
	"with":        true,
	"went":        true,
	"settled":     true,
	"from":        true,
	"into":        true,
	"this":        true,
	"that":        true,
	"then":        true,
	"just":        true,
	"have":        true,
	"been":        true,
	"were":        true,
	"will":        true,
	"would":       true,
	"could":       true,
	"should":      true,
	"about":       true,
	"after":       true,
	"before":      true,
	"using":       true,
	"works":       true,
	"working":     true,
	"issue":       true,
	"message":     true,
	"session":     true,
}

type reportWorkItem struct {
	MessageID    string   `json:"message_id"`
	SessionID    string   `json:"session_id"`
	Timestamp    int64    `json:"timestamp"`
	Summary      string   `json:"summary"`
	Excerpt      string   `json:"excerpt,omitempty"`
	FilePaths    []string `json:"file_paths,omitempty"`
	Keywords     []string `json:"keywords,omitempty"`
	HasCodeBlock bool     `json:"has_code_block,omitempty"`
}

type reportDecisionItem struct {
	MessageID string `json:"message_id"`
	SessionID string `json:"session_id"`
	Timestamp int64  `json:"timestamp"`
	Summary   string `json:"summary"`
	Excerpt   string `json:"excerpt,omitempty"`
}

type reportTopic struct {
	Topic    string           `json:"topic"`
	Sessions []string         `json:"sessions"`
	Items    []reportWorkItem `json:"items"`
}

type reportResult struct {
	Week             string               `json:"week,omitempty"`
	From             string               `json:"from"`
	To               string               `json:"to"`
	Format           string               `json:"format"`
	SessionsAnalyzed int                  `json:"sessions_analyzed"`
	MessagesScanned  int                  `json:"messages_scanned"`
	Accomplishments  []reportTopic        `json:"accomplishments,omitempty"`
	Decisions        []reportDecisionItem `json:"decisions,omitempty"`
	FilesChanged     []string             `json:"files_changed,omitempty"`
	SummaryOnly      bool                 `json:"summary_only"`
	Verbose          bool                 `json:"verbose"`
}

// GenerateReport produces a formatted activity report from the database.
// It accepts a *sql.DB and parameters directly, returning the rendered report string.
// This is the core logic used by both the CLI (ReportCmd) and the MCP tool.
func GenerateReport(database *sql.DB, week, fromDate, toDate, format string, verbose, summaryOnly bool) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" && format != "text" && format != "json" {
		return "", fmt.Errorf("format must be one of: markdown, text, json")
	}

	weekLabel, resolvedFrom, resolvedTo, startTS, endTS, err := review.ResolveReviewRange(week, fromDate, toDate)
	if err != nil {
		return "", fmt.Errorf("resolve range: %w", err)
	}

	messages, err := review.FetchMessagesInRange(database, startTS, endTS)
	if err != nil {
		return "", fmt.Errorf("query messages: %w", err)
	}

	workItems := extractWorkItems(messages)
	decisions := extractDecisions(messages)
	accomplishments := clusterByTopic(workItems)

	out := reportResult{
		Week:             weekLabel,
		From:             resolvedFrom,
		To:               resolvedTo,
		Format:           format,
		SessionsAnalyzed: len(review.GroupBySession(messages)),
		MessagesScanned:  len(messages),
		Accomplishments:  accomplishments,
		Decisions:        decisions,
		FilesChanged:     collectFiles(workItems),
		SummaryOnly:      summaryOnly,
		Verbose:          verbose,
	}

	var rendered string
	switch format {
	case "markdown":
		rendered = formatMarkdown(out)
	case "text":
		rendered = formatText(out)
	case "json":
		rendered, err = formatJSON(out)
		if err != nil {
			return "", fmt.Errorf("format json: %w", err)
		}
	}

	return rendered, nil
}

func ReportCmd(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	weekFlag := fs.String("week", "", "ISO week to report on (YYYY-Wnn)")
	fromFlag := fs.String("from", "", "start date (YYYY-MM-DD)")
	toFlag := fs.String("to", "", "end date (YYYY-MM-DD)")
	formatFlag := fs.String("format", "markdown", "output format: markdown, text, json")
	outputFlag := fs.String("output", "", "write report to file instead of stdout")
	verboseFlag := fs.Bool("verbose", false, "include detailed file paths and excerpts")
	summaryOnlyFlag := fs.Bool("summary-only", false, "only print summary section")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	rendered, err := GenerateReport(database, *weekFlag, *fromFlag, *toFlag, *formatFlag, *verboseFlag, *summaryOnlyFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(*outputFlag) != "" {
		if err := os.WriteFile(*outputFlag, []byte(rendered), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "Error: write output file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Report written to %s\n", *outputFlag)
		return
	}

	fmt.Println(rendered)
}

func extractWorkItems(messages []review.ReviewMessage) []reportWorkItem {
	items := make([]reportWorkItem, 0)
	for _, msg := range messages {
		if !isWorkMessage(msg.Text) {
			continue
		}

		summary := summarizeReportLine(msg.Text)
		if summary == "" {
			continue
		}

		paths := uniqueStrings(extractFilePaths(msg.Text))
		keywords := extractKeywords(msg.Text)

		items = append(items, reportWorkItem{
			MessageID:    msg.ID,
			SessionID:    msg.SessionID,
			Timestamp:    msg.Timestamp,
			Summary:      summary,
			Excerpt:      truncateForReport(cleanReportText(msg.Text), 220),
			FilePaths:    paths,
			Keywords:     keywords,
			HasCodeBlock: strings.Contains(msg.Text, "```"),
		})
	}

	return dedupeWorkItems(items)
}

func extractDecisions(messages []review.ReviewMessage) []reportDecisionItem {
	items := make([]reportDecisionItem, 0)
	for _, msg := range messages {
		lower := strings.ToLower(msg.Text)
		matched := false
		for _, pattern := range reportDecisionPatterns {
			if strings.Contains(lower, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		summary := summarizeReportLine(msg.Text)
		if summary == "" {
			continue
		}

		items = append(items, reportDecisionItem{
			MessageID: msg.ID,
			SessionID: msg.SessionID,
			Timestamp: msg.Timestamp,
			Summary:   summary,
			Excerpt:   truncateForReport(cleanReportText(msg.Text), 220),
		})
	}

	items = dedupeDecisionItems(items)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Timestamp == items[j].Timestamp {
			return items[i].MessageID < items[j].MessageID
		}
		return items[i].Timestamp < items[j].Timestamp
	})
	return items
}

func clusterByTopic(items []reportWorkItem) []reportTopic {
	if len(items) == 0 {
		return []reportTopic{}
	}

	bySession := make(map[string][]reportWorkItem)
	for _, item := range items {
		bySession[item.SessionID] = append(bySession[item.SessionID], item)
	}

	clustersByTopic := make(map[string]*reportTopic)
	for sessionID, sessionItems := range bySession {
		topic := deriveTopic(sessionItems, sessionID)
		cluster, ok := clustersByTopic[topic]
		if !ok {
			cluster = &reportTopic{Topic: topic, Sessions: []string{}, Items: []reportWorkItem{}}
			clustersByTopic[topic] = cluster
		}
		cluster.Sessions = append(cluster.Sessions, sessionID)
		cluster.Items = append(cluster.Items, sessionItems...)
	}

	out := make([]reportTopic, 0, len(clustersByTopic))
	for _, cluster := range clustersByTopic {
		cluster.Sessions = uniqueStrings(cluster.Sessions)
		cluster.Items = dedupeWorkItems(cluster.Items)
		sort.Slice(cluster.Items, func(i, j int) bool {
			if cluster.Items[i].Timestamp == cluster.Items[j].Timestamp {
				return cluster.Items[i].MessageID < cluster.Items[j].MessageID
			}
			return cluster.Items[i].Timestamp < cluster.Items[j].Timestamp
		})
		out = append(out, *cluster)
	}

	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Items) == len(out[j].Items) {
			return out[i].Topic < out[j].Topic
		}
		return len(out[i].Items) > len(out[j].Items)
	})

	return out
}

func formatMarkdown(report reportResult) string {
	b := strings.Builder{}

	b.WriteString(fmt.Sprintf("# Weekly Report: %s\n\n", reportRangeLabel(report.Week, report.From, report.To)))
	b.WriteString("## Summary\n")
	b.WriteString(fmt.Sprintf("- %d sessions analyzed\n", report.SessionsAnalyzed))
	b.WriteString(fmt.Sprintf("- %d major accomplishments\n", len(report.Accomplishments)))
	b.WriteString(fmt.Sprintf("- %d decisions made\n", len(report.Decisions)))

	if report.SummaryOnly {
		return b.String()
	}

	b.WriteString("\n## Accomplishments\n\n")
	if len(report.Accomplishments) == 0 {
		b.WriteString("- No work items detected for this period.\n")
	} else {
		for _, topic := range report.Accomplishments {
			b.WriteString(fmt.Sprintf("### %s\n", topic.Topic))
			for _, item := range topic.Items {
				line := "- " + item.Summary
				if report.Verbose {
					line += fmt.Sprintf(" (session: %s", shortSessionID(item.SessionID))
					if len(item.FilePaths) > 0 {
						line += ", files: " + strings.Join(item.FilePaths, ", ")
					}
					line += ")"
				}
				b.WriteString(line + "\n")
				if report.Verbose && item.Excerpt != "" {
					b.WriteString(fmt.Sprintf("  - Excerpt: %s\n", item.Excerpt))
				}
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Decisions Made\n")
	if len(report.Decisions) == 0 {
		b.WriteString("- No explicit decisions detected for this period.\n")
	} else {
		for _, decision := range report.Decisions {
			line := "- " + decision.Summary
			if report.Verbose {
				line += fmt.Sprintf(" (session: %s)", shortSessionID(decision.SessionID))
			}
			b.WriteString(line + "\n")
			if report.Verbose && decision.Excerpt != "" {
				b.WriteString(fmt.Sprintf("  - Excerpt: %s\n", decision.Excerpt))
			}
		}
	}

	b.WriteString("\n## Technical Details\n")
	b.WriteString(fmt.Sprintf("Files changed: %s\n", joinOrNone(report.FilesChanged)))
	b.WriteString(fmt.Sprintf("Sessions: %d\n", report.SessionsAnalyzed))
	b.WriteString(fmt.Sprintf("Messages: %d\n", report.MessagesScanned))

	return b.String()
}

func formatText(report reportResult) string {
	b := strings.Builder{}

	b.WriteString(fmt.Sprintf("WEEKLY REPORT: %s\n", strings.ToUpper(reportRangeLabel(report.Week, report.From, report.To))))
	b.WriteString(strings.Repeat("=", 42) + "\n\n")
	b.WriteString("SUMMARY\n")
	b.WriteString("-------\n")
	b.WriteString(fmt.Sprintf("Sessions: %d | Accomplishments: %d | Decisions: %d\n", report.SessionsAnalyzed, len(report.Accomplishments), len(report.Decisions)))

	if report.SummaryOnly {
		return b.String()
	}

	b.WriteString("\nACCOMPLISHMENTS\n")
	b.WriteString("---------------\n")
	if len(report.Accomplishments) == 0 {
		b.WriteString("* No work items detected\n")
	} else {
		for _, topic := range report.Accomplishments {
			summaries := make([]string, 0, len(topic.Items))
			for _, item := range topic.Items {
				entry := item.Summary
				if report.Verbose && len(item.FilePaths) > 0 {
					entry += " [" + strings.Join(item.FilePaths, ", ") + "]"
				}
				summaries = append(summaries, entry)
			}
			b.WriteString(fmt.Sprintf("* %s: %s\n", topic.Topic, strings.Join(summaries, "; ")))
		}
	}

	b.WriteString("\nDECISIONS\n")
	b.WriteString("---------\n")
	if len(report.Decisions) == 0 {
		b.WriteString("* No explicit decisions detected\n")
	} else {
		for _, decision := range report.Decisions {
			line := "* " + decision.Summary
			if report.Verbose {
				line += fmt.Sprintf(" (session %s)", shortSessionID(decision.SessionID))
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\nTECHNICAL DETAILS\n")
	b.WriteString("-----------------\n")
	b.WriteString(fmt.Sprintf("Files changed: %s\n", joinOrNone(report.FilesChanged)))
	b.WriteString(fmt.Sprintf("Sessions: %d\n", report.SessionsAnalyzed))
	b.WriteString(fmt.Sprintf("Messages: %d\n", report.MessagesScanned))

	return b.String()
}

func formatJSON(report reportResult) (string, error) {
	target := report
	if report.SummaryOnly {
		target.Accomplishments = nil
		target.Decisions = nil
		target.FilesChanged = nil
	}

	payload, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func isWorkMessage(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}

	lower := strings.ToLower(text)
	hasCode := strings.Contains(text, "```") || reportInlineCodeRegex.MatchString(text)
	hasCommitLike := reportCommitLikeRegex.MatchString(lower)
	hasPath := review.ContainsFilePath(text) || len(extractFilePaths(text)) > 0
	hasActionWord := false
	for _, pattern := range reportWorkPatterns {
		if strings.Contains(lower, pattern) {
			hasActionWord = true
			break
		}
	}

	return hasCode || hasCommitLike || hasPath || hasActionWord
}

func cleanReportText(text string) string {
	text = reportCodeFenceRegex.ReplaceAllString(text, "[code]")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

func summarizeReportLine(text string) string {
	clean := cleanReportText(text)
	if clean == "" {
		return ""
	}

	sentences := splitSentences(clean)
	for _, sentence := range sentences {
		trimmed := strings.TrimSpace(sentence)
		if len(trimmed) < 20 {
			continue
		}
		return capitalizeFirst(truncateForReport(trimmed, 140))
	}

	return capitalizeFirst(truncateForReport(clean, 140))
}

func splitSentences(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func extractFilePaths(text string) []string {
	matches := reportPathRegex.FindAllString(text, -1)
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		clean := strings.Trim(match, ",.;:()[]{}<>\"'")
		if clean == "" || !strings.Contains(clean, "/") {
			continue
		}
		paths = append(paths, clean)
	}
	return uniqueStrings(paths)
}

func extractKeywords(text string) []string {
	tokens := reportTokenRegex.FindAllString(strings.ToLower(text), -1)
	freq := map[string]int{}
	for _, token := range tokens {
		if reportKeywordStoplist[token] {
			continue
		}
		if len(token) < 4 {
			continue
		}
		freq[token]++
	}

	type kv struct {
		Key string
		N   int
	}
	items := make([]kv, 0, len(freq))
	for k, v := range freq {
		items = append(items, kv{Key: k, N: v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].N == items[j].N {
			return items[i].Key < items[j].Key
		}
		return items[i].N > items[j].N
	})

	keywords := []string{}
	for _, item := range items {
		keywords = append(keywords, item.Key)
		if len(keywords) == 5 {
			break
		}
	}
	return keywords
}

func deriveTopic(items []reportWorkItem, sessionID string) string {
	pathRoots := map[string]int{}
	keywords := map[string]int{}

	for _, item := range items {
		for _, path := range item.FilePaths {
			root := firstPathSegment(path)
			if root != "" {
				pathRoots[root]++
			}
		}
		for _, kw := range item.Keywords {
			keywords[kw]++
		}
	}

	if root, n := topKey(pathRoots); n > 0 {
		return fmt.Sprintf("%s Workstream", strings.ToUpper(root))
	}
	if kw, n := topKey(keywords); n > 0 {
		return fmt.Sprintf("%s Updates", capitalizeFirst(kw))
	}

	return fmt.Sprintf("Session %s", shortSessionID(sessionID))
}

func firstPathSegment(path string) string {
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "../")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	seg := strings.TrimSpace(parts[0])
	if seg == "" {
		return ""
	}
	if strings.Contains(seg, ".") && len(parts) > 1 {
		return strings.TrimSpace(parts[1])
	}
	return seg
}

func topKey(freq map[string]int) (string, int) {
	bestKey := ""
	bestN := 0
	for k, n := range freq {
		if n > bestN || (n == bestN && (bestKey == "" || k < bestKey)) {
			bestKey = k
			bestN = n
		}
	}
	return bestKey, bestN
}

func dedupeWorkItems(items []reportWorkItem) []reportWorkItem {
	seen := map[string]bool{}
	out := make([]reportWorkItem, 0, len(items))
	for _, item := range items {
		key := item.MessageID + "::" + strings.ToLower(item.Summary)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func dedupeDecisionItems(items []reportDecisionItem) []reportDecisionItem {
	seen := map[string]bool{}
	out := make([]reportDecisionItem, 0, len(items))
	for _, item := range items {
		key := strings.ToLower(item.Summary)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
	}
	return out
}

func collectFiles(items []reportWorkItem) []string {
	all := []string{}
	for _, item := range items {
		all = append(all, item.FilePaths...)
	}
	files := uniqueStrings(all)
	sort.Strings(files)
	if len(files) > 20 {
		return files[:20]
	}
	return files
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		v := strings.TrimSpace(value)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func reportRangeLabel(week, from, to string) string {
	fromDate, errFrom := time.Parse("2006-01-02", from)
	toDate, errTo := time.Parse("2006-01-02", to)
	if errFrom != nil || errTo != nil {
		if week != "" {
			return week
		}
		return fmt.Sprintf("%s to %s", from, to)
	}

	monthLabel := fromDate.Format("January")
	if fromDate.Month() != toDate.Month() {
		monthLabel = fromDate.Format("January") + "-" + toDate.Format("January")
	}

	base := fmt.Sprintf("%s %d-%d, %d", monthLabel, fromDate.Day(), toDate.Day(), fromDate.Year())
	if week != "" {
		base = fmt.Sprintf("%s (%s)", base, week)
	}
	return base
}

func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func shortSessionID(sessionID string) string {
	if len(sessionID) <= 10 {
		return sessionID
	}
	return sessionID[:10]
}

func truncateForReport(text string, max int) string {
	if len(text) <= max {
		return text
	}
	if max <= 3 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

func capitalizeFirst(text string) string {
	if text == "" {
		return ""
	}
	if len(text) == 1 {
		return strings.ToUpper(text)
	}
	return strings.ToUpper(text[:1]) + text[1:]
}
