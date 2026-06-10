/*
 * ChatCLI - Chat-mode controlled exception for knowledge retrieval.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * Chat mode is tool-less by design; ask_user is its one interactive
 * exception. Knowledge retrieval is the second sanctioned exception, and the
 * same reasoning applies: it executes nothing and touches nothing — it only
 * READS the knowledge bases the user explicitly attached. With it, "attach a
 * corpus and talk about it" works in plain chat: when the per-turn
 * auto-retrieved passages are not enough, the model may pull more (search /
 * get / toc) for a bounded number of rounds before answering, without the
 * user having to switch to /agent or /coder.
 */
package cli

import (
	"context"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

// chatKnowledgeEnvVar is the env knob backing the chat-mode knowledge
// exception. Read live every turn (so /config chat knowledge on|off flips it
// at runtime); default ON.
const chatKnowledgeEnvVar = "CHATCLI_CHAT_KNOWLEDGE"

// chatKnowledgeMaxRounds bounds how many knowledge pulls one chat turn may
// chain before the model must answer — enough for search → get → get-next-page
// without ever looping unbounded.
const chatKnowledgeMaxRounds = 4

// chatKnowledgeEnabled reports whether the chat-mode knowledge exception is
// on. Default is ON when the env var is unset.
func chatKnowledgeEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatKnowledgeEnvVar))) {
	case "false", "0", "off", "no":
		return false
	case "true", "1", "on", "yes":
		return true
	default:
		return true // default ON
	}
}

// chatKnowledgeActive reports whether the exception applies to THIS turn:
// enabled and at least one knowledge base attached to the session. Without an
// attached base the chat turn stays exactly as before.
func (cli *ChatCLI) chatKnowledgeActive() bool {
	if !chatKnowledgeEnabled() || cli.contextHandler == nil {
		return false
	}
	sessionID := cli.currentSessionName
	if sessionID == "" {
		sessionID = "default"
	}
	return len(cli.contextHandler.GetManager().AttachedKnowledge(sessionID)) > 0
}

// knowledgeToolDefinition is the native tool-use definition offered in the
// chat decision turn. The parameters mirror the @knowledge JSON envelope, so
// the call arguments feed the plugin executor unchanged.
func knowledgeToolDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: "knowledge",
			Description: "Query the knowledge bases attached to this session (read-only). " +
				"Use when the auto-retrieved passages are not enough to answer: search finds ranked passages, " +
				"get reads a full document page by page, toc lists what the corpus covers.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{
						"type": "string",
						"enum": []string{"search", "get", "toc", "list"},
					},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"query":  map[string]interface{}{"type": "string", "description": "search: what to look for"},
							"top_k":  map[string]interface{}{"type": "number", "description": "search: passages to return (default 8)"},
							"source": map[string]interface{}{"type": "string", "description": "get: document path as cited by search/toc"},
							"offset": map[string]interface{}{"type": "number", "description": "get: continuation offset from a previous page"},
							"prefix": map[string]interface{}{"type": "string", "description": "toc: path prefix filter"},
							"kb":     map[string]interface{}{"type": "string", "description": "restrict to one knowledge base by name"},
						},
					},
				},
				"required": []string{"cmd"},
			},
		},
	}
}

// isKnowledgeToolName matches the tool name in plugin (@knowledge) or native
// (knowledge) form, case-insensitively.
func isKnowledgeToolName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "@knowledge" || n == "knowledge"
}

// runChatKnowledge executes one knowledge call through the same plugin the
// agent uses (global adapter). Errors come back as a tool-result string so
// the model can correct the call instead of the turn failing.
func (cli *ChatCLI) runChatKnowledge(ctx context.Context, argsJSON string) string {
	out, err := plugins.NewBuiltinKnowledgePlugin().Execute(ctx, []string{argsJSON})
	if err != nil {
		return "knowledge error: " + err.Error()
	}
	return out
}

// appendKnowledgeRound folds one pull into the conversation being built for
// the next decision call: the pending prompt becomes a user turn, the model's
// call is acknowledged, and the tool result becomes the new pending prompt.
func appendKnowledgeRound(history []models.Message, prompt, callJSON, result string) ([]models.Message, string) {
	next := make([]models.Message, 0, len(history)+2)
	next = append(next, history...)
	next = append(next,
		models.Message{Role: "user", Content: prompt},
		models.Message{Role: "assistant", Content: "[knowledge call] " + callJSON},
	)
	followup := "knowledge result:\n" + result +
		"\n\nUse this material to answer the user's question (cite source paths). " +
		"Call knowledge again only if something essential is still missing."
	return next, followup
}

// chatKnowledgeXMLInstruction is appended to the decision-turn prompt for
// providers WITHOUT native tools. It overrides the tool-less framing for this
// turn and pins the exact call format the XML parser expects.
func chatKnowledgeXMLInstruction() string {
	return "\n\n[Chat exception — knowledge retrieval is ENABLED for this turn]\n" +
		"A knowledge base is attached to this conversation. You normally have no tools in chat, " +
		"but for THIS turn you MAY query it (read-only), and the call WILL be executed. " +
		"If — and only if — the auto-retrieved passages are not enough, reply with EXACTLY one tag and nothing else:\n" +
		`<tool_call name="@knowledge" args='{"cmd":"search","args":{"query":"<what to look for>"}}' />` + "\n" +
		`Other forms: {"cmd":"get","args":{"source":"<path>","offset":0}} to read a document page by page, ` +
		`{"cmd":"toc","args":{"prefix":"docs/"}} to list coverage. ` +
		"You will receive the result and may call again (a few rounds) before answering. " +
		"If the passages already suffice, just answer normally."
}
