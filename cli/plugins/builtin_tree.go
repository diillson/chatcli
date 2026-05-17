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

// BuiltinTreePlugin is the atomic directory-tree tool. Equivalent in
// spirit to the listing operations the Claude Code agent surfaces
// alongside Read+Grep. Same design as @read and @search: thin adapter
// over engine.handleTree with a flat dedicated schema.
type BuiltinTreePlugin struct{}

// NewBuiltinTreePlugin builds the @tree singleton.
func NewBuiltinTreePlugin() *BuiltinTreePlugin {
	return &BuiltinTreePlugin{}
}

// Name is the LLM-visible identifier.
func (p *BuiltinTreePlugin) Name() string { return "@tree" }

// Description is shown in the model's tool catalog.
func (p *BuiltinTreePlugin) Description() string {
	return i18n.T("plugins.tree.description")
}

// Usage is a short shell-like example for /help.
func (p *BuiltinTreePlugin) Usage() string { return "@tree <dir>" }

// Version follows semver tied to the engine contract.
func (p *BuiltinTreePlugin) Version() string { return "1.0.0" }

// Path is the builtin sentinel.
func (p *BuiltinTreePlugin) Path() string { return "[builtin]" }

// Schema is the flat JSON schema. The LLM passes {"dir":".", "depth":3}
// directly.
func (p *BuiltinTreePlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "tree",
				"description": "Show a recursive directory structure as an ASCII tree, optionally bounded by depth.",
				"flags": []map[string]interface{}{
					{"name": "dir", "type": "string", "description": "Directory to list (default: current workspace)"},
					{"name": "depth", "type": "integer", "description": "Max recursion depth (default 3)"},
					{"name": "exclude", "type": "string", "description": "Glob pattern of names to skip (e.g. 'node_modules')"},
				},
				"examples": []string{
					`{"dir":"."}`,
					`{"dir":"./src","depth":2}`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute is the legacy synchronous entry-point.
func (p *BuiltinTreePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream parses args, converts to engine argv form, runs
// engine.handleTree. Output streams line-by-line.
func (p *BuiltinTreePlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	parsed, err := parseTreeArgs(args)
	if err != nil {
		return "", err
	}

	argv := buildTreeArgv(parsed)

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
	execErr := eng.Execute(ctx, "tree", argv)

	outWriter.Flush()
	errWriter.Flush()

	if execErr != nil {
		return fullOutput.String(), fmt.Errorf("@tree failed: %w", execErr)
	}
	return fullOutput.String(), nil
}

// treeArgs is the typed view of @tree's JSON input.
type treeArgs struct {
	Dir     string
	Depth   int
	Exclude string
}

// parseTreeArgs supports both flat JSON and the @coder envelope shape.
// Empty input is valid — @tree defaults to the current directory.
func parseTreeArgs(args []string) (treeArgs, error) {
	var out treeArgs
	if len(args) == 0 {
		return out, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &raw); err != nil {
			return out, fmt.Errorf("@tree: malformed JSON args: %w", err)
		}
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if jsonErr := json.Unmarshal(inner, &innerMap); jsonErr == nil {
				raw = innerMap
			}
		}
		out.Dir = jsonString(raw, "dir", "path", "directory")
		out.Depth = jsonInt(raw, "depth", "maxDepth")
		out.Exclude = jsonString(raw, "exclude", "skip")
		return out, nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 < len(args) {
				out.Dir = args[i+1]
				i++
			}
		case "--depth":
			if i+1 < len(args) {
				out.Depth, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--exclude":
			if i+1 < len(args) {
				out.Exclude = args[i+1]
				i++
			}
		default:
			if out.Dir == "" && !strings.HasPrefix(args[i], "-") {
				out.Dir = args[i]
			}
		}
	}
	return out, nil
}

// buildTreeArgv reconstructs the engine.handleTree argv form. We
// expose intuitive names to the LLM (depth/exclude) but the engine
// uses its own conventions (max-depth/ignore) — translation lives
// here in a single site so an engine flag rename only breaks here.
func buildTreeArgv(p treeArgs) []string {
	var argv []string
	if p.Dir != "" {
		argv = append(argv, "--dir", p.Dir)
	}
	if p.Depth > 0 {
		argv = append(argv, "--max-depth", strconv.Itoa(p.Depth))
	}
	if p.Exclude != "" {
		argv = append(argv, "--ignore", p.Exclude)
	}
	return argv
}
