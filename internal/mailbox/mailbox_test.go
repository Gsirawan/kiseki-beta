package mailbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestMailbox creates a Mailbox backed by a temp directory.
func newTestMailbox(t *testing.T, identity string, peers []string) *Mailbox {
	t.Helper()
	dir := t.TempDir()
	return NewMailbox(identity, peers, dir)
}

// TestNewMailbox verifies that NewMailbox populates all fields correctly.
func TestNewMailbox(t *testing.T) {
	dir := t.TempDir()
	mb := NewMailbox("agent-1", []string{"agent-2"}, dir)

	if mb.Identity != "agent-1" {
		t.Errorf("Identity: got %q, want %q", mb.Identity, "agent-1")
	}
	if len(mb.Peers) != 1 || mb.Peers[0] != "agent-2" {
		t.Errorf("Peers: got %v, want [agent-2]", mb.Peers)
	}
	if mb.Dir != dir {
		t.Errorf("Dir: got %q, want %q", mb.Dir, dir)
	}
}

// TestSendCreatesFile verifies that Send writes a JSON file in the correct directory.
func TestSendCreatesFile(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2"})

	msg, err := mb.Send("agent-2", "hello from agent-1")
	if err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	// Check returned message fields.
	if msg.From != "agent-1" {
		t.Errorf("From: got %q, want %q", msg.From, "agent-1")
	}
	if msg.To != "agent-2" {
		t.Errorf("To: got %q, want %q", msg.To, "agent-2")
	}
	if msg.Message != "hello from agent-1" {
		t.Errorf("Message: got %q, want %q", msg.Message, "hello from agent-1")
	}
	if msg.Read {
		t.Error("Read: expected false for a newly sent message")
	}
	if msg.ID == "" {
		t.Error("ID: expected non-empty UUID")
	}

	// Verify the file exists in the expected outbox directory.
	outbox := filepath.Join(mb.Dir, "agent-1_to_agent-2")
	entries, err := os.ReadDir(outbox)
	if err != nil {
		t.Fatalf("ReadDir outbox: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in outbox, got %d", len(entries))
	}

	// Verify the file is valid JSON and matches the returned message.
	data, err := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var stored Message
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if stored.ID != msg.ID {
		t.Errorf("stored ID %q != returned ID %q", stored.ID, msg.ID)
	}
}

// TestReceiveReadsUnreadAndMarksRead verifies that Receive returns unread messages
// and marks them as read on disk.
func TestReceiveReadsUnreadAndMarksRead(t *testing.T) {
	// agent2 sends to agent1.
	agent2 := newTestMailbox(t, "agent-2", []string{"agent-1"})
	agent1 := NewMailbox("agent-1", []string{"agent-2"}, agent2.Dir) // share same Dir

	// agent2 sends two messages.
	_, err := agent2.Send("agent-1", "message one")
	if err != nil {
		t.Fatalf("agent2.Send: %v", err)
	}
	_, err = agent2.Send("agent-1", "message two")
	if err != nil {
		t.Fatalf("agent2.Send: %v", err)
	}

	// agent1 receives — should get both.
	msgs, err := agent1.Receive()
	if err != nil {
		t.Fatalf("agent1.Receive: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Verify ordering (oldest first).
	if !msgs[0].Timestamp.Before(msgs[1].Timestamp) && msgs[0].Timestamp != msgs[1].Timestamp {
		t.Error("messages not sorted by timestamp ascending")
	}

	// Receive again — should get nothing (already marked read).
	msgs2, err := agent1.Receive()
	if err != nil {
		t.Fatalf("agent1.Receive (second call): %v", err)
	}
	if len(msgs2) != 0 {
		t.Errorf("expected 0 messages on second receive, got %d", len(msgs2))
	}
}

// TestReceiveEmptyWhenNoMessages verifies that Receive returns an empty slice
// when no messages exist (inbox directory absent).
func TestReceiveEmptyWhenNoMessages(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2"})

	msgs, err := mb.Receive()
	if err != nil {
		t.Fatalf("Receive: unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

// TestGetAllMailSent verifies GetAllMail("sent") returns all sent messages.
func TestGetAllMailSent(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2"})

	_, err := mb.Send("agent-2", "first")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, err = mb.Send("agent-2", "second")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent, err := mb.GetAllMail("sent")
	if err != nil {
		t.Fatalf("GetAllMail(sent): %v", err)
	}
	if len(sent) != 2 {
		t.Errorf("expected 2 sent messages, got %d", len(sent))
	}
	// Verify sorted ascending.
	if len(sent) == 2 && sent[0].Timestamp.After(sent[1].Timestamp) {
		t.Error("sent messages not sorted ascending by timestamp")
	}
}

// TestGetAllMailReceived verifies GetAllMail("received") returns all received messages
// without modifying read status.
func TestGetAllMailReceived(t *testing.T) {
	agent2 := newTestMailbox(t, "agent-2", []string{"agent-1"})
	agent1 := NewMailbox("agent-1", []string{"agent-2"}, agent2.Dir)

	_, err := agent2.Send("agent-1", "hi agent-1")
	if err != nil {
		t.Fatalf("agent2.Send: %v", err)
	}

	received, err := agent1.GetAllMail("received")
	if err != nil {
		t.Fatalf("GetAllMail(received): %v", err)
	}
	if len(received) != 1 {
		t.Fatalf("expected 1 received message, got %d", len(received))
	}
	if received[0].Message != "hi agent-1" {
		t.Errorf("Message: got %q, want %q", received[0].Message, "hi agent-1")
	}

	// GetAllMail must NOT mark messages as read.
	received2, err := agent1.GetAllMail("received")
	if err != nil {
		t.Fatalf("GetAllMail(received) second call: %v", err)
	}
	if len(received2) != 1 {
		t.Errorf("expected message still present after GetAllMail, got %d", len(received2))
	}
}

// TestGetAllMailInvalidDirection verifies that an unknown direction returns an error.
func TestGetAllMailInvalidDirection(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2"})

	_, err := mb.GetAllMail("sideways")
	if err == nil {
		t.Error("expected error for invalid direction, got nil")
	}
}

// TestGetAllMailEmptyWhenNothingSent verifies GetAllMail returns empty slice
// when no mail directory exists yet.
func TestGetAllMailEmptyWhenNothingSent(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2"})

	sent, err := mb.GetAllMail("sent")
	if err != nil {
		t.Fatalf("GetAllMail(sent): unexpected error: %v", err)
	}
	if len(sent) != 0 {
		t.Errorf("expected 0 sent messages, got %d", len(sent))
	}

	received, err := mb.GetAllMail("received")
	if err != nil {
		t.Fatalf("GetAllMail(received): unexpected error: %v", err)
	}
	if len(received) != 0 {
		t.Errorf("expected 0 received messages, got %d", len(received))
	}
}

// TestSendMultiplePeers verifies that a Mailbox with multiple peers can send
// to each peer independently.
func TestSendMultiplePeers(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2", "agent-3"})

	_, err := mb.Send("agent-2", "hello agent-2")
	if err != nil {
		t.Fatalf("Send to agent2: %v", err)
	}
	_, err = mb.Send("agent-3", "hello agent-3")
	if err != nil {
		t.Fatalf("Send to agent-3: %v", err)
	}

	sent, err := mb.GetAllMail("sent")
	if err != nil {
		t.Fatalf("GetAllMail(sent): %v", err)
	}
	if len(sent) != 2 {
		t.Errorf("expected 2 sent messages across peers, got %d", len(sent))
	}
}

// TestMessageTimestampIsUTC verifies that sent messages carry a UTC timestamp.
func TestMessageTimestampIsUTC(t *testing.T) {
	mb := newTestMailbox(t, "agent-1", []string{"agent-2"})

	before := time.Now().UTC().Add(-time.Second)
	msg, err := mb.Send("agent-2", "timestamp check")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	if msg.Timestamp.Before(before) || msg.Timestamp.After(after) {
		t.Errorf("Timestamp %v not in expected range [%v, %v]", msg.Timestamp, before, after)
	}
	if msg.Timestamp.Location() != time.UTC {
		t.Errorf("Timestamp location: got %v, want UTC", msg.Timestamp.Location())
	}
}
