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
func (p *BuiltinSearchPlugin) Version() string { return "1.0.0" }

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
				"description": "Search files for a regex pattern (Go regexp syntax) and return matching lines with file:line prefixes.",
				"flags": []map[string]interface{}{
					{"name": "term", "type": "string", "required": true, "description": "Regex pattern (Go regexp syntax)"},
					{"name": "dir", "type": "string", "description": "Directory to search recursively (default: current workspace)"},
					{"name": "max_results", "type": "integer", "description": "Cap on the number of matching lines (default 200)"},
					{"name": "include", "type": "string", "description": "Glob pattern for files to include (e.g. '*.go')"},
				},
				"examples": []string{
					`{"term":"Login","dir":"./src"}`,
					`{"term":"TODO\\(.*\\)","include":"*.go"}`,
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
	return fullOutput.String(), nil
}

// searchArgs is the typed view of @search's JSON input.
type searchArgs struct {
	Term       string
	Dir        string
	MaxResults int
	Include    string
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
	argv := []string{"--term", p.Term, "--regex"}
	if p.Dir != "" {
		argv = append(argv, "--dir", p.Dir)
	}
	if p.MaxResults > 0 {
		argv = append(argv, "--max-results", strconv.Itoa(p.MaxResults))
	}
	if p.Include != "" {
		argv = append(argv, "--glob", p.Include)
	}
	return argv
}
