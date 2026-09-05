/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// roleAt names the role of one wire message, whatever concrete map shape
// the builder produced.
func roleAt(m interface{}) string {
	switch v := m.(type) {
	case map[string]interface{}:
		s, _ := v["role"].(string)
		return s
	}
	return ""
}

func clearAtOf(m interface{}) string {
	if v, ok := m.(map[string]interface{}); ok {
		s, _ := v["clear_at"].(string)
		return s
	}
	return ""
}

func turnScopedHistory() []models.Message {
	return []models.Message{
		{Role: "system", Content: "you are a CLI"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		models.TurnContextMessage("today is Friday; cwd is /repo"),
		{Role: "user", Content: "second question"},
	}
}

// TestBuildMessages_TurnContextRendersAfterItsUserTurn is the placement
// contract. A turn-scoped system message renders only while no user
// message follows it, so emitting the block where the history holds it —
// ahead of the user's turn — would clear it on the very request that
// introduced it.
func TestBuildMessages_TurnContextRendersAfterItsUserTurn(t *testing.T) {
	c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}
	messages, _ := c.buildMessagesAndSystem("second question", turnScopedHistory())

	var roles []string
	for _, m := range messages {
		roles = append(roles, roleAt(m))
	}
	want := []string{"user", "assistant", "user", "system"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v — the block must follow the user turn it belongs to", roles, want)
		}
	}
	last := messages[len(messages)-1]
	if clearAtOf(last) != client.ClearAtNextUserMessage {
		t.Fatalf("the block must be turn-scoped, got clear_at=%q", clearAtOf(last))
	}
	if !c.turnScoped.Used() {
		t.Fatal("a request carrying the block must declare the beta")
	}
}

// TestBuildMessages_UnsupportedModelIsUnchanged is the no-regression
// guard on this surface: a model without the capability must serialize
// exactly what it always did, block included, as a user message in place.
func TestBuildMessages_UnsupportedModelIsUnchanged(t *testing.T) {
	c := &ClaudeClient{model: "claude-sonnet-5", logger: zap.NewNop()}
	messages, _ := c.buildMessagesAndSystem("second question", turnScopedHistory())

	var roles []string
	for _, m := range messages {
		roles = append(roles, roleAt(m))
		if clearAtOf(m) != "" {
			t.Fatal("a model without the capability must never receive clear_at")
		}
	}
	want := []string{"user", "assistant", "user", "user"}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v — the block stays a user message in place", roles, want)
		}
	}
	if c.turnScoped.Used() {
		t.Fatal("nothing was emitted, so no beta may be declared")
	}
}

// TestBuildClaudeToolMessages_PlacesTheBlockIdentically pins the tool loop
// against the same rule: the two builders must not disagree, or the same
// conversation would serialize differently depending on whether the turn
// used tools, and the prompt cache would miss on every switch.
func TestBuildClaudeToolMessages_PlacesTheBlockIdentically(t *testing.T) {
	e := client.NewTurnContextEmitter(catalog.ProviderClaudeAI, "claude-opus-5")
	messages := buildClaudeToolMessages("second question", turnScopedHistory(), e)

	var roles []string
	for _, m := range messages {
		roles = append(roles, roleAt(m))
	}
	want := []string{"user", "assistant", "user", "system"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v", roles, want)
		}
	}
	if clearAtOf(messages[len(messages)-1]) != client.ClearAtNextUserMessage {
		t.Fatal("the tool loop must scope the block to the turn too")
	}
}

// TestBuildClaudeToolMessages_StablePositionAcrossRequests is what makes
// a cleared block safe to keep re-sending. A block that lands at a
// different index on a later request is an edit to an earlier message:
// the prompt cache misses from there on, and on models that check it
// every later thinking block fails the conversation check.
func TestBuildClaudeToolMessages_StablePositionAcrossRequests(t *testing.T) {
	history := turnScopedHistory()
	first := buildClaudeToolMessages("second question", history,
		client.NewTurnContextEmitter(catalog.ProviderClaudeAI, "claude-opus-5"))

	// The conversation moves on: the answer lands, a new turn context and
	// a new user turn are appended.
	grown := append(append([]models.Message{}, history...),
		models.Message{Role: "assistant", Content: "second answer"},
		models.TurnContextMessage("today is Saturday; cwd is /repo"),
		models.Message{Role: "user", Content: "third question"})
	second := buildClaudeToolMessages("third question", grown,
		client.NewTurnContextEmitter(catalog.ProviderClaudeAI, "claude-opus-5"))

	if len(second) <= len(first) {
		t.Fatalf("the array must only grow: %d then %d", len(first), len(second))
	}
	for i := range first {
		if roleAt(first[i]) != roleAt(second[i]) || clearAtOf(first[i]) != clearAtOf(second[i]) {
			t.Fatalf("message %d moved between requests: %v then %v", i, first[i], second[i])
		}
	}
}

// renderedTurnContextBlocks counts the turn-context blocks the model
// actually reads in one request: a turn-scoped system message renders
// only while no user message follows it.
func renderedTurnContextBlocks(messages []map[string]interface{}) int {
	rendered := 0
	for i, m := range messages {
		if roleAt(m) != "system" {
			continue
		}
		visible := true
		for _, later := range messages[i+1:] {
			if roleAt(later) == "user" {
				visible = false
				break
			}
		}
		if visible {
			rendered++
		}
	}
	return rendered
}

// TestTurnScoped_SecondTurnStillCarriesContext is the end-to-end guard on
// the regression turn-scoping introduced. The per-turn block is skipped
// when it repeats the last one still in history, and on a turn-scoped
// model "still in history" stopped implying "still read": the earlier
// block stays in the array and renders for nobody. A day-resolution date
// line is byte-identical all day, so the model read its context on turn 1
// and never again.
func TestTurnScoped_SecondTurnStillCarriesContext(t *testing.T) {
	const block = "[TURN CONTEXT] today is 2026-09-04; cwd /repo"
	c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}

	// Turn 1: the block is injected and read.
	turn1 := []models.Message{
		{Role: "system", Content: "you are a CLI"},
		models.TurnContextMessage(block),
		{Role: "user", Content: "first question"},
	}
	messages, _ := c.buildMessagesAndSystem("first question", turn1)
	if got := renderedTurnContextBlocks(messages); got != 1 {
		t.Fatalf("turn 1 rendered %d blocks, want 1", got)
	}

	// Turn 2: the text has not changed. The block must still be injected,
	// because nothing the model can read carries it any more.
	if !client.SupportsTurnScopedSystem(catalog.ProviderClaudeAI, c.model) {
		t.Fatal("precondition: the model under test must be turn-scoped")
	}
	turn2 := append(append([]models.Message{}, turn1...),
		models.Message{Role: "assistant", Content: "first answer"},
		models.TurnContextMessage(block),
		models.Message{Role: "user", Content: "second question"})
	messages, _ = c.buildMessagesAndSystem("second question", turn2)
	if got := renderedTurnContextBlocks(messages); got != 1 {
		t.Fatalf("turn 2 rendered %d blocks, want exactly 1 — 0 means the model lost its context, 2 means the earlier one never cleared", got)
	}
}

// TestOAuthBuildMessages_CarriesTheBlockToo closes the surface Audit VI
// found missing. The Anthropic client has three message builders, not
// two: the API-key path, the tool loop, and this one for OAuth sessions.
// Only the first two were wired, so an OAuth session kept sending the
// block as a user message while the other two sent a system message —
// the same conversation serialized two ways depending on how the user
// had authenticated.
func TestOAuthBuildMessages_CarriesTheBlockToo(t *testing.T) {
	c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}
	messages, _ := c.buildOAuthMessagesAndSystem("second question", turnScopedHistory())

	var roles []string
	for _, m := range messages {
		roles = append(roles, roleAt(m))
	}
	want := []string{"user", "assistant", "user", "system"}
	if len(roles) != len(want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("roles = %v, want %v — the OAuth builder must place the block like the other two", roles, want)
		}
	}
	if clearAtOf(messages[len(messages)-1]) != client.ClearAtNextUserMessage {
		t.Fatal("the OAuth path must scope the block to the turn too")
	}

	// The OAuth header helper sets anthropic-beta wholesale, so the
	// clear_at opt-in has to be appended after it rather than replaced by
	// it. Without the flag the field is rejected as an unknown field.
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	applyOAuthHeaders(req, "tok")
	c.applyTurnScopedSystemBeta(req)
	betas := req.Header.Get("anthropic-beta")
	if !strings.Contains(betas, client.TurnScopedSystemBeta) {
		t.Fatalf("anthropic-beta = %q, want the clear_at beta appended", betas)
	}
	if !strings.Contains(betas, oauthAnthropicBeta) {
		t.Fatalf("anthropic-beta = %q, the OAuth betas must survive", betas)
	}
}

// TestOAuthBuildMessages_UnsupportedModelIsUnchanged keeps the OAuth path
// under the same no-regression rule as the others.
func TestOAuthBuildMessages_UnsupportedModelIsUnchanged(t *testing.T) {
	c := &ClaudeClient{model: "claude-sonnet-5", logger: zap.NewNop()}
	messages, _ := c.buildOAuthMessagesAndSystem("second question", turnScopedHistory())
	for _, m := range messages {
		if clearAtOf(m) != "" {
			t.Fatal("a model without the capability must never receive clear_at")
		}
	}
	if c.turnScoped.Used() {
		t.Fatal("nothing was emitted, so no beta may be declared")
	}
	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	applyOAuthHeaders(req, "tok")
	before := req.Header.Get("anthropic-beta")
	c.applyTurnScopedSystemBeta(req)
	if req.Header.Get("anthropic-beta") != before {
		t.Fatal("a request carrying no turn-scoped message must carry no extra beta")
	}
}

// assertSystemPlacementLegal encodes the rule the provider stated in the
// 400 that this test exists because of:
//
//	messages.N: role 'system' must precede an 'assistant' message or end
//	the array
//
// The original placement — directly after the user turn the block belongs
// to — satisfied it only when an assistant turn happened to come next. A
// tool loop is mostly user-role messages, because that is how tool results
// travel, and two user turns in a row is what a failed request leaves
// behind. Both produced [user, system, user] and were rejected outright.
func assertSystemPlacementLegal(t *testing.T, label string, messages []interface{}) {
	t.Helper()
	for i, m := range messages {
		if roleAt(m) != "system" {
			continue
		}
		if i == len(messages)-1 {
			continue // ends the array
		}
		if next := roleAt(messages[i+1]); next != "assistant" {
			t.Errorf("%s: messages.%d is role 'system' followed by %q — must precede an assistant message or end the array", label, i, next)
		}
	}
}

func asAny(messages []map[string]interface{}) []interface{} {
	out := make([]interface{}, 0, len(messages))
	for _, m := range messages {
		out = append(out, m)
	}
	return out
}

// TestTurnScoped_PlacementIsLegalOnEveryHistoryShape runs every Anthropic
// builder over the shapes that broke in production and over the tool loop
// that would have broken next.
func TestTurnScoped_PlacementIsLegalOnEveryHistoryShape(t *testing.T) {
	tc := func(n string) models.Message { return models.TurnContextMessage("[TURN CONTEXT] " + n) }

	shapes := map[string][]models.Message{
		"chat, one turn": {
			{Role: "system", Content: "sys"},
			tc("t1"), {Role: "user", Content: "q1"},
		},
		"chat, several turns": {
			{Role: "system", Content: "sys"},
			tc("t1"), {Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"},
			tc("t2"), {Role: "user", Content: "q2"}, {Role: "assistant", Content: "a2"},
			tc("t3"), {Role: "user", Content: "q3"},
		},
		// What the production session actually held: a turn whose request
		// failed leaves a user message with no assistant answer, and the
		// next turn appends another block and another user message.
		"two user turns in a row after a failed request": {
			{Role: "system", Content: "sys"},
			tc("t1"), {Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"},
			tc("t2"), {Role: "user", Content: "q2"},
			tc("t3"), {Role: "user", Content: "q3"},
		},
		// Tool results travel as user-role messages, so a block held for
		// the next assistant turn has to survive several of them.
		"tool loop": {
			{Role: "system", Content: "sys"},
			tc("t1"), {Role: "user", Content: "build it"},
			{Role: "assistant", Content: "running", ToolCalls: []models.ToolCall{{ID: "c1", Name: "run"}}},
			{Role: "tool", Content: "exit 1", ToolCallID: "c1"},
			tc("t2"),
			{Role: "tool", Content: "exit 0", ToolCallID: "c1"},
			{Role: "assistant", Content: "done"},
			tc("t3"), {Role: "user", Content: "thanks"},
		},
		"block is the last entry": {
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "q1"}, {Role: "assistant", Content: "a1"}, tc("t1"),
		},
	}

	for name, history := range shapes {
		t.Run(name, func(t *testing.T) {
			last := ""
			if n := len(history); n > 0 {
				last = history[n-1].Content
			}
			c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}

			msgs, _ := c.buildMessagesAndSystem(last, history)
			assertSystemPlacementLegal(t, "api-key builder", asAny(msgs))

			oauth, _ := c.buildOAuthMessagesAndSystem(last, history)
			assertSystemPlacementLegal(t, "oauth builder", asAny(oauth))

			tool := buildClaudeToolMessages(last, history,
				client.NewTurnContextEmitter(catalog.ProviderClaudeAI, "claude-opus-5"))
			assertSystemPlacementLegal(t, "tool loop", tool)
		})
	}
}

// countCacheBreakpoints counts the cache_control markers a request carries
// on its message array.
func countCacheBreakpoints(messages []map[string]interface{}) int {
	n := 0
	for _, m := range messages {
		blocks, ok := m["content"].([]map[string]interface{})
		if !ok {
			continue
		}
		for _, b := range blocks {
			if _, marked := b["cache_control"]; marked {
				n++
			}
		}
	}
	return n
}

// TestTurnScoped_HistoryStaysCacheable covers what fixing the 400 cost.
// The rolling breakpoint marks the last message so the whole conversation
// becomes a cacheable prefix, and it gives up when that message is not a
// user turn — a guard written for assistant prefills, before a system
// message could appear inside the array at all.
//
// Putting the block at the end of the array made that guard fire on every
// turn: no breakpoint, no cached prefix, and nothing said so. The block
// itself can never carry the marker — cache_control is rejected on a
// turn-scoped message — so the breakpoint belongs on the turn behind it.
func TestTurnScoped_HistoryStaysCacheable(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		models.TurnContextMessage("[TURN CONTEXT] today is 2026-09-05"),
		{Role: "user", Content: "q2"},
	}

	for _, model := range []string{"claude-opus-5", "claude-fable-5-1", "claude-sonnet-5"} {
		t.Run(model, func(t *testing.T) {
			c := &ClaudeClient{model: model, logger: zap.NewNop()}
			messages, _ := c.buildMessagesAndSystem("q2", history)
			client.MarkAnthropicHistoryBreakpoint(client.AnthropicMessages{Maps: messages}, client.AnthropicCacheMarker())

			if got := countCacheBreakpoints(messages); got != 1 {
				t.Fatalf("%s: %d cache breakpoints, want 1 — the conversation must stay a cacheable prefix", model, got)
			}
			// The marker must never land on the block itself.
			for _, m := range messages {
				if roleAt(m) != "system" {
					continue
				}
				if _, ok := m["cache_control"]; ok {
					t.Fatal("cache_control is rejected on a turn-scoped system message")
				}
			}
		})
	}

	// An assistant prefill still stays unmarked: the guard this walks past
	// exists for that case and must survive.
	c := &ClaudeClient{model: "claude-opus-5", logger: zap.NewNop()}
	prefill := append(append([]models.Message{}, history...), models.Message{Role: "assistant", Content: "partial"})
	messages, _ := c.buildMessagesAndSystem("", prefill)
	client.MarkAnthropicHistoryBreakpoint(client.AnthropicMessages{Maps: messages}, client.AnthropicCacheMarker())
	if got := countCacheBreakpoints(messages); got != 0 {
		t.Fatalf("an assistant prefill must stay unmarked, got %d breakpoints", got)
	}
}
