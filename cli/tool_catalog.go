/*
 * ChatCLI - Tool-catalog rendering and deferral policy.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The agent system prompt used to carry the FULL definition of every builtin
 * (~11k tokens across 28 tools, measured) on every turn, even though a
 * typical session touches a handful. This file implements the same deferral
 * the MCP section already applies to external servers: a small CORE set keeps
 * full definitions inline, every other builtin appears as a one-line index
 * entry, and the model pulls a full definition on demand through the @tools
 * meta-tool. External (user-installed) plugins stay fully rendered — they are
 * few and explicitly chosen.
 *
 * CHATCLI_AGENT_TOOL_CATALOG=full restores the legacy always-inline behavior.
 */
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/cli/plugins"
)

// toolCatalogEnvVar selects the catalog mode: "deferred" (default) or "full".
const toolCatalogEnvVar = "CHATCLI_AGENT_TOOL_CATALOG"

// coreToolNames always keep their full definition inline: the essential work
// loop (execution, navigation, planning, interaction) plus the @tools
// meta-tool itself — deferring the key to the catalog would be circular.
var coreToolNames = map[string]bool{
	"@coder":  true,
	"@read":   true,
	"@search": true,
	"@tree":   true,
	"@todo":   true,
	"@ask":    true,
	"@tools":  true,
}

// toolCatalogDeferred reports whether the deferred catalog is active. Read
// live so /config flips apply to the next prompt build.
func toolCatalogDeferred() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv(toolCatalogEnvVar))) != "full"
}

// isCoreTool reports whether name keeps its full inline definition.
func isCoreTool(name string) bool { return coreToolNames[name] }

// renderToolBlock renders one plugin's full prompt block — the exact shape
// the agent prompt has always used. compact (coder mode) keeps required
// flags + one example; the full form lists every flag with defaults and up
// to two examples. Shared by the prompt builder and the @tools meta-tool so
// an on-demand description is byte-identical to an inline one.
func renderToolBlock(plugin plugins.Plugin, compact bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("- Ferramenta: %s\n", plugin.Name()))
	b.WriteString(fmt.Sprintf("  Descrição: %s\n", plugin.Description()))

	if plugin.Schema() == "" {
		b.WriteString(fmt.Sprintf("  Uso: %s\n", plugin.Usage()))
		return b.String()
	}

	var schema struct {
		ArgsFormat  string `json:"argsFormat"`
		Subcommands []struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Examples    []string `json:"examples"`
			Flags       []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Type        string `json:"type"`
				Default     string `json:"default"`
				Required    bool   `json:"required"`
			} `json:"flags"`
		} `json:"subcommands"`
	}
	if err := json.Unmarshal([]byte(plugin.Schema()), &schema); err != nil {
		b.WriteString(fmt.Sprintf("  Uso: %s\n", plugin.Usage()))
		return b.String()
	}

	if schema.ArgsFormat != "" {
		b.WriteString(fmt.Sprintf("  Formato args: %s\n", schema.ArgsFormat))
	}
	if compact {
		b.WriteString("  Subcomandos:\n")
		for _, sub := range schema.Subcommands {
			b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
			var requiredFlags []string
			for _, flag := range sub.Flags {
				if flag.Required {
					requiredFlags = append(requiredFlags, fmt.Sprintf("%s (%s)", flag.Name, flag.Type))
				}
			}
			if len(requiredFlags) > 0 {
				b.WriteString("      Obrigatórios: " + strings.Join(requiredFlags, ", ") + "\n")
			}
			if len(sub.Examples) > 0 {
				b.WriteString(fmt.Sprintf("      Ex: %s\n", sub.Examples[0]))
			}
		}
		return b.String()
	}

	b.WriteString("  Subcomandos Disponíveis:\n")
	for _, sub := range schema.Subcommands {
		b.WriteString(fmt.Sprintf("    - %s: %s\n", sub.Name, sub.Description))
		if len(sub.Flags) > 0 {
			b.WriteString("      Flags:\n")
			for _, flag := range sub.Flags {
				req := ""
				if flag.Required {
					req = " [obrigatório]"
				}
				flagDesc := fmt.Sprintf("        - %s (%s)%s: %s", flag.Name, flag.Type, req, flag.Description)
				if flag.Default != "" {
					flagDesc += fmt.Sprintf(" (padrão: %s)", flag.Default)
				}
				b.WriteString(flagDesc + "\n")
			}
		}
		if len(sub.Examples) > 0 {
			limit := 2
			if len(sub.Examples) < limit {
				limit = len(sub.Examples)
			}
			b.WriteString("      Exemplos:\n")
			for i := 0; i < limit; i++ {
				b.WriteString(fmt.Sprintf("        - %s\n", sub.Examples[i]))
			}
		}
	}
	return b.String()
}

// renderToolIndexLine renders one deferred tool as a single catalog line:
// name plus the first sentence of its description. The full definition is one
// @tools describe away.
func renderToolIndexLine(plugin plugins.Plugin) string {
	return fmt.Sprintf("- %s: %s\n", plugin.Name(), firstSentence(plugin.Description()))
}

// firstSentence trims a description to its first sentence (bounded fallback
// when no terminator is found), keeping index lines one line.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".!?"); i > 0 && i < len(s)-1 {
		return s[:i+1]
	}
	const maxLen = 160
	if len(s) > maxLen {
		cut := maxLen
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut] + "…"
	}
	return s
}

// deferredCatalogInstruction tells the model how to pull a full definition.
// Model-facing English, consistent with the other prompt scaffolding.
const deferredCatalogInstruction = "The tools above are fully documented. The INDEX below lists additional " +
	"available tools by name and purpose only — before using one of them for the first time, call " +
	`<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@example"}}' /> to get its full ` +
	"subcommands, flags and examples. Do not guess an indexed tool's arguments.\n\nTool index:\n"
