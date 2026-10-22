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

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/pkg/textfold"
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
	fmt.Fprintf(&b, "- Ferramenta: %s%s\n", plugin.Name(), toolVersionTag(plugin))
	fmt.Fprintf(&b, "  Descrição: %s\n", plugin.Description())

	if plugin.Schema() == "" {
		fmt.Fprintf(&b, "  Uso: %s\n", plugin.Usage())
		return b.String()
	}

	var schema struct {
		ArgsFormat  string           `json:"argsFormat"`
		Flags       []toolSchemaFlag `json:"flags"`    // tools without subcommands
		Examples    []string         `json:"examples"` // tool-level examples
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
		fmt.Fprintf(&b, "  Uso: %s\n", plugin.Usage())
		return b.String()
	}

	if schema.ArgsFormat != "" {
		fmt.Fprintf(&b, "  Formato args: %s\n", schema.ArgsFormat)
	}
	// A tool without subcommands documents its arguments at the top level;
	// before, only subcommand flags were rendered and such a tool reached the
	// model with no arguments at all.
	if len(schema.Subcommands) == 0 {
		renderTopLevelFlags(&b, schema.Flags, schema.Examples, compact)
		return b.String()
	}

	if compact {
		b.WriteString("  Subcomandos:\n")
		for _, sub := range schema.Subcommands {
			fmt.Fprintf(&b, "    - %s: %s\n", sub.Name, sub.Description)
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
				fmt.Fprintf(&b, "      Ex: %s\n", sub.Examples[0])
			}
		}
		return b.String()
	}

	b.WriteString("  Subcomandos Disponíveis:\n")
	for _, sub := range schema.Subcommands {
		fmt.Fprintf(&b, "    - %s: %s\n", sub.Name, sub.Description)
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
				fmt.Fprintf(&b, "        - %s\n", sub.Examples[i])
			}
		}
	}
	renderToolExamples(&b, schema.Examples, "  ", toolExamplesShown)
	return b.String()
}

// toolExamplesShown bounds the tool-level examples a full block carries.
const toolExamplesShown = 3

// renderTopLevelFlags renders the arguments of a tool without subcommands.
// compact keeps the required ones and one example.
func renderTopLevelFlags(b *strings.Builder, flags []toolSchemaFlag, examples []string, compact bool) {
	if compact {
		var required []string
		for _, f := range flags {
			if f.Required {
				required = append(required, fmt.Sprintf("%s (%s)", f.Name, f.Type))
			}
		}
		if len(required) > 0 {
			b.WriteString("  Obrigatórios: " + strings.Join(required, ", ") + "\n")
		}
		renderToolExamples(b, examples, "  ", 1)
		return
	}
	if len(flags) > 0 {
		b.WriteString("  Flags:\n")
		for _, f := range flags {
			req := ""
			if f.Required {
				req = " [obrigatório]"
			}
			line := fmt.Sprintf("    - %s (%s)%s: %s", f.Name, f.Type, req, f.Description)
			if f.Default != "" {
				line += fmt.Sprintf(" (padrão: %s)", f.Default)
			}
			b.WriteString(line + "\n")
		}
	}
	renderToolExamples(b, examples, "  ", toolExamplesShown)
}

// toolSchemaFlag is one argument as a builtin's Schema() declares it.
type toolSchemaFlag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default"`
	Required    bool   `json:"required"`
}

// renderToolExamples writes up to limit tool-level examples.
func renderToolExamples(b *strings.Builder, examples []string, indent string, limit int) {
	if len(examples) == 0 || limit <= 0 {
		return
	}
	if len(examples) < limit {
		limit = len(examples)
	}
	b.WriteString(indent + "Exemplos:\n")
	for _, e := range examples[:limit] {
		fmt.Fprintf(b, "%s  - %s\n", indent, e)
	}
}

// toolVersionTag renders a builtin's version for the catalog, so the model
// (and whoever reads a transcript) knows which revision of a tool it used.
func toolVersionTag(plugin plugins.Plugin) string {
	if v := strings.TrimSpace(plugin.Version()); v != "" {
		return " (v" + v + ")"
	}
	return ""
}

// renderToolIndexLine renders one deferred tool as a single catalog line:
// name, version and the first sentence of its description. The full
// definition is one @tools describe away.
func renderToolIndexLine(plugin plugins.Plugin) string {
	return fmt.Sprintf("- %s%s: %s\n", plugin.Name(), toolVersionTag(plugin), firstSentence(plugin.Description()))
}

// firstSentence trims a description to its first sentence (bounded fallback
// when no terminator is found), keeping index lines one line.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := sentenceEnd(s); i > 0 {
		return s[:i+1]
	}
	const maxLen = 160
	if len(s) > maxLen {
		cut := maxLen
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return textfold.Cut(s, cut) + "…"
	}
	return s
}

// sentenceEnd returns the index of the terminator that ends the first
// sentence of s, or -1. A terminator counts only when the sentence goes on
// with a space and a capital letter, a digit or an opening quote — so
// "e.g. foo", "go.mod", "v1.2" and a trailing "OSV." never cut the line
// short — or when it is the last character.
func sentenceEnd(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '.' && s[i] != '!' && s[i] != '?' {
			continue
		}
		if i == len(s)-1 {
			return -1 // the whole description is one sentence
		}
		if s[i+1] != ' ' {
			continue
		}
		rest := strings.TrimLeft(s[i+1:], " ")
		if rest == "" {
			return -1
		}
		switch c := rest[0]; {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '"', c == '\'', c == '(', c == '`', c >= 0x80:
			return i
		}
	}
	return -1
}

// renderMCPToolBlock renders one MCP tool's full prompt block — the MCP
// counterpart of renderToolBlock. MCP servers describe their tools with a
// JSON Schema rather than the plugin subcommand/flag shape, so the block
// carries the schema verbatim (pretty-printed) plus the invocation form.
// Shared shape with @tools describe so the model learns one format.
func renderMCPToolBlock(tool mcp.MCPTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- Ferramenta: mcp_%s (MCP, servidor: %s)\n", tool.Name, tool.ServerName)
	fmt.Fprintf(&b, "  Descrição: %s\n", tool.Description)
	fmt.Fprintf(&b, "  Invocação: <tool_call name=\"mcp_%s\" args='{...}' /> (args = JSON validado pelo schema abaixo)\n", tool.Name)
	if len(tool.Parameters) == 0 {
		b.WriteString("  Parâmetros: nenhum — invoque com args='{}'\n")
		return b.String()
	}
	schemaJSON, err := json.MarshalIndent(tool.Parameters, "    ", "  ")
	if err != nil {
		// A schema that came in over JSON-RPC always re-marshals; guard anyway.
		b.WriteString("  Parâmetros: schema indisponível — invoque e o sistema retornará o schema\n")
		return b.String()
	}
	b.WriteString("  Parâmetros (JSON Schema):\n    ")
	b.Write(schemaJSON)
	b.WriteString("\n")
	return b.String()
}

// renderMCPToolIndexLine renders one MCP tool as a single catalog line,
// mirroring renderToolIndexLine. The [MCP:server] tag matches the format the
// system-prompt MCP section already uses, so both surfaces name tools the
// same way.
func renderMCPToolIndexLine(tool mcp.MCPTool) string {
	return fmt.Sprintf("- mcp_%s [MCP:%s]: %s\n", tool.Name, tool.ServerName, firstSentence(tool.Description))
}

// deferredCatalogInstruction tells the model how to pull a full definition.
// Model-facing English, consistent with the other prompt scaffolding.
const deferredCatalogInstruction = "The tools above are fully documented. The INDEX below lists additional " +
	"available tools by name and purpose only — before using one of them for the first time, call " +
	`<tool_call name="@tools" args='{"cmd":"describe","args":{"name":"@example"}}' /> to get its full ` +
	"subcommands, flags and examples. Do not guess an indexed tool's arguments.\n\nTool index:\n"
