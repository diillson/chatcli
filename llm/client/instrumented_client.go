/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// MetricsRecorder is the interface for recording LLM call metrics.
// Implemented by the server-side metrics adapter to avoid importing metrics/ here.
type MetricsRecorder interface {
	RecordRequest(provider, model, status string, duration time.Duration)
	RecordError(provider, model, errorType string)
}

// InstrumentedClient wraps an LLMClient and records metrics for each call.
type InstrumentedClient struct {
	inner    LLMClient
	recorder MetricsRecorder
	provider string
}

// NewInstrumentedClient creates a new metrics-recording wrapper around an LLMClient.
func NewInstrumentedClient(inner LLMClient, recorder MetricsRecorder, provider string) *InstrumentedClient {
	return &InstrumentedClient{
		inner:    inner,
		recorder: recorder,
		provider: provider,
	}
}

// GetModelName delegates to the inner client.
func (c *InstrumentedClient) GetModelName() string {
	return c.inner.GetModelName()
}

// SendPrompt delegates to the inner client and records metrics.
func (c *InstrumentedClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	model := c.inner.GetModelName()
	start := time.Now()

	response, err := c.inner.SendPrompt(ctx, prompt, history, maxTokens)
	duration := time.Since(start)

	if err != nil {
		errType := classifyError(err)
		c.recorder.RecordRequest(c.provider, model, "error", duration)
		c.recorder.RecordError(c.provider, model, errType)
		return response, err
	}

	c.recorder.RecordRequest(c.provider, model, "success", duration)
	return response, nil
}

// SendPromptWithTools delegates to the inner client if it supports native tools.
func (c *InstrumentedClient) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	tac, ok := c.inner.(ToolAwareClient)
	if !ok {
		return nil, fmt.Errorf("inner client %T does not support native tools", c.inner)
	}

	model := c.inner.GetModelName()
	start := time.Now()

	response, err := tac.SendPromptWithTools(ctx, prompt, history, tools, maxTokens)
	duration := time.Since(start)

	if err != nil {
		errType := classifyError(err)
		c.recorder.RecordRequest(c.provider, model, "error", duration)
		c.recorder.RecordError(c.provider, model, errType)
		return response, err
	}

	c.recorder.RecordRequest(c.provider, model, "success", duration)
	return response, nil
}

// SupportsNativeTools returns true if the inner client supports native tool calling.
func (c *InstrumentedClient) SupportsNativeTools() bool {
	tac, ok := c.inner.(ToolAwareClient)
	if !ok {
		return false
	}
	return tac.SupportsNativeTools()
}

// classifyError extracts an error type from the error for metrics labeling.
func classifyError(err error) string {
	if err == nil {
		return ""
	}

	msg := strings.ToLower(err.Error())

	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		switch {
		case llmErr.Code == 429:
			return "rate_limit"
		case llmErr.Code == 401 || llmErr.Code == 403:
			return "auth_error"
		case llmErr.Code >= 500:
			return "server_error"
		case llmErr.Code == 408:
			return "timeout"
		}
	}

	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit") || strings.Contains(msg, "429"):
		return "rate_limit"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "401") || strings.Contains(msg, "403"):
		return "auth_error"
	case strings.Contains(msg, "500") || strings.Contains(msg, "502") || strings.Contains(msg, "503") || strings.Contains(msg, "server error"):
		return "server_error"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "unknown"
	}
}
