// Package toolshim provides a conversion layer that enables LLM providers
// without native tool calling support to use structured tool definitions.
//
// Instead of injecting XML syntax instructions into the system prompt and
// hoping the model generates valid XML, this shim converts tool definitions
// into the provider's native format (e.g., Ollama's tool calling API) or
// generates optimized prompt instructions with JSON schema.
//
// Architecture:
//
//	ToolDefinition (models) → ProviderShim → provider-specific format
//	                                       ↓
//	provider response → ProviderShim → []models.ToolCall (unified)
package toolshim

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/models"
)

// ProviderFormat identifies which format a provider expects.
type ProviderFormat string

const (
	// FormatOllamaTools uses Ollama's native /api/chat tools parameter.
	FormatOllamaTools ProviderFormat = "ollama_tools"

	// FormatOpenAICompat uses the OpenAI-compatible function calling format.
	// Works with: LM Studio, Groq, Together, Fireworks, DeepSeek, vLLM, etc.
	FormatOpenAICompat ProviderFormat = "openai_compat"

	// FormatJSONPrompt injects tool schemas into the system prompt as JSON.
	// Fallback for providers with no tool calling API at all.
	FormatJSONPrompt ProviderFormat = "json_prompt"
)

// Shim converts between unified tool definitions and provider-specific formats.
type Shim struct {
	format ProviderFormat
}

// New creates a new Shim for the given provider format.
func New(format ProviderFormat) *Shim {
	return &Shim{format: format}
}

// ConvertToolDefinitions converts models.ToolDefinition to provider-specific tool format.
func (s *Shim) ConvertToolDefinitions(tools []models.ToolDefinition) []map[string]interface{} {
	switch s.format {
	case FormatOllamaTools:
		return s.toOllamaTools(tools)
	case FormatOpenAICompat:
		return s.toOpenAITools(tools)
	default:
		return nil
	}
}

// BuildToolPrompt generates a system prompt section describing available tools.
// Used with FormatJSONPrompt or as a supplement for providers with weak tool support.
func (s *Shim) BuildToolPrompt(tools []models.ToolDefinition) string {
	if len(tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")
	sb.WriteString("You have access to the following tools. To call a tool, respond with a JSON object:\n")
	sb.WriteString("```json\n{\"tool_call\": \"<tool_name>\", \"args\": {<arguments>}}\n```\n\n")
	sb.WriteString("You may call multiple tools in one response by outputting multiple JSON objects.\n\n")

	for _, tool := range tools {
		sb.WriteString(fmt.Sprintf("### %s\n", tool.Function.Name))
		sb.WriteString(fmt.Sprintf("%s\n\n", tool.Function.Description))

		// Write parameter schema
		if props, ok := tool.Function.Parameters["properties"].(map[string]interface{}); ok {
			sb.WriteString("Parameters:\n")
			required := getRequiredFields(tool.Function.Parameters)
			for name, propRaw := range props {
				prop, ok := propRaw.(map[string]interface{})
				if !ok {
					continue
				}
				typ, _ := prop["type"].(string)
				desc, _ := prop["description"].(string)
				req := ""
				if contains(required, name) {
					req = " (required)"
				}
				sb.WriteString(fmt.Sprintf("- `%s` (%s%s): %s\n", name, typ, req, desc))
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString("IMPORTANT: Always output valid JSON for tool calls. Use double quotes for strings.\n")
	sb.WriteString("Do NOT use XML format. Only use the JSON format shown above.\n")

	return sb.String()
}

// ParseToolResponse extracts tool calls from a text response using JSON recovery.
// This is used when the provider doesn't return structured tool calls.
func (s *Shim) ParseToolResponse(text string, tools []models.ToolDefinition) []models.ToolCall {
	// Use the existing parser which already handles JSON + XML + recovery
	parsed, _ := agent.ParseToolCalls(text)
	if len(parsed) == 0 {
		return nil
	}

	var result []models.ToolCall
	for i, tc := range parsed {
		// Normalize the tool name — it might be "read_file" or "@coder" or just "read"
		name := tc.Name
		if strings.HasPrefix(name, "@") {
			name = strings.TrimPrefix(name, "@")
		}

		// Try to match against known tool definitions
		matchedName := matchToolName(name, tc.Args, tools)
		if matchedName == "" {
			matchedName = name
		}

		// Parse arguments using JSON recovery
		args := make(map[string]interface{})
		normalized, ok := agent.NormalizeToolArgs(matchedName, tc.Args)
		if ok {
			json.Unmarshal([]byte(normalized), &args)
		}

		result = append(result, models.ToolCall{
			ID:        fmt.Sprintf("shim_%d", i),
			Type:      "function",
			Name:      matchedName,
			Arguments: args,
			Raw:       tc.Raw,
		})
	}

	return result
}

// toOllamaTools converts to Ollama's native tool format.
// See: https://github.com/ollama/ollama/blob/main/docs/api.md#chat-request-with-tools
func (s *Shim) toOllamaTools(tools []models.ToolDefinition) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		result = append(result, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"parameters":  t.Function.Parameters,
			},
		})
	}
	return result
}

// toOpenAITools converts to OpenAI-compatible format.
func (s *Shim) toOpenAITools(tools []models.ToolDefinition) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tools))
	for _, t := range tools {
		result = append(result, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t.Function.Name,
				"description": t.Function.Description,
				"parameters":  t.Function.Parameters,
			},
		})
	}
	return result
}

// matchToolName tries to find the best matching tool name from the definitions.
func matchToolName(name, args string, tools []models.ToolDefinition) string {
	// Direct match
	for _, t := range tools {
		if strings.EqualFold(t.Function.Name, name) {
			return t.Function.Name
		}
	}

	// Try matching subcommand embedded in args (JSON: {"cmd":"read"})
	var jsonArgs struct {
		Cmd string `json:"cmd"`
	}
	if json.Unmarshal([]byte(args), &jsonArgs) == nil && jsonArgs.Cmd != "" {
		for _, t := range tools {
			if strings.EqualFold(t.Function.Name, jsonArgs.Cmd) {
				return t.Function.Name
			}
		}
	}

	return ""
}

// getRequiredFields extracts the "required" array from a JSON schema.
func getRequiredFields(params map[string]interface{}) []string {
	reqRaw, ok := params["required"]
	if !ok {
		return nil
	}
	reqSlice, ok := reqRaw.([]string)
	if ok {
		return reqSlice
	}
	// Handle []interface{} from JSON
	reqISlice, ok := reqRaw.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(reqISlice))
	for _, v := range reqISlice {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
