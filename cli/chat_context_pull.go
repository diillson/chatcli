/*
 * ChatCLI - Chat-mode controlled exception for context recovery.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * Curation without capability loss.
 *
 * The chat prefix carries material that is large, mostly unread, and paid
 * for on every turn: full skill bodies, the MCP tool catalog. Trimming it
 * is only safe if what leaves the prompt stays REACHABLE — otherwise the
 * saving is bought with a dumber model, which is not a saving.
 *
 * This is the third sanctioned chat exception (after ask_user and
 * knowledge), and the narrowest kind: it executes nothing, touches nothing
 * and reaches nothing outside the process. It re-reads material ChatCLI
 * itself curated out of THIS turn's prompt. Every deferral in the prompt
 * cites the exact call that brings it back, so the model is never told to
 * consult something it cannot open.
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/persona"
)

// chatContextPullEnvVar gates the exception. Read live every turn so
// /config flips it at runtime; default ON, because the curation that pays
// for itself elsewhere in the prefix depends on it.
const chatContextPullEnvVar = "CHATCLI_CHAT_CONTEXT_PULL"

// chatContextPullMaxRounds bounds how many recoveries one turn may chain.
// Three is enough for "list what activated, read the two that matter"
// without ever looping unbounded.
const chatContextPullMaxRounds = 3

// contextPullToolName is the single name the model calls, in native and
// plugin-ish spelling.
const contextPullToolName = "context_pull"

// chatContextPullEnabled reports whether the exception is on (default ON).
func chatContextPullEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatContextPullEnvVar))) {
	case "false", "0", "off", "no":
		return false
	default:
		return true
	}
}

// chatContextPullActive reports whether the exception applies to THIS turn:
// enabled and something is actually recoverable (a skill catalog to read
// from, or an MCP catalog that was summarized). With nothing to recover the
// tool is not offered at all, so its definition costs no prefix bytes.
func (cli *ChatCLI) chatContextPullActive() bool {
	if !chatContextPullEnabled() {
		return false
	}
	// MCP first: reading the connected tool summary is in-memory, while the
	// skill catalog is a directory scan. This runs several times per turn.
	return cli.contextPullHasMCP() || cli.contextPullHasSkills()
}

// contextPullHasSkills reports whether anything is installed for kind=skill
// to read. Memoized for the conversation: listing skills walks every skill
// directory and parses frontmatter, and the answer changes only when the
// user installs one. A skill installed mid-session is picked up on the next
// conversation; until then the tool simply is not offered, which disarms the
// curation and inlines everything — the safe direction.
func (cli *ChatCLI) contextPullHasSkills() bool {
	if cli == nil || cli.personaHandler == nil {
		return false
	}
	if cli.chatPullSkillsAvailable != nil {
		return *cli.chatPullSkillsAvailable
	}
	mgr := cli.personaHandler.GetManager()
	available := mgr != nil && len(mgr.ListAllSkills()) > 0
	cli.chatPullSkillsAvailable = &available
	return available
}

func (cli *ChatCLI) contextPullHasMCP() bool {
	return cli != nil && cli.mcpManager != nil && len(cli.mcpManager.GetToolsSummary()) > 0
}

// contextPullToolDefinition is the native tool-use definition offered in the
// chat decision turn.
func contextPullToolDefinition() models.ToolDefinition {
	return models.ToolDefinition{
		Type: "function",
		Function: models.ToolFunctionDef{
			Name: contextPullToolName,
			Description: "Recover context ChatCLI summarized out of this turn's prompt (read-only, local). " +
				"Use it when a block above says a body was not inlined, or when you need the exact " +
				"capabilities of an external tool before describing them. " +
				"skill: read a skill's full instructions before applying it. " +
				"skills: list every installed skill with its description. " +
				"mcp_tools: read the full catalog of external MCP tools, with descriptions.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"kind": map[string]interface{}{
						"type": "string",
						"enum": []string{"skill", "skills", "mcp_tools"},
					},
					"name": map[string]interface{}{
						"type": "string",
						"description": "kind=skill: the skill name exactly as announced. " +
							"kind=mcp_tools: optional filter, a server or tool name substring.",
					},
				},
				"required": []string{"kind"},
			},
		},
	}
}

// isContextPullToolName matches the tool name in native or plugin form.
func isContextPullToolName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == contextPullToolName || n == "@"+contextPullToolName
}

// contextPullArgs is the decoded call. Tolerant on purpose: a model that
// sends {"kind":"skill","name":"x"} and one that sends
// {"cmd":"skill","args":{"name":"x"}} must both work — strict parsing here
// only teaches the model to retry.
type contextPullArgs struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func parseContextPullArgs(argsJSON string) contextPullArgs {
	var direct struct {
		Kind  string `json:"kind"`
		Cmd   string `json:"cmd"`
		Name  string `json:"name"`
		Skill string `json:"skill"`
		Args  struct {
			Kind  string `json:"kind"`
			Name  string `json:"name"`
			Skill string `json:"skill"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &direct); err != nil {
		// Bare string ("skills") or malformed JSON: treat the payload as the kind.
		return contextPullArgs{Kind: strings.Trim(strings.TrimSpace(argsJSON), `"`)}
	}
	out := contextPullArgs{
		Kind: firstNonEmpty(direct.Kind, direct.Cmd, direct.Args.Kind),
		Name: firstNonEmpty(direct.Name, direct.Skill, direct.Args.Name, direct.Args.Skill),
	}
	out.Kind = strings.ToLower(strings.TrimSpace(out.Kind))
	out.Name = strings.TrimSpace(out.Name)
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// runChatContextPull executes one recovery. Errors come back as tool-result
// text so the model can correct the call instead of the turn failing.
func (cli *ChatCLI) runChatContextPull(argsJSON string) string {
	args := parseContextPullArgs(argsJSON)
	switch args.Kind {
	case "skill":
		return cli.pullSkillBody(args.Name)
	case "skills", "skill_list", "list":
		return cli.pullSkillCatalog()
	case "mcp_tools", "mcp", "tools":
		return cli.pullMCPCatalog(args.Name)
	case "":
		return "context_pull error: missing kind. Use kind=skill with a name, kind=skills, or kind=mcp_tools."
	default:
		return fmt.Sprintf("context_pull error: unknown kind %q. Use skill, skills or mcp_tools.", args.Kind)
	}
}

// pullSkillBody returns one skill's full instructions.
func (cli *ChatCLI) pullSkillBody(name string) string {
	if name == "" {
		return "context_pull error: kind=skill needs a name. Call kind=skills to see what is installed."
	}
	if cli.personaHandler == nil {
		return "context_pull error: no skill manager in this session."
	}
	mgr := cli.personaHandler.GetManager()
	if mgr == nil {
		return "context_pull error: no skill manager in this session."
	}
	skill, err := mgr.GetSkillByName(name)
	if err != nil || skill == nil {
		return fmt.Sprintf("context_pull: no skill named %q. Call kind=skills to see what is installed.", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Skill: %s", skill.Name)
	if skill.Version != "" {
		fmt.Fprintf(&b, " (v%s)", skill.Version)
	}
	b.WriteString("\n\n")
	if skill.Description != "" {
		b.WriteString(skill.Description + "\n\n")
	}
	body := strings.TrimSpace(skill.Content)
	if body == "" {
		b.WriteString("_This skill has no body beyond its description._\n")
		return b.String()
	}
	b.WriteString(body)
	b.WriteString("\n")
	return b.String()
}

// pullSkillCatalog lists every installed skill with its description — the
// index the model reads before deciding which body is worth a round trip.
func (cli *ChatCLI) pullSkillCatalog() string {
	if cli.personaHandler == nil {
		return "context_pull error: no skill manager in this session."
	}
	mgr := cli.personaHandler.GetManager()
	if mgr == nil {
		return "context_pull error: no skill manager in this session."
	}
	skills := mgr.ListAllSkills()
	if len(skills) == 0 {
		return "context_pull: no skills installed."
	}
	sorted := append([]*persona.Skill(nil), skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var b strings.Builder
	fmt.Fprintf(&b, "# Installed skills (%d)\n\n", len(sorted))
	for _, s := range sorted {
		fmt.Fprintf(&b, "- **%s**", s.Name)
		if d := strings.TrimSpace(s.Description); d != "" {
			fmt.Fprintf(&b, ": %s", d)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nCall context_pull with kind=skill and one of these names to read its full instructions.\n")
	return b.String()
}

// pullMCPCatalog returns the full MCP tool catalog with descriptions — what
// the chat prefix now summarizes to names only.
func (cli *ChatCLI) pullMCPCatalog(filter string) string {
	if cli.mcpManager == nil {
		return "context_pull: no MCP servers are connected in this session."
	}
	tools := cli.mcpManager.GetToolsSummary()
	if len(tools) == 0 {
		return "context_pull: no MCP servers are connected in this session."
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	var b strings.Builder
	b.WriteString("# MCP tools (full catalog)\n\n")
	shown := 0
	for _, t := range tools {
		name := t.Function.Name
		if f != "" && !strings.Contains(strings.ToLower(name), f) &&
			!strings.Contains(strings.ToLower(t.Function.Description), f) {
			continue
		}
		fmt.Fprintf(&b, "- **%s**: %s\n", name, t.Function.Description)
		shown++
	}
	if shown == 0 {
		return fmt.Sprintf("context_pull: no MCP tool matches %q. Call kind=mcp_tools with no name to see all %d.",
			filter, len(tools))
	}
	b.WriteString("\nThese run only in agent/coder mode; in chat you can describe them, not call them.\n")
	return b.String()
}

// appendContextPullRound folds one recovery into the conversation being
// built for the next decision call, mirroring the knowledge/memory rounds.
func appendContextPullRound(history []models.Message, prompt, callJSON, result string) ([]models.Message, string) {
	next := make([]models.Message, 0, len(history)+2)
	next = append(next, history...)
	next = append(next,
		models.Message{Role: "user", Content: prompt},
		models.Message{Role: "assistant", Content: "[context_pull call] " + callJSON},
	)
	followup := "context_pull result:\n" + result +
		"\n\nUse this material to answer the user. Call context_pull again only if something " +
		"essential is still missing."
	return next, followup
}

// chatContextPullXMLInstruction is appended to the decision-turn prompt for
// providers WITHOUT native tools.
func chatContextPullXMLInstruction() string {
	return "\n\n[Chat exception — context recovery is ENABLED for this turn]\n" +
		"Some material was summarized out of this prompt (skill bodies, the external tool catalog). " +
		"You normally have no tools in chat, but for THIS turn you MAY recover it, and the call WILL " +
		"be executed. If — and only if — you need something that was not inlined, reply with EXACTLY " +
		"one tag and nothing else:\n" +
		`<tool_call name="@context_pull" args='{"kind":"skill","name":"<skill name>"}' />` + "\n" +
		`Other forms: {"kind":"skills"} to list every installed skill, ` +
		`{"kind":"mcp_tools"} to read the full external tool catalog. ` +
		"You will receive the result and may call again (a few rounds) before answering. " +
		"If everything you need is already inlined, just answer normally."
}
