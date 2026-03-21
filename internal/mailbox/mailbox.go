package mailbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Message represents a message exchanged between agent peers.
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Read      bool      `json:"read"`
}

// Mailbox manages agent mail for a given identity.
type Mailbox struct {
	Identity string   // e.g. "agent-1"
	Peers    []string // e.g. ["agent-2"]
	Dir      string   // base directory for mail storage
}

// NewMailbox creates a new Mailbox with the given identity, peers, and base directory.
func NewMailbox(identity string, peers []string, dir string) *Mailbox {
	return &Mailbox{
		Identity: identity,
		Peers:    peers,
		Dir:      dir,
	}
}

// getOutboxPath returns the directory where messages from → to are stored.
func (m *Mailbox) getOutboxPath(from, to string) string {
	return filepath.Join(m.Dir, fmt.Sprintf("%s_to_%s", from, to))
}

// getInboxPath returns the directory where messages from → to are stored.
// (Symmetric with getOutboxPath — same path, different perspective.)
func (m *Mailbox) getInboxPath(from, to string) string {
	return filepath.Join(m.Dir, fmt.Sprintf("%s_to_%s", from, to))
}

// Send writes a new message from m.Identity to the given peer.
func (m *Mailbox) Send(to, message string) (*Message, error) {
	from := m.Identity
	outbox := m.getOutboxPath(from, to)

	if err := os.MkdirAll(outbox, 0755); err != nil {
		return nil, fmt.Errorf("create outbox: %w", err)
	}

	msg := &Message{
		ID:        uuid.New().String(),
		From:      from,
		To:        to,
		Message:   message,
		Timestamp: time.Now().UTC(),
		Read:      false,
	}

	filename := fmt.Sprintf("%s_%s.json", msg.Timestamp.Format("2006-01-02T15-04-05"), msg.ID[:8])
	path := filepath.Join(outbox, filename)

	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return nil, fmt.Errorf("write message: %w", err)
	}

	return msg, nil
}

// Receive reads all unread messages sent to m.Identity from any peer,
// marks them as read, and returns them sorted by timestamp.
func (m *Mailbox) Receive() ([]Message, error) {
	to := m.Identity
	var messages []Message

	for _, from := range m.Peers {
		inbox := m.getInboxPath(from, to)
		entries, err := os.ReadDir(inbox)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read inbox from %s: %w", from, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}

			path := filepath.Join(inbox, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}

			if !msg.Read {
				messages = append(messages, msg)
				msg.Read = true
				updated, _ := json.MarshalIndent(msg, "", "  ")
				os.WriteFile(path, updated, 0644) //nolint:errcheck
			}
		}
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	return messages, nil
}

// GetAllMail returns all messages in the given direction: "sent" or "received".
// It does not modify read status.
func (m *Mailbox) GetAllMail(direction string) ([]Message, error) {
	switch direction {
	case "sent":
		if len(m.Peers) == 0 {
			return nil, fmt.Errorf("no peers configured")
		}
		// Aggregate sent mail to all peers.
		var all []Message
		for _, peer := range m.Peers {
			msgs, err := m.readDir(m.getOutboxPath(m.Identity, peer))
			if err != nil {
				return nil, err
			}
			all = append(all, msgs...)
		}
		sort.Slice(all, func(i, j int) bool {
			return all[i].Timestamp.Before(all[j].Timestamp)
		})
		return all, nil

	case "received":
		if len(m.Peers) == 0 {
			return nil, fmt.Errorf("no peers configured")
		}
		// Aggregate received mail from all peers.
		var all []Message
		for _, peer := range m.Peers {
			msgs, err := m.readDir(m.getInboxPath(peer, m.Identity))
			if err != nil {
				return nil, err
			}
			all = append(all, msgs...)
		}
		sort.Slice(all, func(i, j int) bool {
			return all[i].Timestamp.Before(all[j].Timestamp)
		})
		return all, nil

	default:
		return nil, fmt.Errorf("invalid direction: %s (use 'sent' or 'received')", direction)
	}
}

// readDir reads all Message JSON files from a directory.
// Returns an empty slice (not an error) if the directory does not exist.
func (m *Mailbox) readDir(dir string) ([]Message, error) {
	var messages []Message

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return messages, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}

	return messages, nil
}
