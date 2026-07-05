/*
 * ChatCLI - Chat-mode controlled exception for long-term memory.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * Chat mode is tool-less by design; ask_user, knowledge retrieval and
 * graphview are its sanctioned exceptions. Memory is the fourth, and the same
 * reasoning applies: it executes nothing and touches no files outside the
 * memory store the background extractor already writes to. Without it, chat
 * could only PROMISE profile updates ("vou considerar daqui pra frente") while
 * the throttled extractor decided later whether anything persisted — the
 * model looked like a liar and the profile rotted. With it, "atualiza meu
 * perfil" works in plain chat, deterministically, the moment the user asks.
 */
package cli

import (
	"context"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/models"
)

// chatMemoryEnvVar is the env knob backing the chat-mode memory exception.
// Read live every turn (so /config chat memory on|off flips it at runtime);
// default ON.
const chatMemoryEnvVar = "CHATCLI_CHAT_MEMORY"

// chatMemoryMaxRounds bounds how many memory calls one chat turn may chain
// before the model must answer — enough for recall → targeted update →
// verification without ever looping unbounded.
const chatMemoryMaxRounds = 4

// chatMemoryEnabled reports whether the chat-mode memory exception is on.
// Default is ON when the env var is unset.
func chatMemoryEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatMemoryEnvVar))) {
	case "false", "0", "off", "no":
		return false
	case "true", "1", "on", "yes":
		return true
	default:
		return true // default ON
	}
}

// chatMemoryActive reports whether the exception applies to THIS turn:
// enabled and the session has a live memory store. Without one the chat turn
// stays exactly as before.
func (cli *ChatCLI) chatMemoryActive() bool {
	return chatMemoryEnabled() && cli.memoryStore != nil
}

// memoryToolDefinition is the native tool-use definition offered in the chat
// decision turn. The parameters mirror the @memory JSON envelope, so the call
// arguments feed the plugin executor unchanged.
func memoryToolDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: "memory",
			Description: "Persist or update the user's long-term memory and profile the moment they reveal or correct " +
				"a durable fact about themselves (role, certifications, skills, goals, interests, standing directives, milestones). " +
				"Use 'profile' for profile attributes and 'remember' for durable facts; 'recall' reads current memory first. " +
				"Profile list fields upsert by default; key suffixes rewrite: goals_replace overwrites the whole list, " +
				"goals_done/goals_remove delete matching entries (same suffixes for certifications/skills/interests/directives). " +
				"When the user completes a goal, move it: goals_done + milestone (+ certifications when a credential was earned). " +
				"NEVER claim the profile was updated without calling this tool.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cmd": map[string]interface{}{
						"type": "string",
						"enum": []string{"remember", "profile", "forget", "recall"},
					},
					"args": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"content":  map[string]interface{}{"type": "string", "description": "remember: the fact to store, one sentence"},
							"category": map[string]interface{}{"type": "string", "description": "remember: architecture|pattern|preference|gotcha|project|personal|general"},
							"fields":   map[string]interface{}{"type": "object", "description": "profile: key/value map (name, role, certifications, skills, goals, interests, directives, milestone, goals_done, goals_replace, ...)"},
							"match":    map[string]interface{}{"type": "string", "description": "forget: substring of the stored FACT to remove (profile fields use goals_remove etc.)"},
							"query":    map[string]interface{}{"type": "string", "description": "recall: topic to look up"},
						},
					},
				},
				"required": []string{"cmd"},
			},
		},
	}
}

// The tool-name matcher (isMemoryToolName) lives in moa_turn.go and is shared
// with the MoA panel's read-only memory recall.

// runChatMemory executes one memory call through the same plugin the agent
// uses (global adapter). Errors come back as a tool-result string so the
// model can correct the call instead of the turn failing.
func (cli *ChatCLI) runChatMemory(ctx context.Context, argsJSON string) string {
	out, err := plugins.NewBuiltinMemoryPlugin().Execute(ctx, []string{argsJSON})
	if err != nil {
		return "memory error: " + err.Error()
	}
	return out
}

// appendMemoryRound folds one memory call into the conversation being built
// for the next decision call: the pending prompt becomes a user turn, the
// model's call is acknowledged, and the tool result becomes the new pending
// prompt.
func appendMemoryRound(history []models.Message, prompt, callJSON, result string) ([]models.Message, string) {
	next := make([]models.Message, 0, len(history)+2)
	next = append(next, history...)
	next = append(next,
		models.Message{Role: "user", Content: prompt},
		models.Message{Role: "assistant", Content: "[memory call] " + callJSON},
	)
	followup := "memory result:\n" + result +
		"\n\nContinue: call memory again only if the update is incomplete, " +
		"then confirm to the user exactly what was persisted (never more than that)."
	return next, followup
}

// chatMemoryXMLInstruction is appended to the decision-turn prompt for
// providers WITHOUT native tools. It overrides the tool-less framing for this
// turn and pins the exact call format the XML parser expects.
func chatMemoryXMLInstruction() string {
	return "\n\n[Chat exception — long-term memory is ENABLED for this turn]\n" +
		"You normally have no tools in chat, but for THIS turn you MAY persist or update the user's " +
		"long-term memory/profile, and the call WILL be executed. Use it the moment the user reveals or " +
		"corrects a durable fact about themselves (role, certifications, skills, goals, interests, " +
		"standing directives, milestones) — NEVER claim the profile was updated without calling it. " +
		"Reply with EXACTLY one tag and nothing else:\n" +
		`<tool_call name="@memory" args='{"cmd":"profile","args":{"fields":{"certifications":"CKA"}}}' />` + "\n" +
		`Other forms: {"cmd":"remember","args":{"content":"...","category":"personal"}}, ` +
		`{"cmd":"recall","args":{"query":"..."}} to read current memory first, ` +
		`{"cmd":"forget","args":{"match":"..."}} to remove a stored fact. ` +
		"Profile list fields upsert by default; suffixes rewrite: goals_replace overwrites the list, " +
		"goals_done/goals_remove delete matching entries (same for certifications/skills/interests/directives); " +
		"milestone=... records a dated event; preferences_remove=... deletes a preference. " +
		"You will receive the result and may call again (a few rounds) before answering. " +
		"If no durable fact was revealed, just answer normally."
}
