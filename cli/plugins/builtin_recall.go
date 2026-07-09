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
	"regexp"
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

// ccrPrefixedKeyRe finds CCR keys in every shape models actually produce:
// the canonical "<<ccr:KEY>>" marker, a single-bracket "<ccr:KEY>", the bare
// "ccr:KEY" prefix, spacing quirks and uppercase hex. Keys are 16 lowercase
// hex chars in the store (SHA-256 prefix — see cli/compress), so hex is
// matched case-insensitively here and normalized to lowercase before lookup.
var ccrPrefixedKeyRe = regexp.MustCompile(`(?i)<{0,2}\s*ccr\s*:\s*([0-9a-f]{16})\b`)

// ccrBareKeyRe matches a candidate that is EXACTLY one key, used only as a
// fallback when nothing ccr-prefixed was found — matching naked hex inside an
// arbitrary payload would false-positive on hashes in tool output.
var ccrBareKeyRe = regexp.MustCompile(`(?i)^[0-9a-f]{16}$`)

// extractCCRKeys pulls every distinct CCR key out of a raw @recall payload,
// in first-seen order. Two tiers:
//
//  1. Anything ccr-prefixed anywhere in the payload — this works regardless
//     of JSON field names ({"key":...}, {"marker":...}, prose around the
//     marker), because the marker itself is the anchor.
//  2. When tier 1 finds nothing: the payload (or its JSON "key"/"keys"/
//     "marker"/"markers"/"ccr"/"id" fields) trimmed of quotes, backticks and
//     angle brackets, accepted only when it is exactly one bare 16-hex key.
//
// Models paste back what they saw with small mutations — "ccr:KEY" without
// the angle brackets is the most common — and a strict parser here turns a
// perfectly recoverable request into a dead end.
func extractCCRKeys(payload string) []string {
	seen := make(map[string]struct{})
	var keys []string
	add := func(k string) {
		k = strings.ToLower(k)
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		keys = append(keys, k)
	}

	for _, m := range ccrPrefixedKeyRe.FindAllStringSubmatch(payload, -1) {
		add(m[1])
	}
	if len(keys) > 0 {
		return keys
	}

	for _, candidate := range bareKeyCandidates(payload) {
		candidate = strings.Trim(strings.TrimSpace(candidate), "`'\"<> ")
		if ccrBareKeyRe.MatchString(candidate) {
			add(candidate)
		}
	}
	return keys
}

// bareKeyCandidates returns the strings that may hold a bare key: the JSON
// alias fields when the payload is an object, otherwise the payload itself.
func bareKeyCandidates(payload string) []string {
	payload = strings.TrimSpace(payload)
	if !strings.HasPrefix(payload, "{") {
		return []string{payload}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return []string{payload}
	}
	var out []string
	for _, field := range []string{"key", "keys", "marker", "markers", "ccr", "id"} {
		v, ok := raw[field]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			out = append(out, s)
			continue
		}
		var list []string
		if json.Unmarshal(v, &list) == nil {
			out = append(out, list...)
		}
	}
	return out
}

// RecallToolName is the canonical name of the @recall builtin. It is the
// single source of truth other packages compare against (e.g. the history
// trimmer's verbatim guard and the agent loop) so the name lives in one place.
const RecallToolName = "@recall"

// IsRecallTool reports whether toolName refers to the @recall builtin. Tool
// names reach callers either bare ("recall") or '@'-prefixed ("@recall") —
// GetPlugin accepts both — so the prefix is normalized before comparing.
func IsRecallTool(toolName string) bool {
	name := strings.TrimPrefix(strings.TrimSpace(toolName), "@")
	return strings.EqualFold(name, strings.TrimPrefix(RecallToolName, "@"))
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

Accepts the bare key or the full marker (THE_KEY is the 16-hex id shown in the marker):
  {"key":"THE_KEY"}
  {"key":"<<ccr:THE_KEY>>"}

Lenient on paste-back variants ("ccr:THE_KEY", uppercase hex, extra text around
the marker) and accepts multiple markers in one call — each original is
returned in a labeled section.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinRecallPlugin) Version() string { return "1.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinRecallPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinRecallPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON object {key}",
		"flags": []map[string]interface{}{
			{"name": "key", "type": "string", "required": true, "description": "The CCR key from a '<<ccr:KEY>>' marker — 16 hex chars. Bare key, 'ccr:KEY' or the full marker all accepted; multiple markers in one call return labeled sections."},
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
	keys := extractCCRKeys(payload)
	if len(keys) == 0 {
		return "", fmt.Errorf(
			`@recall: no CCR key found in %q — pass the value from a <<ccr:KEY>> marker (16 hex chars), e.g. {"key":"<<ccr:KEY>>"} or {"key":"KEY"}`,
			truncateForLog(payload, 120))
	}

	// Single key: return the original verbatim — recall's contract is a
	// byte-identical restore, and downstream guards (the compression layer's
	// recall passthrough, the history trimmer) rely on that.
	if len(keys) == 1 {
		content, ok := adapter.Recall(keys[0])
		if !ok {
			return "", fmt.Errorf("@recall: key %q not found (it may have expired or been evicted from the local store)", keys[0])
		}
		return content, nil
	}

	// Multiple keys in one call: label each section so the model can tell
	// them apart. Partial hits still return — a missing key is reported in
	// place instead of failing the whole batch.
	var b strings.Builder
	found := 0
	for i, key := range keys {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "--- @recall <<ccr:%s>> ---\n", key)
		content, ok := adapter.Recall(key)
		if !ok {
			b.WriteString("[not found — expired or evicted from the local store]")
			continue
		}
		b.WriteString(content)
		found++
	}
	if found == 0 {
		return "", fmt.Errorf("@recall: none of the %d keys were found (they may have expired or been evicted from the local store)", len(keys))
	}
	return b.String(), nil
}

// truncateForLog clamps a payload echo used in error messages.
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
