/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * rpc_support_full.go
 *
 * The full capability surface behind the MCP/ACP servers: every built-in and
 * external plugin (not just the old curated five), the agent/coder loops with
 * per-call provider/model routing and quality-harness toggles, the skill
 * catalog (served as MCP prompts), and provider discovery.
 *
 * Exposure policy (CHATCLI_MCP_TOOLS):
 *   all (default)  every tool, including write/exec — the operator opted in
 *                  by starting the server.
 *   safe           only tools whose capability metadata reports read-only
 *                  for a bare invocation.
 *   <csv>          explicit allowlist of tool names (with or without '@').
 *
 * Interactive tools that require a live TTY/user (ask, voice, park) are
 * excluded unconditionally: over stdio RPC they would hang the turn waiting
 * for input that can never arrive.
 */
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/llm/catalog"
)

// RPCToolInfo describes one plugin tool exposed over MCP/ACP.
type RPCToolInfo struct {
	Name        string // without the '@' prefix
	Description string
	Usage       string
	Schema      string // the plugin's self-declared schema (JSON, free-form)
	ReadOnly    bool   // capability metadata for a bare invocation
}

// rpcInteractiveTools can never work over stdio RPC: they block on a live
// terminal (interactive question overlay, microphone capture, TTY resume).
var rpcInteractiveTools = map[string]bool{
	"@ask":   true,
	"@voice": true,
	"@park":  true,
}

// rpcToolPolicy returns the normalized CHATCLI_MCP_TOOLS policy: "all",
// "safe", or a set of allowlisted names.
func rpcToolPolicy() (mode string, allow map[string]bool) {
	raw := strings.TrimSpace(os.Getenv("CHATCLI_MCP_TOOLS"))
	switch strings.ToLower(raw) {
	case "", "all":
		return "all", nil
	case "safe":
		return "safe", nil
	}
	allow = map[string]bool{}
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(strings.TrimPrefix(tok, "@"))
		if tok != "" {
			allow["@"+tok] = true
		}
	}
	return "list", allow
}

// rpcToolAllowed applies the exposure policy to one plugin.
func rpcToolAllowed(p plugins.Plugin, mode string, allow map[string]bool) bool {
	name := p.Name()
	if rpcInteractiveTools[name] {
		return false
	}
	// CLI pseudo-plugins (help/version) are meaningless over RPC and their
	// colon-bearing names violate the MCP tool-name pattern.
	if strings.Contains(name, ":") {
		return false
	}
	switch mode {
	case "all":
		return true
	case "safe":
		return probeReadOnly(p)
	default:
		return allow[name]
	}
}

// probeReadOnly asks a plugin's capability layer whether a bare invocation
// is read-only, failing closed. The recover guard is a server boundary: caps
// implementations are per-plugin and a panicking one (38 of them and
// counting) must degrade that one tool to "not read-only", never kill the
// RPC server.
func probeReadOnly(p plugins.Plugin) (ro bool) {
	defer func() {
		if r := recover(); r != nil {
			ro = false
		}
	}()
	if aware, ok := p.(plugins.ReadOnlyAware); ok {
		return aware.IsReadOnly(nil)
	}
	return false
}

// ListAllRPCTools returns every plugin the exposure policy admits.
func (cli *ChatCLI) ListAllRPCTools() []RPCToolInfo {
	if cli.pluginManager == nil {
		return nil
	}
	mode, allow := rpcToolPolicy()
	all := cli.pluginManager.GetPlugins()
	out := make([]RPCToolInfo, 0, len(all))
	for _, p := range all {
		if !rpcToolAllowed(p, mode, allow) {
			continue
		}
		info := RPCToolInfo{
			Name:        strings.TrimPrefix(p.Name(), "@"),
			Description: p.Description(),
			Usage:       p.Usage(),
			Schema:      p.Schema(),
		}
		info.ReadOnly = probeReadOnly(p)
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RunAnyRPCTool invokes any policy-admitted plugin by name with a raw
// argument string (JSON envelope or flat args, exactly as the agent passes).
func (cli *ChatCLI) RunAnyRPCTool(ctx context.Context, name, args string) (string, error) {
	if cli.pluginManager == nil {
		return "", fmt.Errorf("plugins not available")
	}
	full := "@" + strings.TrimPrefix(name, "@")
	p, ok := cli.pluginManager.GetPlugin(full)
	if !ok {
		return "", fmt.Errorf("tool %q not found", name)
	}
	mode, allow := rpcToolPolicy()
	if !rpcToolAllowed(p, mode, allow) {
		return "", fmt.Errorf("tool %q is not exposed under the current CHATCLI_MCP_TOOLS policy (%s)", name, mode)
	}
	// Same argv contract as the agent loop: JSON envelopes become the argv
	// the plugin's parser expects ({"cmd":...} → ["cmd", ...]); flat strings
	// split like a command line. Wrapping the raw string as a single argv
	// element broke every subcommand-style tool (coder, memory, session…).
	argv, parseErr := parseToolArgsWithJSON(args)
	if parseErr != nil {
		return "", fmt.Errorf("invalid args for %q: %w", name, parseErr)
	}

	// Capture stdout for the duration: plugins that print progress would
	// otherwise write raw text into the JSON-RPC protocol stream. Captured
	// output is appended to the result so nothing the tool said is lost.
	var result string
	var execErr error
	captured, _ := captureRPCStdout(func() error {
		result, execErr = execBuiltin(ctx, p, argv)
		return nil
	})
	if execErr != nil {
		return "", execErr
	}
	if strings.TrimSpace(captured) != "" && !strings.Contains(result, captured) {
		if result != "" {
			result += "\n"
		}
		result += captured
	}
	return result, nil
}

// RPCRunOpts parameterizes an agent/coder run driven over RPC.
type RPCRunOpts struct {
	// Provider/Model temporarily reroute the run to another configured
	// provider (restored afterwards). Empty keeps the session default.
	Provider string
	Model    string
	// Quality maps CHATCLI_QUALITY_* env overrides applied for the run
	// only (e.g. "CHATCLI_QUALITY_ENABLED": "true"). The quality pipeline
	// re-reads env per run, so this is the canonical toggle surface.
	Quality map[string]string
	// Emit, when non-nil, receives the rendered transcript line by line
	// as the loop works (ACP streaming).
	Emit func(string)
}

// RunAgentRPC runs the agent loop with per-call options.
func (cli *ChatCLI) RunAgentRPC(ctx context.Context, task string, o RPCRunOpts) (string, error) {
	return cli.runLoopRPC(ctx, o, func(runCtx context.Context) error {
		return cli.RunAgentOnce(runCtx, "/agent "+task, true)
	})
}

// RunCoderRPC runs the coder loop with per-call options.
func (cli *ChatCLI) RunCoderRPC(ctx context.Context, task string, o RPCRunOpts) (string, error) {
	return cli.runLoopRPC(ctx, o, func(runCtx context.Context) error {
		return cli.RunCoderOnce(runCtx, "/coder "+task)
	})
}

// runLoopRPC wraps a captured loop run with provider/model and quality-env
// overrides. captureStreaming serializes runs process-wide (rpcStdoutMu), so
// the save/mutate/restore of shared state below cannot interleave with
// another captured run.
func (cli *ChatCLI) runLoopRPC(ctx context.Context, o RPCRunOpts, fn func(context.Context) error) (string, error) {
	out, err := captureStreaming(o.Emit, func() error {
		restore, oerr := cli.applyRPCOverrides(ctx, o)
		if oerr != nil {
			return oerr
		}
		defer restore()
		return fn(ctx)
	})
	if err != nil {
		return out, err
	}
	if out == "" {
		out = "(no textual output)"
	}
	return out, nil
}

// applyRPCOverrides mutates provider/model and quality env for one run and
// returns the restore function. Caller holds the run serialization lock.
func (cli *ChatCLI) applyRPCOverrides(ctx context.Context, o RPCRunOpts) (func(), error) {
	prevProvider, prevModel, prevClient := cli.Provider, cli.Model, cli.Client
	if o.Provider != "" || o.Model != "" {
		if err := cli.ApplyOverrides(ctx, cli.manager, o.Provider, o.Model); err != nil {
			return nil, fmt.Errorf("provider/model override failed: %w", err)
		}
	}
	prevEnv := map[string]*string{}
	for k, v := range o.Quality {
		if !strings.HasPrefix(k, "CHATCLI_QUALITY_") {
			continue // only the quality namespace is overridable per call
		}
		if cur, ok := os.LookupEnv(k); ok {
			c := cur
			prevEnv[k] = &c
		} else {
			prevEnv[k] = nil
		}
		_ = os.Setenv(k, v)
	}
	return func() {
		for k, v := range prevEnv {
			if v == nil {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, *v)
			}
		}
		cli.Provider, cli.Model, cli.Client = prevProvider, prevModel, prevClient
		// context.WithoutCancel(ctx): the restore runs on defer, typically
		// AFTER the run's ctx is done — the cache refresh must still happen.
		cli.refreshModelCache(context.WithoutCancel(ctx))
	}, nil
}

// RPCSkillInfo describes one skill served as an MCP prompt.
type RPCSkillInfo struct {
	Name        string
	Description string
}

// ListSkillsRPC returns the installed skill catalog.
func (cli *ChatCLI) ListSkillsRPC() []RPCSkillInfo {
	if cli.skillHandler == nil || cli.skillHandler.personaMgr == nil {
		return nil
	}
	skills, err := cli.skillHandler.personaMgr.ListSkills()
	if err != nil {
		return nil
	}
	out := make([]RPCSkillInfo, 0, len(skills))
	for _, s := range skills {
		out = append(out, RPCSkillInfo{Name: s.Name, Description: s.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SkillContentRPC returns a skill's markdown body for prompts/get.
func (cli *ChatCLI) SkillContentRPC(name string) (string, error) {
	if cli.skillHandler == nil || cli.skillHandler.personaMgr == nil {
		return "", fmt.Errorf("skills not available")
	}
	s, err := cli.skillHandler.personaMgr.GetSkill(name)
	if err != nil {
		return "", err
	}
	return s.Content, nil
}

// ProvidersRPC returns a JSON document describing the configured providers,
// the active provider/model, and each provider's cataloged models — the
// discovery surface for per-call routing.
func (cli *ChatCLI) ProvidersRPC() (string, error) {
	type providerInfo struct {
		Name   string   `json:"name"`
		Active bool     `json:"active"`
		Models []string `json:"models"`
	}
	var out struct {
		ActiveProvider string         `json:"active_provider"`
		ActiveModel    string         `json:"active_model"`
		Providers      []providerInfo `json:"providers"`
	}
	out.ActiveProvider = cli.Provider
	out.ActiveModel = cli.Model
	for _, name := range cli.manager.GetAvailableProviders() {
		info := providerInfo{Name: name, Active: name == cli.Provider}
		for _, meta := range catalog.ListByProvider(name) {
			info.Models = append(info.Models, meta.ID)
		}
		out.Providers = append(out.Providers, info)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
