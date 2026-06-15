/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinContextPlugin — @context: autonomous knowledge-base management.
 *
 * Gives the agent the same kind of self-service power it has for skills
 * (@skill), but for KNOWLEDGE: it can build a context/knowledge base from a
 * source it discovered (a corpus.jsonl produced by @docs-flatten, a local
 * docs directory, or files), attach it to the session — optionally with
 * semantic RAG retrieval — and then query it with @knowledge, all without the
 * user having to run /context by hand.
 *
 * The typical autonomous pipeline is:
 *   1. recognize a knowledge gap (an unfamiliar library/API);
 *   2. locate the source — @websearch for the official docs repo/site, or a
 *      path/repo/URL the user gave;
 *   3. flatten it with @docs-flatten (root=<dir> | repo=<git> | url=<site>)
 *      into a corpus.jsonl;
 *   4. @context create <name> <corpus.jsonl> --mode knowledge;
 *   5. @context attach <name> (--rag when embeddings are configured);
 *   6. @knowledge search/get to ground the answer.
 *
 * The user keeps full visibility and control through the /context slash
 * command (list / attached / status / detach / delete) and can ask the agent
 * to detach via @context detach.
 */
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/diillson/chatcli/i18n"
)

// ContextAdapter is the interface BuiltinContextPlugin uses to reach the live
// context manager, bound to the current session. The cli package provides the
// concrete implementation and wires it via SetContextAdapter at startup.
type ContextAdapter interface {
	// Create builds (and persists) a named context from one or more sources
	// (a corpus.jsonl from @docs-flatten, a local directory, or files) in the
	// given mode (default "knowledge"). Returns an LLM-readable summary.
	Create(name, mode string, paths []string, description string, force bool) (string, error)
	// Update re-ingests/modifies an existing context. Empty fields keep the
	// current value (mode/description); empty paths keep the current sources.
	Update(name string, paths []string, mode, description string, tags []string) (string, error)
	// Attach attaches a named context to the session. ragTopK > 0 turns on
	// semantic top-K retrieval (hybrid when embeddings are configured);
	// ragTopK <= 0 leaves the mode's default behavior. priority orders
	// multiple attachments (lower first).
	Attach(name string, ragTopK, priority int) (string, error)
	// Detach removes a named context from the session.
	Detach(name string) (string, error)
	// List describes every available context (attached or not).
	List() (string, error)
	// Show renders one context's metadata (mode, size, tags, provenance).
	Show(name string) (string, error)
	// Inspect renders a deeper view of one context — its files/chunks, and the
	// content of a specific chunk when chunk > 0.
	Inspect(name string, chunk int) (string, error)
	// Merge combines sources into a new named context.
	Merge(name string, sources []string, description string) (string, error)
	// Status describes what is attached to the current session.
	Status() (string, error)
	// Export writes a context to a portable file at path.
	Export(name, path string) (string, error)
	// Import loads a context from a file at path.
	Import(path string) (string, error)
	// Metrics summarizes the context store (counts, sizes, modes).
	Metrics() (string, error)
	// Delete permanently removes a context.
	Delete(name string) (string, error)
}

// ctxAdapterHolder wraps the interface so atomic.Value never sees a bare nil
// interface or a type switch (mirrors knowAdapterHolder).
type ctxAdapterHolder struct{ a ContextAdapter }

var contextAdapterAtom atomic.Value // stores ctxAdapterHolder

// SetContextAdapter wires the live adapter. Called from the cli package once
// the context manager exists. Pass nil to clear it.
func SetContextAdapter(a ContextAdapter) {
	contextAdapterAtom.Store(ctxAdapterHolder{a: a})
}

// currentContextAdapter returns the wired adapter or nil.
func currentContextAdapter() ContextAdapter {
	v := contextAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(ctxAdapterHolder)
	return h.a
}

// BuiltinContextPlugin is the @context tool.
type BuiltinContextPlugin struct{}

// NewBuiltinContextPlugin returns a ready-to-register plugin.
func NewBuiltinContextPlugin() *BuiltinContextPlugin { return &BuiltinContextPlugin{} }

// Name returns "@context".
func (*BuiltinContextPlugin) Name() string { return "@context" }

// Description surfaces the tool in the catalog.
func (*BuiltinContextPlugin) Description() string {
	return i18n.T("plugins.context.description")
}

// Usage explains the canonical invocation.
func (*BuiltinContextPlugin) Usage() string {
	return `<tool_call name="@context" args='{"cmd":"create","args":{"name":"react-docs","paths":["/tmp/react.jsonl"],"mode":"knowledge"}}' />

Subcommands (cmd + args):
  create  {name, paths[], mode?, description?, force?}  build a context/knowledge
          base. mode defaults to "knowledge" (retrieval-first). paths is a
          corpus.jsonl (from @docs-flatten), a directory, or files.
  attach  {name, rag?, priority?}  attach to the session. rag (bool or int K)
          turns on semantic retrieval; priority orders multiple attachments.
  detach  {name}                   remove an attachment from the session.
  list                             list every available context.
  status                           show what is attached to this session.
  delete  {name}                   permanently delete a context.

Autonomous docs pipeline: @websearch (find the docs source) → @docs-flatten
(root/repo/url → corpus.jsonl) → @context create … --mode knowledge →
@context attach … → @knowledge (search/get) to ground the answer.`
}

// Version is semver.
func (*BuiltinContextPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinContextPlugin) Path() string { return "" }

// Schema describes the subcommands for the LLM catalog.
func (*BuiltinContextPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "create",
				"description": "Build a context/knowledge base from a source you located. Use mode=knowledge (default) for a retrieval-first base from a corpus.jsonl produced by @docs-flatten, a docs directory, or files. This is how you give yourself documentation you lack.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Context name, e.g. 'react-docs'."},
					{"name": "paths", "type": "array", "required": true, "description": "Sources: a corpus.jsonl from @docs-flatten, a directory, or files."},
					{"name": "mode", "type": "string", "description": "knowledge (default, retrieval-first) | full | summary | chunked | smart."},
					{"name": "description", "type": "string", "description": "Optional human description."},
					{"name": "force", "type": "boolean", "description": "Overwrite an existing context with the same name."},
				},
				"examples": []string{
					`{"cmd":"create","args":{"name":"react-docs","paths":["/tmp/react.jsonl"],"mode":"knowledge"}}`,
					`{"cmd":"create","args":{"name":"projdocs","paths":["./docs"],"mode":"knowledge"}}`,
				},
			},
			{
				"name":        "attach",
				"description": "Attach a context to the session so it grounds subsequent answers. For knowledge mode, retrieval is automatic (keyless BM25 + embeddings when configured). Pass rag to force/size semantic top-K retrieval.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Context name to attach."},
					{"name": "rag", "type": "integer", "description": "Top-K passages for semantic retrieval (e.g. 8). Omit for the mode default."},
					{"name": "priority", "type": "integer", "description": "Order among attachments (lower first). Default 100."},
				},
				"examples": []string{`{"cmd":"attach","args":{"name":"react-docs"}}`, `{"cmd":"attach","args":{"name":"react-docs","rag":8}}`},
			},
			{
				"name":        "detach",
				"description": "Remove a context from the session (it stays on disk; re-attach later).",
				"flags":       []map[string]interface{}{{"name": "name", "type": "string", "required": true, "description": "Context name to detach."}},
				"examples":    []string{`{"cmd":"detach","args":{"name":"react-docs"}}`},
			},
			{
				"name":        "list",
				"description": "List every available context (attached or not), with mode and size.",
				"examples":    []string{`{"cmd":"list"}`},
			},
			{
				"name":        "status",
				"description": "Show which contexts are attached to THIS session and their footprint.",
				"examples":    []string{`{"cmd":"status"}`},
			},
			{
				"name":        "delete",
				"description": "Permanently delete a context from disk.",
				"flags":       []map[string]interface{}{{"name": "name", "type": "string", "required": true, "description": "Context name to delete."}},
				"examples":    []string{`{"cmd":"delete","args":{"name":"react-docs"}}`},
			},
			{
				"name":        "update",
				"description": "Re-ingest or modify an existing context. Pass only the fields to change; omitted ones keep their current value.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Context name to update."},
					{"name": "paths", "type": "array", "description": "New sources to re-ingest (replaces the previous ones). Omit to keep them."},
					{"name": "mode", "type": "string", "description": "New mode. Omit to keep."},
					{"name": "description", "type": "string", "description": "New description. Omit to keep."},
					{"name": "tags", "type": "array", "description": "New tags. Omit to keep."},
				},
				"examples": []string{`{"cmd":"update","args":{"name":"react-docs","paths":["/tmp/react-v19.jsonl"]}}`},
			},
			{
				"name":        "show",
				"description": "Show one context's metadata: mode, size, document/passage count, tags, provenance and timestamps.",
				"flags":       []map[string]interface{}{{"name": "name", "type": "string", "required": true, "description": "Context name."}},
				"examples":    []string{`{"cmd":"show","args":{"name":"react-docs"}}`},
			},
			{
				"name":        "inspect",
				"description": "Deeper view of a context: its documents/chunks, and the content of one chunk when chunk>0.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Context name."},
					{"name": "chunk", "type": "integer", "description": "1-based chunk number to dump in full. Omit for the overview."},
				},
				"examples": []string{`{"cmd":"inspect","args":{"name":"react-docs"}}`, `{"cmd":"inspect","args":{"name":"react-docs","chunk":2}}`},
			},
			{
				"name":        "merge",
				"description": "Combine two or more existing contexts into a new one (deduplicated).",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Name of the merged context."},
					{"name": "sources", "type": "array", "required": true, "description": "Context names to merge (>= 2)."},
					{"name": "description", "type": "string", "description": "Optional description."},
				},
				"examples": []string{`{"cmd":"merge","args":{"name":"all-docs","sources":["react-docs","next-docs"]}}`},
			},
			{
				"name":        "export",
				"description": "Write a context to a portable file so it can be shared or backed up.",
				"flags": []map[string]interface{}{
					{"name": "name", "type": "string", "required": true, "description": "Context name to export."},
					{"name": "path", "type": "string", "required": true, "description": "Destination file path."},
				},
				"examples": []string{`{"cmd":"export","args":{"name":"react-docs","path":"/tmp/react-docs.json"}}`},
			},
			{
				"name":        "import",
				"description": "Load a context from a file previously produced by export.",
				"flags":       []map[string]interface{}{{"name": "path", "type": "string", "required": true, "description": "Source file path."}},
				"examples":    []string{`{"cmd":"import","args":{"path":"/tmp/react-docs.json"}}`},
			},
			{
				"name":        "metrics",
				"description": "Summarize the context store: total contexts, how many are attached, total size and a per-mode breakdown.",
				"examples":    []string{`{"cmd":"metrics"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the invocation and dispatches to the adapter.
func (p *BuiltinContextPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the subcommand. The streaming callback is unused —
// context operations are short — but the signature satisfies the contract.
func (p *BuiltinContextPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentContextAdapter()
	if adapter == nil {
		return "", fmt.Errorf("@context: context management not available in this session")
	}
	cmd, raw, err := parseContextInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@context: %w", err)
	}

	switch cmd {
	case "create":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context create: \"name\" is required")
		}
		paths := contextStringSlice(raw, "paths", "path", "source", "corpus")
		if len(paths) == 0 {
			return "", fmt.Errorf("@context create: at least one source path is required (a corpus.jsonl, directory or file)")
		}
		mode := strings.TrimSpace(jsonString(raw, "mode"))
		if mode == "" {
			mode = "knowledge"
		}
		out, cerr := adapter.Create(name, mode, paths, jsonString(raw, "description", "desc"), jsonBool(raw, "force"))
		return wrapContextErr("create", out, cerr)
	case "update":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context update: \"name\" is required")
		}
		out, uerr := adapter.Update(name,
			contextStringSlice(raw, "paths", "path", "source"),
			jsonString(raw, "mode"),
			jsonString(raw, "description", "desc"),
			contextStringSlice(raw, "tags", "tag"))
		return wrapContextErr("update", out, uerr)
	case "show":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context show: \"name\" is required")
		}
		out, serr := adapter.Show(name)
		return wrapContextErr("show", out, serr)
	case "inspect":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context inspect: \"name\" is required")
		}
		out, ierr := adapter.Inspect(name, jsonInt(raw, "chunk"))
		return wrapContextErr("inspect", out, ierr)
	case "merge":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context merge: \"name\" is required")
		}
		sources := contextStringSlice(raw, "sources", "source", "contexts", "from")
		if len(sources) < 2 {
			return "", fmt.Errorf("@context merge: \"sources\" needs at least two context names")
		}
		out, merr := adapter.Merge(name, sources, jsonString(raw, "description", "desc"))
		return wrapContextErr("merge", out, merr)
	case "export":
		name := strings.TrimSpace(jsonString(raw, "name"))
		path := strings.TrimSpace(jsonString(raw, "path", "to", "output"))
		if name == "" || path == "" {
			return "", fmt.Errorf("@context export: \"name\" and \"path\" are required")
		}
		out, eerr := adapter.Export(name, path)
		return wrapContextErr("export", out, eerr)
	case "import":
		path := strings.TrimSpace(jsonString(raw, "path", "from", "source"))
		if path == "" {
			return "", fmt.Errorf("@context import: \"path\" is required")
		}
		out, ierr := adapter.Import(path)
		return wrapContextErr("import", out, ierr)
	case "metrics":
		out, merr := adapter.Metrics()
		return wrapContextErr("metrics", out, merr)
	case "attach":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context attach: \"name\" is required")
		}
		out, aerr := adapter.Attach(name, contextRagTopK(raw), jsonInt(raw, "priority"))
		return wrapContextErr("attach", out, aerr)
	case "detach":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context detach: \"name\" is required")
		}
		out, derr := adapter.Detach(name)
		return wrapContextErr("detach", out, derr)
	case "list":
		out, lerr := adapter.List()
		return wrapContextErr("list", out, lerr)
	case "status":
		out, serr := adapter.Status()
		return wrapContextErr("status", out, serr)
	case "delete":
		name := strings.TrimSpace(jsonString(raw, "name"))
		if name == "" {
			return "", fmt.Errorf("@context delete: \"name\" is required")
		}
		out, derr := adapter.Delete(name)
		return wrapContextErr("delete", out, derr)
	default:
		return "", fmt.Errorf("@context: unknown cmd %q (valid: create|update|attach|detach|list|show|inspect|merge|status|export|import|delete|metrics)", cmd)
	}
}

// wrapContextErr namespaces an adapter error under the subcommand.
func wrapContextErr(sub, out string, err error) (string, error) {
	if err != nil {
		return "", fmt.Errorf("@context %s: %w", sub, err)
	}
	return out, nil
}

// contextRagTopK reads the rag flag, which the model may send as a bool
// (true → default top-K, encoded here as -1 meaning "mode default on") or an
// int (explicit K). 0 / absent / false means "no override".
func contextRagTopK(raw map[string]json.RawMessage) int {
	v, ok := raw["rag"]
	if !ok {
		v, ok = raw["retrieve"]
	}
	if !ok || len(v) == 0 {
		return 0
	}
	var b bool
	if err := json.Unmarshal(v, &b); err == nil {
		if b {
			return contextDefaultRagTopK
		}
		return 0
	}
	if n := jsonInt(raw, "rag", "retrieve"); n > 0 {
		return n
	}
	return 0
}

// contextDefaultRagTopK is the top-K applied when rag is requested as a bare
// boolean. Mirrors ctxmgr.DefaultRetrievalTopK without importing it here.
const contextDefaultRagTopK = 8

// parseContextInvocation accepts the {cmd, args} envelope, a flat object whose
// shape implies the subcommand, or `<sub> --flag value` argv.
func parseContextInvocation(args []string) (string, map[string]json.RawMessage, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &top); err != nil {
			return "", nil, fmt.Errorf("malformed JSON args: %w", err)
		}
		cmd := canonicalContextCmd(rawJSONString(top, "cmd", "command", "action"))
		inner := top
		if rargs, ok := top["args"]; ok && len(rargs) > 0 {
			var im map[string]json.RawMessage
			if err := json.Unmarshal(rargs, &im); err == nil {
				inner = im
			}
		}
		if cmd == "" {
			cmd = inferContextCmd(inner)
		}
		if cmd == "" {
			return "", nil, fmt.Errorf("missing or unknown cmd (valid: create|update|attach|detach|list|show|inspect|merge|status|export|import|delete|metrics)")
		}
		return cmd, inner, nil
	}

	// argv form: first token is the subcommand.
	if len(args) == 0 {
		return "", nil, fmt.Errorf("no subcommand")
	}
	cmd := canonicalContextCmd(strings.TrimSpace(args[0]))
	if cmd == "" {
		return "", nil, fmt.Errorf("unknown subcommand %q", args[0])
	}
	return cmd, contextArgvToMap(args[1:]), nil
}

// canonicalContextCmd normalizes subcommand aliases.
func canonicalContextCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "create", "new":
		return "create"
	case "update", "edit":
		return "update"
	case "attach", "add":
		return "attach"
	case "detach", "remove", "rm":
		return "detach"
	case "list", "ls":
		return "list"
	case "show", "info", "view":
		return "show"
	case "inspect":
		return "inspect"
	case "merge", "join":
		return "merge"
	case "status", "attached":
		return "status"
	case "export":
		return "export"
	case "import":
		return "import"
	case "metrics", "stats":
		return "metrics"
	case "delete", "del", "destroy":
		return "delete"
	default:
		return ""
	}
}

// inferContextCmd guesses the subcommand from a flat args object that omits cmd.
func inferContextCmd(raw map[string]json.RawMessage) string {
	if _, ok := raw["paths"]; ok {
		return "create"
	}
	if _, ok := raw["name"]; ok {
		return "attach"
	}
	return ""
}

// rawJSONString reads a string field from a raw map (top-level cmd lookup).
func rawJSONString(m map[string]json.RawMessage, keys ...string) string {
	return jsonString(m, keys...)
}

// contextStringSlice reads a string array OR a single string OR a
// comma-separated string from the first matching key.
func contextStringSlice(raw map[string]json.RawMessage, keys ...string) []string {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok || len(v) == 0 {
			continue
		}
		var arr []string
		if err := json.Unmarshal(v, &arr); err == nil {
			return trimNonEmpty(arr)
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if strings.Contains(s, ",") {
				return trimNonEmpty(strings.Split(s, ","))
			}
			if t := strings.TrimSpace(s); t != "" {
				return []string{t}
			}
		}
	}
	return nil
}

// trimNonEmpty trims each element and drops the empties.
func trimNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// contextArgvToMap converts `--flag value`, `--flag=value` and bare positional
// names into the raw map the JSON path produces.
func contextArgvToMap(args []string) map[string]json.RawMessage {
	raw := map[string]json.RawMessage{}
	put := func(k, v string) {
		if b, err := json.Marshal(v); err == nil {
			raw[k] = b
		}
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		switch {
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			kv := strings.SplitN(strings.TrimPrefix(a, "--"), "=", 2)
			put(kv[0], trimQuotes(kv[1]))
		case strings.HasPrefix(a, "--"):
			key := strings.TrimPrefix(a, "--")
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				put(key, trimQuotes(args[i+1]))
				i++
			} else {
				raw[key] = json.RawMessage("true")
			}
		case a != "":
			positional = append(positional, a)
		}
	}
	if len(positional) > 0 {
		if _, ok := raw["name"]; !ok {
			put("name", positional[0])
		}
		if len(positional) > 1 {
			if b, err := json.Marshal(positional[1:]); err == nil {
				raw["paths"] = b
			}
		}
	}
	return raw
}

// jsonBool reads a boolean field, tolerating "true"/"1" strings.
func jsonBool(raw map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		var b bool
		if err := json.Unmarshal(v, &b); err == nil {
			return b
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true", "1", "yes", "on":
				return true
			}
		}
	}
	return false
}
