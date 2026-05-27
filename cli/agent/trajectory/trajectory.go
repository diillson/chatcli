/*
 * Package trajectory exports a conversation history to JSONL in the
 * ShareGPT-style {"from","value"} shape widely used to train tool-calling
 * models. It is provider-agnostic: it operates on the unified
 * []models.Message history, so it works identically regardless of which of
 * the configured providers produced the turns.
 *
 * Tool calls and tool results are flattened into readable <tool_call> /
 * <tool_response> blocks so a single linear transcript captures the full
 * ReAct trajectory.
 */
package trajectory

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/diillson/chatcli/models"
)

// argsString renders a tool call's arguments (a map) as compact JSON.
func argsString(args map[string]interface{}) string {
	if len(args) == 0 {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// Turn is one ShareGPT-style record.
type Turn struct {
	From  string `json:"from"`  // human | gpt | system | tool
	Value string `json:"value"` // flattened text content
}

// roleToFrom maps internal roles to the ShareGPT vocabulary.
func roleToFrom(role string) string {
	switch strings.ToLower(role) {
	case "user":
		return "human"
	case "assistant":
		return "gpt"
	case "tool":
		return "tool"
	case "system":
		return "system"
	default:
		return role
	}
}

// ToTurns converts messages into trajectory turns. Empty messages (no text
// and no tool calls) are skipped so the transcript stays clean.
func ToTurns(msgs []models.Message) []Turn {
	turns := make([]Turn, 0, len(msgs))
	for _, m := range msgs {
		value := flatten(m)
		if strings.TrimSpace(value) == "" {
			continue
		}
		turns = append(turns, Turn{From: roleToFrom(m.Role), Value: value})
	}
	return turns
}

// flatten renders a message (content + any tool calls) to a single string.
func flatten(m models.Message) string {
	var b strings.Builder
	if c := strings.TrimSpace(m.Content); c != "" {
		b.WriteString(c)
	}
	for _, tc := range m.ToolCalls {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("<tool_call name=\"")
		b.WriteString(tc.Name)
		b.WriteString("\">")
		b.WriteString(argsString(tc.Arguments))
		b.WriteString("</tool_call>")
	}
	if m.ToolCallID != "" {
		// A tool-result message: wrap so the trajectory shows the response.
		inner := strings.TrimSpace(m.Content)
		return "<tool_response>" + inner + "</tool_response>"
	}
	return b.String()
}

// WriteJSONL writes one JSON object per line to w and returns the count.
func WriteJSONL(w io.Writer, msgs []models.Message) (int, error) {
	bw := bufio.NewWriter(w)
	turns := ToTurns(msgs)
	for _, t := range turns {
		line, err := json.Marshal(t)
		if err != nil {
			return 0, err
		}
		if _, err := bw.Write(line); err != nil {
			return 0, err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return 0, err
		}
	}
	return len(turns), bw.Flush()
}
