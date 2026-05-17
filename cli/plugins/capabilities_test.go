/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// minimalPlugin implements the bare Plugin interface and nothing else,
// used to verify that the helpers return fail-closed defaults for plugins
// that don't opt into the new capabilities.
type minimalPlugin struct{}

func (minimalPlugin) Name() string        { return "minimal" }
func (minimalPlugin) Description() string { return "minimal plugin" }
func (minimalPlugin) Usage() string       { return "minimal" }
func (minimalPlugin) Version() string     { return "0.0.0" }
func (minimalPlugin) Path() string        { return "[test]" }
func (minimalPlugin) Schema() string      { return "{}" }
func (minimalPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return "", nil
}
func (minimalPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	return "", nil
}

// readOnlyPlugin opts in to ReadOnlyAware. Used to verify the helper
// reflects the opt-in correctly.
type readOnlyPlugin struct{ minimalPlugin }

func (readOnlyPlugin) IsReadOnly(_ []string) bool { return true }

type concurrentPlugin struct{ minimalPlugin }

func (concurrentPlugin) IsConcurrencySafe(_ []string) bool { return true }

type describePlugin struct{ minimalPlugin }

func (describePlugin) DescribeCall(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return "describe:" + args[0]
}

// TestIsReadOnly_DefaultFalse pins the fail-closed contract: a plugin
// that doesn't opt in is treated as side-effecting. This is the safe
// default — the orchestrator can never accidentally parallelize an
// unknown plugin.
func TestIsReadOnly_DefaultFalse(t *testing.T) {
	assert.False(t, IsReadOnly(minimalPlugin{}, []string{"any"}))
	assert.False(t, IsReadOnly(nil, nil), "nil plugin returns false safely")
}

// TestIsReadOnly_OptIn confirms the helper does call into the assertion.
func TestIsReadOnly_OptIn(t *testing.T) {
	assert.True(t, IsReadOnly(readOnlyPlugin{}, []string{"foo"}))
}

// TestIsConcurrencySafe_DefaultFalse mirrors the read-only contract.
func TestIsConcurrencySafe_DefaultFalse(t *testing.T) {
	assert.False(t, IsConcurrencySafe(minimalPlugin{}, nil))
	assert.False(t, IsConcurrencySafe(nil, nil))
}

func TestIsConcurrencySafe_OptIn(t *testing.T) {
	assert.True(t, IsConcurrencySafe(concurrentPlugin{}, nil))
}

// TestDescribeCall_FallsBackToDescription guarantees that callers can
// rely on DescribeCall for the spinner string regardless of which
// interfaces the plugin implements. When neither DescribeCall nor a
// non-empty contextual value is available, the static Description wins.
func TestDescribeCall_FallsBackToDescription(t *testing.T) {
	assert.Equal(t, "minimal plugin", DescribeCall(minimalPlugin{}, nil))
}

func TestDescribeCall_UsesOptIn(t *testing.T) {
	assert.Equal(t, "describe:read", DescribeCall(describePlugin{}, []string{"read"}))
}

// TestDescribeCall_FallbackWhenOptInReturnsEmpty asserts that the helper
// doesn't return "" when a DescribeCall implementation gives up — the
// caller's UI gets the static description instead.
func TestDescribeCall_FallbackWhenOptInReturnsEmpty(t *testing.T) {
	assert.Equal(t, "minimal plugin", DescribeCall(describePlugin{}, nil),
		"empty DescribeCall must fall through to Description()")
}

// TestBuiltinWebSearch_AdvertisesCapabilities verifies the websearch
// builtin opts in to read-only + concurrency-safe + describe, and that
// the describe value includes the query.
func TestBuiltinWebSearch_AdvertisesCapabilities(t *testing.T) {
	p := NewBuiltinWebSearchPlugin()
	assert.True(t, IsReadOnly(p, nil))
	assert.True(t, IsConcurrencySafe(p, nil))
	desc := DescribeCall(p, []string{`{"query":"golang errgroup"}`})
	assert.Contains(t, desc, "golang errgroup")
}

// TestBuiltinWebFetch_AdvertisesCapabilities pins the same for webfetch
// and surfaces the URL in DescribeCall.
func TestBuiltinWebFetch_AdvertisesCapabilities(t *testing.T) {
	p := NewBuiltinWebFetchPlugin()
	assert.True(t, IsReadOnly(p, nil))
	assert.True(t, IsConcurrencySafe(p, nil))
	desc := DescribeCall(p, []string{`{"url":"https://example.com/api"}`})
	assert.Contains(t, desc, "https://example.com/api")
}

// TestBuiltinCoder_ReadOnlyForReads covers the subcommand-aware
// read-only contract: read / search / tree are pure reads;
// exec / write / patch are not.
func TestBuiltinCoder_ReadOnlyForReads(t *testing.T) {
	p := NewBuiltinCoderPlugin()
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"read"}, true},
		{[]string{"search"}, true},
		{[]string{"tree"}, true},
		{[]string{"exec"}, false},
		{[]string{"write"}, false},
		{[]string{"patch"}, false},
		{[]string{`{"cmd":"read","args":{"file":"x"}}`}, true},
		{[]string{`{"cmd":"exec","args":{"cmd":"ls"}}`}, false},
		{nil, false},
	}
	for _, c := range cases {
		t.Run(joinForName(c.args), func(t *testing.T) {
			assert.Equal(t, c.want, IsReadOnly(p, c.args))
			assert.Equal(t, c.want, IsConcurrencySafe(p, c.args))
		})
	}
}

// TestBuiltinCoder_DescribeCallExtractsTarget makes sure that for each
// subcommand the human-readable label includes the relevant identifier:
// path for read/write/patch, term for search, command for exec.
func TestBuiltinCoder_DescribeCallExtractsTarget(t *testing.T) {
	p := NewBuiltinCoderPlugin()
	cases := []struct {
		args   []string
		expect string
	}{
		{[]string{`{"cmd":"read","args":{"file":"main.go"}}`}, "main.go"},
		{[]string{`{"cmd":"search","args":{"term":"Login"}}`}, "Login"},
		{[]string{`{"cmd":"exec","args":{"cmd":"go test ./..."}}`}, "go test"},
	}
	for _, c := range cases {
		t.Run(c.expect, func(t *testing.T) {
			got := DescribeCall(p, c.args)
			assert.Contains(t, got, c.expect)
		})
	}
}

// TestBuiltinScheduler_ReadOnlyForQueryList pins the contract: only
// query / list are read-only; schedule / wait / cancel mutate state.
func TestBuiltinScheduler_ReadOnlyForQueryList(t *testing.T) {
	p := NewBuiltinSchedulerPlugin()
	assert.True(t, IsReadOnly(p, []string{"query"}))
	assert.True(t, IsReadOnly(p, []string{"list"}))
	assert.False(t, IsReadOnly(p, []string{"schedule"}))
	assert.False(t, IsReadOnly(p, []string{"cancel"}))
	assert.False(t, IsReadOnly(p, []string{"wait"}))
}

// TestBuiltinPark_NeverReadOnlyOrConcurrent codifies that @park is a
// state-mutation primitive. The orchestrator must never put it into a
// parallel batch.
func TestBuiltinPark_NeverReadOnlyOrConcurrent(t *testing.T) {
	p := NewBuiltinParkPlugin()
	assert.False(t, IsReadOnly(p, []string{"delay"}))
	assert.False(t, IsConcurrencySafe(p, []string{"delay"}))
}

// TestPromptFor_NoOpForLegacyPlugins returns empty string + no error
// when the plugin doesn't implement Prompter. The orchestrator can
// blindly call PromptFor in a loop without checking types.
func TestPromptFor_NoOpForLegacyPlugins(t *testing.T) {
	got, err := PromptFor(minimalPlugin{}, PromptOpts{ToolName: "x"})
	assert.NoError(t, err)
	assert.Empty(t, got)
}

// TestPushStreamingInput_NoOpForLegacyPlugins ensures the helper
// silently ignores plugins that don't implement StreamingInputAware.
// We can't observe the inverse (a plugin that DOES implement it
// receives the update) here without bringing in a mock plugin from
// the runtime layer; the contract is the no-op guarantee.
func TestPushStreamingInput_NoOpForLegacyPlugins(t *testing.T) {
	PushStreamingInput(minimalPlugin{}, "query", "value") // must not panic
}

func joinForName(args []string) string {
	if len(args) == 0 {
		return "nil"
	}
	return args[0]
}
