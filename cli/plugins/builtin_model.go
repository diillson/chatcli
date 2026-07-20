/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinModelPlugin — model/provider routing as an @model ReAct tool.
 *
 * Lets the agent itself pick the right model for the task at hand: list the
 * configured providers and their models (with pricing tier, context window and
 * capabilities), switch the agent loop to another model/provider for the rest
 * of the task, or delegate a self-contained subtask to a cheaper model without
 * touching the main loop (keeping the provider prompt cache intact).
 *
 * Like @moa/@mcp-login, the cli package owns the LLM manager and the agent
 * loop, so the plugin reaches them through an adapter supplied via
 * SetModelRoutingAdapter. All adapter results are pre-rendered, model-facing
 * text.
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
	"unicode"
)

// ModelRoutingAdapter is the surface the @model tool needs from the live
// session (LLM manager, catalog, cost tables, agent-loop route override).
type ModelRoutingAdapter interface {
	// List renders the configured providers and their models with routing
	// metadata (tier, price per 1M tokens, context window, capabilities).
	// providerFilter narrows the listing to one provider ("" = all).
	List(ctx context.Context, providerFilter string) (string, error)
	// Use routes the rest of the agent task to the given model handle
	// ("PROVIDER:model" preferred, bare model accepted). Sticky until Reset
	// or the task ends.
	Use(ctx context.Context, handle string) (string, error)
	// Reset restores the session's own model for the next turns.
	Reset() (string, error)
	// Status reports the session model and any active route override.
	Status() (string, error)
	// Delegate runs prompt as a one-shot call on the given model handle and
	// returns its answer. The main loop and its history are untouched.
	Delegate(ctx context.Context, handle, prompt string, maxTokens int) (string, error)
}

type modelRoutingAdapterHolder struct{ a ModelRoutingAdapter }

var modelRoutingAdapterAtom atomic.Value // modelRoutingAdapterHolder

// SetModelRoutingAdapter wires the live adapter. Called from the top-level
// cli package at startup; pass nil to detach.
func SetModelRoutingAdapter(a ModelRoutingAdapter) {
	modelRoutingAdapterAtom.Store(modelRoutingAdapterHolder{a: a})
}

func currentModelRoutingAdapter() ModelRoutingAdapter {
	v := modelRoutingAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(modelRoutingAdapterHolder)
	return h.a
}

// BuiltinModelPlugin is the @model tool.
type BuiltinModelPlugin struct{}

// NewBuiltinModelPlugin returns a ready-to-register plugin.
func NewBuiltinModelPlugin() *BuiltinModelPlugin { return &BuiltinModelPlugin{} }

// Name returns "@model".
func (*BuiltinModelPlugin) Name() string { return "@model" }

// Description surfaces the tool in the catalog. It doubles as the routing
// policy the agent should follow, so the guidance lives here instead of
// inflating the system prompt.
func (*BuiltinModelPlugin) Description() string {
	return "Inspect and route which LLM model/provider serves this task. " +
		"list shows every configured provider's models with pricing tier (fast-cheap/balanced/frontier), cost per 1M tokens, context window and capabilities. " +
		"use switches the rest of the task to another model (provider switches automatically with it); delegate runs one self-contained prompt on another model and returns the answer without touching the main loop. " +
		"Routing policy: prefer delegate for mechanical subtasks (summarize, extract, reformat, translate) on a fast-cheap model; use `use` only at a clear phase change, never per-call — model switches invalidate the provider's prompt cache and frequent flip-flopping costs more than it saves. " +
		"Always pass the qualified PROVIDER:model handle exactly as shown by list."
}

// Usage explains the canonical invocation forms.
func (*BuiltinModelPlugin) Usage() string {
	return `<tool_call name="@model" args='{"cmd":"list"}' />
<tool_call name="@model" args='{"cmd":"use","args":{"model":"CLAUDEAI:claude-haiku-4-5-20251001"}}' />
<tool_call name="@model" args='{"cmd":"delegate","args":{"model":"GOOGLEAI:gemini-2.5-flash","prompt":"Summarize: ..."}}' />

Subcommands (cmd + args):
  list                       providers and models with tier, price/1M tokens, context window, capabilities
  use {model}                switch the REST OF THE TASK to this model (sticky; provider follows the model)
  reset                      return to the session's own model
  status                     current session model and active override, if any
  delegate {model, prompt, max_tokens?}
                             one-shot the prompt on that model and return its answer;
                             main loop, history and prompt cache stay untouched

Handles: use the qualified "PROVIDER:model" form from list (e.g. "CLAUDEAI:claude-haiku-4-5-20251001").
Bare model names are accepted and resolved to the owning provider (active provider preferred).`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinModelPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinModelPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders
// into per-subcommand flag lists with examples.
func (*BuiltinModelPlugin) Schema() string {
	modelFlag := map[string]interface{}{"name": "model", "description": `model handle — qualified "PROVIDER:model" from list preferred; bare model accepted`, "type": "string", "required": true}
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "list",
				"description": "list configured providers and their models with pricing tier, cost per 1M tokens, context window and capabilities",
				"flags": []map[string]interface{}{
					{"name": "provider", "description": "optional provider filter (e.g. CLAUDEAI) to narrow the listing", "type": "string", "required": false},
				},
				"examples": []string{`{"cmd":"list"}`, `{"cmd":"list","args":{"provider":"CLAUDEAI"}}`},
			},
			{
				"name":        "use",
				"description": "switch the rest of the task to this model (sticky until reset; the provider switches together with the model)",
				"flags":       []map[string]interface{}{modelFlag},
				"examples":    []string{`{"cmd":"use","args":{"model":"CLAUDEAI:claude-haiku-4-5-20251001"}}`},
			},
			{
				"name":        "reset",
				"description": "clear the route override and return to the session's own model",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"reset"}`},
			},
			{
				"name":        "status",
				"description": "show the session model and the active route override, if any",
				"flags":       []map[string]interface{}{},
				"examples":    []string{`{"cmd":"status"}`},
			},
			{
				"name":        "delegate",
				"description": "run one self-contained prompt on another model and return its answer; the main loop, its history and its prompt cache stay untouched — the cheapest way to offload mechanical subtasks",
				"flags": []map[string]interface{}{
					modelFlag,
					{"name": "prompt", "description": "the complete, self-contained prompt for the delegated model (it sees nothing else)", "type": "string", "required": true},
					{"name": "max_tokens", "description": "optional response cap for the delegated call", "type": "number", "required": false},
				},
				"examples": []string{`{"cmd":"delegate","args":{"model":"GOOGLEAI:gemini-2.5-flash","prompt":"Summarize the following build log into the 3 root errors: ..."}}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// Execute implements the plugin contract.
func (p *BuiltinModelPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream dispatches to the adapter. This tool produces no
// incremental output, so the stream callback is ignored.
func (p *BuiltinModelPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentModelRoutingAdapter()
	if adapter == nil {
		return "", errors.New("@model: model routing is not available in this session")
	}

	inv, err := parseModelInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@model: %w", err)
	}

	switch inv.cmd {
	case "list":
		return adapter.List(ctx, inv.model)
	case "use":
		if inv.model == "" {
			return "", errors.New(`@model: "model" is required for use. Example: {"cmd":"use","args":{"model":"CLAUDEAI:claude-haiku-4-5-20251001"}}`)
		}
		return adapter.Use(ctx, inv.model)
	case "reset":
		return adapter.Reset()
	case "status":
		return adapter.Status()
	case "delegate":
		if inv.model == "" || strings.TrimSpace(inv.prompt) == "" {
			return "", errors.New(`@model: delegate requires "model" and "prompt". Example: {"cmd":"delegate","args":{"model":"GOOGLEAI:gemini-2.5-flash","prompt":"..."}}`)
		}
		return adapter.Delegate(ctx, inv.model, inv.prompt, inv.maxTokens)
	default:
		return "", fmt.Errorf("@model: unknown cmd %q (valid: list|use|reset|status|delegate)", inv.cmd)
	}
}

// modelInvocation is the normalized result of parseModelInvocation.
type modelInvocation struct {
	cmd       string
	model     string
	prompt    string
	maxTokens int
}

// parseModelInvocation extracts the subcommand and fields from any of the
// shapes the agent may produce. It is deliberately lenient — strict parsing
// here just makes the model retry blindly:
//
//   - JSON envelope:  {"cmd":"use","args":{"model":"CLAUDEAI:claude-haiku-4-5"}}
//   - bare JSON:      {"model":"haiku"}                      (implies use)
//   - positional:     use haiku | list | status | delegate PROVIDER:model the prompt...
//   - flag form:      use --model haiku | --cmd delegate --model x --prompt "..."
//
// The flag form matters because the agent's tool flattener rewrites a
// {cmd,args} envelope into "--flag value" pairs.
func parseModelInvocation(args []string) (modelInvocation, error) {
	inv := modelInvocation{}
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		inv.cmd = "status"
		return inv, nil
	}

	if strings.HasPrefix(payload, "{") {
		var raw struct {
			Cmd       string          `json:"cmd"`
			Model     string          `json:"model"`
			Provider  string          `json:"provider"`
			Prompt    string          `json:"prompt"`
			MaxTokens int             `json:"max_tokens"`
			Args      json.RawMessage `json:"args"`
		}
		if uerr := json.Unmarshal([]byte(payload), &raw); uerr != nil {
			return inv, fmt.Errorf(`parse envelope: %w. Expected {"cmd":"use","args":{"model":"PROVIDER:model"}}`, uerr)
		}
		inv.model = firstNonEmptyStr(raw.Model, raw.Provider)
		inv.prompt = raw.Prompt
		inv.maxTokens = raw.MaxTokens
		if len(raw.Args) > 0 {
			var nested struct {
				Model     string `json:"model"`
				Handle    string `json:"handle"`
				Provider  string `json:"provider"`
				Prompt    string `json:"prompt"`
				MaxTokens int    `json:"max_tokens"`
			}
			if uerr := json.Unmarshal(raw.Args, &nested); uerr == nil {
				inv.model = firstNonEmptyStr(nested.Model, nested.Handle, nested.Provider, inv.model)
				inv.prompt = firstNonEmptyStr(nested.Prompt, inv.prompt)
				if nested.MaxTokens > 0 {
					inv.maxTokens = nested.MaxTokens
				}
			}
		}
		inv.cmd = canonicalModelCmd(raw.Cmd)
		if inv.cmd == "" {
			if inv.model != "" {
				inv.cmd = "use"
			} else {
				inv.cmd = "status"
			}
		}
		return inv, nil
	}

	// argv / flag form. Scan tokens, honoring --model / --prompt / --cmd /
	// --max_tokens (and =forms), and collect the rest as positionals.
	//
	// Token boundaries matter here: the agent's tool flattener rewrites the
	// {cmd,args} envelope into "--flag value" argv elements where a value —
	// notably a delegate prompt — is ONE element that may contain spaces and
	// newlines. Re-splitting the joined payload on whitespace would shatter
	// that prompt into words and the delegated model would receive only the
	// first one. So multi-element argv is consumed element-wise; only a
	// single free-text argument is tokenized, and even then quoted runs are
	// kept together so `--prompt "long text"` survives.
	var positionals []string
	var fields []string
	if len(args) > 1 {
		for _, a := range args {
			if t := strings.TrimSpace(a); t != "" {
				fields = append(fields, t)
			}
		}
	} else {
		fields = splitArgsRespectingQuotes(payload)
	}
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		key, val, hasEq := strings.Cut(f, "=")
		key = strings.TrimLeft(key, "-")
		grab := func() string {
			if hasEq {
				return trimQuotes(val)
			}
			if i+1 < len(fields) {
				i++
				return trimQuotes(fields[i])
			}
			return ""
		}
		switch strings.ToLower(key) {
		case "model", "handle", "name", "provider":
			inv.model = grab()
		case "prompt", "task", "query":
			inv.prompt = grab()
		case "max_tokens", "maxtokens", "max-tokens":
			if n, err := strconv.Atoi(grab()); err == nil && n > 0 {
				inv.maxTokens = n
			}
		case "cmd", "command", "action":
			inv.cmd = canonicalModelCmd(grab())
		default:
			if strings.HasPrefix(f, "-") {
				continue // unknown flag — ignore
			}
			positionals = append(positionals, trimQuotes(f))
		}
	}
	for idx, p := range positionals {
		if inv.cmd == "" {
			if c := canonicalModelCmd(p); c != "" {
				inv.cmd = c
				continue
			}
		}
		if inv.model == "" {
			inv.model = p
			continue
		}
		// Everything after cmd+model is the delegate prompt.
		inv.prompt = firstNonEmptyStr(inv.prompt, strings.Join(positionals[idx:], " "))
		break
	}
	if inv.cmd == "" {
		if inv.model != "" {
			inv.cmd = "use"
		} else {
			inv.cmd = "status"
		}
	}
	return inv, nil
}

// splitArgsRespectingQuotes tokenizes a single free-text invocation on
// whitespace while keeping "double" and 'single' quoted runs together, so a
// one-string `delegate x --prompt "long text"` keeps the prompt whole. A
// quote only opens a run at a token boundary — apostrophes inside prose
// (user's, don't) stay literal.
func splitArgsRespectingQuotes(s string) []string {
	var (
		tokens []string
		cur    strings.Builder
		quote  rune
	)
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case (r == '"' || r == '\'') && cur.Len() == 0:
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func canonicalModelCmd(cmd string) string {
	switch strings.ToLower(strings.TrimSpace(cmd)) {
	case "list", "ls", "models", "providers":
		return "list"
	case "use", "switch", "set", "route":
		return "use"
	case "reset", "clear", "restore":
		return "reset"
	case "status", "current", "active":
		return "status"
	case "delegate", "offload", "oneshot", "run":
		return "delegate"
	default:
		return ""
	}
}
