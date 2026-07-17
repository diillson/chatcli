/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinMemoryPlugin — exposes long-term memory as an @memory ReAct tool
 * so the agent can persist knowledge DETERMINISTICALLY, the moment the user
 * reveals it, instead of relying on the throttled background extractor that
 * silently drops facts. Subcommands:
 *
 *   remember { content, category? }          -> stored fact
 *   profile  { fields:{key:value,...} }       -> updated profile
 *   forget   { match }                        -> removed matching facts
 *   recall   { query? }                       -> current relevant memory
 *
 * Like @scheduler, the top-level ChatCLI owns the memory store but the
 * plugin is instantiated before it, so the plugin reaches the store through
 * a package-level adapter supplied via SetMemoryAdapter.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
)

// MemoryAdapter is the interface the BuiltinMemoryPlugin uses to reach the
// live memory store. The chatcli top-level package provides an
// implementation bound to the current session.
type MemoryAdapter interface {
	// Remember stores a single fact. category may be "" to auto-classify.
	Remember(content, category string) (string, error)
	// UpdateProfile applies key/value updates to the user profile.
	UpdateProfile(updates map[string]string) (string, error)
	// Forget removes facts whose content matches the substring.
	Forget(match string) (string, error)
	// Recall returns relevant stored memory (profile + facts) for query.
	// The implementation may run HyDE/embedding round-trips to widen recall
	// semantically; it bounds those internally.
	Recall(query string) (string, error)
}

// GraphAccessor is an OPTIONAL capability a MemoryAdapter may also implement to
// back the @memory graph subcommands (neighbors/map). It is a separate
// interface, not part of MemoryAdapter, so adding the knowledge-graph view never
// breaks existing MemoryAdapter implementations; the plugin type-asserts to it
// and degrades gracefully when an adapter does not provide it.
type GraphAccessor interface {
	// GraphMap returns the knowledge-graph map-of-content (node counts + hubs).
	GraphMap() (string, error)
	// GraphNeighbors returns the local graph (backlinks + related notes) of the
	// subject best matching the query.
	GraphNeighbors(query string) (string, error)
}

// TimelineAccessor is an OPTIONAL capability a MemoryAdapter may implement to
// back the @memory timeline subcommand — the chronological view of episodic
// memory ("what did we do three months ago?"). Same optional-interface
// pattern as GraphAccessor so existing adapters keep compiling.
type TimelineAccessor interface {
	// Timeline returns dated episodes filtered by project substring, a
	// from/to window (ISO dates or natural EN/PT expressions) and a content
	// query. limit <= 0 applies the default.
	Timeline(project, from, to, query string, limit int) (string, error)
}

// memAdapterHolder wraps the adapter so atomic.Value always receives a
// consistent, non-nil concrete type — storing a bare nil interface (or
// switching concrete types) would panic. The wrapper lets SetMemoryAdapter(nil)
// cleanly clear the adapter.
type memAdapterHolder struct{ a MemoryAdapter }

var memoryAdapterAtom atomic.Value // stores memAdapterHolder

// SetMemoryAdapter wires the live adapter. Called from the top-level cli
// package after the memory store is initialized. Pass nil to clear it.
func SetMemoryAdapter(a MemoryAdapter) {
	memoryAdapterAtom.Store(memAdapterHolder{a: a})
}

// currentMemoryAdapter returns the wired adapter or nil.
func currentMemoryAdapter() MemoryAdapter {
	v := memoryAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(memAdapterHolder)
	return h.a
}

// BuiltinMemoryPlugin is the @memory tool.
type BuiltinMemoryPlugin struct{}

// NewBuiltinMemoryPlugin returns a ready-to-register plugin.
func NewBuiltinMemoryPlugin() *BuiltinMemoryPlugin { return &BuiltinMemoryPlugin{} }

// Name returns "@memory".
func (*BuiltinMemoryPlugin) Name() string { return "@memory" }

// Description surfaces the tool in /plugin list and the help.
func (*BuiltinMemoryPlugin) Description() string {
	return "Persist or recall long-term memory, and explore the knowledge graph of how it connects. Use it the moment the user reveals a durable fact about themselves (certifications, skills, role, preferences, goals) or the project — don't wait for background extraction. Use 'recall' to find facts by content, 'timeline' for WHEN questions (\"what did we do 3 months ago?\", per-project work history), and 'neighbors'/'map' to see how a subject connects (backlinks, related notes)."
}

// Usage explains the canonical invocation forms.
func (*BuiltinMemoryPlugin) Usage() string {
	return `<tool_call name="@memory" args='{"cmd":"remember","args":{"content":"User earned the AWS Solutions Architect certification","category":"personal"}}' />

Subcommands (cmd + args):
  remember {content, category?:architecture|pattern|preference|gotcha|project|personal|general}
  profile  {fields:{certifications:"AWS SAA", role:"SRE", goals:"...", interests:"...", directives:"...", milestone:"...", ...}}
  forget    {match:"<substring of the fact to remove>"}
  recall    {query?:"topic to recall"}
  timeline  {project?, from?, to?, query?, limit?}   dated episode history (WHEN)
  neighbors {query:"<subject or node id>"}   local graph: backlinks + related notes
  map                                         knowledge-graph overview (counts + hubs)

Prefer 'profile' for stable attributes of the user (name/role/certifications/
skills/goals/interests/directives/milestone or any key=value), and 'remember'
for project facts, conventions, and gotchas. Use 'recall' to search by content,
'timeline' when the question is about WHEN ("o que fizemos há 3 meses?",
"what happened in april?") — from/to accept ISO dates (2026-04) or natural
EN/PT expressions ("3 months ago", "abril") — and 'neighbors' to follow
relationships from a subject.

Profile LIST fields (certifications/skills/goals/interests/directives) UPSERT
by default: new items append, and an item restating an existing entry (same
text apart from status/parenthetical) supersedes it in place. To overwrite or
remove, add an operation suffix to the key:
  goals_replace="only goal"    replace the whole list ("" clears it)
  goals_done="tirar CKA"       remove completed goal(s) — also record milestone=/certifications=
  goals_remove="Anthropic"     remove every goal matching the substring
The same suffixes work on all list fields; preferences_remove="key" deletes a
preference. 'forget' only removes stored FACTS — use the suffixes above for
profile fields.

Deeper profile keys — capture these the moment they surface:
  directives="[scope:<project>] rule"  project-scoped rule (injected only in that workspace); no tag = global
  stance="position :: reason"       technical position WITH its why (supersedes on restate)
  env_os= / env_shell= / env_...    structured machine/tooling facts
  milestone="dated event that happened"
  sensitive_mark="key"              flag a field as private (finance/health/etc auto-flag by key name)
Sensitive fields personalize answers but are NEVER quoted into code, tests,
examples or generated artifacts.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinMemoryPlugin) Version() string { return "1.2.0" }

// Path is empty for builtin plugins.
func (*BuiltinMemoryPlugin) Path() string { return "" }

// Schema exposes a structured description the agent prompt builder renders
// into per-subcommand flag lists with examples.
func (*BuiltinMemoryPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "remember",
				"description": "Store a single durable fact. Use category 'personal' for facts about the user; omit to auto-classify.",
				"flags": []map[string]interface{}{
					{"name": "content", "type": "string", "required": true, "description": "The fact to remember, one sentence."},
					{"name": "category", "type": "string", "description": "architecture|pattern|preference|gotcha|project|personal|general"},
				},
				"examples": []string{
					`{"cmd":"remember","args":{"content":"User earned the CKA certification","category":"personal"}}`,
					`{"cmd":"remember","args":{"content":"embed.FS requires '/' separators on Windows","category":"gotcha"}}`,
				},
			},
			{
				"name":        "profile",
				"description": "Update durable attributes of the user. Known keys: name, role, expertise_level, preferred_language, communication_style, company, location, certifications, skills, goals, interests, directives, milestone, stance (\"position :: reason\"), env_<key> (machine/tooling). Directives accept a project scope: \"[scope:<project>] rule\" is only injected when that workspace is active; no tag = global. Any other key=value is preserved too. List fields UPSERT by default (append new, supersede restated); use key suffixes to change that: goals_replace overwrites the whole list (empty value clears it), goals_done/goals_remove removes matching entries — same suffixes on certifications/skills/interests/directives. preferences_remove deletes preference keys; sensitive_mark/sensitive_unmark flip a field's privacy flag. When the user completes a goal, move it: goals_done + milestone (+ certifications when a credential was earned).",
				"flags": []map[string]interface{}{
					{"name": "fields", "type": "object", "required": true, "description": "key/value map of profile attributes; list fields accept comma-separated values (commas inside parentheses are safe)."},
				},
				"examples": []string{
					`{"cmd":"profile","args":{"fields":{"certifications":"AWS Solutions Architect","role":"SRE"}}}`,
					`{"cmd":"profile","args":{"fields":{"goals_done":"obter CKA","milestone":"Earned the CKA certification","certifications":"CKA"}}}`,
					`{"cmd":"profile","args":{"fields":{"goals_replace":"publicar um blog pessoal (Hugo, tema claro)","interests":"fotografia","directives":"evitar jargão"}}}`,
				},
			},
			{
				"name":        "forget",
				"description": "Remove stored FACTS whose text contains the given substring (case-insensitive). Does not touch profile fields — use profile with goals_remove/certifications_remove/... for those.",
				"flags": []map[string]interface{}{
					{"name": "match", "type": "string", "required": true, "description": "Substring identifying the fact(s) to remove."},
				},
				"examples": []string{`{"cmd":"forget","args":{"match":"prefers tabs"}}`},
			},
			{
				"name":        "recall",
				"description": "Retrieve relevant stored memory (profile + scored facts), optionally filtered by a query.",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "description": "Optional topic to narrow recall."},
				},
				"examples": []string{`{"cmd":"recall","args":{"query":"certifications"}}`},
			},
			{
				"name":        "timeline",
				"description": "Chronological history of dated work episodes — answers WHEN questions (\"what did we do 3 months ago?\", \"o que fizemos em abril?\") and per-project work history. from/to accept ISO dates (2026-04, 2026-04-12) or natural EN/PT expressions (\"3 months ago\", \"há 3 semanas\", \"abril\"); a time expression inside query works too.",
				"flags": []map[string]interface{}{
					{"name": "project", "type": "string", "description": "Filter by project path/name substring."},
					{"name": "from", "type": "string", "description": "Window start: ISO date or natural expression."},
					{"name": "to", "type": "string", "description": "Window end: ISO date or natural expression."},
					{"name": "query", "type": "string", "description": "Content filter; may embed the time expression."},
					{"name": "limit", "type": "number", "description": "Max episodes returned (default 30, most recent win)."},
				},
				"examples": []string{
					`{"cmd":"timeline","args":{"query":"3 months ago"}}`,
					`{"cmd":"timeline","args":{"project":"chatcli","from":"2026-04","to":"2026-06"}}`,
				},
			},
			{
				"name":        "neighbors",
				"description": "Show the local knowledge graph of a subject — its backlinks and related notes (facts, topics, projects, skills). Answers 'what connects to X', which content recall cannot.",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "required": true, "description": "A subject or exact node id to expand."},
				},
				"examples": []string{`{"cmd":"neighbors","args":{"query":"authentication"}}`},
			},
			{
				"name":        "map",
				"description": "Knowledge-graph overview: node counts by kind and the hub subjects.",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"map"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses the args and dispatches to the adapter.
func (p *BuiltinMemoryPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream mirrors Execute — this plugin produces no incremental
// output, so the stream callback is ignored.
func (p *BuiltinMemoryPlugin) ExecuteWithStream(_ context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentMemoryAdapter()
	if adapter == nil {
		return "", errors.New("@memory: memory is not enabled in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@memory: empty args. Example: <tool_call name="@memory" args='{"cmd":"remember","args":{"content":"User prefers Go"}}' />`)
	}

	cmd, inner, err := parseMemoryInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@memory: %w", err)
	}

	switch cmd {
	case "remember":
		var in struct {
			Content  string `json:"content"`
			Category string `json:"category"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Content) == "" {
			return "", errors.New(`@memory remember: "content" is required`)
		}
		return adapter.Remember(in.Content, in.Category)
	case "profile":
		fields, err := parseProfileFields(inner)
		if err != nil {
			return "", fmt.Errorf("@memory profile: %w", err)
		}
		if len(fields) == 0 {
			return "", errors.New(`@memory profile: provide "fields" with at least one key/value`)
		}
		return adapter.UpdateProfile(fields)
	case "forget":
		var in struct {
			Match string `json:"match"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Match) == "" {
			return "", errors.New(`@memory forget: "match" is required`)
		}
		return adapter.Forget(in.Match)
	case "recall":
		var in struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		return adapter.Recall(in.Query)
	case "timeline":
		ta, ok := adapter.(TimelineAccessor)
		if !ok {
			return "", errors.New("@memory timeline: episodic timeline not available")
		}
		var in struct {
			Project string          `json:"project"`
			From    string          `json:"from"`
			To      string          `json:"to"`
			Query   string          `json:"query"`
			Limit   json.RawMessage `json:"limit"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		return ta.Timeline(in.Project, in.From, in.To, in.Query, lenientInt(in.Limit))
	case "map":
		ga, ok := adapter.(GraphAccessor)
		if !ok {
			return "", errors.New("@memory map: knowledge graph not available")
		}
		return ga.GraphMap()
	case "neighbors":
		ga, ok := adapter.(GraphAccessor)
		if !ok {
			return "", errors.New("@memory neighbors: knowledge graph not available")
		}
		var in struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Query) == "" {
			return "", errors.New(`@memory neighbors: "query" is required (a subject or node id)`)
		}
		return ga.GraphNeighbors(in.Query)
	default:
		return "", fmt.Errorf(
			"@memory: unknown cmd %q (valid: remember|profile|forget|recall|timeline|neighbors|map)", cmd,
		)
	}
}

// parseMemoryInvocation accepts the JSON envelope {"cmd":..,"args":{..}},
// flat JSON {"cmd":..,"content":..}, and the flattened argv form the agent
// tool sanitizer may produce. Returns the canonical (cmd, innerJSON).
func parseMemoryInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(
				`parse envelope: %w. Expected {"cmd":"remember","args":{"content":"..."}}`, err,
			)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalMemoryCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf(
				"missing or unknown cmd %q (valid: remember|profile|forget|recall|timeline|neighbors|map)", cmdStr,
			)
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

	canon := canonicalMemoryCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf(
			`expected JSON envelope or subcommand; got %q. Example: {"cmd":"remember","args":{"content":"..."}}`,
			args[0],
		)
	}
	inner, err := memoryFlagsToJSON(args[1:])
	if err != nil {
		return "", "", err
	}
	return canon, inner, nil
}

// canonicalMemoryCmd folds aliases into the canonical subcommand names.
func canonicalMemoryCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "remember", "add", "store", "note":
		return "remember"
	case "profile", "profile_set", "user":
		return "profile"
	case "forget", "remove", "delete":
		return "forget"
	case "recall", "read", "get", "search":
		return "recall"
	case "neighbors", "related", "links", "backlinks", "connected":
		return "neighbors"
	case "map", "graph", "moc", "overview":
		return "map"
	case "timeline", "history", "episodes", "chronology":
		return "timeline"
	}
	return ""
}

// lenientInt reads a JSON number that models sometimes send as a string
// ("limit":"20") or a float ("limit":20.0). Anything unparseable yields 0 so
// the caller applies its default — strict parsing here would make the model
// fail and retry.
func lenientInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return v
		}
	}
	return 0
}

// parseProfileFields extracts the key/value map from a profile invocation.
// It accepts {"fields":{...}} as well as a bare object {key:value,...} so
// the LLM can omit the wrapper.
func parseProfileFields(inner string) (map[string]string, error) {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return nil, nil
	}
	var wrapped struct {
		Fields json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal([]byte(inner), &wrapped); err == nil && len(wrapped.Fields) > 0 {
		if m := decodeFieldsObject(wrapped.Fields); len(m) > 0 {
			return stringifyMap(m), nil
		}
	}
	var bare map[string]interface{}
	if err := json.Unmarshal([]byte(inner), &bare); err != nil {
		return nil, fmt.Errorf("invalid fields JSON: %w", err)
	}
	delete(bare, "fields")
	return stringifyMap(bare), nil
}

// decodeFieldsObject reads a JSON object from raw, accepting the direct
// object form and the JSON-encoded string the agent flattener produces when
// it stringifies a nested object into a "--fields" argv value.
func decodeFieldsObject(raw json.RawMessage) map[string]interface{} {
	var direct map[string]interface{}
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	var inner map[string]interface{}
	if err := json.Unmarshal([]byte(s), &inner); err != nil {
		return nil
	}
	return inner
}

// stringifyMap coerces JSON values to strings (numbers/bools become text;
// arrays are joined with commas so list fields round-trip).
func stringifyMap(in map[string]interface{}) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch val := v.(type) {
		case string:
			out[k] = val
		case []interface{}:
			parts := make([]string, 0, len(val))
			for _, item := range val {
				parts = append(parts, fmt.Sprintf("%v", item))
			}
			out[k] = strings.Join(parts, ", ")
		case nil:
			// skip
		default:
			out[k] = fmt.Sprintf("%v", val)
		}
	}
	return out
}

// memoryFlagsToJSON converts ["--key","value",...] into a JSON object.
func memoryFlagsToJSON(argv []string) (string, error) {
	obj := map[string]interface{}{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			return "", fmt.Errorf("unexpected positional argument %q (use --key value or a JSON envelope)", a)
		}
		key := strings.TrimLeft(a, "-")
		if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
			obj[key] = true
			continue
		}
		obj[key] = argv[i+1]
		i++
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
