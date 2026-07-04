/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @lsp — semantic code navigation for the agent, backed by real language
 * servers (gopls, pyright, typescript-language-server, rust-analyzer, clangd,
 * jdtls, solargraph). Where @search finds text, @lsp understands code:
 * compiler-grade diagnostics without a build, jump-to-definition, every
 * reference to a symbol, a file's symbol outline, and hover types/docs.
 *
 * Same architecture as @knowledge/@memory: the plugin is a thin schema +
 * dispatch layer over a package-level adapter supplied via SetLSPAdapter;
 * the cli package wires the adapter over a session-scoped server pool.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// LSPAdapter is the surface the @lsp tool needs from the live session. All
// results are pre-rendered, model-facing text; positions are 1-based.
type LSPAdapter interface {
	Diagnostics(file string) (string, error)
	Definition(file string, line, column int) (string, error)
	References(file string, line, column int, includeDeclaration bool, limit int) (string, error)
	Symbols(file string) (string, error)
	Hover(file string, line, column int) (string, error)
}

type lspAdapterHolder struct{ a LSPAdapter }

var lspAdapterAtom atomic.Value // lspAdapterHolder

// SetLSPAdapter wires the live adapter. Called from the top-level cli package
// at startup; pass nil to detach.
func SetLSPAdapter(a LSPAdapter) {
	lspAdapterAtom.Store(lspAdapterHolder{a: a})
}

func currentLSPAdapter() LSPAdapter {
	v := lspAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(lspAdapterHolder)
	return h.a
}

// BuiltinLSPPlugin is the @lsp tool.
type BuiltinLSPPlugin struct{}

// NewBuiltinLSPPlugin returns a ready-to-register plugin.
func NewBuiltinLSPPlugin() *BuiltinLSPPlugin { return &BuiltinLSPPlugin{} }

// Name returns "@lsp".
func (*BuiltinLSPPlugin) Name() string { return "@lsp" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinLSPPlugin) Description() string {
	return "Semantic code navigation via real language servers (Go, Python, TypeScript/JavaScript, Rust, C/C++, Java, Ruby). Get compiler-grade diagnostics without running a build, jump to a symbol's definition, list every reference to it across the project, outline a file's symbols, and read hover type/docs. Prefer this over text search when you need to know where something is DEFINED or USED, or whether an edit broke the file."
}

// Usage explains the canonical invocation forms.
func (*BuiltinLSPPlugin) Usage() string {
	return `<tool_call name="@lsp" args='{"cmd":"diagnostics","args":{"file":"cli/cli.go"}}' />

Subcommands (cmd + args; line/column are 1-based):
  diagnostics {file}                                   errors/warnings for the file
  definition  {file, line, column}                     where the symbol at the position is defined
  references  {file, line, column, include_declaration?, limit?}  every usage of the symbol
  symbols     {file}                                   outline: functions, types, methods
  hover       {file, line, column}                     signature/type/docs at the position

Workflow: after editing a file, diagnostics tells you if it still compiles;
before changing a symbol, references shows the blast radius; symbols maps an
unfamiliar file faster than reading it.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinLSPPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinLSPPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders
// into per-subcommand flag lists with examples.
func (*BuiltinLSPPlugin) Schema() string {
	fileFlag := map[string]interface{}{"name": "file", "description": "path to the source file (workspace-relative or absolute)", "type": "string", "required": true}
	lineFlag := map[string]interface{}{"name": "line", "description": "1-based line of the symbol", "type": "int", "required": true}
	colFlag := map[string]interface{}{"name": "column", "description": "1-based column of the symbol", "type": "int", "required": true}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "diagnostics",
				"description": "compiler/linter problems for the file, without running a build",
				"flags":       []map[string]interface{}{fileFlag},
				"examples":    []string{`{"cmd":"diagnostics","args":{"file":"cli/agent_mode.go"}}`},
			},
			{
				"name":        "definition",
				"description": "resolve where the symbol at file:line:column is defined",
				"flags":       []map[string]interface{}{fileFlag, lineFlag, colFlag},
				"examples":    []string{`{"cmd":"definition","args":{"file":"cli/cli.go","line":128,"column":14}}`},
			},
			{
				"name":        "references",
				"description": "list every reference to the symbol at file:line:column across the project",
				"flags": []map[string]interface{}{fileFlag, lineFlag, colFlag,
					{"name": "include_declaration", "description": "include the declaration itself", "type": "bool", "default": "true"},
					{"name": "limit", "description": "max locations returned", "type": "int", "default": "50"},
				},
				"examples": []string{`{"cmd":"references","args":{"file":"cli/cli.go","line":128,"column":14}}`},
			},
			{
				"name":        "symbols",
				"description": "outline of the file: functions, types, methods with their lines",
				"flags":       []map[string]interface{}{fileFlag},
				"examples":    []string{`{"cmd":"symbols","args":{"file":"pkg/coder/engine/engine.go"}}`},
			},
			{
				"name":        "hover",
				"description": "signature, type and docs of the symbol at file:line:column",
				"flags":       []map[string]interface{}{fileFlag, lineFlag, colFlag},
				"examples":    []string{`{"cmd":"hover","args":{"file":"cli/cli.go","line":128,"column":14}}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinLSPPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — this plugin produces no incremental
// output, so the stream callback is ignored.
func (p *BuiltinLSPPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentLSPAdapter()
	if adapter == nil {
		return "", errors.New("@lsp: no LSP adapter wired in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@lsp: empty args. Example: <tool_call name="@lsp" args='{"cmd":"diagnostics","args":{"file":"main.go"}}' />`)
	}

	cmd, inner, err := parseLSPInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@lsp: %w", err)
	}

	switch cmd {
	case "diagnostics":
		in, err := decodeLSPArgs(inner, false)
		if err != nil {
			return "", err
		}
		return adapter.Diagnostics(in.File)
	case "definition":
		in, err := decodeLSPArgs(inner, true)
		if err != nil {
			return "", err
		}
		return adapter.Definition(in.File, in.Line, in.Column)
	case "references":
		in, err := decodeLSPArgs(inner, true)
		if err != nil {
			return "", err
		}
		includeDecl := true
		if in.IncludeDeclaration != nil {
			includeDecl = *in.IncludeDeclaration
		}
		return adapter.References(in.File, in.Line, in.Column, includeDecl, in.Limit)
	case "symbols":
		in, err := decodeLSPArgs(inner, false)
		if err != nil {
			return "", err
		}
		return adapter.Symbols(in.File)
	case "hover":
		in, err := decodeLSPArgs(inner, true)
		if err != nil {
			return "", err
		}
		return adapter.Hover(in.File, in.Line, in.Column)
	default:
		return "", fmt.Errorf("@lsp: unknown cmd %q (valid: diagnostics|definition|references|symbols|hover)", cmd)
	}
}

// lspArgs is the shared argument shape across subcommands.
type lspArgs struct {
	File               string `json:"file"`
	Line               int    `json:"line"`
	Column             int    `json:"column"`
	IncludeDeclaration *bool  `json:"include_declaration"`
	Limit              int    `json:"limit"`
}

// decodeLSPArgs parses the inner args, enforcing file (always) and 1-based
// line/column (when the subcommand is positional).
func decodeLSPArgs(inner string, needsPosition bool) (lspArgs, error) {
	var in lspArgs
	_ = json.Unmarshal([]byte(inner), &in)
	if strings.TrimSpace(in.File) == "" {
		return in, errors.New(`"file" is required`)
	}
	if needsPosition {
		if in.Line < 1 || in.Column < 1 {
			return in, errors.New(`"line" and "column" are required and 1-based`)
		}
	}
	return in, nil
}

// canonicalLSPCmd folds aliases onto the canonical subcommand names.
func canonicalLSPCmd(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "diagnostics", "diag", "diags", "check", "errors":
		return "diagnostics"
	case "definition", "def", "goto", "goto-definition":
		return "definition"
	case "references", "refs", "ref", "usages", "uses":
		return "references"
	case "symbols", "symbol", "outline", "structure":
		return "symbols"
	case "hover", "type", "signature", "doc":
		return "hover"
	default:
		return ""
	}
}

// parseLSPInvocation accepts the JSON envelope {cmd, args} or the argv form
// ("diagnostics <file>" / "definition <file> <line> <column>").
func parseLSPInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(
				`parse envelope: %w. Expected {"cmd":"diagnostics","args":{"file":"..."}}`, err,
			)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalLSPCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: diagnostics|definition|references|symbols|hover)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}

	canon := canonicalLSPCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("unknown cmd %q (valid: diagnostics|definition|references|symbols|hover)", args[0])
	}
	// argv form: the agent flattener delivers the {cmd,args} envelope as
	// "--flag value" pairs; the legacy positional "file [line column]" form
	// stays supported when no flag token is present.
	tail := args[1:]
	if !hasFlagToken(tail) {
		in := map[string]interface{}{}
		if len(tail) > 0 {
			in["file"] = tail[0]
		}
		if len(tail) > 2 {
			in["line"] = atoiSafe(tail[1])
			in["column"] = atoiSafe(tail[2])
		}
		b, _ := json.Marshal(in)
		return canon, string(b), nil
	}
	return canon, argvInner(tail, "file", nil, map[string]bool{"line": true, "column": true, "limit": true}), nil
}

// hasFlagToken reports whether any argv token is flag-shaped.
func hasFlagToken(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(strings.TrimSpace(a), "-") {
			return true
		}
	}
	return false
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
