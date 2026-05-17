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

// BuiltinReadPlugin is the atomic read tool — equivalent to Claude Code's
// Read primitive. It exposes a dedicated, flat-schema interface to the
// LLM ("@read") rather than requiring the model to remember the @coder
// envelope (`{"cmd":"read","args":{"file":"x"}}`). The legacy @coder
// read subcommand stays operational for backwards compatibility; both
// paths funnel into the same engine.handleRead implementation.
//
// Why split this out from @coder:
//
//   - A narrow tool with one job and one schema gives the model a
//     cleaner choice surface (Claude Code-style).
//   - Read is unambiguously read-only and concurrency-safe, so the
//     orchestrator's partition policy can batch multiple @read calls
//     in parallel — which @coder couldn't claim because of its
//     write/exec subcommands.
//   - The DescribeCall spinner can show the file path directly with
//     no envelope-unwrap heuristics.
type BuiltinReadPlugin struct{}

// NewBuiltinReadPlugin builds the @read singleton.
func NewBuiltinReadPlugin() *BuiltinReadPlugin {
	return &BuiltinReadPlugin{}
}

// Name returns the LLM-visible tool name.
func (p *BuiltinReadPlugin) Name() string { return "@read" }

// Description is the one-liner shown in the tool catalog the LLM
// sees in its system prompt. i18n-resolved at startup.
func (p *BuiltinReadPlugin) Description() string {
	return i18n.T("plugins.read.description")
}

// Usage is the short shell-like example the user sees in /help.
func (p *BuiltinReadPlugin) Usage() string { return "@read <file>" }

// Version follows semver for the plugin; tied to the engine's contract.
func (p *BuiltinReadPlugin) Version() string { return "1.0.0" }

// Path is a sentinel matching the existing builtin convention.
func (p *BuiltinReadPlugin) Path() string { return "[builtin]" }

// Schema returns the JSON schema the LLM uses to format tool calls.
// Flat shape on purpose — no @coder envelope; the LLM passes
// {"file":"main.go","from_line":10,"to_line":50} directly.
func (p *BuiltinReadPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON",
		"subcommands": []map[string]interface{}{
			{
				"name":        "read",
				"description": "Read file contents (optionally line range, head, tail, or base64 encoded)",
				"flags": []map[string]interface{}{
					{"name": "file", "type": "string", "required": true, "description": "Absolute or workspace-relative file path"},
					{"name": "from_line", "type": "integer", "description": "1-indexed starting line (inclusive)"},
					{"name": "to_line", "type": "integer", "description": "1-indexed ending line (inclusive)"},
					{"name": "head", "type": "integer", "description": "Read only the first N lines"},
					{"name": "tail", "type": "integer", "description": "Read only the last N lines"},
					{"name": "max_bytes", "type": "integer", "description": "Truncate after this many bytes (default 200000)"},
					{"name": "encoding", "type": "string", "description": "text|base64 (default text)"},
				},
				"examples": []string{
					`{"file":"main.go"}`,
					`{"file":"main.go","from_line":10,"to_line":50}`,
					`{"file":"image.png","encoding":"base64"}`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute is the legacy synchronous entry-point.
func (p *BuiltinReadPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream parses the flat JSON args into the engine's argv
// form and dispatches to engine.handleRead via a fresh Engine instance.
// Output is streamed line-by-line through onOutput when present.
func (p *BuiltinReadPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	parsed, err := parseReadArgs(args)
	if err != nil {
		return "", err
	}
	if parsed.File == "" {
		return "", fmt.Errorf("file required (usage: @read {\"file\":\"path\"})")
	}

	argv := buildReadArgv(parsed)

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
	execErr := eng.Execute(ctx, "read", argv)

	outWriter.Flush()
	errWriter.Flush()

	if execErr != nil {
		return fullOutput.String(), fmt.Errorf("@read failed: %w", execErr)
	}
	return fullOutput.String(), nil
}

// readArgs is the typed view of @read's JSON input. Keeping this
// separate from the JSON unmarshal call makes the dispatch logic
// easier to test (no engine spin-up needed).
type readArgs struct {
	File     string
	FromLine int
	ToLine   int
	Head     int
	Tail     int
	MaxBytes int
	Encoding string
}

// parseReadArgs converts the LLM-supplied JSON envelope into a typed
// readArgs. Accepts:
//
//   - JSON in the first arg: `{"file":"x","from_line":10}` (preferred).
//   - Positional + flag mix: `--file x --from_line 10` (compat with the
//     CLI invocation path so /read at the user prompt still works).
//
// Returns a zero-value readArgs and nil error when args is empty so
// callers can fail with a clear "file required" message rather than a
// parse error.
func parseReadArgs(args []string) (readArgs, error) {
	var out readArgs
	if len(args) == 0 {
		return out, nil
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(first), &raw); err != nil {
			return out, fmt.Errorf("@read: malformed JSON args: %w", err)
		}
		// Support both flat (`{"file":...}`) and nested @coder envelope
		// (`{"cmd":"read","args":{...}}`) shapes. The nested case lets
		// us reuse a single parser for legacy @coder read invocations.
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if jsonErr := json.Unmarshal(inner, &innerMap); jsonErr == nil {
				raw = innerMap
			}
		}
		out.File = jsonString(raw, "file", "path", "filepath")
		out.FromLine = jsonInt(raw, "from_line", "start", "fromLine")
		out.ToLine = jsonInt(raw, "to_line", "end", "toLine")
		out.Head = jsonInt(raw, "head")
		out.Tail = jsonInt(raw, "tail")
		out.MaxBytes = jsonInt(raw, "max_bytes", "maxBytes")
		out.Encoding = jsonString(raw, "encoding")
		return out, nil
	}
	// Positional / flag form. Scan pairs.
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if i+1 < len(args) {
				out.File = args[i+1]
				i++
			}
		case "--from_line", "--start":
			if i+1 < len(args) {
				out.FromLine, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--to_line", "--end":
			if i+1 < len(args) {
				out.ToLine, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--head":
			if i+1 < len(args) {
				out.Head, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--tail":
			if i+1 < len(args) {
				out.Tail, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--max_bytes", "--max-bytes":
			if i+1 < len(args) {
				out.MaxBytes, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--encoding":
			if i+1 < len(args) {
				out.Encoding = args[i+1]
				i++
			}
		default:
			// Free-floating positional → treat as file when nothing else
			// has matched yet.
			if out.File == "" && !strings.HasPrefix(args[i], "-") {
				out.File = args[i]
			}
		}
	}
	return out, nil
}

// buildReadArgv translates the typed args back into the argv form the
// engine.handleRead flag parser expects. Keeping this conversion small
// and explicit means a flag rename in the engine breaks at exactly
// this one site and not in twelve places.
func buildReadArgv(p readArgs) []string {
	argv := []string{"--file", p.File}
	if p.FromLine > 0 {
		argv = append(argv, "--start", strconv.Itoa(p.FromLine))
	}
	if p.ToLine > 0 {
		argv = append(argv, "--end", strconv.Itoa(p.ToLine))
	}
	if p.Head > 0 {
		argv = append(argv, "--head", strconv.Itoa(p.Head))
	}
	if p.Tail > 0 {
		argv = append(argv, "--tail", strconv.Itoa(p.Tail))
	}
	if p.MaxBytes > 0 {
		argv = append(argv, "--max-bytes", strconv.Itoa(p.MaxBytes))
	}
	if p.Encoding != "" {
		argv = append(argv, "--encoding", p.Encoding)
	}
	return argv
}
