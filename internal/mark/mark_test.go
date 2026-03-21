package mark

import (
	"fmt"
	"testing"
	"time"

	"github.com/Gsirawan/kiseki-beta/internal/db"
)

func TestNormalizeImportance(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "solution", input: "solution", want: "solution"},
		{name: "key uppercase", input: " KEY ", want: "key"},
		{name: "normal mixed", input: "NoRmAl", want: "normal"},
		{name: "invalid", input: "urgent", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeImportance(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeImportance error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestMarkImportance(t *testing.T) {
	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer dbConn.Close()

	res, err := dbConn.Exec(
		`INSERT INTO chunks (text, source_file, section_title, section_sequence, chunk_sequence, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		"chunk text", "mark.md", "Section", 1, 1, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert chunk: %v", err)
	}
	chunkID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}

	_, err = dbConn.Exec(
		`INSERT INTO messages (id, session_id, role, timestamp, text)
		 VALUES (?, ?, ?, ?, ?)`,
		"msg-1", "session-1", "assistant", time.Now().UTC().UnixMilli(), "done",
	)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	tests := []struct {
		name       string
		targetType string
		id         string
		importance string
		wantUpdate bool
		wantErr    bool
	}{
		{name: "mark chunk", targetType: "chunk", id: fmt.Sprintf("%d", chunkID), importance: "solution", wantUpdate: true},
		{name: "mark message", targetType: "message", id: "msg-1", importance: "key", wantUpdate: true},
		{name: "invalid type", targetType: "stone", id: "1", importance: "solution", wantErr: true},
		{name: "nonexistent chunk", targetType: "chunk", id: "99999", importance: "normal", wantUpdate: false},
		{name: "nonexistent message", targetType: "message", id: "missing", importance: "normal", wantUpdate: false},
		{name: "bad chunk id", targetType: "chunk", id: "abc", importance: "normal", wantErr: true},
		{name: "bad importance", targetType: "message", id: "msg-1", importance: "urgent", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, err := MarkImportance(dbConn, tt.targetType, tt.id, tt.importance)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("markImportance error: %v", err)
			}
			if updated != tt.wantUpdate {
				t.Fatalf("expected updated=%v, got %v", tt.wantUpdate, updated)
			}
		})
	}

	var chunkImportance string
	if err := dbConn.QueryRow(`SELECT importance FROM chunks WHERE id = ?`, chunkID).Scan(&chunkImportance); err != nil {
		t.Fatalf("query chunk importance: %v", err)
	}
	if chunkImportance != "solution" {
		t.Fatalf("expected chunk importance solution, got %q", chunkImportance)
	}

	var messageImportance string
	if err := dbConn.QueryRow(`SELECT importance FROM messages WHERE id = ?`, "msg-1").Scan(&messageImportance); err != nil {
		t.Fatalf("query message importance: %v", err)
	}
	if messageImportance != "key" {
		t.Fatalf("expected message importance key, got %q", messageImportance)
	}
}
