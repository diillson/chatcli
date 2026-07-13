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
 * Beyond plugins, the server re-exports every tool discovered from the MCP
 * servers ChatCLI itself is connected to (mcp_servers.json), under the same
 * mcp_<tool> names the agent loop uses — ChatCLI as an MCP hub: one endpoint
 * aggregating many servers. Proxied tools keep their origin JSON Schema and
 * receive the caller's arguments verbatim.
 *
 * Exposure policy (CHATCLI_MCP_TOOLS):
 *   all (default)  every tool, including write/exec — the operator opted in
 *                  by starting the server.
 *   safe           only tools whose capability metadata reports read-only
 *                  for a bare invocation. For proxied MCP tools this trusts
 *                  the origin server's annotations.readOnlyHint — the same
 *                  trust the operator extended by configuring the server.
 *   <csv>          explicit allowlist of tool names (with or without '@');
 *                  proxied MCP tools are named with their mcp_ prefix
 *                  (e.g. "read,search,mcp_list_regions").
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
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
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

// RPCMCPToolInfo describes one tool proxied from an MCP server ChatCLI is
// connected to, re-exported on the MCP/ACP server surface.
type RPCMCPToolInfo struct {
	Name        string // origin tool name, WITHOUT the mcp_ prefix
	Server      string // owning MCP server (mcp_servers.json name)
	Description string
	InputSchema map[string]interface{} // origin JSON Schema, passed through
	ReadOnly    bool                   // origin annotations.readOnlyHint
}

// rpcMCPToolAllowed applies the CHATCLI_MCP_TOOLS exposure policy to one
// proxied MCP tool. Allowlist entries use the prefixed form ("mcp_<tool>");
// safe mode trusts the origin server's readOnlyHint annotation.
func rpcMCPToolAllowed(t mcp.MCPTool, mode string, allow map[string]bool) bool {
	switch mode {
	case "all":
		return true
	case "safe":
		return t.ReadOnlyHint()
	default:
		return allow["@mcp_"+t.Name]
	}
}

// mcpProxyListWait bounds how long ListMCPProxyTools waits for ChatCLI's
// own MCP servers to finish their initial connect pass. Clients that support
// tools.listChanged get a notification either way; the wait keeps the
// catalog complete for clients that only list once. After the initial pass
// the startup channel is closed and listing is immediate.
const mcpProxyListWait = 10 * time.Second

// ListMCPProxyTools returns every tool discovered from connected MCP servers
// that the exposure policy admits (visibility masks from EnabledTools/
// DisabledTools already honored by VisibleTools). Sorted by name. Waits,
// bounded, for the initial connect pass so an early listing doesn't miss
// the aggregated catalog.
func (cli *ChatCLI) ListMCPProxyTools() []RPCMCPToolInfo {
	if cli.mcpManager == nil {
		return nil
	}
	select {
	case <-cli.MCPStartupDone():
	case <-time.After(mcpProxyListWait):
	}
	mode, allow := rpcToolPolicy()
	visible := cli.mcpManager.VisibleTools()
	out := make([]RPCMCPToolInfo, 0, len(visible))
	for _, t := range visible {
		if !rpcMCPToolAllowed(t, mode, allow) {
			continue
		}
		out = append(out, RPCMCPToolInfo{
			Name:        t.Name,
			Server:      t.ServerName,
			Description: t.Description,
			InputSchema: t.Parameters,
			ReadOnly:    t.ReadOnlyHint(),
		})
	}
	return out
}

// RunMCPProxyTool executes one proxied MCP tool. name accepts both the
// prefixed ("mcp_list_regions") and bare ("list_regions") forms; args is the
// caller's raw MCP arguments object, forwarded verbatim to the origin server.
// A tool-level error result (isError) is surfaced as a Go error carrying the
// origin content, so the RPC layer relays it in-band per the MCP convention.
func (cli *ChatCLI) RunMCPProxyTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if cli.mcpManager == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.proxy.not_enabled"))
	}
	bare := strings.TrimPrefix(name, "mcp_")
	var tool *mcp.MCPTool
	for _, t := range cli.mcpManager.VisibleTools() {
		if t.Name == bare {
			tt := t
			tool = &tt
			break
		}
	}
	if tool == nil {
		return "", fmt.Errorf("%s", i18n.T("mcp.proxy.tool_not_found", bare))
	}
	mode, allow := rpcToolPolicy()
	if !rpcMCPToolAllowed(*tool, mode, allow) {
		return "", fmt.Errorf("%s", i18n.T("mcp.proxy.policy_blocked", bare, mode))
	}

	var argMap map[string]interface{}
	if trimmed := strings.TrimSpace(string(args)); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal(args, &argMap); err != nil {
			return "", fmt.Errorf("invalid arguments for %q: %w", bare, err)
		}
	}
	result, err := cli.mcpManager.ExecuteTool(ctx, bare, argMap)
	if err != nil {
		return "", err
	}
	if result.IsError {
		return "", fmt.Errorf("%s", result.Content)
	}
	return result.Content, nil
}

// MCPStartupDone returns a channel closed once the initial StartAll pass
// over the configured MCP servers has finished (regardless of per-server
// success). When MCP is disabled the channel is already closed, so callers
// can always select on it.
func (cli *ChatCLI) MCPStartupDone() <-chan struct{} {
	if cli.mcpStartupDone == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return cli.mcpStartupDone
}

// SetMCPToolsObserver registers fn to run whenever the MCP tool catalog
// changes (servers connecting, dynamic list_changed refreshes, disconnects).
// No-op when MCP is disabled. The RPC server uses it to relay
// notifications/tools/list_changed to its own clients.
func (cli *ChatCLI) SetMCPToolsObserver(fn func()) {
	if cli.mcpManager != nil {
		cli.mcpManager.SetCatalogObserver(fn)
	}
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

// SaveSessionRPC persists a chat history under name in the saved-session
// store (the same store the REPL /session command uses). Name validation is
// the store's own (alphanumeric with dash/underscore/dot).
func (cli *ChatCLI) SaveSessionRPC(name string, history []models.Message) error {
	if cli.sessionManager == nil {
		return fmt.Errorf("%s", i18n.T("rpc.session.store_unavailable"))
	}
	return cli.sessionManager.SaveSession(name, history)
}

// LoadSessionRPC returns a saved session's chat history. The name is
// validated before touching the store — this surface is reachable by
// remote MCP clients.
func (cli *ChatCLI) LoadSessionRPC(name string) ([]models.Message, error) {
	if cli.sessionManager == nil {
		return nil, fmt.Errorf("%s", i18n.T("rpc.session.store_unavailable"))
	}
	if err := validateSessionName(name); err != nil {
		return nil, err
	}
	return cli.sessionManager.LoadSession(name)
}

// ListSessionsRPC returns the saved session names.
func (cli *ChatCLI) ListSessionsRPC() ([]string, error) {
	if cli.sessionManager == nil {
		return nil, fmt.Errorf("%s", i18n.T("rpc.session.store_unavailable"))
	}
	return cli.sessionManager.ListSessions()
}

// DeleteSessionRPC removes a saved session, validating the name first.
func (cli *ChatCLI) DeleteSessionRPC(name string) error {
	if cli.sessionManager == nil {
		return fmt.Errorf("%s", i18n.T("rpc.session.store_unavailable"))
	}
	if err := validateSessionName(name); err != nil {
		return err
	}
	return cli.sessionManager.DeleteSession(name)
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

// rpcProviderListTimeout bounds the per-call window for live model listing
// across every provider. Providers that answer in time contribute their API
// models (merged with the catalog, like the interactive /model picker);
// slow or failing ones degrade to the static catalog for this call.
const rpcProviderListTimeout = 10 * time.Second

// ProvidersRPC returns a JSON document describing the configured providers,
// the active provider/model, and each provider's models — the discovery
// surface for per-call routing. Models come from the same path the
// interactive picker uses (ListModelsForProvider: API listing when the
// provider supports it, merged and deduplicated with the catalog), fetched
// concurrently with a bounded timeout and a catalog fallback per provider.
func (cli *ChatCLI) ProvidersRPC() (string, error) {
	type providerInfo struct {
		Name   string   `json:"name"`
		Active bool     `json:"active"`
		Models []string `json:"models"`
		// ModelsSource is "api+catalog" when the provider answered the
		// live listing, "catalog" when this call fell back to the static
		// catalog (unsupported, error, or timeout).
		ModelsSource string `json:"models_source"`
	}
	var out struct {
		ActiveProvider string         `json:"active_provider"`
		ActiveModel    string         `json:"active_model"`
		Providers      []providerInfo `json:"providers"`
	}
	out.ActiveProvider = cli.Provider
	out.ActiveModel = cli.Model

	names := cli.manager.GetAvailableProviders()
	infos := make([]providerInfo, len(names))

	ctx, cancel := context.WithTimeout(context.Background(), rpcProviderListTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			info := providerInfo{Name: name, Active: name == cli.Provider}
			models, err := cli.manager.ListModelsForProvider(ctx, name)
			if err == nil && len(models) > 0 {
				sawAPI := false
				for _, m := range models {
					info.Models = append(info.Models, m.ID)
					if m.Source != client.ModelSourceCatalog {
						sawAPI = true
					}
				}
				info.ModelsSource = "catalog"
				if sawAPI {
					info.ModelsSource = "api+catalog"
				}
			} else {
				for _, meta := range catalog.ListByProvider(name) {
					info.Models = append(info.Models, meta.ID)
				}
				info.ModelsSource = "catalog"
			}
			infos[i] = info
		}(i, name)
	}
	wg.Wait()
	out.Providers = infos

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
