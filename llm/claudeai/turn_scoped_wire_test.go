/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package claudeai

import (
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
