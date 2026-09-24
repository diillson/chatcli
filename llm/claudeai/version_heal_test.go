/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const tooOldBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Claude Code 2.1.259 does not support this model; version 9.9.9 or newer is required. Run 'claude update', or update the Claude desktop app, then try again.","details":{"error_code":"claude_code_version_too_old"}},"request_id":"req_011"}`

// isMainRequest tells the model request from the title requests the OAuth
// surface sends around it; the heal is about the model request.
func isMainRequest(r *http.Request, model string) bool {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.Model == model
}

const healedStream = "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"healed\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// The bug this guards: a new model rejected the request because the
// Claude Code release the OAuth surface presented was older than the one
// the API gates it on, and the error reached the user as a 400. The
// client now reads the release the API asks for, presents it and sends
// the same request again — once per process, not once per request.
func TestSendPrompt_HealsClaudeCodeVersionAndResends(t *testing.T) {
	t.Cleanup(auth.ResetLearnedClaudeCodeVersion)
	auth.ResetLearnedClaudeCodeVersion()
	t.Setenv(auth.ClaudeCodeVersionEnv, "")

	var calls int32
	var agents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMainRequest(r, "claude-opus-5-5") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(healedStream))
			return
		}
		agents = append(agents, r.Header.Get("User-Agent"))
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(tooOldBody))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(healedStream))
	}))
	defer srv.Close()

	// OAuth is the surface that presents the fingerprint.
	c := NewClaudeClient(auth.NewStaticTokenProvider("tok", auth.AuthModeOAuth, auth.ProviderAnthropic), "claude-opus-5-5", zap.NewNop(), 1, time.Millisecond)
	c.apiURL = srv.URL
	out, err := c.SendPrompt(context.Background(), "hi", []models.Message{{Role: "user", Content: "hi"}}, 64)
	require.NoError(t, err)
	assert.Equal(t, "healed", out)
	require.Len(t, agents, 2, "one rejection, one resend")
	assert.Equal(t, "claude-cli/"+auth.ClaudeCodeVersion+" (external, cli)", agents[0])
	assert.Equal(t, "claude-cli/9.9.9 (external, cli)", agents[1], "the resend presents the release the API asked for")
	assert.Equal(t, "9.9.9", auth.EffectiveClaudeCodeVersion(), "learned for the process")

	// The next request starts on the learned release: no rejection round.
	atomic.StoreInt32(&calls, 5)
	agents = nil
	_, err = c.SendPrompt(context.Background(), "again", []models.Message{{Role: "user", Content: "again"}}, 64)
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, "claude-cli/9.9.9 (external, cli)", agents[0])
}

// A second rejection on the same release, or a 400 for another reason,
// comes back as the error it is: the heal fires only when the fingerprint
// actually changed.
func TestSendPrompt_VersionHealDoesNotLoop(t *testing.T) {
	t.Cleanup(auth.ResetLearnedClaudeCodeVersion)
	auth.ResetLearnedClaudeCodeVersion()
	t.Setenv(auth.ClaudeCodeVersionEnv, "")

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMainRequest(r, "claude-opus-5-5") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(healedStream))
			return
		}
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(tooOldBody))
	}))
	defer srv.Close()
	c := NewClaudeClient(auth.NewStaticTokenProvider("tok", auth.AuthModeOAuth, auth.ProviderAnthropic), "claude-opus-5-5", zap.NewNop(), 1, time.Millisecond)
	c.apiURL = srv.URL
	_, err := c.SendPrompt(context.Background(), "hi", []models.Message{{Role: "user", Content: "hi"}}, 64)
	require.Error(t, err)
	var apiErr *utils.APIError
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls), "one heal, then the rejection stands")

	// Already on that release: a plain 400, no resend.
	atomic.StoreInt32(&calls, 0)
	_, err = c.SendPrompt(context.Background(), "hi", []models.Message{{Role: "user", Content: "hi"}}, 64)
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isMainRequest(r, "claude-opus-5-5") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(healedStream))
			return
		}
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`))
	}))
	defer other.Close()
	atomic.StoreInt32(&calls, 0)
	c.apiURL = other.URL
	_, err = c.SendPrompt(context.Background(), "hi", []models.Message{{Role: "user", Content: "hi"}}, 64)
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "an unrelated 400 is not resent")
	assert.False(t, c.healClaudeCodeVersion(assert.AnError))
}
