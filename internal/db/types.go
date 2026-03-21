package db

import "time"

// TextMessage represents a parsed message from a session
type TextMessage struct {
	Role      string
	Text      string
	Timestamp time.Time
	IsUser    bool
	MessageID string // unique message identifier
	SessionID string // session this message belongs to
}

// ThoughtBlock represents a reasoning/thinking block from an AI assistant.
// These are the internal thought processes (extended thinking, chain-of-thought)
// that models produce before generating their visible response.
type ThoughtBlock struct {
	MessageID string
	SessionID string
	Text      string
	Timestamp time.Time
	Source    string // "opencode" or "claudecode"
}
