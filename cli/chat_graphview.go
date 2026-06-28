/*
 * ChatCLI - Chat-mode controlled exception for @graphview.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * Chat mode is tool-less by design; ask_user and read-only knowledge retrieval
 * are its sanctioned exceptions. Interactive graph rendering is the third: the
 * user explicitly asks "draw a graph of what we discussed", so for THAT turn
 * the model may call @graphview once. It only writes a self-contained HTML file
 * and opens a viewer — it never touches the workspace — so it is a benign,
 * user-requested visualization rather than an execution/file tool. Gated by
 * CHATCLI_CHAT_GRAPHVIEW (default ON), flippable at runtime via
 * /config chat graphview on|off.
 */
package cli

import (
	"context"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

// chatGraphViewEnvVar backs the chat-mode @graphview exception. Read live every
// turn so /config flips it at runtime; default ON.
const chatGraphViewEnvVar = "CHATCLI_CHAT_GRAPHVIEW"

// chatGraphViewEnabled reports whether the chat-mode graph exception is on.
// Default ON when the env var is unset.
func chatGraphViewEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatGraphViewEnvVar))) {
	case "false", "0", "off", "no":
		return false
	default:
		return true
	}
}

// graphViewToolDefinition is the native tool-use definition offered in the chat
// decision turn. The parameters mirror the @graphview flat-JSON envelope, so the
// call arguments feed the plugin executor unchanged.
func graphViewToolDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: "graphview",
			Description: "Render an interactive, draggable force-directed graph (Obsidian-style) to a self-contained HTML " +
				"file and open it in the browser. Use it when the user asks to visualize how concepts/entities/topics " +
				"relate — especially to map out this conversation. For the conversation, extract the entities and " +
				"relations yourself and pass them as nodes/edges with source=json.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"source": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"json", "knowledge", "conversation"},
						"description": "json (default): you supply nodes/edges. knowledge: the in-core knowledge graph. conversation: a structural session graph.",
					},
					"title": map[string]interface{}{"type": "string", "description": "Graph title shown in the toolbar."},
					"theme": map[string]interface{}{"type": "string", "enum": []string{"dark", "light"}},
					"nodes": map[string]interface{}{
						"type":        "array",
						"description": "source=json: nodes as {id, label, kind?, summary?, weight?}. kind drives node color.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id":      map[string]interface{}{"type": "string"},
								"label":   map[string]interface{}{"type": "string"},
								"kind":    map[string]interface{}{"type": "string"},
								"summary": map[string]interface{}{"type": "string"},
								"weight":  map[string]interface{}{"type": "number"},
							},
							"required": []string{"id", "label"},
						},
					},
					"edges": map[string]interface{}{
						"type":        "array",
						"description": "source=json: edges as {source, target, weight?} between node ids.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"source": map[string]interface{}{"type": "string"},
								"target": map[string]interface{}{"type": "string"},
								"weight": map[string]interface{}{"type": "number"},
							},
							"required": []string{"source", "target"},
						},
					},
				},
				"required": []string{},
			},
		},
	}
}

// isGraphViewToolName matches the tool name in plugin (@graphview) or native
// (graphview) form, case-insensitively.
func isGraphViewToolName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "@graphview" || n == "graphview"
}

// runChatGraphView executes one @graphview call through the same plugin the
// agent uses. Errors come back as a tool-result string so the model can correct
// the call instead of the turn failing.
func (cli *ChatCLI) runChatGraphView(ctx context.Context, argsJSON string) string {
	out, err := plugins.NewBuiltinGraphViewPlugin().Execute(ctx, []string{argsJSON})
	if err != nil {
		return "graphview error: " + err.Error()
	}
	return out
}

// appendGraphViewRound folds the graph render into the conversation so the model
// can produce a short natural-language confirmation as its final answer.
func appendGraphViewRound(history []models.Message, prompt, callJSON, result string) ([]models.Message, string) {
	next := make([]models.Message, 0, len(history)+2)
	next = append(next, history...)
	next = append(next,
		models.Message{Role: "user", Content: prompt},
		models.Message{Role: "assistant", Content: "[graphview call] " + callJSON},
	)
	followup := "graphview result:\n" + result +
		"\n\nThe interactive graph has been generated (and opened if a display is available). " +
		"Briefly confirm to the user what was visualized and where the file is. Do not call graphview again."
	return next, followup
}

// chatGraphViewXMLInstruction is appended to the decision-turn prompt for
// providers WITHOUT native tools. It overrides the tool-less framing for this
// turn and pins the exact call format the XML parser expects.
func chatGraphViewXMLInstruction() string {
	return "\n\n[Chat exception — interactive graph rendering is ENABLED for this turn]\n" +
		"You normally have no tools in chat, but for THIS turn you MAY render an interactive graph if — and only if — " +
		"the user asked to visualize relationships (a graph/map of the conversation, concepts, topics, files…). " +
		"To do so, reply with EXACTLY one tag and nothing else:\n" +
		`<tool_call name="@graphview" args='{"title":"...","nodes":[{"id":"a","label":"A","kind":"topic"}],"edges":[{"source":"a","target":"b"}]}' />` + "\n" +
		`You may also use {"source":"knowledge"} or {"source":"conversation"} to render the in-core/structural graph. ` +
		"You will receive the result and should then briefly confirm what was visualized. " +
		"If the user did not ask for a visualization, just answer normally."
}
