package models

import "time"

// Conversation event roles for the cross-channel conversation log held by
// the Hub. They are intentionally distinct from Message.Role: a Hub event
// describes a turn in the shared dialogue, not an executable LLM message.
const (
	ConvRoleUser        = "user"         // a user message, from any channel
	ConvRoleAssistant   = "assistant"    // an assistant final reply
	ConvRoleToolSummary = "tool_summary" // compact textual summary of tool activity (not replayable)
	ConvRoleCheckpoint  = "checkpoint"   // a compaction summary standing in for older events
)

// ConversationEvent is one entry in the append-only, cross-channel
// conversation log maintained by the Hub.
//
// Unlike SessionData — a snapshot blob saved/loaded as a whole — events are
// appended individually with a server-assigned monotonic Seq. That lets
// concurrent writers (e.g. a Telegram adapter on the server and a notebook
// CLI) extend the same conversation without clobbering one another, and lets
// a reconnecting client tail from the last Seq it saw.
type ConversationEvent struct {
	ConvID      string    `json:"conv_id"`
	Seq         int64     `json:"seq"`                     // server-assigned, monotonic per conversation; 0 until appended
	Principal   string    `json:"principal"`               // owning principal (the identity shared across channels)
	Channel     string    `json:"channel"`                 // origin channel: "telegram", "slack", "local", ...
	Role        string    `json:"role"`                    // one of the ConvRole* constants
	Content     string    `json:"content"`                 // the dialogue text (prose or tool summary)
	ClientMsgID string    `json:"client_msg_id,omitempty"` // idempotency key: a repeat append with the same id is a no-op
	Timestamp   time.Time `json:"timestamp"`
}

// HubBinding maps a per-platform channel identity to a principal, as exchanged
// between the CLI and the hub. Shared here so neither the cli nor the remote
// client package needs to import the other.
type HubBinding struct {
	Platform  string `json:"platform"`
	UserID    string `json:"user_id"`
	Principal string `json:"principal"`
}

// ToMessage projects a conversation event onto the Message type used to build
// LLM history when a frontend hydrates the shared conversation.
//
// tool_summary and checkpoint events become system context rather than tool
// messages: tool execution stays local to whichever frontend produced it, so
// the other machines receive only a portable textual summary — never a
// tool-call to replay.
func (e ConversationEvent) ToMessage() Message {
	switch e.Role {
	case ConvRoleUser:
		return Message{Role: "user", Content: e.Content}
	case ConvRoleAssistant:
		return Message{Role: "assistant", Content: e.Content}
	default: // tool_summary, checkpoint
		return Message{
			Role:    "system",
			Content: e.Content,
			Meta:    &MessageMeta{IsSummary: e.Role == ConvRoleCheckpoint},
		}
	}
}
