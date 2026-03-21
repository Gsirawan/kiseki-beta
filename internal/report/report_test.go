package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Gsirawan/kiseki-beta/internal/review"
)

func TestExtractWorkItems(t *testing.T) {
	messages := []review.ReviewMessage{
		{ID: "m1", SessionID: "s1", Timestamp: 1, Text: "Implemented auth retries in src/auth/service.go"},
		{ID: "m2", SessionID: "s1", Timestamp: 2, Text: "Here is the patch:\n```go\nfunc x(){}\n```"},
		{ID: "m3", SessionID: "s1", Timestamp: 3, Text: "random chat with no action"},
	}

	items := extractWorkItems(messages)
	if len(items) != 2 {
		t.Fatalf("expected 2 work items, got %d", len(items))
	}

	if items[0].MessageID != "m1" && items[1].MessageID != "m1" {
		t.Fatalf("expected m1 to be included")
	}

	hasCodeItem := false
	for _, item := range items {
		if item.MessageID == "m2" {
			hasCodeItem = true
			if !item.HasCodeBlock {
				t.Fatalf("expected code block marker for m2")
			}
		}
	}
	if !hasCodeItem {
		t.Fatalf("expected m2 to be included")
	}
}

func TestExtractDecisions(t *testing.T) {
	messages := []review.ReviewMessage{
		{ID: "d1", SessionID: "s1", Timestamp: 1, Text: "We decided to use sqlite for this tool."},
		{ID: "d2", SessionID: "s1", Timestamp: 2, Text: "Team agreed to keep markdown output."},
		{ID: "d3", SessionID: "s1", Timestamp: 3, Text: "just a status update"},
	}

	items := extractDecisions(messages)
	if len(items) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(items))
	}
	if items[0].MessageID != "d1" || items[1].MessageID != "d2" {
		t.Fatalf("unexpected decision order/items: %+v", items)
	}
}

func TestClusterByTopic(t *testing.T) {
	items := []reportWorkItem{
		{MessageID: "m1", SessionID: "s1", Timestamp: 1, Summary: "Implemented API handler", FilePaths: []string{"src/api/handler.go"}},
		{MessageID: "m2", SessionID: "s2", Timestamp: 2, Summary: "Fixed API validation", FilePaths: []string{"src/api/validate.go"}},
		{MessageID: "m3", SessionID: "s3", Timestamp: 3, Summary: "Updated docs", FilePaths: []string{"docs/README.md"}},
	}

	clusters := clusterByTopic(items)
	if len(clusters) < 2 {
		t.Fatalf("expected at least 2 clusters, got %d", len(clusters))
	}

	foundSRC := false
	for _, cluster := range clusters {
		if cluster.Topic == "SRC Workstream" {
			foundSRC = true
			if len(cluster.Items) != 2 {
				t.Fatalf("expected SRC cluster to contain 2 items, got %d", len(cluster.Items))
			}
		}
	}
	if !foundSRC {
		t.Fatalf("expected SRC Workstream cluster, got %+v", clusters)
	}
}

func TestFormatMarkdown(t *testing.T) {
	report := reportResult{
		Week:             "2026-W08",
		From:             "2026-02-16",
		To:               "2026-02-22",
		SessionsAnalyzed: 3,
		MessagesScanned:  12,
		Accomplishments: []reportTopic{{
			Topic: "SRC Workstream",
			Items: []reportWorkItem{{Summary: "Implemented auth retries", SessionID: "session-1234567890"}},
		}},
		Decisions:    []reportDecisionItem{{Summary: "Decided to keep sqlite", SessionID: "session-1234567890"}},
		FilesChanged: []string{"src/auth/service.go"},
		Verbose:      true,
	}

	out := formatMarkdown(report)
	required := []string{
		"# Weekly Report:",
		"## Summary",
		"## Accomplishments",
		"## Decisions Made",
		"## Technical Details",
	}
	for _, part := range required {
		if !strings.Contains(out, part) {
			t.Fatalf("markdown missing %q\n%s", part, out)
		}
	}
}

func TestFormatText(t *testing.T) {
	report := reportResult{
		From:             "2026-02-16",
		To:               "2026-02-22",
		SessionsAnalyzed: 2,
		MessagesScanned:  5,
		Accomplishments: []reportTopic{{
			Topic: "Docs Updates",
			Items: []reportWorkItem{{Summary: "Updated docs"}},
		}},
		Decisions:    []reportDecisionItem{{Summary: "Agreed on naming"}},
		FilesChanged: []string{"docs/README.md"},
	}

	out := formatText(report)
	required := []string{
		"WEEKLY REPORT:",
		"SUMMARY",
		"ACCOMPLISHMENTS",
		"DECISIONS",
		"TECHNICAL DETAILS",
	}
	for _, part := range required {
		if !strings.Contains(out, part) {
			t.Fatalf("text output missing %q\n%s", part, out)
		}
	}
}

func TestFormatJSON(t *testing.T) {
	report := reportResult{
		Week:             "2026-W08",
		From:             "2026-02-16",
		To:               "2026-02-22",
		Format:           "json",
		SessionsAnalyzed: 1,
		MessagesScanned:  2,
		Accomplishments: []reportTopic{{
			Topic: "SRC Workstream",
			Items: []reportWorkItem{{Summary: "Implemented feature"}},
		}},
		Decisions:    []reportDecisionItem{{Summary: "Selected sqlite"}},
		FilesChanged: []string{"src/main.go"},
	}

	raw, err := formatJSON(report)
	if err != nil {
		t.Fatalf("formatJSON error: %v", err)
	}

	var parsed reportResult
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}

	if parsed.Week != report.Week || parsed.SessionsAnalyzed != report.SessionsAnalyzed {
		t.Fatalf("parsed json mismatch: %+v", parsed)
	}
}
