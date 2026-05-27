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
	Recall(query string) (string, error)
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
	return "Persist or recall long-term memory. Use it the moment the user reveals a durable fact about themselves (certifications, skills, role, preferences, goals) or the project — don't wait for background extraction."
}

// Usage explains the canonical invocation forms.
func (*BuiltinMemoryPlugin) Usage() string {
	return `<tool_call name="@memory" args='{"cmd":"remember","args":{"content":"User earned the AWS Solutions Architect certification","category":"personal"}}' />

Subcommands (cmd + args):
  remember {content, category?:architecture|pattern|preference|gotcha|project|personal|general}
  profile  {fields:{certifications:"AWS SAA", role:"SRE", company:"...", skills:"Go, k8s", ...}}
  forget   {match:"<substring of the fact to remove>"}
  recall   {query?:"topic to recall"}

Prefer 'profile' for stable attributes of the user (name/role/certifications/
skills/company/location/goals or any key=value), and 'remember' for project
facts, conventions, and gotchas.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinMemoryPlugin) Version() string { return "1.0.0" }

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
				"description": "Update durable attributes of the user. Known keys: name, role, expertise_level, preferred_language, communication_style, company, location, certifications, skills, goals. Any other key=value is preserved too.",
				"flags": []map[string]interface{}{
					{"name": "fields", "type": "object", "required": true, "description": "key/value map of profile attributes; list fields (certifications/skills/goals) accept comma-separated values."},
				},
				"examples": []string{
					`{"cmd":"profile","args":{"fields":{"certifications":"AWS Solutions Architect","role":"SRE"}}}`,
					`{"cmd":"profile","args":{"fields":{"company":"Acme","location":"São Paulo","skills":"Go, Kubernetes"}}}`,
				},
			},
			{
				"name":        "forget",
				"description": "Remove stored facts whose text contains the given substring (case-insensitive).",
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
	default:
		return "", fmt.Errorf(
			"@memory: unknown cmd %q (valid: remember|profile|forget|recall)", cmd,
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
				"missing or unknown cmd %q (valid: remember|profile|forget|recall)", cmdStr,
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

// canonicalMemoryCmd folds aliases into the four canonical names.
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
	}
	return ""
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
		Fields map[string]interface{} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(inner), &wrapped); err == nil && len(wrapped.Fields) > 0 {
		return stringifyMap(wrapped.Fields), nil
	}
	var bare map[string]interface{}
	if err := json.Unmarshal([]byte(inner), &bare); err != nil {
		return nil, fmt.Errorf("invalid fields JSON: %w", err)
	}
	delete(bare, "fields")
	return stringifyMap(bare), nil
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
