/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func bedrockTurnScopedHistory() []models.Message {
	return []models.Message{
		{Role: "system", Content: "you are a CLI"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		models.TurnContextMessage("today is Friday; cwd is /repo"),
		{Role: "user", Content: "second question"},
	}
}

func bedrockRoles(messages []map[string]interface{}) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		s, _ := m["role"].(string)
		out = append(out, s)
	}
	return out
}

// TestBedrockBuildMessages_TurnContextFollowsItsUserTurn pins that the
// Bedrock mirror takes the same path as the first-party API. The two
// clients build the Anthropic array independently, and a conversation
// that serialized differently on one surface would miss the cache on
// every switch between them.
func TestBedrockBuildMessages_TurnContextFollowsItsUserTurn(t *testing.T) {
	c := &BedrockClient{model: "anthropic.claude-opus-5", logger: zap.NewNop()}
	messages, _ := c.buildMessagesAndSystem("second question", bedrockTurnScopedHistory())

	want := []string{"user", "assistant", "user", "system"}
	got := bedrockRoles(messages)
	if len(got) != len(want) {
		t.Fatalf("roles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles = %v, want %v", got, want)
		}
	}
	last := messages[len(messages)-1]
	if last["clear_at"] != client.ClearAtNextUserMessage {
		t.Fatalf("the block must be turn-scoped, got %v", last["clear_at"])
	}

	// Bedrock takes beta flags in the InvokeModel body rather than in a
	// header, and only on a request that actually carries one.
	body := map[string]interface{}{}
	c.applyTurnScopedSystemBeta(body)
	betas, _ := body["anthropic_beta"].([]string)
	if len(betas) != 1 || betas[0] != client.TurnScopedSystemBeta {
		t.Fatalf("anthropic_beta = %v, want the clear_at beta", body["anthropic_beta"])
	}
	// Applying twice must not duplicate the flag.
	c.applyTurnScopedSystemBeta(body)
	if betas, _ = body["anthropic_beta"].([]string); len(betas) != 1 {
		t.Fatalf("beta duplicated: %v", betas)
	}
}

// TestBedrockBuildMessages_UnsupportedModelIsUnchanged is the
// no-regression guard: Sonnet 5 on Bedrock keeps the user-role block and
// must never be opted into the beta.
func TestBedrockBuildMessages_UnsupportedModelIsUnchanged(t *testing.T) {
	c := &BedrockClient{model: "anthropic.claude-sonnet-5", logger: zap.NewNop()}
	messages, _ := c.buildMessagesAndSystem("second question", bedrockTurnScopedHistory())

	want := []string{"user", "assistant", "user", "user"}
	got := bedrockRoles(messages)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles = %v, want %v", got, want)
		}
	}
	for _, m := range messages {
		if _, ok := m["clear_at"]; ok {
			t.Fatal("a model without the capability must never receive clear_at")
		}
	}
	body := map[string]interface{}{}
	c.applyTurnScopedSystemBeta(body)
	if _, ok := body["anthropic_beta"]; ok {
		t.Fatal("a request carrying no turn-scoped message must carry no beta")
	}
}
