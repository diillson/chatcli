/*
 * ChatCLI - Streaming Client Interface
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Defines the StreamingClient interface for LLM providers that support
 * real-time streaming of responses. Providers implement this optional
 * interface to enable character-by-character display in the TUI.
 *
 * Non-streaming providers continue to work via the base LLMClient interface.
 */
package client

import (
	"context"

	"github.com/diillson/chatcli/models"
)

// StreamChunk represents a single chunk from a streaming LLM response.
type StreamChunk struct {
	// Text is the incremental text content in this chunk.
	// May be empty for non-text events (tool_use start, metadata).
	Text string

	// Done indicates this is the final chunk. After receiving a Done chunk,
	// no more chunks will be sent on the channel.
	Done bool

	// Usage contains token usage info. Only populated on the final chunk (Done=true).
	Usage *models.UsageInfo

	// StopReason indicates why generation stopped. Only on final chunk.
	// Common values: "end_turn", "max_tokens", "stop_sequence", "tool_use".
	StopReason string

	// Error carries any error that occurred during streaming.
	// When set, the stream should be considered terminated.
	Error error
}

// StreamingClient extends LLMClient with real-time streaming support.
// Providers that implement this interface can send response chunks as they
// arrive from the API, enabling character-by-character display.
//
// The streaming contract:
//   - The returned channel will receive zero or more text chunks
//   - The final chunk will have Done=true and may include Usage/StopReason
//   - If an error occurs, a chunk with Error set will be sent, then the channel closes
//   - The channel is closed after the final chunk or error
//   - The caller can cancel via the context to abort streaming
type StreamingClient interface {
	LLMClient

	// SendPromptStream sends a prompt and returns a channel of streaming chunks.
	// The channel is closed when streaming completes or an error occurs.
	// maxTokens of 0 means use provider default.
	SendPromptStream(ctx context.Context, prompt string, history []models.Message, maxTokens int) (<-chan StreamChunk, error)

	// SupportsStreaming returns true if the provider supports streaming.
	// Some providers may conditionally support streaming (e.g., only for certain models).
	SupportsStreaming() bool
}

// IsStreamingCapable checks if a client supports streaming.
func IsStreamingCapable(c LLMClient) bool {
	sc, ok := c.(StreamingClient)
	return ok && sc.SupportsStreaming()
}

// AsStreamingClient casts a client to StreamingClient if supported.
func AsStreamingClient(c LLMClient) (StreamingClient, bool) {
	sc, ok := c.(StreamingClient)
	if !ok || !sc.SupportsStreaming() {
		return nil, false
	}
	return sc, true
}

// DrainStream reads all chunks from a streaming channel and returns
// the concatenated text. Useful for converting streaming to non-streaming.
func DrainStream(chunks <-chan StreamChunk) (string, *models.UsageInfo, string, error) {
	var text string
	var usage *models.UsageInfo
	var stopReason string

	for chunk := range chunks {
		if chunk.Error != nil {
			return text, usage, stopReason, chunk.Error
		}
		text += chunk.Text
		if chunk.Done {
			usage = chunk.Usage
			stopReason = chunk.StopReason
		}
	}

	return text, usage, stopReason, nil
}
