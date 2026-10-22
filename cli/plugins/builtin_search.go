/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/coder/engine"
)

// BuiltinSearchPlugin is the atomic regex-search tool — the chatcli
// analog of Claude Code's Grep primitive. Like BuiltinReadPlugin, it
// is a thin adapter over engine.handleSearch that gives the LLM a
// flat, dedicated schema instead of forcing it through the @coder
// envelope. The legacy `@coder search` subcommand stays working.
//
// The tool advertises IsReadOnly + IsConcurrencySafe so multiple
// searches in the same turn can run in parallel batches — typical
// scenario: the model greps for an identifier in src/ and another in
// tests/ simultaneously.
type BuiltinSearchPlugin struct{}

// NewBuiltinSearchPlugin builds the @search singleton.
func NewBuiltinSearchPlugin() *BuiltinSearchPlugin {
	return &BuiltinSearchPlugin{}
}

// Name returns the LLM-visible tool name. The "@" prefix
// distinguishes it from @websearch (which hits the public internet);
// the description further disambiguates by stating the scope is
// "files in the workspace".
func (p *BuiltinSearchPlugin) Name() string { return "@search" }

// Description is shown in the model's tool catalog.
func (p *BuiltinSearchPlugin) Description() string {
	return i18n.T("plugins.search.description")
}

// Usage is a short shell-like example for /help.
func (p *BuiltinSearchPlugin) Usage() string { return "@search <regex>" }

// Version follows semver tied to the engine's contract.
func (p *BuiltinSearchPlugin) Version() string { return "1.1.0" }

// Path is the builtin sentinel.
func (p *BuiltinSearchPlugin) Path() string { return "[builtin]" }

// Schema returns the flat JSON schema the LLM uses to format calls.
// No @coder envelope — model passes {"term":"Login","dir":"./src"}
// directly.
func (p *BuiltinSearchPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "search",
				"description": "Search files for a pattern and return matching lines as file:line:col: text. Regex (Go syntax) by default, case-insensitive; literal:true matches the text as typed; context adds surrounding lines; files_only lists the files with a match count instead of every line (cheap first pass on a large tree).",
				"flags": []map[string]interface{}{
					{"name": "term", "type": "string", "required": true, "description": "Pattern (Go regexp syntax; literal text with literal:true)"},
					{"name": "dir", "type": "string", "description": "Directory to search recursively (default: current workspace)"},
					{"name": "max_results", "type": "integer", "description": "Cap on the number of matching lines (default 200)"},
					{"name": "include", "type": "string", "description": "Glob pattern for files to include (e.g. '*.go')"},
					{"name": "literal", "type": "boolean", "description": "Match term as plain text, not a regex (default false)"},
					{"name": "case_sensitive", "type": "boolean", "description": "Match case exactly (default false)"},
					{"name": "context", "type": "integer", "description": "Lines of context before and after each match (default 0)"},
					{"name": "files_only", "type": "boolean", "description": "Return only the matching files with their match counts (default false)"},
				},
				"examples": []string{
					`{"term":"Login","dir":"./src"}`,
					`{"term":"TODO\\(.*\\)","include":"*.go"}`,
					`{"term":"config.Load(","literal":true,"files_only":true}`,
					`{"term":"func Login","context":3,"case_sensitive":true}`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute is the legacy synchronous entry-point.
func (p *BuiltinSearchPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream parses the flat JSON args, converts to engine
// argv form, and runs engine.handleSearch via a fresh Engine instance.
// Output streams line-by-line through onOutput.
func (p *BuiltinSearchPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	parsed, err := parseSearchArgs(args)
	if err != nil {
		return "", err
	}
	if parsed.Term == "" {
		return "", fmt.Errorf("term required (usage: @search {\"term\":\"regex\"})")
	}

	argv := buildSearchArgv(parsed)

	var fullOutput strings.Builder
	var mu sync.Mutex
	emit := func(line string, isErr bool) {
		mu.Lock()
		defer mu.Unlock()
		if onOutput != nil {
			prefix := ""
			if isErr {
				prefix = "ERR: "
			}
			onOutput(prefix + line)
		}
		fullOutput.WriteString(line)
		fullOutput.WriteString("\n")
	}
	outWriter := engine.NewStreamWriter(func(line string) { emit(line, false) })
	errWriter := engine.NewStreamWriter(func(line string) { emit(line, true) })

	eng := engine.NewEngine(outWriter, errWriter, "")
	execErr := eng.Execute(ctx, "search", argv)

	outWriter.Flush()
	errWriter.Flush()

	if execErr != nil {
		return fullOutput.String(), fmt.Errorf("@search failed: %w", execErr)
	}
	if parsed.FilesOnly {
		return filesOnlySummary(fullOutput.String()), nil
	}
	return fullOutput.String(), nil
}

// filesOnlySummary folds a file:line:col: listing into one line per file
// with its match count, most matches first — the shape of a first pass
// over a large tree, before reading the lines of the files that matter.
func filesOnlySummary(out string) string {
	counts := map[string]int{}
	var order []string
	for _, line := range strings.Split(out, "\n") {
		path, ok := matchLinePath(line)
		if !ok {
			continue
		}
		if _, seen := counts[path]; !seen {
			order = append(order, path)
		}
		counts[path]++
	}
	if len(order) == 0 {
		return out
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	var b strings.Builder
	total := 0
	for _, p := range order {
		total += counts[p]
	}
	fmt.Fprintf(&b, "%d file(s), %d match(es):\n", len(order), total)
	for _, p := range order {
		fmt.Fprintf(&b, "%s (%d)\n", p, counts[p])
	}
	return b.String()
}

// matchLinePath returns the file of a "path:line:col:" or "path:line:"
// match line; false for context lines and anything else.
func matchLinePath(line string) (string, bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", false
	}
	rest := line[i+1:]
	j := strings.Index(rest, ":")
	if j <= 0 {
		return "", false
	}
	for _, r := range rest[:j] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return line[:i], true
}

// searchArgs is the typed view of @search's JSON input.
type searchArgs struct {
	Term          string
	Dir           string
	MaxResults    int
	Include       string
	Literal       bool
	CaseSensitive bool
	Context       int
	FilesOnly     bool
}

// parseSearchArgs converts the LLM-supplied JSON envelope into typed
// search arguments. Supports both flat (`{"term":...}`) and nested
// @coder envelope (`{"cmd":"search","args":{...}}`) shapes for
// compatibility with the legacy invocation path.
func parseSearchArgs(args []string) (searchArgs, error) {
	var out searchArgs
	if len(args) == 0 {
		return out, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &raw); err != nil {
			return out, fmt.Errorf("@search: malformed JSON args: %w", err)
		}
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if jsonErr := json.Unmarshal(inner, &innerMap); jsonErr == nil {
				raw = innerMap
			}
		}
		out.Term = jsonString(raw, "term", "pattern", "query", "regex")
		out.Dir = jsonString(raw, "dir", "path", "directory")
		out.MaxResults = jsonInt(raw, "max_results", "maxResults", "limit")
		out.Include = jsonString(raw, "include", "glob")
		out.Literal = jsonBool(raw, "literal", "fixed", "plain")
		out.CaseSensitive = jsonBool(raw, "case_sensitive", "caseSensitive")
		out.Context = jsonInt(raw, "context", "context_lines")
		out.FilesOnly = jsonBool(raw, "files_only", "filesOnly", "files")
		return out, nil
	}
	// Positional / flag form (legacy CLI path).
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--term", "--pattern":
			if i+1 < len(args) {
				out.Term = args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				out.Dir = args[i+1]
				i++
			}
		case "--max_results", "--max-results":
			if i+1 < len(args) {
				out.MaxResults, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--include":
			if i+1 < len(args) {
				out.Include = args[i+1]
				i++
			}
		case "--literal", "--fixed":
			out.Literal = flatBoolAt(args, &i)
		case "--case_sensitive", "--case-sensitive":
			out.CaseSensitive = flatBoolAt(args, &i)
		case "--context":
			if i+1 < len(args) {
				out.Context, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--files_only", "--files-only":
			out.FilesOnly = flatBoolAt(args, &i)
		default:
			if out.Term == "" && !strings.HasPrefix(args[i], "-") {
				out.Term = args[i]
			}
		}
	}
	return out, nil
}

// buildSearchArgv reconstructs the engine.handleSearch argv from the
// typed args. Engine uses --glob for the include pattern, while we
// expose "include" to the LLM (more intuitive); single conversion
// site means an engine flag rename only breaks here.
//
// We always enable --regex because the @search contract advertises
// regex semantics in its schema. The engine defaults to literal match
// when --regex is absent — exposing that subtle dual-mode to the LLM
// would invite confusion.
func buildSearchArgv(p searchArgs) []string {
	argv := []string{"--term", p.Term}
	if !p.Literal {
		argv = append(argv, "--regex")
	}
	if p.CaseSensitive {
		argv = append(argv, "--case-sensitive")
	}
	if p.Context > 0 {
		argv = append(argv, "--context", strconv.Itoa(p.Context))
	}
	if p.Dir != "" {
		argv = append(argv, "--dir", p.Dir)
	}
	limit := p.MaxResults
	if limit <= 0 {
		limit = searchDefaultMaxResults // the cap the schema promises
	}
	argv = append(argv, "--max-results", strconv.Itoa(limit))
	if p.Include != "" {
		argv = append(argv, "--glob", p.Include)
	}
	return argv
}

// searchDefaultMaxResults is the cap applied when the call sets none.
const searchDefaultMaxResults = 200

// flatBoolAt reads a boolean flag at args[*i]: a following true/false
// token is consumed, a bare flag means true.
func flatBoolAt(args []string, i *int) bool {
	if *i+1 < len(args) {
		switch strings.ToLower(args[*i+1]) {
		case "true", "1", "yes":
			*i++
			return true
		case "false", "0", "no":
			*i++
			return false
		}
	}
	return true
}
