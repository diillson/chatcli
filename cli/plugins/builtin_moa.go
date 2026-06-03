/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinMoaPlugin — Mixture-of-Agents as an @moa ReAct tool.
 *
 * It fans the same prompt out to several models (across the providers the user
 * has configured), then has an aggregator model synthesize one best answer from
 * all the candidates. This turns ChatCLI's multi-provider support into a quality
 * lever: independent models catch each other's mistakes, and the aggregator
 * resolves conflicts. Inspired by hermes-agent's mixture_of_agents tool, but
 * implemented natively against ChatCLI's own LLM manager — keyless beyond the
 * providers the user already configured.
 *
 * Like @memory/@send, the cli package owns the LLM manager, so the plugin
 * reaches it through an adapter supplied via SetMoaAdapter.
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

// MoaAdapter runs a Mixture-of-Agents query through the live LLM manager.
type MoaAdapter interface {
	// Run queries each member model with prompt in parallel, then has the
	// aggregator synthesize a single answer. Empty members → a sensible default
	// set drawn from configured providers. Empty aggregator → the session's
	// current model. Each member is "provider" or "provider:model".
	Run(ctx context.Context, prompt string, members []string, aggregator string) (string, error)
	// List reports the providers/models available to participate.
	List(ctx context.Context) (string, error)
}

type moaAdapterHolder struct{ a MoaAdapter }

var moaAdapterAtom atomic.Value // stores moaAdapterHolder

// SetMoaAdapter wires the live adapter; pass nil to clear it.
func SetMoaAdapter(a MoaAdapter) { moaAdapterAtom.Store(moaAdapterHolder{a: a}) }

func currentMoaAdapter() MoaAdapter {
	v := moaAdapterAtom.Load()
	if v == nil {
		return nil
	}
	h, _ := v.(moaAdapterHolder)
	return h.a
}

// BuiltinMoaPlugin is the @moa tool.
type BuiltinMoaPlugin struct{}

// NewBuiltinMoaPlugin returns a ready-to-register plugin.
func NewBuiltinMoaPlugin() *BuiltinMoaPlugin { return &BuiltinMoaPlugin{} }

// Name returns "@moa".
func (*BuiltinMoaPlugin) Name() string { return "@moa" }

// Description surfaces the tool in the catalog.
func (*BuiltinMoaPlugin) Description() string {
	return "Mixture-of-Agents: ask several models the same question in parallel and synthesize one best answer. Use for hard, high-stakes, or ambiguous questions where cross-checking multiple models improves accuracy."
}

// Usage explains the canonical invocation.
func (*BuiltinMoaPlugin) Usage() string {
	return `<tool_call name="@moa" args='{"cmd":"ask","args":{"prompt":"Design a rate limiter for 1M rps"}}' />

Subcommands (cmd + args):
  ask {prompt, models?, aggregator?}
       prompt      the question (required)
       models      optional list like ["openai","anthropic:claude-opus-4-8","googleai"]
                   (default: a set drawn from your configured providers)
       aggregator  optional "provider" or "provider:model" to synthesize
                   (default: your current session model)
  list  show providers/models available to participate`
}

// Version is semver.
func (*BuiltinMoaPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinMoaPlugin) Path() string { return "" }

// Schema describes the subcommands for the prompt builder.
func (*BuiltinMoaPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "ask",
				"description": "Query several models in parallel and synthesize one best answer.",
				"flags": []map[string]interface{}{
					{"name": "prompt", "type": "string", "required": true, "description": "The question to ask all member models."},
					{"name": "models", "type": "array", "required": false, "description": "Member models: each 'provider' or 'provider:model'. Defaults to configured providers."},
					{"name": "aggregator", "type": "string", "required": false, "description": "Model that synthesizes the final answer. Defaults to the current session model."},
				},
				"examples": []string{
					`{"cmd":"ask","args":{"prompt":"What are the trade-offs of event sourcing?"}}`,
					`{"cmd":"ask","args":{"prompt":"Review this design","models":["openai","anthropic","googleai"]}}`,
				},
			},
			{
				"name":        "list",
				"description": "List providers/models available to participate.",
				"examples":    []string{`{"cmd":"list"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinMoaPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback (no incremental output).
func (p *BuiltinMoaPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	adapter := currentMoaAdapter()
	if adapter == nil {
		return "", errors.New("@moa: mixture-of-agents is not available in this session")
	}
	if len(args) == 0 {
		return "", errors.New(`@moa: empty args. Example: <tool_call name="@moa" args='{"cmd":"ask","args":{"prompt":"..."}}' />`)
	}

	cmd, inner, err := parseMoaInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@moa: %w", err)
	}

	switch cmd {
	case "ask":
		var in struct {
			Prompt     string   `json:"prompt"`
			Models     []string `json:"models"`
			Aggregator string   `json:"aggregator"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Prompt) == "" {
			return "", errors.New(`@moa ask: "prompt" is required`)
		}
		return adapter.Run(ctx, in.Prompt, in.Models, in.Aggregator)
	case "list":
		return adapter.List(ctx)
	default:
		return "", fmt.Errorf("@moa: unknown cmd %q (valid: ask|list)", cmd)
	}
}

// parseMoaInvocation accepts the JSON envelope {"cmd":..,"args":{..}}, flat
// JSON, and the argv form. Returns canonical (cmd, innerJSON).
func parseMoaInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf(`parse envelope: %w. Expected {"cmd":"ask","args":{"prompt":"..."}}`, err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalMoaCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: ask|list)", cmdStr)
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

	canon := canonicalMoaCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf(
			`expected JSON envelope or subcommand; got %q. Example: {"cmd":"ask","args":{"prompt":"..."}}`,
			args[0],
		)
	}
	// argv form: treat the remainder as the prompt for `ask`.
	if canon == "ask" {
		rest := strings.TrimSpace(strings.TrimPrefix(payload, args[0]))
		b, _ := json.Marshal(map[string]string{"prompt": rest})
		return canon, string(b), nil
	}
	return canon, "{}", nil
}

// canonicalMoaCmd folds aliases.
func canonicalMoaCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ask", "run", "query", "consult":
		return "ask"
	case "list", "models", "providers":
		return "list"
	}
	return ""
}
