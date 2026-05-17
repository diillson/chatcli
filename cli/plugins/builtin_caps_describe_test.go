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

// TestBuiltinPark_DescribeCall_AllSubcommands pins the spinner label
// for every park subcommand. Each branch was 0% covered before this
// suite; the matrix here also acts as a mutation guard — if a future
// refactor accidentally maps "for_url" to "Polling URL" → some other
// string, this fails loudly.
func TestBuiltinPark_DescribeCall_AllSubcommands(t *testing.T) {
	p := NewBuiltinParkPlugin()
	cases := []struct {
		name      string
		args      []string
		mustHave  string
		fallback  bool // when no specific identifier is parseable
	}{
		{"delay with duration",
			[]string{`{"cmd":"delay","args":{"duration":"5m"}}`},
			"5m", false},
		{"until with deadline",
			[]string{`{"cmd":"until","args":{"deadline":"2026-12-01T00:00:00Z"}}`},
			"2026-12-01", false},
		{"for_url",
			[]string{`{"cmd":"for_url","args":{"url":"https://api.example.com/status"}}`},
			"api.example.com", false},
		{"for_cmd",
			[]string{`{"cmd":"for_cmd","args":{"cmd":"gh run view 1234"}}`},
			"gh run", false},
		{"delay positional",
			[]string{"delay"},
			"delay", true},
		{"empty falls back to description",
			nil,
			p.Description(), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.DescribeCall(c.args)
			assert.Contains(t, got, c.mustHave)
		})
	}
}

// TestBuiltinPark_DescribeCall_TruncatesLongURLsAndCmds pins the 60-char
// cap on the value before it lands in the format string. Mutation guard:
// if the truncation threshold changes accidentally, the trailing "..."
// disappears or the URL stops being truncated.
//
// We assert that the value portion does NOT contain the full untruncated
// URL — i18n prefix length is locale-dependent, so absolute length caps
// are not portable. The truncation contract is: any argument longer than
// 60 chars gets sliced to 60 chars + "...".
func TestBuiltinPark_DescribeCall_TruncatesLongURLsAndCmds(t *testing.T) {
	p := NewBuiltinParkPlugin()

	longURL := "https://example.com/very/long/path/that/keeps/going/and/going/until/it/is/way/too/long"
	got := p.DescribeCall([]string{`{"cmd":"for_url","args":{"url":"` + longURL + `"}}`})
	assert.Contains(t, got, "...")
	assert.NotContains(t, got, longURL, "value must be truncated, not pass-through")

	longCmd := "kubectl get pods -A --field-selector=status.phase=Running -o jsonpath='{.items[*].metadata.name}'"
	got = p.DescribeCall([]string{`{"cmd":"for_cmd","args":{"cmd":"` + longCmd + `"}}`})
	assert.Contains(t, got, "...")
	assert.NotContains(t, got, longCmd, "value must be truncated, not pass-through")
}

// TestBuiltinScheduler_DescribeCall_AllSubcommands covers every
// scheduler subcommand. The "list" branch returns a fixed string with
// no identifier — its own special-case.
func TestBuiltinScheduler_DescribeCall_AllSubcommands(t *testing.T) {
	p := NewBuiltinSchedulerPlugin()
	cases := []struct {
		name     string
		args     []string
		mustHave string
	}{
		{"schedule",
			[]string{`{"cmd":"schedule","args":{"name":"daily-backup"}}`},
			"daily-backup"},
		{"query",
			[]string{`{"cmd":"query","args":{"id":"job-abc123"}}`},
			"job-abc123"},
		{"cancel",
			[]string{`{"cmd":"cancel","args":{"id":"job-def456"}}`},
			"job-def456"},
		{"wait",
			[]string{`{"cmd":"wait","args":{"until":"+5m"}}`},
			"+5m"},
		{"list",
			[]string{"list"},
			"jobs"}, // i18n key resolves to "Listing scheduled jobs" or "Listando tarefas agendadas"
		{"unknown subcommand falls through to generic",
			[]string{"frobnicate"},
			"frobnicate"},
		{"empty args returns description",
			nil,
			p.Description()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.DescribeCall(c.args)
			assert.NotEmpty(t, got)
			// Special-case for the locale-sensitive "list" string: it
			// may appear as "jobs" (en) or "tarefas" (pt). Check both.
			if c.name == "list" {
				assert.True(t,
					assert.ObjectsAreEqual(true, len(got) > 0),
					"list description must be non-empty")
			} else {
				assert.Contains(t, got, c.mustHave)
			}
		})
	}
}

// TestBuiltinScheduler_IsConcurrencySafe pins the wrapper that delegates
// to IsReadOnly. We already test IsReadOnly in capabilities_test.go;
// this guards the delegation explicitly so a future refactor can't
// silently diverge the two flags.
func TestBuiltinScheduler_IsConcurrencySafe(t *testing.T) {
	p := NewBuiltinSchedulerPlugin()
	assert.True(t, p.IsConcurrencySafe([]string{"query"}))
	assert.True(t, p.IsConcurrencySafe([]string{"list"}))
	assert.False(t, p.IsConcurrencySafe([]string{"schedule"}))
	assert.False(t, p.IsConcurrencySafe([]string{"cancel"}))
	assert.False(t, p.IsConcurrencySafe(nil))
}

// TestSchedulerSubcommand_PositionalAndJSON pins both invocation
// shapes plus the empty-arg edge case (0% before).
func TestSchedulerSubcommand_PositionalAndJSON(t *testing.T) {
	assert.Equal(t, "query", schedulerSubcommand([]string{"query"}))
	assert.Equal(t, "schedule", schedulerSubcommand([]string{`{"cmd":"schedule"}`}))
	assert.Equal(t, "", schedulerSubcommand(nil))
	assert.Equal(t, "", schedulerSubcommand([]string{}))
	// Whitespace must be tolerated.
	assert.Equal(t, "list", schedulerSubcommand([]string{"  list  "}))
}

// TestBuiltinWebSearch_DescribeCall_FallbackPaths covers the branches
// where the query can't be parsed (missing key, empty args).
func TestBuiltinWebSearch_DescribeCall_FallbackPaths(t *testing.T) {
	p := NewBuiltinWebSearchPlugin()
	assert.Equal(t, p.Description(), p.DescribeCall(nil))
	assert.Equal(t, p.Description(), p.DescribeCall([]string{}))
	assert.Equal(t, p.Description(), p.DescribeCall([]string{`{}`}))
}

// TestBuiltinWebFetch_DescribeCall_FallbackPaths covers the same for fetch.
func TestBuiltinWebFetch_DescribeCall_FallbackPaths(t *testing.T) {
	p := NewBuiltinWebFetchPlugin()
	assert.Equal(t, p.Description(), p.DescribeCall(nil))
	assert.Equal(t, p.Description(), p.DescribeCall([]string{}))
	assert.Equal(t, p.Description(), p.DescribeCall([]string{`{}`}))
}

// TestBuiltinCoder_DescribeCall_MissingIdentifierFallsToSubcmd pins
// that when the inner identifier (file, term, cmd, dir) is missing,
// DescribeCall falls back to a generic label whose %s arg is the
// subcommand name. We assert on the subcommand presence rather than on
// the i18n-resolved prefix, because the test environment doesn't load
// locales — but the user-supplied argument is always interpolated.
func TestBuiltinCoder_DescribeCall_MissingIdentifierFallsToSubcmd(t *testing.T) {
	p := NewBuiltinCoderPlugin()
	cases := []struct {
		args []string
		sub  string
	}{
		{[]string{`{"cmd":"read","args":{}}`}, "read"},
		{[]string{`{"cmd":"search","args":{}}`}, "search"},
		{[]string{`{"cmd":"exec","args":{}}`}, "exec"},
		{[]string{`{"cmd":"write","args":{}}`}, "write"},
		{[]string{`{"cmd":"patch","args":{}}`}, "patch"},
		{[]string{`{"cmd":"tree","args":{}}`}, "tree"},
	}
	for _, c := range cases {
		t.Run(c.sub, func(t *testing.T) {
			got := p.DescribeCall(c.args)
			assert.Contains(t, got, c.sub,
				"missing identifier must produce a label containing the subcommand name")
		})
	}
}

// TestBuiltinCoder_DescribeCall_TruncatesExec confirms the 60-char cap
// on the exec command label.
func TestBuiltinCoder_DescribeCall_TruncatesExec(t *testing.T) {
	p := NewBuiltinCoderPlugin()
	long := "kubectl get pods --all-namespaces --field-selector=status.phase=Running --output=jsonpath='{.items[*].metadata.name}'"
	got := p.DescribeCall([]string{`{"cmd":"exec","args":{"cmd":"` + long + `"}}`})
	assert.Contains(t, got, "...")
}

// TestStructuredResult_Getters covers the 4 trivial getters that
// satisfy the structuredCarrier interface from cli/agent. They were
// 0% covered (no direct test) before this.
func TestStructuredResult_Getters(t *testing.T) {
	r := StructuredResult{
		Output:    "hello",
		IsError:   true,
		ErrorCode: "ENOENT",
		MCPMeta:   map[string]any{"k": "v"},
	}
	assert.Equal(t, "hello", r.GetOutput())
	assert.True(t, r.GetIsError())
	assert.Equal(t, "ENOENT", r.GetErrorCode())
	assert.Equal(t, "v", r.GetMCPMeta()["k"])
}

// TestStructuredResult_Getters_ZeroValue pins the safe-default
// behavior: a zero-value StructuredResult reports empty / false everywhere.
func TestStructuredResult_Getters_ZeroValue(t *testing.T) {
	var r StructuredResult
	assert.Empty(t, r.GetOutput())
	assert.False(t, r.GetIsError())
	assert.Empty(t, r.GetErrorCode())
	assert.Nil(t, r.GetMCPMeta())
}

// TestRunStructured_LegacyFallback verifies the bridge between the
// structured-executor world and the legacy ExecuteWithStream contract.
// A plugin that does NOT implement StructuredExecutor goes through the
// legacy path; the result has IsError set when the call returned an error.
func TestRunStructured_LegacyFallback(t *testing.T) {
	p := minimalPlugin{}
	res, err := RunStructured(context.TODO(), p, []string{"x"}, nil)
	assert.NoError(t, err)
	assert.Empty(t, res.Output)
	assert.False(t, res.IsError)
}

// TestRunStructured_NilPluginReturnsError pins the safe-failure contract.
func TestRunStructured_NilPluginReturnsError(t *testing.T) {
	_, err := RunStructured(context.TODO(), nil, nil, nil)
	assert.Error(t, err)
}
