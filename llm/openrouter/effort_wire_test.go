package openrouter

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

func TestOpenRouterCarriesEffortOnOfficialHost(t *testing.T) {
	c := NewOpenRouterClient(testProvider("k"), "anthropic/claude-opus-5", zap.NewNop(), 1, 0)
	ctx := client.WithEffortHint(context.Background(), client.EffortXHigh)
	payload := c.buildPayload(ctx, nil, 100)
	reasoning, ok := payload["reasoning"].(map[string]interface{})
	if !ok {
		t.Fatalf("reasoning object missing: %+v", payload)
	}
	// OpenRouter normalizes across vendors onto the OpenAI scale.
	if reasoning["effort"] != "high" {
		t.Errorf("effort = %v, want high", reasoning["effort"])
	}
}

func TestOpenRouterSendsNoEffortWithoutHint(t *testing.T) {
	c := NewOpenRouterClient(testProvider("k"), "anthropic/claude-opus-5", zap.NewNop(), 1, 0)
	payload := c.buildPayload(context.Background(), nil, 100)
	if _, ok := payload["reasoning"]; ok {
		t.Errorf("an unset hint must send nothing: %+v", payload)
	}
}

// A generic OpenAI-compatible backend behind OPENROUTER_API_URL rejects
// request fields it does not know, so the proprietary object stays off.
func TestOpenRouterSkipsEffortOnCustomEndpoint(t *testing.T) {
	t.Setenv("OPENROUTER_API_URL", "https://gateway.internal/v1/chat/completions")
	c := NewOpenRouterClient(testProvider("k"), "anthropic/claude-opus-5", zap.NewNop(), 1, 0)
	ctx := client.WithEffortHint(context.Background(), client.EffortHigh)
	payload := c.buildPayload(ctx, nil, 100)
	if _, ok := payload["reasoning"]; ok {
		t.Errorf("custom endpoint must not receive the reasoning object: %+v", payload)
	}
}
