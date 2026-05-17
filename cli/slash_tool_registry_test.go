/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetSlashRegistry clears the package-level registry between tests so
// one test's RegisterSlashTool doesn't leak into another. We do not
// export this helper — it is test-only state hygiene.
func resetSlashRegistry(t *testing.T) {
	t.Helper()
	slashToolRegistryMu.Lock()
	defer slashToolRegistryMu.Unlock()
	slashToolRegistry = make(map[string]*SlashToolEntry)
}

// TestRegisterSlashTool_PutAndLookup confirms the basic registry
// behavior: adding an entry by canonical name and retrieving it with
// or without the leading slash.
func TestRegisterSlashTool_PutAndLookup(t *testing.T) {
	resetSlashRegistry(t)
	entry := &SlashToolEntry{
		Name:        "/foo",
		Description: "test foo",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "ok", nil
		},
	}
	RegisterSlashTool(entry)

	assert.Equal(t, entry, LookupSlashTool("/foo"))
	assert.Equal(t, entry, LookupSlashTool("foo"), "lookup must accept name without leading slash")
}

// TestRegisterSlashTool_OverwriteIsIdempotent pins that a second
// registration overwrites the first — useful when test fixtures or
// hot-reload paths re-register entries.
func TestRegisterSlashTool_OverwriteIsIdempotent(t *testing.T) {
	resetSlashRegistry(t)
	RegisterSlashTool(&SlashToolEntry{Name: "/dup", Description: "old"})
	RegisterSlashTool(&SlashToolEntry{Name: "/dup", Description: "new"})

	got := LookupSlashTool("/dup")
	require.NotNil(t, got)
	assert.Equal(t, "new", got.Description)
}

// TestRegisterSlashTool_RejectsEmpty documents the defensive guard that
// keeps the registry from being polluted by nil or unnamed entries.
func TestRegisterSlashTool_RejectsEmpty(t *testing.T) {
	resetSlashRegistry(t)
	RegisterSlashTool(nil)
	RegisterSlashTool(&SlashToolEntry{Name: ""})
	assert.Empty(t, AllSlashTools())
}

// TestAllSlashTools_StableOrder confirms the snapshot is sorted by
// Name so the tool catalog emitted to the model is reproducible
// across turns (important for prompt caching).
func TestAllSlashTools_StableOrder(t *testing.T) {
	resetSlashRegistry(t)
	RegisterSlashTool(&SlashToolEntry{Name: "/zeta"})
	RegisterSlashTool(&SlashToolEntry{Name: "/alpha"})
	RegisterSlashTool(&SlashToolEntry{Name: "/mu"})

	all := AllSlashTools()
	require.Len(t, all, 3)
	assert.Equal(t, "/alpha", all[0].Name)
	assert.Equal(t, "/mu", all[1].Name)
	assert.Equal(t, "/zeta", all[2].Name)
}

// TestSlashToolPlugin_ImplementsPluginContract is the structural check:
// a wrapped entry must satisfy plugins.Plugin so the manager can store
// it alongside the @coder/@websearch builtins.
func TestSlashToolPlugin_ImplementsPluginContract(t *testing.T) {
	entry := &SlashToolEntry{
		Name:        "/help",
		Description: "help text",
		ReadOnly:    true,
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "summary", nil
		},
	}
	var p plugins.Plugin = NewSlashToolPlugin(entry)
	require.NotNil(t, p)
	assert.Equal(t, "@cmd:help", p.Name())
	assert.Equal(t, "help text", p.Description())
	assert.Equal(t, "[builtin-slash]", p.Path())
	assert.NotEmpty(t, p.Schema(), "schema must default to a valid empty-object JSON schema")
}

// TestSlashToolPlugin_RunsHandler is the end-to-end smoke test: the
// plugin parses JSON args, invokes the handler, and returns the
// handler's string output.
func TestSlashToolPlugin_RunsHandler(t *testing.T) {
	captured := map[string]any{}
	entry := &SlashToolEntry{
		Name:        "/echo",
		Description: "echo",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			for k, v := range args {
				captured[k] = v
			}
			return "echoed", nil
		},
	}
	p := NewSlashToolPlugin(entry)
	out, err := p.ExecuteWithStream(context.Background(), []string{`{"msg":"hi"}`}, nil)
	require.NoError(t, err)
	assert.Equal(t, "echoed", out)
	assert.Equal(t, "hi", captured["msg"])
}

// TestSlashToolPlugin_EmptyArgsParsesToEmptyMap keeps the no-args call
// shape working — the handler still gets a (non-nil) empty map.
func TestSlashToolPlugin_EmptyArgsParsesToEmptyMap(t *testing.T) {
	var seen map[string]any
	entry := &SlashToolEntry{
		Name: "/noop",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			seen = args
			return "ok", nil
		},
	}
	_, err := NewSlashToolPlugin(entry).ExecuteWithStream(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.NotNil(t, seen, "handler must always receive a non-nil map")
	assert.Empty(t, seen)
}

// TestSlashToolPlugin_HandlesMalformedJSONGracefully ensures a broken
// argv from the model doesn't take down the dispatch — the handler is
// invoked with an empty map and can decide whether that's an error.
func TestSlashToolPlugin_HandlesMalformedJSONGracefully(t *testing.T) {
	calls := 0
	entry := &SlashToolEntry{
		Name: "/lenient",
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			calls++
			return "ok", nil
		},
	}
	_, err := NewSlashToolPlugin(entry).ExecuteWithStream(context.Background(), []string{"this is not json"}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "handler is still invoked when JSON is malformed")
}

// TestSlashToolPlugin_ReadOnlyAndConcurrencyAwareReflectEntry confirms
// the plugin propagates the entry's capability flags so the
// orchestrator's partition policy can group read-only slash calls into
// concurrent batches with other read-only tools.
func TestSlashToolPlugin_ReadOnlyAndConcurrencyAwareReflectEntry(t *testing.T) {
	roEntry := &SlashToolEntry{Name: "/info", ReadOnly: true}
	rwEntry := &SlashToolEntry{Name: "/mutate", ReadOnly: false}

	ro := NewSlashToolPlugin(roEntry)
	rw := NewSlashToolPlugin(rwEntry)

	assert.True(t, plugins.IsReadOnly(ro, nil))
	assert.True(t, plugins.IsConcurrencySafe(ro, nil))
	assert.False(t, plugins.IsReadOnly(rw, nil))
	assert.False(t, plugins.IsConcurrencySafe(rw, nil))
}

// TestSlashToolPlugin_PropagatesHandlerError forwards a handler error
// up to the caller so the agent loop can report IsError=true.
func TestSlashToolPlugin_PropagatesHandlerError(t *testing.T) {
	entry := &SlashToolEntry{
		Name: "/fail",
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			return "", assert.AnError
		},
	}
	out, err := NewSlashToolPlugin(entry).ExecuteWithStream(context.Background(), nil, nil)
	assert.Error(t, err)
	assert.Empty(t, out)
}
