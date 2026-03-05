package client

import (
	"context"
	"sort"

	"github.com/diillson/chatcli/models"
)

// ToolAwareClient extends LLMClient with native tool use support.
// Providers that support native tool calling (OpenAI, Claude, Gemini) implement this.
// Providers that don't (Ollama, StackSpot) only implement LLMClient.
type ToolAwareClient interface {
	LLMClient

	// SendPromptWithTools sends a prompt with tool definitions and returns structured response.
	SendPromptWithTools(ctx context.Context, prompt string, history []models.Message,
		tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error)

	// SupportsNativeTools returns true if the provider supports native tool calling.
	SupportsNativeTools() bool
}

// IsToolAware checks if a client supports native tools.
func IsToolAware(c LLMClient) bool {
	_, ok := c.(ToolAwareClient)
	return ok
}

// AsToolAware casts a client to ToolAwareClient if possible.
func AsToolAware(c LLMClient) (ToolAwareClient, bool) {
	tac, ok := c.(ToolAwareClient)
	return tac, ok
}

// SortToolDefinitions sorts tools alphabetically by name for KV cache stability.
func SortToolDefinitions(tools []models.ToolDefinition) []models.ToolDefinition {
	sorted := make([]models.ToolDefinition, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Function.Name < sorted[j].Function.Name
	})
	return sorted
}

// NormalizeToolCall ensures consistent ToolCall structure.
func NormalizeToolCall(tc models.ToolCall) models.ToolCall {
	if tc.Type == "" {
		tc.Type = "function"
	}
	if tc.Arguments == nil {
		tc.Arguments = make(map[string]interface{})
	}
	return tc
}
