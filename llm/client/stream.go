/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"strings"

	"github.com/diillson/chatcli/models"
)

// StreamChunk representa um delta de uma resposta LLM em streaming.
type StreamChunk struct {
	Text     string            // texto parcial (delta)
	Done     bool              // true no último chunk
	Usage    *UsageInfo        // populado no chunk final
	Error    error             // erro, se ocorreu
	Metadata map[string]string // dados extras (tool_call_id, etc.)
}

// UsageInfo contém métricas de uso de tokens.
type UsageInfo struct {
	InputTokens     int
	OutputTokens    int
	CacheRead       int
	CacheWrite      int
	ReasoningTokens int
}

// StreamingClient estende LLMClient com suporte a streaming.
type StreamingClient interface {
	LLMClient
	SendPromptStream(ctx context.Context, prompt string, history []models.Message, maxTokens int) (<-chan StreamChunk, error)
}

// CollectStream bloqueia e coleta toda a stream em uma string.
// Usado pelo one-shot mode e testes que não precisam de streaming.
func CollectStream(ch <-chan StreamChunk) (string, *UsageInfo, error) {
	var sb strings.Builder
	var usage *UsageInfo
	for chunk := range ch {
		if chunk.Error != nil {
			return sb.String(), usage, chunk.Error
		}
		sb.WriteString(chunk.Text)
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}
	return sb.String(), usage, nil
}

// StreamFromSync wrapa uma chamada SendPrompt síncrona em um channel de streaming.
// Usado por providers que não suportam streaming nativo.
func StreamFromSync(ctx context.Context, fn func(ctx context.Context) (string, error)) <-chan StreamChunk {
	ch := make(chan StreamChunk, 1)
	go func() {
		defer close(ch)
		result, err := fn(ctx)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}
		ch <- StreamChunk{Text: result, Done: true}
	}()
	return ch
}

// AsStreamingClient attempts to cast an LLMClient to StreamingClient.
// If the client doesn't implement StreamingClient, it returns a wrapper
// that uses StreamFromSync to simulate streaming.
func AsStreamingClient(c LLMClient) StreamingClient {
	if sc, ok := c.(StreamingClient); ok {
		return sc
	}
	return &syncStreamWrapper{inner: c}
}

// syncStreamWrapper wraps a non-streaming LLMClient to satisfy StreamingClient.
type syncStreamWrapper struct {
	inner LLMClient
}

func (w *syncStreamWrapper) GetModelName() string {
	return w.inner.GetModelName()
}

func (w *syncStreamWrapper) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	return w.inner.SendPrompt(ctx, prompt, history, maxTokens)
}

func (w *syncStreamWrapper) SendPromptStream(ctx context.Context, prompt string, history []models.Message, maxTokens int) (<-chan StreamChunk, error) {
	ch := StreamFromSync(ctx, func(ctx context.Context) (string, error) {
		return w.inner.SendPrompt(ctx, prompt, history, maxTokens)
	})
	return ch, nil
}
