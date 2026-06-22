/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinCompressPlugin — the @compress tool. Lets the model (or a workflow)
 * explicitly compress an arbitrary payload on demand: paste a large log,
 * search dump, diff or JSON array and get back the content-aware reduced form,
 * with the full original offloaded to CCR and recoverable via @recall. This is
 * the on-demand complement to the automatic compression applied to tool output
 * in the agent/coder loop.
 *
 * Reaches the live compression layer through the shared CompressionAdapter
 * (see builtin_recall.go / SetCompressionAdapter).
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// BuiltinCompressPlugin is the @compress tool.
type BuiltinCompressPlugin struct{}

// NewBuiltinCompressPlugin returns a ready-to-register plugin.
func NewBuiltinCompressPlugin() *BuiltinCompressPlugin { return &BuiltinCompressPlugin{} }

// Name returns "@compress".
func (*BuiltinCompressPlugin) Name() string { return "@compress" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinCompressPlugin) Description() string {
	return "Compress a large payload (logs, grep/search results, diffs, JSON arrays) into a content-aware, much smaller form to save context — the full original is preserved locally and recoverable with @recall. Use it before pasting bulky output into the conversation, or to shrink something you already received. Also reports session compression stats via the 'stats' subcommand."
}

// Usage explains the canonical invocation forms.
func (*BuiltinCompressPlugin) Usage() string {
	return `<tool_call name="@compress" args='{"content":"<large text>","hint":"auto"}' />

Args:
  content : the payload to compress (required for compression)
  hint    : auto | log | search | diff | json | code | prose  (optional; default auto)

Subcommand:
  {"cmd":"stats"}  -> session compression savings and CCR store footprint`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinCompressPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinCompressPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinCompressPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON object {content, hint?} or {cmd:\"stats\"}",
		"flags": []map[string]interface{}{
			{"name": "content", "type": "string", "description": "Payload to compress."},
			{"name": "hint", "type": "string", "description": "Content type: auto|log|search|diff|json|code|prose (default auto)."},
			{"name": "cmd", "type": "string", "description": "Use \"stats\" to report session compression savings instead of compressing."},
		},
		"examples": []string{
			`{"content":"<paste a long build log here>","hint":"log"}`,
			`{"cmd":"stats"}`,
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinCompressPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinCompressPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentCompressionAdapter()
	if adapter == nil {
		return "", errors.New("@compress: no compression layer wired in this session")
	}
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		return "", errors.New(`@compress: empty args. Example: {"content":"...","hint":"auto"}`)
	}

	var in struct {
		Cmd     string `json:"cmd"`
		Content string `json:"content"`
		Hint    string `json:"hint"`
	}
	if strings.HasPrefix(payload, "{") {
		if err := json.Unmarshal([]byte(payload), &in); err != nil {
			return "", fmt.Errorf(`@compress: parse args: %w. Expected {"content":"...","hint":"auto"}`, err)
		}
	} else {
		// Bare text: treat the whole payload as content with auto hint.
		in.Content = payload
	}

	if strings.EqualFold(strings.TrimSpace(in.Cmd), "stats") {
		return adapter.Stats(), nil
	}
	if strings.TrimSpace(in.Content) == "" {
		return "", errors.New(`@compress: "content" is required (or use {"cmd":"stats"})`)
	}

	hint := strings.TrimSpace(in.Hint)
	if hint == "" || strings.EqualFold(hint, "auto") {
		hint = ""
	}
	return adapter.Compress(hint, in.Content)
}
