/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newEmptyReplyClient() *ClaudeClient {
	return NewClaudeClient(auth.NewStaticTokenProvider("t", auth.AuthModeAPIKey, auth.ProviderID("claudeai")), "claude-fable-5-1", zap.NewNop(), 1, time.Millisecond)
}

// sseBody is a streamed reply; the parser closes it.
func sseBody(body string) io.ReadCloser { return io.NopCloser(strings.NewReader(body)) }

// The bug this guards: a reply the safety classifier stopped arrived with
// no text and surfaced as the generic "no response", which the coder took
// as fatal. The stream carries the cause in message_delta.
func TestProcessStreamResponse_RefusalIsTypedAndBilled(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":118000,\"cache_read_input_tokens\":100000}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"usage\":{\"output_tokens\":0}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c := newEmptyReplyClient()
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: sseBody(body)}
	text, err := c.processStreamResponse(resp, true)
	require.Error(t, err)
	assert.Empty(t, text)

	empty := client.AsEmptyResponse(err)
	require.NotNil(t, empty, "the empty reply is typed: %v", err)
	assert.Equal(t, "refusal", empty.StopReason)
	assert.True(t, empty.Refused())
	assert.True(t, client.IsRefusal(err))
	assert.Equal(t, "claude-fable-5-1", empty.Model)
	assert.False(t, utils.IsTemporaryError(err), "a refusal is not retried blindly: the same request gets the same answer")

	usage := c.LastUsage()
	require.NotNil(t, usage, "the input was billed whether or not text came back")
	assert.Equal(t, 118000, usage.PromptTokens)
	assert.Equal(t, "refusal", c.LastStopReason())
}

func TestProcessStreamResponse_ReasoningOnlyNamesTheBlocks(t *testing.T) {
	body := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"},\"usage\":{\"output_tokens\":4096}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: sseBody(body)}
	_, err := newEmptyReplyClient().processStreamResponse(resp, false)
	empty := client.AsEmptyResponse(err)
	require.NotNil(t, empty)
	assert.Equal(t, "max_tokens", empty.StopReason)
	assert.Equal(t, 1, empty.Blocks["thinking"])
	assert.False(t, empty.Refused())

	// A normal stream is untouched by the diagnosis.
	ok := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	okResp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: sseBody(ok)}
	text, err := newEmptyReplyClient().processStreamResponse(okResp, false)
	require.NoError(t, err)
	assert.Equal(t, "hello", text)
}

func TestProcessResponse_BufferedRefusal(t *testing.T) {
	body := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-fable-5-1","content":[],"stop_reason":"refusal","usage":{"input_tokens":50,"output_tokens":0}}`
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	c := newEmptyReplyClient()
	_, err := c.processResponse(resp)
	empty := client.AsEmptyResponse(err)
	require.NotNil(t, empty, "%v", err)
	assert.True(t, empty.Refused())
	require.NotNil(t, c.LastUsage(), "billed input is recorded before the empty check")
	assert.Equal(t, 50, c.LastUsage().PromptTokens)
}

func TestSendPromptWithTools_RefusalWithNothingInItIsTheTypedError(t *testing.T) {
	body := `{"id":"msg_2","type":"message","role":"assistant","model":"claude-fable-5-1","content":[],"stop_reason":"refusal","usage":{"input_tokens":70,"output_tokens":0}}`
	response, err := parseClaudeToolResponse(body, zap.NewNop())
	require.NoError(t, err)
	assert.Equal(t, "refusal", response.StopReason)
	assert.Empty(t, response.Content)
	assert.Empty(t, response.ToolCalls)
	// The client-level guard turns that into the typed error (see
	// SendPromptWithTools); the parse itself stays neutral.
	c := newEmptyReplyClient()
	typed := c.emptyResponse(response.StopReason, client.ParseAnthropicStopDetails([]byte(body)), map[string]int{"thinking": len(response.Thinking)}, "tool_use")
	assert.True(t, client.IsRefusal(typed))
	assert.Empty(t, client.AsEmptyResponse(typed).Category(), "no stop_details in the body, no category")
}

// A refusal names what the classifier flagged: the stream carries
// stop_details in message_delta, the buffered body at the top level, and
// the tool path re-reads the body it already holds. The category and the
// recommended model reach the typed error so the loop can print the
// reason and route to the model Anthropic suggests.
func TestRefusalCarriesStopDetails(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"refusal\",\"category\":\"cyber\",\"explanation\":\"offensive tooling\",\"recommended_model\":\"claude-opus-4-8\"}},\"usage\":{\"output_tokens\":0}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: sseBody(stream)}
	_, err := newEmptyReplyClient().processStreamResponse(resp, true)
	empty := client.AsEmptyResponse(err)
	require.NotNil(t, empty)
	assert.Equal(t, "cyber", empty.Category())
	assert.Equal(t, "claude-opus-4-8", empty.RecommendedModel())
	assert.Contains(t, err.Error(), "cyber")
	assert.Contains(t, err.Error(), "offensive tooling")

	buffered := `{"id":"msg_3","type":"message","role":"assistant","model":"claude-fable-5-1","content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"bio","explanation":null,"recommended_model":null},"usage":{"input_tokens":5,"output_tokens":0}}`
	_, err = newEmptyReplyClient().processResponse(&http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(buffered))})
	empty = client.AsEmptyResponse(err)
	require.NotNil(t, empty)
	assert.Equal(t, "bio", empty.Category())
	assert.Empty(t, empty.RecommendedModel(), "null recommendation is none")
	assert.Contains(t, err.Error(), "bio")

	details := client.ParseAnthropicStopDetails([]byte(buffered))
	require.NotNil(t, details)
	assert.Equal(t, "bio", details.Category)
	assert.Nil(t, client.ParseAnthropicStopDetails([]byte(`{"stop_reason":"refusal","stop_details":null}`)), "null details are none")
	assert.Nil(t, client.ParseAnthropicStopDetails([]byte(`not json`)))
}
