/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// SlashToolEntry describes a slash command we expose to the LLM as a
// tool. The shape mirrors what the model needs to know about any tool:
// a stable name, a one-line description, a JSON schema for inputs, and
// the function that produces the output.
//
// The LLM-visible name uses an `@cmd:` prefix (e.g. `@cmd:help`) so it
// can be discriminated in tool dispatch without colliding with the
// existing `@coder`/`@websearch` builtins or with MCP `mcp_*` tools.
// Users still type the unprefixed slash form (`/help`) at the prompt.
//
// Handler receives the parsed input map (already JSON-decoded) and the
// invocation context, and returns the string to surface back to the
// LLM. Errors are surfaced via the second return value and mapped to
// IsError on the plugin side.
type SlashToolEntry struct {
	Name        string // canonical slash form, e.g. "/help"
	Description string // one-line, i18n-resolved
	InputSchema string // JSON schema; can be empty for no-arg commands
	Handler     func(ctx context.Context, args map[string]any) (string, error)

	// ReadOnly tells the orchestrator's partition policy whether this
	// command can run in a concurrent batch with other read-only tools.
	// /help, /version, /session list, /memory list are read-only.
	// /context attach, /skill add are not.
	ReadOnly bool
}

// slashToolRegistry is the in-process catalogue of slash commands
// exposed to the LLM as tools. Populated by RegisterSlashTool at init
// time; resolved by the plugin manager at agent-mode startup.
var (
	slashToolRegistryMu sync.RWMutex
	slashToolRegistry   = make(map[string]*SlashToolEntry)
)

// RegisterSlashTool adds an entry to the registry. Idempotent: a second
// registration with the same Name overwrites the first.
func RegisterSlashTool(entry *SlashToolEntry) {
	if entry == nil || entry.Name == "" {
		return
	}
	slashToolRegistryMu.Lock()
	defer slashToolRegistryMu.Unlock()
	slashToolRegistry[entry.Name] = entry
}

// LookupSlashTool returns the entry for a canonical slash name (with or
// without the leading slash), or nil when not registered.
func LookupSlashTool(name string) *SlashToolEntry {
	n := strings.TrimPrefix(strings.TrimSpace(name), "/")
	if n == "" {
		return nil
	}
	slashToolRegistryMu.RLock()
	defer slashToolRegistryMu.RUnlock()
	return slashToolRegistry["/"+n]
}

// AllSlashTools returns a deterministic snapshot of every registered
// entry, sorted by name. Used to seed the plugin manager.
func AllSlashTools() []*SlashToolEntry {
	slashToolRegistryMu.RLock()
	defer slashToolRegistryMu.RUnlock()
	out := make([]*SlashToolEntry, 0, len(slashToolRegistry))
	for _, e := range slashToolRegistry {
		out = append(out, e)
	}
	// Stable order so the plugin list emitted to the model is
	// reproducible turn-to-turn (helps prompt caching).
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].Name > out[j].Name {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// slashToolPlugin adapts a SlashToolEntry into the plugins.Plugin
// interface. It also implements ReadOnlyAware / ConcurrencySafeAware so
// the orchestrator's partition policy can group read-only slash calls
// in concurrent batches alongside other read-only builtins.
type slashToolPlugin struct {
	entry *SlashToolEntry
}

// NewSlashToolPlugin wraps the entry. Returns nil for nil input so
// callers can pass the result of LookupSlashTool unchecked.
func NewSlashToolPlugin(entry *SlashToolEntry) plugins.Plugin {
	if entry == nil {
		return nil
	}
	return &slashToolPlugin{entry: entry}
}

// Name returns the LLM-visible tool name, prefixed with `@cmd:` so it
// is unmistakably a slash-command adapter (distinct from @coder /
// @websearch / @webfetch builtins and mcp_* MCP tools).
func (p *slashToolPlugin) Name() string {
	return "@cmd:" + strings.TrimPrefix(p.entry.Name, "/")
}

// Description forwards the entry's already-i18n-resolved description.
func (p *slashToolPlugin) Description() string { return p.entry.Description }

// Usage is a generic JSON-flag example so the model has a quick
// reference; the precise contract is in the schema.
func (p *slashToolPlugin) Usage() string {
	return fmt.Sprintf("%s args-as-JSON", p.Name())
}

// Version reports the chatcli minor stream — slash tools don't have an
// independent version since they're tied to the binary.
func (p *slashToolPlugin) Version() string { return "1.0.0" }

// Path is a sentinel matching the existing builtin convention.
func (p *slashToolPlugin) Path() string { return "[builtin-slash]" }

// Schema returns the entry's JSON schema. When empty, the registry
// callers supply a minimal `{}` so downstream tooling-aware code
// doesn't trip on empty strings.
func (p *slashToolPlugin) Schema() string {
	if strings.TrimSpace(p.entry.InputSchema) == "" {
		return `{"type":"object","additionalProperties":true}`
	}
	return p.entry.InputSchema
}

// Execute parses the argv and dispatches to the handler.
func (p *slashToolPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream parses the JSON argv (first arg is the full JSON
// envelope, or args is empty meaning no-args invocation) and calls the
// handler. onOutput is ignored: slash commands return their entire
// result in one shot, like a function call.
func (p *slashToolPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	parsed := map[string]any{}
	if len(args) > 0 {
		raw := strings.TrimSpace(args[0])
		if raw != "" && raw != "{}" {
			if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
				// Fall through with empty map — handler decides whether it
				// needs arguments.
				parsed = map[string]any{}
			}
		}
	}
	if p.entry.Handler == nil {
		return "", fmt.Errorf("%s: no handler registered", p.entry.Name)
	}
	return p.entry.Handler(ctx, parsed)
}

// IsReadOnly mirrors the entry flag for the orchestrator.
func (p *slashToolPlugin) IsReadOnly(_ []string) bool { return p.entry.ReadOnly }

// IsConcurrencySafe matches ReadOnly: a read-only slash command (no
// shared-state mutation) can run in parallel batches.
func (p *slashToolPlugin) IsConcurrencySafe(_ []string) bool { return p.entry.ReadOnly }

// registerBuiltinSlashTools wires the curated set of slash commands
// the model is allowed to invoke. Adding a new command here is a
// deliberate decision — slash commands have access to internal state
// that external plugins do not, and exposing them broadens the model's
// reach into the CLI's own surface area.
//
// Initial set (conservative — read-only metadata commands only):
//   - /help     → human-readable usage summary
//   - /version  → build metadata
//
// /session list, /context list, /memory list will be added once their
// handlers are extracted into top-level functions returning strings
// (today they print directly to stdout, which doesn't fit the
// request-response shape the model expects).
func (cli *ChatCLI) registerBuiltinSlashTools() {
	RegisterSlashTool(&SlashToolEntry{
		Name:        "/help",
		Description: i18n.T("slash.help.description"),
		ReadOnly:    true,
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return cli.helpText(), nil
		},
	})
	RegisterSlashTool(&SlashToolEntry{
		Name:        "/version",
		Description: i18n.T("slash.version.description"),
		ReadOnly:    true,
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return cli.versionText(), nil
		},
	})
}
