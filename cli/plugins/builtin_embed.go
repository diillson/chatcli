/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinEmbedPlugin — text embeddings as an @embed ReAct tool.
 *
 * It exposes the configured embedding backend (CHATCLI_EMBED_PROVIDER:
 * voyage/openai/google/bedrock) directly to the model: embed texts to
 * vectors (optionally saved to JSONL for external pipelines), compare two
 * texts by cosine similarity, or rank candidate texts against a query —
 * ad-hoc semantic operations without building an index. For persistent
 * RAG over documents, the @context + @knowledge pipeline is the right
 * tool; @embed's status output points there. Self-contained — it reads
 * the backend from the environment via embedding.NewFromEnv, so no
 * adapter wiring is required.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/diillson/chatcli/llm/embedding"
)

// embedProviderFactory builds the provider from the environment; a
// package-level seam so tests can inject a deterministic fake.
var embedProviderFactory = embedding.NewFromEnv

// BuiltinEmbedPlugin is the @embed tool.
type BuiltinEmbedPlugin struct{}

// NewBuiltinEmbedPlugin returns a ready-to-register plugin.
func NewBuiltinEmbedPlugin() *BuiltinEmbedPlugin { return &BuiltinEmbedPlugin{} }

// Name returns "@embed".
func (*BuiltinEmbedPlugin) Name() string { return "@embed" }

// Description surfaces the tool.
func (*BuiltinEmbedPlugin) Description() string {
	return "Semantic text operations via the configured embedding backend (CHATCLI_EMBED_PROVIDER: voyage/openai/google/bedrock). Use 'similarity' to compare two texts, 'rank' to order candidate texts by relevance to a query (ad-hoc semantic matching, dedupe, clustering hints), 'text' to embed texts and save vectors to a JSONL file for external pipelines, 'status' to inspect the backend. For persistent RAG over documents use @context (build/attach) + @knowledge (query) instead."
}

// Usage explains the canonical invocation.
func (*BuiltinEmbedPlugin) Usage() string {
	return `<tool_call name="@embed" args='{"cmd":"similarity","args":{"a":"first text","b":"second text"}}' />

Subcommands (cmd + args):
  similarity {a, b}
       a, b   the two texts to compare (required); returns cosine similarity in [-1, 1]
  rank {query, candidates, k?}
       query       the reference text (required)
       candidates  array of texts to rank (required)
       k           optional top-k to return (default: all)
  text {text | texts, out?}
       text/texts  one text or an array of texts to embed (required)
       out         optional JSONL output path; without it only dim + count are reported
  status  show the effective embedding backend (provider, model, dimension)`
}

// Version is semver.
func (*BuiltinEmbedPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinEmbedPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinEmbedPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "similarity",
				"description": "Cosine similarity between two texts, in [-1, 1].",
				"flags": []map[string]interface{}{
					{"name": "a", "type": "string", "required": true, "description": "First text."},
					{"name": "b", "type": "string", "required": true, "description": "Second text."},
				},
				"examples": []string{`{"cmd":"similarity","args":{"a":"reset a password","b":"recover account access"}}`},
			},
			{
				"name":        "rank",
				"description": "Rank candidate texts by semantic relevance to a query.",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "required": true, "description": "Reference text."},
					{"name": "candidates", "type": "array", "required": true, "description": "Texts to rank."},
					{"name": "k", "type": "number", "required": false, "description": "Top-k to return (default: all)."},
				},
				"examples": []string{`{"cmd":"rank","args":{"query":"billing error","candidates":["invoice bug","login issue"],"k":1}}`},
			},
			{
				"name":        "text",
				"description": "Embed text(s); with out, write full vectors to a JSONL file.",
				"flags": []map[string]interface{}{
					{"name": "text", "type": "string", "required": false, "description": "One text to embed."},
					{"name": "texts", "type": "array", "required": false, "description": "Several texts to embed."},
					{"name": "out", "type": "string", "required": false, "description": "JSONL output path for the vectors."},
				},
				"examples": []string{`{"cmd":"text","args":{"texts":["chunk one","chunk two"],"out":"/tmp/vectors.jsonl"}}`},
			},
			{
				"name":        "status",
				"description": "Show the effective embedding backend.",
				"examples":    []string{`{"cmd":"status"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinEmbedPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the operation. Progress feedback is the agent
// loop's animated spinner (this tool is blocking, not streaming).
func (p *BuiltinEmbedPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@embed: empty args. Example: <tool_call name="@embed" args='{"cmd":"similarity","args":{"a":"...","b":"..."}}' />`)
	}
	cmd, inner, err := parseEmbedInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@embed: %w", err)
	}

	provider, err := embedProviderFactory()
	if err != nil {
		return "", fmt.Errorf("@embed: %w", err)
	}
	if cmd == "status" {
		if embedding.IsNull(provider) {
			return "@embed: no embedding backend configured. Set CHATCLI_EMBED_PROVIDER=voyage|openai|google|bedrock (plus the provider's API key). For persistent RAG over documents use @context + @knowledge.", nil
		}
		return fmt.Sprintf("@embed backend: %s (dimension %d)", provider.Name(), provider.Dimension()), nil
	}
	if embedding.IsNull(provider) {
		return "", errors.New("@embed: no embedding backend configured (set CHATCLI_EMBED_PROVIDER=voyage|openai|google|bedrock)")
	}

	switch cmd {
	case "similarity":
		return embedSimilarity(ctx, provider, inner)
	case "rank":
		return embedRank(ctx, provider, inner)
	case "text":
		return embedTexts(ctx, provider, inner)
	}
	return "", fmt.Errorf("@embed: unknown subcommand %q", cmd)
}

func embedSimilarity(ctx context.Context, provider embedding.Provider, inner string) (string, error) {
	var in struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	if strings.TrimSpace(in.A) == "" || strings.TrimSpace(in.B) == "" {
		return "", errors.New(`@embed similarity: "a" and "b" are required`)
	}
	vecs, err := provider.Embed(ctx, []string{in.A, in.B})
	if err != nil {
		return "", fmt.Errorf("@embed: %w", err)
	}
	sim := embedding.CosineSimilarity(vecs[0], vecs[1])
	return fmt.Sprintf("cosine similarity: %.4f (backend %s)", sim, provider.Name()), nil
}

func embedRank(ctx context.Context, provider embedding.Provider, inner string) (string, error) {
	var in struct {
		Query      string   `json:"query"`
		Candidates []string `json:"candidates"`
		K          int      `json:"k"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	if strings.TrimSpace(in.Query) == "" || len(in.Candidates) == 0 {
		return "", errors.New(`@embed rank: "query" and a non-empty "candidates" array are required`)
	}
	vecs, err := provider.Embed(ctx, append([]string{in.Query}, in.Candidates...))
	if err != nil {
		return "", fmt.Errorf("@embed: %w", err)
	}
	type scored struct {
		idx int
		sim float32
	}
	ranked := make([]scored, len(in.Candidates))
	for i := range in.Candidates {
		ranked[i] = scored{idx: i, sim: embedding.CosineSimilarity(vecs[0], vecs[i+1])}
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].sim > ranked[b].sim })
	k := in.K
	if k <= 0 || k > len(ranked) {
		k = len(ranked)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "top %d of %d candidates for %q (backend %s):\n", k, len(in.Candidates), describeTrunc(in.Query, 60), provider.Name())
	for pos := 0; pos < k; pos++ {
		r := ranked[pos]
		fmt.Fprintf(&b, "  %d. [%.4f] #%d %s\n", pos+1, r.sim, r.idx, describeTrunc(in.Candidates[r.idx], 100))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func embedTexts(ctx context.Context, provider embedding.Provider, inner string) (string, error) {
	texts, out, err := parseEmbedTextArgs(inner)
	if err != nil {
		return "", err
	}
	vecs, err := provider.Embed(ctx, texts)
	if err != nil {
		return "", fmt.Errorf("@embed: %w", err)
	}
	if out == "" {
		return fmt.Sprintf("embedded %d text(s) at dimension %d (backend %s). Pass \"out\" to save the vectors to a JSONL file.", len(vecs), len(vecs[0]), provider.Name()), nil
	}
	if err := writeVectorsJSONL(out, texts, vecs); err != nil {
		return "", fmt.Errorf("@embed: %w", err)
	}
	return fmt.Sprintf("embedded %d text(s) at dimension %d (backend %s) → %s", len(vecs), len(vecs[0]), provider.Name(), out), nil
}

// parseEmbedTextArgs accepts {"text":"..."} or {"texts":[...]} plus "out".
func parseEmbedTextArgs(inner string) ([]string, string, error) {
	var in struct {
		Text  string   `json:"text"`
		Texts []string `json:"texts"`
		Out   string   `json:"out"`
	}
	_ = json.Unmarshal([]byte(inner), &in)
	texts := in.Texts
	if len(texts) == 0 && strings.TrimSpace(in.Text) != "" {
		texts = []string{in.Text}
	}
	if len(texts) == 0 {
		return nil, "", errors.New(`@embed text: "text" or a non-empty "texts" array is required`)
	}
	return texts, strings.TrimSpace(in.Out), nil
}

// writeVectorsJSONL persists one {"index","text","embedding"} object per
// line, creating parent directories as needed.
func writeVectorsJSONL(path string, texts []string, vecs [][]float32) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	f, err := os.Create(path) // #nosec G304 -- caller-chosen output path; writing vectors where the user asked is the subcommand's purpose (same policy as @image/@diagram outputs)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	for i, v := range vecs {
		if err := enc.Encode(map[string]interface{}{"index": i, "text": texts[i], "embedding": v}); err != nil {
			return err
		}
	}
	return nil
}

// parseEmbedInvocation mirrors the other builtins' lenient envelope
// parsing: a JSON envelope {cmd, args}, flat native args, or an argv
// spelling ("similarity ..."), so a slightly-off model call still lands.
func parseEmbedInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalEmbedCmd(cmdStr)
		if canon == "" {
			canon = inferEmbedCmd(raw)
			if canon == "" {
				return "", "", fmt.Errorf("missing or unknown cmd %q (valid: similarity|rank|text|status)", cmdStr)
			}
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
	if len(args) == 0 {
		return "", "", errors.New("empty args")
	}
	canon := canonicalEmbedCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	return canon, argvInner(args[1:], "text", map[string]bool{"texts": true, "candidates": true}, map[string]bool{"k": true}), nil
}

// inferEmbedCmd resolves flat native args (no cmd) from their fields.
func inferEmbedCmd(raw map[string]json.RawMessage) string {
	has := func(k string) bool { _, ok := raw[k]; return ok }
	switch {
	case has("a") && has("b"):
		return "similarity"
	case has("query") && has("candidates"):
		return "rank"
	case has("text") || has("texts"):
		return "text"
	}
	return ""
}

func canonicalEmbedCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "similarity", "compare", "sim", "cosine":
		return "similarity"
	case "rank", "search", "match", "retrieve":
		return "rank"
	case "text", "embed", "vector", "vectors":
		return "text"
	case "status", "backend":
		return "status"
	}
	return ""
}
