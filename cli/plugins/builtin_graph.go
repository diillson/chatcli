/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinGraphPlugin — the @graph ReAct tool: read-only access to the in-core
 * knowledge graph (the "Obsidian in the core" substrate).
 *
 * It is the on-demand "pull" half of the index/pull split: per turn the model
 * sees only the tiny graph index card; when it needs depth it calls @graph to
 * search the graph or walk a node's local neighborhood (backlinks + links). The
 * graph itself is derived at the CLI layer from the existing memory and skill
 * stores; this plugin reaches it through a package-level GraphAdapter supplied
 * via SetGraphAdapter, mirroring @memory's wiring (no import cycle).
 *
 * Read-only by design, so — like @knowledge — it is safe to expose in chat mode.
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

// GraphAdapter is the interface the @graph tool uses to reach the live graph.
// The CLI layer implements it.
type GraphAdapter interface {
	// Index renders the map-of-content card (counts + hubs).
	Index() (string, error)
	// Search returns nodes matching the query.
	Search(query string) (string, error)
	// Neighbors returns the local graph (backlinks + links) of the node that
	// best matches idOrQuery — accepting either an exact node ID or free text.
	Neighbors(idOrQuery string) (string, error)
}

// graphAdapterHolder keeps atomic.Value's stored concrete type consistent even
// when SetGraphAdapter is passed nil (mirrors memAdapterHolder).
type graphAdapterHolder struct{ a GraphAdapter }

var graphAdapterAtom atomic.Value // stores graphAdapterHolder

// SetGraphAdapter wires the live adapter. Called from the top-level cli once the
// memory store and persona manager exist.
func SetGraphAdapter(a GraphAdapter) {
	graphAdapterAtom.Store(graphAdapterHolder{a: a})
}

func graphAdapter() GraphAdapter {
	h, _ := graphAdapterAtom.Load().(graphAdapterHolder)
	return h.a
}

// BuiltinGraphPlugin is the @graph tool.
type BuiltinGraphPlugin struct{}

// NewBuiltinGraphPlugin returns a ready-to-register plugin.
func NewBuiltinGraphPlugin() *BuiltinGraphPlugin { return &BuiltinGraphPlugin{} }

// Name returns "@graph".
func (*BuiltinGraphPlugin) Name() string { return "@graph" }

// Description surfaces the tool to the model.
func (*BuiltinGraphPlugin) Description() string {
	return "Explore the knowledge graph of what you know about the user: facts, topics, projects and skills and how they connect. Use it to pull the neighborhood of a subject (its backlinks and related notes) instead of guessing. Subcommands: index, search, neighbors."
}

// Usage explains the canonical invocation.
func (*BuiltinGraphPlugin) Usage() string {
	return `<tool_call name="@graph" args='{"cmd":"neighbors","args":{"query":"authentication"}}' />

Subcommands (cmd + args):
  index                  the graph map-of-content: node counts and hubs
  search   {query}       nodes whose title/summary match the query
  neighbors {query|id}   the local graph of the best-matching node (links + backlinks)`
}

// Version is semver.
func (*BuiltinGraphPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinGraphPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinGraphPlugin) Schema() string {
	field := func(name, typ string, req bool, desc string) map[string]interface{} {
		return map[string]interface{}{"name": name, "type": typ, "required": req, "description": desc}
	}
	schema := map[string]interface{}{
		"commands": []map[string]interface{}{
			{"cmd": "index", "args": []map[string]interface{}{}},
			{"cmd": "search", "args": []map[string]interface{}{field("query", "string", true, "keywords to match")}},
			{"cmd": "neighbors", "args": []map[string]interface{}{field("query", "string", false, "subject or exact node id"), field("id", "string", false, "exact node id")}},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute parses args and dispatches.
func (p *BuiltinGraphPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

type graphInput struct {
	Query string `json:"query"`
	ID    string `json:"id"`
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinGraphPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@graph: empty args. Example: <tool_call name="@graph" args='{"cmd":"index"}' />`)
	}
	adapter := graphAdapter()
	if adapter == nil {
		return "", errors.New("@graph: knowledge graph not available")
	}
	cmd, inner, err := parseGraphInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@graph: %w", err)
	}

	switch cmd {
	case "index":
		return adapter.Index()
	case "search":
		var in graphInput
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Query) == "" {
			return "", errors.New(`@graph search: "query" is required`)
		}
		return adapter.Search(in.Query)
	case "neighbors":
		var in graphInput
		_ = json.Unmarshal([]byte(inner), &in)
		target := strings.TrimSpace(in.ID)
		if target == "" {
			target = strings.TrimSpace(in.Query)
		}
		if target == "" {
			return "", errors.New(`@graph neighbors: "query" or "id" is required`)
		}
		return adapter.Neighbors(target)
	default:
		return "", fmt.Errorf("@graph: unknown cmd %q (valid: index|search|neighbors)", cmd)
	}
}

func parseGraphInvocation(args []string) (string, string, error) {
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
		canon := canonicalGraphCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: index|search|neighbors)", cmdStr)
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
	canon := canonicalGraphCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	inner := argvInner(args[1:], "query", nil, nil)
	return canon, inner, nil
}

func canonicalGraphCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "index", "moc", "map", "overview":
		return "index"
	case "search", "find", "query":
		return "search"
	case "neighbors", "neighbours", "neighborhood", "local", "links", "backlinks", "related":
		return "neighbors"
	}
	return ""
}
