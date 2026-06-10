/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinKnowledgePlugin — exposes attached knowledge bases (/context attach
 * of a knowledge-mode context) as an @knowledge ReAct tool, so agent and
 * coder can interrogate a multi-megabyte corpus iteratively instead of
 * relying only on the per-turn auto-retrieved passages. Subcommands:
 *
 *   search { query, top_k?, kb? }   -> hybrid-ranked passages (keyless BM25 floor)
 *   get    { source, offset?, kb? } -> one page of a full source document
 *   toc    { prefix?, kb? }         -> table of contents (document paths)
 *   list   {}                       -> attached knowledge bases
 *
 * Like @memory, the top-level ChatCLI owns the context manager but the
 * plugin is instantiated before it, so the plugin reaches it through a
 * package-level adapter supplied via SetKnowledgeAdapter.
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

// KnowledgeAdapter is the interface the BuiltinKnowledgePlugin uses to reach
// the live context manager, bound to the current session.
type KnowledgeAdapter interface {
	// Search returns hybrid-ranked passages for query across the attached
	// knowledge bases (one of them when kb is non-empty).
	Search(query, kb string, topK int) (string, error)
	// Get returns one budget-bounded page of a source document, starting at
	// the given character offset.
	Get(source, kb string, offset int) (string, error)
	// TOC lists the source documents, optionally filtered by path prefix.
	TOC(kb, prefix string) (string, error)
	// List describes the knowledge bases attached to the session.
	List() (string, error)
}

// knowAdapterHolder mirrors memAdapterHolder: a concrete wrapper type so
// atomic.Value never sees a bare nil interface or a type switch.
type knowAdapterHolder struct{ a KnowledgeAdapter }

var knowledgeAdapterAtom atomic.Value // stores knowAdapterHolder

// SetKnowledgeAdapter wires the live adapter. Called from the top-level cli
// package once the context manager exists. Pass nil to clear it.
func SetKnowledgeAdapter(a KnowledgeAdapter) {
	knowledgeAdapterAtom.Store(knowAdapterHolder{a: a})
}

// currentKnowledgeAdapter returns the wired adapter or nil.
func currentKnowledgeAdapter() KnowledgeAdapter {
	v := knowledgeAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(knowAdapterHolder)
	return h.a
}

// BuiltinKnowledgePlugin is the @knowledge tool.
type BuiltinKnowledgePlugin struct{}

// NewBuiltinKnowledgePlugin returns a ready-to-register plugin.
func NewBuiltinKnowledgePlugin() *BuiltinKnowledgePlugin { return &BuiltinKnowledgePlugin{} }

// Name returns "@knowledge".
func (*BuiltinKnowledgePlugin) Name() string { return "@knowledge" }

// Description surfaces the tool in /plugin list and the agent tool catalog.
func (*BuiltinKnowledgePlugin) Description() string {
	return "Query the knowledge bases attached to this session (documentation corpora indexed by /context --mode knowledge). Search passages, read full documents page by page, and walk the table of contents — ground answers in the corpus and cite source paths. Ideal for authoring skills from documentation: search the topic, read the relevant documents with get, then write the skill."
}

// Usage explains the canonical invocation forms.
func (*BuiltinKnowledgePlugin) Usage() string {
	return `<tool_call name="@knowledge" args='{"cmd":"search","args":{"query":"how to configure the gateway"}}' />

Subcommands (cmd + args):
  search {query, top_k?:1-30, kb?:"<base name>"}
  get    {source:"docs/install.md", offset?:0, kb?}
  toc    {prefix?:"docs/", kb?}
  list   {}

Workflow: search to locate, get to read whole documents (paginate via the
returned next offset), toc when you need to see what exists.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinKnowledgePlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinKnowledgePlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders
// into per-subcommand flag lists with examples.
func (*BuiltinKnowledgePlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "search",
				"description": "Hybrid search (lexical + semantic when available) over the attached knowledge bases. Returns ranked passages with source citations.",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "required": true, "description": "What to look for; use the corpus' own terminology."},
					{"name": "top_k", "type": "number", "description": "Passages to return (default 8, max 30)."},
					{"name": "kb", "type": "string", "description": "Restrict to one knowledge base by name."},
				},
				"examples": []string{`{"cmd":"search","args":{"query":"gateway voice transcription env vars","top_k":10}}`},
			},
			{
				"name":        "get",
				"description": "Read one source document in full, one bounded page per call. The response reports the next offset when more remains.",
				"flags": []map[string]interface{}{
					{"name": "source", "type": "string", "required": true, "description": "Document path exactly as cited by search/toc (the part before '#')."},
					{"name": "offset", "type": "number", "description": "Character offset to continue from (default 0)."},
					{"name": "kb", "type": "string", "description": "Restrict to one knowledge base by name."},
				},
				"examples": []string{`{"cmd":"get","args":{"source":"docs/gateway.md"}}`},
			},
			{
				"name":        "toc",
				"description": "List the documents a knowledge base covers, optionally narrowed by path prefix.",
				"flags": []map[string]interface{}{
					{"name": "prefix", "type": "string", "description": "Path prefix filter, e.g. 'docs/'."},
					{"name": "kb", "type": "string", "description": "Restrict to one knowledge base by name."},
				},
				"examples": []string{`{"cmd":"toc","args":{"prefix":"guide/"}}`},
			},
			{
				"name":        "list",
				"description": "Show which knowledge bases are attached to this session and their scale.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"list"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinKnowledgePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — this plugin produces no incremental
// output, so the stream callback is ignored.
func (p *BuiltinKnowledgePlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentKnowledgeAdapter()
	if adapter == nil {
		return "", errors.New("@knowledge: no knowledge adapter wired in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@knowledge: empty args. Example: <tool_call name="@knowledge" args='{"cmd":"search","args":{"query":"..."}}' />`)
	}

	cmd, inner, err := parseKnowledgeInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@knowledge: %w", err)
	}

	switch cmd {
	case "search":
		var in struct {
			Query string `json:"query"`
			TopK  int    `json:"top_k"`
			KB    string `json:"kb"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Query) == "" {
			return "", errors.New(`@knowledge search: "query" is required`)
		}
		return adapter.Search(in.Query, in.KB, in.TopK)
	case "get":
		var in struct {
			Source string `json:"source"`
			Offset int    `json:"offset"`
			KB     string `json:"kb"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Source) == "" {
			return "", errors.New(`@knowledge get: "source" is required (a document path from search/toc)`)
		}
		return adapter.Get(in.Source, in.KB, in.Offset)
	case "toc":
		var in struct {
			Prefix string `json:"prefix"`
			KB     string `json:"kb"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		return adapter.TOC(in.KB, in.Prefix)
	case "list":
		return adapter.List()
	default:
		return "", fmt.Errorf("@knowledge: unknown cmd %q (valid: search|get|toc|list)", cmd)
	}
}

// parseKnowledgeInvocation accepts the JSON envelope {"cmd":..,"args":{..}},
// flat JSON, and the flattened argv form. Returns the canonical (cmd, innerJSON).
func parseKnowledgeInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(
				`parse envelope: %w. Expected {"cmd":"search","args":{"query":"..."}}`, err,
			)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalKnowledgeCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: search|get|toc|list)", cmdStr)
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

	canon := canonicalKnowledgeCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf(
			`expected JSON envelope or subcommand; got %q. Example: {"cmd":"search","args":{"query":"..."}}`,
			args[0],
		)
	}
	inner, err := memoryFlagsToJSON(args[1:])
	if err != nil {
		return "", "", err
	}
	return canon, inner, nil
}

// canonicalKnowledgeCmd folds aliases into the four canonical names.
func canonicalKnowledgeCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "search", "find", "query", "lookup":
		return "search"
	case "get", "read", "open", "doc", "document", "fetch":
		return "get"
	case "toc", "index", "tree", "sources", "ls":
		return "toc"
	case "list", "kbs", "bases", "status":
		return "list"
	}
	return ""
}
