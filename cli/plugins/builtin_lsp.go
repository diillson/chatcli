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

// LSPWorkspaceNavigator is an OPTIONAL capability of the wired LSPAdapter:
// the workspace-wide, read-only requests (textDocument/implementation and
// workspace/symbol). A separate interface because LSPAdapter is exported
// API — extending it would break existing implementers.
type LSPWorkspaceNavigator interface {
	// Implementation lists where the symbol at the position is implemented
	// (concrete types of an interface, overrides of a method).
	Implementation(file string, line, column int) (string, error)
	// WorkspaceSymbol searches the whole workspace for symbols matching
	// query. file is optional and only selects the language server.
	WorkspaceSymbol(file, query string, limit int) (string, error)
}

// LSPRenamer is the OPTIONAL mutating capability: textDocument/rename.
// dryRun lists every file/range that would change; false applies the edits
// to disk (inside the workspace, atomic per file).
type LSPRenamer interface {
	Rename(file string, line, column int, newName string, dryRun bool) (string, error)
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
  implementation   {file, line, column}                concrete implementations of the interface/method at the position
  workspace_symbol {query, file?, limit?}              search symbols across the whole workspace (file only picks the language server)
  rename      {file, line, column, new_name, dry_run?} rename the symbol everywhere. dry_run defaults to TRUE and only
                                                       lists the files/ranges that would change; pass "dry_run": false
                                                       to write the edits (inside the workspace, atomic per file)

Workflow: after editing a file, diagnostics tells you if it still compiles;
before changing a symbol, references shows the blast radius; symbols maps an
unfamiliar file faster than reading it; rename with dry_run first, then
dry_run:false, then diagnostics on the touched files.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinLSPPlugin) Version() string { return "1.1.0" }

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
			{
				"name":        "implementation",
				"description": "list where the symbol at file:line:column is implemented (concrete types of an interface, overrides of a method)",
				"flags":       []map[string]interface{}{fileFlag, lineFlag, colFlag},
				"examples":    []string{`{"cmd":"implementation","args":{"file":"cli/plugins/plugin.go","line":40,"column":6}}`},
			},
			{
				"name":        "workspace_symbol",
				"description": "search symbols across the whole workspace by (partial) name",
				"flags": []map[string]interface{}{
					{"name": "query", "description": "symbol name or fragment to search for", "type": "string", "required": true},
					{"name": "file", "description": "optional: any source file of the language to search — selects (and starts) the language server; omit to reuse a running one", "type": "string", "required": false},
					{"name": "limit", "description": "max symbols returned", "type": "int", "default": "50"},
				},
				"examples": []string{`{"cmd":"workspace_symbol","args":{"query":"ChatCLI","file":"cli/cli.go"}}`},
			},
			{
				"name":        "rename",
				"description": "rename the symbol at file:line:column everywhere it is referenced. dry_run (default true) only lists the files and ranges that would change; dry_run:false writes the edits to disk (workspace-bounded, atomic per file, refused if a target changed meanwhile)",
				"flags": []map[string]interface{}{fileFlag, lineFlag, colFlag,
					{"name": "new_name", "description": "the new identifier", "type": "string", "required": true},
					{"name": "dry_run", "description": "true (default) previews; false applies the edits", "type": "bool", "default": "true"},
				},
				"examples": []string{
					`{"cmd":"rename","args":{"file":"cli/cli.go","line":128,"column":14,"new_name":"handleTurn"}}`,
					`{"cmd":"rename","args":{"file":"cli/cli.go","line":128,"column":14,"new_name":"handleTurn","dry_run":false}}`,
				},
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
	case "implementation":
		nav, ok := adapter.(LSPWorkspaceNavigator)
		if !ok {
			return "", errors.New("@lsp implementation: not available in this session")
		}
		in, err := decodeLSPArgs(inner, true)
		if err != nil {
			return "", err
		}
		return nav.Implementation(in.File, in.Line, in.Column)
	case "workspace_symbol":
		nav, ok := adapter.(LSPWorkspaceNavigator)
		if !ok {
			return "", errors.New("@lsp workspace_symbol: not available in this session")
		}
		in := decodeLSPRaw(inner)
		if in.Query == "" {
			return "", errors.New(`@lsp workspace_symbol: "query" is required`)
		}
		return nav.WorkspaceSymbol(in.File, in.Query, in.Limit)
	case "rename":
		rn, ok := adapter.(LSPRenamer)
		if !ok {
			return "", errors.New("@lsp rename: not available in this session")
		}
		in, err := decodeLSPArgs(inner, true)
		if err != nil {
			return "", err
		}
		if in.NewName == "" {
			return "", errors.New(`@lsp rename: "new_name" is required`)
		}
		return rn.Rename(in.File, in.Line, in.Column, in.NewName, in.DryRun)
	default:
		return "", fmt.Errorf("@lsp: unknown cmd %q (valid: %s)", cmd, lspValidCmds)
	}
}

// lspValidCmds is the canonical verb list echoed in parse errors.
const lspValidCmds = "diagnostics|definition|references|symbols|hover|implementation|workspace_symbol|rename"

// lspArgs is the shared argument shape across subcommands.
type lspArgs struct {
	File               string
	Line               int
	Column             int
	IncludeDeclaration *bool
	Limit              int
	Query              string
	NewName            string
	// DryRun defaults to true: a rename never writes unless asked to.
	DryRun bool
}

// decodeLSPRaw reads the inner args leniently: aliased keys, stringified
// ints ("128") and stringified booleans ("false") are all accepted — the
// agent flattener and the various providers produce every one of them.
func decodeLSPRaw(inner string) lspArgs {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(inner), &raw)
	in := lspArgs{
		File:    strings.TrimSpace(jsonString(raw, "file", "path", "filepath")),
		Line:    jsonInt(raw, "line", "row"),
		Column:  jsonInt(raw, "column", "col", "character"),
		Limit:   jsonInt(raw, "limit", "max"),
		Query:   strings.TrimSpace(jsonString(raw, "query", "q", "name", "symbol")),
		NewName: strings.TrimSpace(jsonString(raw, "new_name", "newName", "new", "to")),
		DryRun:  true,
	}
	if v, present := jsonBoolLookup(raw, "include_declaration", "includeDeclaration"); present {
		in.IncludeDeclaration = &v
	}
	if v, present := jsonBoolLookup(raw, "dry_run", "dryRun", "preview"); present {
		in.DryRun = v
	}
	if v, present := jsonBoolLookup(raw, "apply", "write"); present && v {
		in.DryRun = false
	}
	return in
}

// decodeLSPArgs parses the inner args, enforcing file (always) and 1-based
// line/column (when the subcommand is positional).
func decodeLSPArgs(inner string, needsPosition bool) (lspArgs, error) {
	in := decodeLSPRaw(inner)
	if in.File == "" {
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
	case "implementation", "implementations", "impl", "impls", "implementors", "implementers":
		return "implementation"
	case "workspace_symbol", "workspace_symbols", "workspace-symbol", "workspace-symbols", "wsymbol", "search", "search_symbol", "find_symbol":
		return "workspace_symbol"
	case "rename", "refactor-rename", "rename_symbol":
		return "rename"
	default:
		return ""
	}
}

// parseLSPInvocation accepts the JSON envelope {cmd, args} or the argv form
// ("diagnostics <file>" / "definition <file> <line> <column>").
func parseLSPInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		return "", "", fmt.Errorf("missing cmd (valid: %s)", lspValidCmds)
	}

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
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: %s)", cmdStr, lspValidCmds)
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
		return "", "", fmt.Errorf("unknown cmd %q (valid: %s)", args[0], lspValidCmds)
	}
	// argv form: the agent flattener delivers the {cmd,args} envelope as
	// "--flag value" pairs; the legacy positional "file [line column]" form
	// (or "workspace_symbol <query>", "rename <file> <line> <column> <new>")
	// stays supported when no flag token is present.
	tail := args[1:]
	if !hasFlagToken(tail) {
		in := map[string]interface{}{}
		if canon == "workspace_symbol" {
			in["query"] = strings.Join(tail, " ")
		} else if len(tail) > 0 {
			in["file"] = tail[0]
		}
		if canon != "workspace_symbol" && len(tail) > 2 {
			in["line"] = atoiSafe(tail[1])
			in["column"] = atoiSafe(tail[2])
		}
		if canon == "rename" && len(tail) > 3 {
			in["new_name"] = tail[3]
		}
		b, _ := json.Marshal(in)
		return canon, string(b), nil
	}
	primary := "file"
	if canon == "workspace_symbol" {
		primary = "query"
	}
	return canon, argvInner(tail, primary, nil, map[string]bool{"line": true, "column": true, "limit": true}), nil
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
