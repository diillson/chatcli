/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/diillson/chatcli/llm/internal/visionwire"
	"github.com/diillson/chatcli/llm/toolshim"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/utils"
)

// toProtoMessages flattens chatcli's history plus the pending prompt into
// the normalized schema, mirroring how the built-in OpenAI adapter builds
// its request (the prompt is appended unless it already closes history).
func toProtoMessages(prompt string, history []models.Message) []providerproto.Message {
	out := make([]providerproto.Message, 0, len(history)+1)
	for _, m := range history {
		out = append(out, toProtoMessage(m))
	}
	last := len(history) - 1
	if strings.TrimSpace(prompt) != "" && (last < 0 || history[last].Role != "user" || history[last].Content != prompt) {
		out = append(out, providerproto.Message{
			Role:    providerproto.RoleUser,
			Content: []providerproto.Part{{Type: providerproto.PartText, Text: prompt}},
		})
	}
	return out
}

func toProtoMessage(m models.Message) providerproto.Message {
	role := strings.ToLower(strings.TrimSpace(m.Role))
	switch role {
	case providerproto.RoleSystem, providerproto.RoleUser, providerproto.RoleAssistant, providerproto.RoleTool:
	default:
		role = providerproto.RoleUser
	}
	pm := providerproto.Message{Role: role}

	if role == providerproto.RoleTool {
		pm.ToolCallID = m.ToolCallID
		pm.Content = []providerproto.Part{{Type: providerproto.PartText, Text: toolshim.MarkOpenAICompatibleToolError(m)}}
		return pm
	}

	// Thinking blocks go first: providers that require them back expect
	// them ahead of the text and tool calls of the same turn.
	if role == providerproto.RoleAssistant {
		for _, t := range m.Thinking {
			pm.Content = append(pm.Content, providerproto.Part{
				Type: providerproto.PartThinking, Text: t.Thinking, Signature: t.Signature, Redacted: t.Data,
			})
		}
	}

	// Structured system parts carry the cache breakpoints the planner chose.
	if role == providerproto.RoleSystem && len(m.SystemParts) > 0 {
		for _, sp := range m.SystemParts {
			if sp.Text == "" {
				continue
			}
			pm.Content = append(pm.Content, providerproto.Part{
				Type: providerproto.PartText, Text: sp.Text, CacheBreakpoint: sp.CacheControl != nil,
			})
		}
	} else if text := visionwire.TextWithDocuments(m); text != "" {
		// The plugin protocol has no document part: PDFs travel as their
		// extracted text.
		pm.Content = append(pm.Content, providerproto.Part{Type: providerproto.PartText, Text: text})
	}

	for _, img := range m.Images {
		part := providerproto.Part{Type: providerproto.PartImage, MediaType: img.MediaType, URL: img.URL}
		if len(img.Data) > 0 {
			part.Data = base64.StdEncoding.EncodeToString(img.Data)
		}
		pm.Content = append(pm.Content, part)
	}
	for _, tc := range m.ToolCalls {
		pm.ToolCalls = append(pm.ToolCalls, providerproto.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.ArgumentsJSON()})
	}
	return pm
}

func toProtoTools(tools []models.ToolDefinition) []providerproto.Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]providerproto.Tool, 0, len(tools))
	for _, t := range tools {
		params := t.Function.Parameters
		if params == nil {
			params = map[string]any{"type": "object"}
		}
		out = append(out, providerproto.Tool{Name: t.Function.Name, Description: t.Function.Description, Parameters: params})
	}
	return out
}

// fromProtoResult converts a chat/send result into chatcli's response.
func fromProtoResult(res providerproto.ChatResult) *models.LLMResponse {
	resp := &models.LLMResponse{StopReason: mapStopReason(res.StopReason)}
	var text strings.Builder
	for _, p := range res.Message.Content {
		switch p.Type {
		case providerproto.PartText:
			text.WriteString(p.Text)
		case providerproto.PartThinking:
			blk := models.ThinkingBlock{Type: "thinking", Thinking: p.Text, Signature: p.Signature}
			if p.Redacted != "" {
				blk = models.ThinkingBlock{Type: "redacted_thinking", Data: p.Redacted}
			}
			resp.Thinking = append(resp.Thinking, blk)
		}
	}
	resp.Content = text.String()
	for _, tc := range res.Message.ToolCalls {
		args := map[string]interface{}{}
		if strings.TrimSpace(tc.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				args = map[string]interface{}{}
			}
		}
		resp.ToolCalls = append(resp.ToolCalls, models.ToolCall{
			ID: tc.ID, Type: "function", Name: tc.Name, Arguments: args, Raw: tc.Arguments,
		})
	}
	resp.Usage = fromProtoUsage(res.Usage)
	return resp
}

// mapStopReason translates the protocol's stop reasons into the values the
// agent loop already understands (OpenAI-style finish reasons).
func mapStopReason(s string) string {
	switch s {
	case providerproto.StopMaxTokens:
		return "length"
	case providerproto.StopToolUse:
		return "tool_calls"
	case providerproto.StopRefusal:
		return "refusal"
	case providerproto.StopEnd, "":
		return "stop"
	}
	return s
}

// fromProtoUsage maps protocol usage, whose input excludes cache tokens
// (the additive schema), onto UsageInfo and normalizes it.
func fromProtoUsage(u *providerproto.Usage) *models.UsageInfo {
	if u == nil {
		return nil
	}
	info := &models.UsageInfo{
		PromptTokens:             u.InputTokens,
		CompletionTokens:         u.OutputTokens,
		CacheReadInputTokens:     u.CacheReadTokens,
		CacheCreationInputTokens: u.CacheWriteTokens,
		ReasoningTokens:          u.ReasoningTokens,
		IsReal:                   true,
	}
	if u.CostUSD != nil {
		info.CostUSD = *u.CostUSD
	}
	info.TotalTokens = info.PromptTokens + info.CompletionTokens
	info.Normalize(models.CacheAdditive)
	return info
}

// mapError turns protocol errors into the errors chatcli's retry, failover,
// compaction and re-login paths already recognize.
func mapError(provider string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrUnavailable) {
		return &utils.APIError{StatusCode: http.StatusServiceUnavailable, Message: err.Error()}
	}
	var perr *providerproto.Error
	if !errors.As(err, &perr) {
		return fmt.Errorf("%s: %w", provider, err)
	}
	msg := fmt.Sprintf("%s: %s", provider, perr.Message)
	switch perr.Code {
	case providerproto.ErrCodeAuthRequired:
		return &utils.APIError{StatusCode: http.StatusUnauthorized, Message: msg}
	case providerproto.ErrCodeRateLimited:
		return &utils.APIError{StatusCode: http.StatusTooManyRequests, Message: msg}
	case providerproto.ErrCodeContextTooLong:
		return &utils.APIError{StatusCode: http.StatusBadRequest, Message: "context_length_exceeded: " + msg}
	case providerproto.ErrCodeModelNotFound:
		return &utils.APIError{StatusCode: http.StatusNotFound, Message: msg}
	case providerproto.ErrCodeUnavailable:
		return &utils.APIError{StatusCode: http.StatusServiceUnavailable, Message: msg}
	case providerproto.ErrCodeCancelled:
		return context.Canceled
	case providerproto.ErrCodeBadRequest, providerproto.ErrCodeRefused:
		return &utils.APIError{StatusCode: http.StatusBadRequest, Message: msg}
	}
	return &utils.APIError{StatusCode: http.StatusBadGateway, Message: msg}
}
