package bus

import "time"

// MessageType classifies the message for routing.
type MessageType int

const (
	MessageTypeChat MessageType = iota
	MessageTypeToolCall
	MessageTypeToolResult
	MessageTypeAgentCall
	MessageTypeAgentResult
	MessageTypeSystem
	MessageTypeError
	MessageTypeChannel // MCP channel push messages
)

// String returns the human-readable name.
func (mt MessageType) String() string {
	switch mt {
	case MessageTypeChat:
		return "chat"
	case MessageTypeToolCall:
		return "tool_call"
	case MessageTypeToolResult:
		return "tool_result"
	case MessageTypeAgentCall:
		return "agent_call"
	case MessageTypeAgentResult:
		return "agent_result"
	case MessageTypeSystem:
		return "system"
	case MessageTypeError:
		return "error"
	case MessageTypeChannel:
		return "channel"
	default:
		return "unknown"
	}
}

// InboundMessage represents a message coming into the agent from any channel.
type InboundMessage struct {
	ID        string
	Channel   string // "cli", "grpc", "system"
	SenderID  string
	ChatID    string
	Content   string
	Media     []MediaRef
	Metadata  map[string]string
	Timestamp time.Time
	Type      MessageType
}

// OutboundMessage represents a message going from the agent to a channel.
type OutboundMessage struct {
	ID        string
	Channel   string
	ChatID    string
	Content   string
	ReplyToID string
	Metadata  map[string]string
	Timestamp time.Time
	Type      MessageType
	Streaming bool
}

// OutboundMediaMessage carries binary/media payloads.
type OutboundMediaMessage struct {
	OutboundMessage
	MediaType string // "image", "file", "audio"
	MediaData []byte
	MediaURL  string
	FileName  string
}

// MediaRef references media in an inbound message.
type MediaRef struct {
	Type     string // "image", "file", "audio"
	URL      string
	MimeType string
	Data     []byte
}
