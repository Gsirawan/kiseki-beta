package review

import (
	"strings"
	"testing"
)

func TestScoreMessage(t *testing.T) {
	long := strings.Repeat("x", 1500)

	tests := []struct {
		name    string
		msg     ReviewMessage
		session []ReviewMessage
		idx     int
		wantMin int
	}{
		{
			name:    "code block",
			msg:     ReviewMessage{ID: "m1", Text: "Here is the fix:\n```go\nfmt.Println(1)\n```"},
			session: []ReviewMessage{{ID: "m1", Text: "Here is the fix:\n```go\nfmt.Println(1)\n```"}},
			idx:     0,
			wantMin: 2,
		},
		{
			name:    "solution words",
			msg:     ReviewMessage{ID: "m2", Text: "Fixed the auth issue."},
			session: []ReviewMessage{{ID: "m2", Text: "Fixed the auth issue."}},
			idx:     0,
			wantMin: 1,
		},
		{
			name: "end of session",
			msg:  ReviewMessage{ID: "m6", Text: "final message"},
			session: []ReviewMessage{
				{ID: "m1", Text: "a"},
				{ID: "m2", Text: "b"},
				{ID: "m3", Text: "c"},
				{ID: "m4", Text: "d"},
				{ID: "m5", Text: "e"},
				{ID: "m6", Text: "final message"},
			},
			idx:     5,
			wantMin: 2,
		},
		{
			name: "compression signal",
			msg:  ReviewMessage{ID: "m7", Text: "short summary"},
			session: []ReviewMessage{
				{ID: "m1", Text: long},
				{ID: "m2", Text: long},
				{ID: "m7", Text: "short summary"},
			},
			idx:     2,
			wantMin: 1,
		},
		{
			name:    "contains file path",
			msg:     ReviewMessage{ID: "m8", Text: "updated src/service/auth.go with retries"},
			session: []ReviewMessage{{ID: "m8", Text: "updated src/service/auth.go with retries"}},
			idx:     0,
			wantMin: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreMessage(tt.msg, tt.session, tt.idx)
			if got < tt.wantMin {
				t.Fatalf("expected score >= %d, got %d", tt.wantMin, got)
			}
		})
	}
}

func TestScoreThresholds(t *testing.T) {
	all := []reviewCandidate{
		{Message: ReviewMessage{ID: "s"}, Score: 4},
		{Message: ReviewMessage{ID: "k"}, Score: 3},
		{Message: ReviewMessage{ID: "n"}, Score: 1},
	}

	solutions, keys := categorizeCandidates(all)
	if len(solutions) != 1 || solutions[0].Message.ID != "s" {
		t.Fatalf("expected score>=4 to be solution, got %+v", solutions)
	}
	if len(keys) != 1 || keys[0].Message.ID != "k" {
		t.Fatalf("expected score 2-3 to be key, got %+v", keys)
	}
}

func TestContainsFilePath(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "absolute unix", text: "see /home/user/projects/myapp/review.go", want: true},
		{name: "relative src", text: "changed src/app/main.go and tests", want: true},
		{name: "windows", text: `updated C:\repo\app\main.go yesterday`, want: true},
		{name: "no path", text: "just discussed design", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainsFilePath(tt.text)
			if got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestIsInLastN(t *testing.T) {
	session := []ReviewMessage{{ID: "m1"}, {ID: "m2"}, {ID: "m3"}, {ID: "m4"}, {ID: "m5"}}

	if !isInLastN(ReviewMessage{ID: "m5"}, session, 2) {
		t.Fatalf("expected m5 in last 2")
	}
	if isInLastN(ReviewMessage{ID: "m1"}, session, 2) {
		t.Fatalf("expected m1 not in last 2")
	}
	if isInLastN(ReviewMessage{ID: "m1"}, session, 0) {
		t.Fatalf("expected false when n=0")
	}
}

func TestGroupBySession(t *testing.T) {
	messages := []ReviewMessage{
		{ID: "b", SessionID: "s1", Timestamp: 20, Text: "later"},
		{ID: "a", SessionID: "s1", Timestamp: 20, Text: "same ts lower id"},
		{ID: "c", SessionID: "s1", Timestamp: 10, Text: "earlier"},
		{ID: "z", SessionID: "s2", Timestamp: 5, Text: "other session"},
	}

	grouped := GroupBySession(messages)
	if len(grouped) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(grouped))
	}

	s1 := grouped["s1"]
	if len(s1) != 3 {
		t.Fatalf("expected 3 messages in s1, got %d", len(s1))
	}
	if s1[0].ID != "c" || s1[1].ID != "a" || s1[2].ID != "b" {
		t.Fatalf("unexpected sort order in s1: %s, %s, %s", s1[0].ID, s1[1].ID, s1[2].ID)
	}
}
