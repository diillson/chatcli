package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ChannelMessage represents a push message from an MCP server.
type ChannelMessage struct {
	ServerName string            `json:"serverName"`
	Channel    string            `json:"channel"` // e.g., "ci", "alerts", "chat"
	Content    string            `json:"content"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Timestamp  time.Time         `json:"timestamp"`
}

// ChannelHandler is called when a channel message is received.
type ChannelHandler func(msg ChannelMessage)

// ChannelManager manages MCP channel subscriptions and message delivery.
type ChannelManager struct {
	messages    []ChannelMessage
	handlers    []ChannelHandler
	mu          sync.RWMutex
	maxMessages int // circular buffer size
	logger      *zap.Logger
}

// NewChannelManager creates a new channel manager.
func NewChannelManager(logger *zap.Logger) *ChannelManager {
	return &ChannelManager{
		maxMessages: 100,
		logger:      logger,
	}
}

// OnMessage registers a handler that will be called for each incoming channel message.
func (cm *ChannelManager) OnMessage(handler ChannelHandler) {
	cm.mu.Lock()
	cm.handlers = append(cm.handlers, handler)
	cm.mu.Unlock()
}

// Push processes an incoming channel message from an MCP server.
func (cm *ChannelManager) Push(msg ChannelMessage) {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}

	cm.mu.Lock()
	cm.messages = append(cm.messages, msg)
	// Trim to max size
	if len(cm.messages) > cm.maxMessages {
		cm.messages = cm.messages[len(cm.messages)-cm.maxMessages:]
	}
	handlers := make([]ChannelHandler, len(cm.handlers))
	copy(handlers, cm.handlers)
	cm.mu.Unlock()

	cm.logger.Info("MCP channel message received",
		zap.String("server", msg.ServerName),
		zap.String("channel", msg.Channel),
		zap.Int("content_len", len(msg.Content)))

	// Dispatch to all handlers
	for _, h := range handlers {
		h(msg)
	}
}

// GetRecent returns the N most recent channel messages.
func (cm *ChannelManager) GetRecent(n int) []ChannelMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if n > len(cm.messages) {
		n = len(cm.messages)
	}
	if n <= 0 {
		return nil
	}

	result := make([]ChannelMessage, n)
	copy(result, cm.messages[len(cm.messages)-n:])
	return result
}

// GetByChannel returns recent messages filtered by channel name.
func (cm *ChannelManager) GetByChannel(channel string, n int) []ChannelMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	var filtered []ChannelMessage
	for i := len(cm.messages) - 1; i >= 0 && len(filtered) < n; i-- {
		if cm.messages[i].Channel == channel || channel == "*" {
			filtered = append(filtered, cm.messages[i])
		}
	}

	// Reverse to chronological order
	for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	}
	return filtered
}

// Count returns the total number of stored messages.
func (cm *ChannelManager) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.messages)
}

// FormatForPrompt returns recent channel messages formatted for system prompt injection.
func (cm *ChannelManager) FormatForPrompt(maxMessages int) string {
	recent := cm.GetRecent(maxMessages)
	if len(recent) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## MCP Channel Messages (Recent)\n\n")
	for _, msg := range recent {
		sb.WriteString(fmt.Sprintf("[%s/%s %s] %s\n",
			msg.ServerName, msg.Channel,
			msg.Timestamp.Format("15:04:05"),
			msg.Content))
	}
	return sb.String()
}

// ProcessSSENotification handles a notification pushed via SSE from an MCP server.
// This is called when the SSE stream receives a message that's not a response to a request.
func (cm *ChannelManager) ProcessSSENotification(serverName string, data []byte) {
	// Try to parse as a JSON-RPC notification
	var notification struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &notification); err != nil {
		// Not valid JSON-RPC, treat as plain text
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    "raw",
			Content:    string(data),
		})
		return
	}

	// Route based on notification method
	switch {
	case strings.HasPrefix(notification.Method, "notifications/"):
		// MCP standard notification
		channel := strings.TrimPrefix(notification.Method, "notifications/")
		content := string(notification.Params)
		if content == "" || content == "null" {
			content = notification.Method
		}
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    channel,
			Content:    content,
		})

	case notification.Method == "message" || notification.Method == "channel/message":
		// Custom channel message
		var payload struct {
			Channel string `json:"channel"`
			Content string `json:"content"`
			Text    string `json:"text"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(notification.Params, &payload)
		content := payload.Content
		if content == "" {
			content = payload.Text
		}
		if content == "" {
			content = payload.Message
		}
		if content == "" {
			content = string(notification.Params)
		}
		channel := payload.Channel
		if channel == "" {
			channel = "default"
		}
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    channel,
			Content:    content,
		})

	default:
		// Unknown notification type
		cm.Push(ChannelMessage{
			ServerName: serverName,
			Channel:    notification.Method,
			Content:    string(notification.Params),
		})
	}
}
