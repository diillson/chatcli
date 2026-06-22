/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinRecallPlugin — the retrieval half of ChatCLI's reversible context
 * compression (CCR). When a tool's output is compressed, the dropped detail is
 * offloaded to a local store and a "<<ccr:KEY>>" marker is left in the prompt.
 * If the model needs the full original, it calls @recall with the key and gets
 * it back verbatim. This is what makes aggressive compression safe: nothing is
 * ever truly lost.
 *
 * Like @knowledge and @memory, the live compression layer is owned by the
 * top-level ChatCLI, so this plugin reaches it through a package-level adapter
 * wired via SetCompressionAdapter.
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

// CompressionAdapter is the interface the recall/compress builtins use to
// reach the live compression layer, bound to the current session.
type CompressionAdapter interface {
	// Recall returns the original content stored under a CCR key, or ok=false
	// when the key is unknown or has been evicted.
	Recall(key string) (content string, ok bool)
	// Compress reduces content (hint is an optional content-type or tool name)
	// and returns the compressed form. Used by the @compress tool.
	Compress(hint, content string) (string, error)
	// Stats renders a human-readable compression summary for @compress stats.
	Stats() string
}

// compAdapterHolder mirrors knowAdapterHolder: a concrete wrapper so
// atomic.Value never sees a bare nil interface.
type compAdapterHolder struct{ a CompressionAdapter }

var compressionAdapterAtom atomic.Value // stores compAdapterHolder

// SetCompressionAdapter wires the live adapter. Called from the top-level cli
// package once the compression layer exists. Pass nil to clear it.
func SetCompressionAdapter(a CompressionAdapter) {
	compressionAdapterAtom.Store(compAdapterHolder{a: a})
}

// currentCompressionAdapter returns the wired adapter or nil.
func currentCompressionAdapter() CompressionAdapter {
	v := compressionAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(compAdapterHolder)
	return h.a
}

// stripCCRMarker accepts either a bare key or a full "<<ccr:KEY>>" marker and
// returns the bare key. The model often pastes back exactly what it saw.
func stripCCRMarker(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<<ccr:")
	s = strings.TrimSuffix(s, ">>")
	return strings.TrimSpace(s)
}

// BuiltinRecallPlugin is the @recall tool.
type BuiltinRecallPlugin struct{}

// NewBuiltinRecallPlugin returns a ready-to-register plugin.
func NewBuiltinRecallPlugin() *BuiltinRecallPlugin { return &BuiltinRecallPlugin{} }

// Name returns "@recall".
func (*BuiltinRecallPlugin) Name() string { return "@recall" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinRecallPlugin) Description() string {
	return "Retrieve the full, uncompressed original of content that was compressed earlier in this session. When a tool's output shows a '<<ccr:KEY>>' marker, it means the detail was offloaded to save context; call @recall with that key to read the complete original verbatim. Use it when the compressed view omitted something you need."
}

// Usage explains the canonical invocation forms.
func (*BuiltinRecallPlugin) Usage() string {
	return `<tool_call name="@recall" args='{"key":"THE_KEY"}' />

Accepts the bare key or the full marker (THE_KEY is the hex id shown in the marker):
  {"key":"THE_KEY"}
  {"key":"<<ccr:THE_KEY>>"}`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinRecallPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinRecallPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinRecallPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON object {key}",
		"flags": []map[string]interface{}{
			{"name": "key", "type": "string", "required": true, "description": "The CCR key from a '<<ccr:KEY>>' marker (bare key or full marker both accepted)."},
		},
		"examples": []string{`{"key":"THE_KEY"}`},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinRecallPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — no incremental output.
func (p *BuiltinRecallPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentCompressionAdapter()
	if adapter == nil {
		return "", errors.New("@recall: no compression layer wired in this session")
	}
	payload := strings.TrimSpace(strings.Join(args, " "))
	key := ""
	if strings.HasPrefix(payload, "{") {
		var in struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal([]byte(payload), &in); err != nil {
			return "", fmt.Errorf(`@recall: parse args: %w. Expected {"key":"..."}`, err)
		}
		key = in.Key
	} else {
		key = payload
	}
	key = stripCCRMarker(key)
	if key == "" {
		return "", errors.New(`@recall: "key" is required (the value from a <<ccr:KEY>> marker)`)
	}
	content, ok := adapter.Recall(key)
	if !ok {
		return "", fmt.Errorf("@recall: key %q not found (it may have expired or been evicted from the local store)", key)
	}
	return content, nil
}
