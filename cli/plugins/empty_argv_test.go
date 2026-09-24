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
	"github.com/stretchr/testify/require"
)

// The bug this guards: the coder handed @proc an empty argv (a parser leak
// upstream) and parseProcInvocation indexed args[0] — a panic in
// DescribeCall, consulted before the security gate, ended the session.
// Every invocation parser answers an empty argv with an error, never a
// panic, so the model gets the usage line back and corrects itself.
func TestInvocationParsers_EmptyArgvIsAnErrorNotAPanic(t *testing.T) {
	type parser struct {
		name string
		fn   func([]string) error
	}
	parsers := []parser{
		{"proc", func(a []string) error { _, _, err := parseProcInvocation(a); return err }},
		{"tools", func(a []string) error { _, _, err := parseToolsInvocation(a); return err }},
		{"knowledge", func(a []string) error { _, _, err := parseKnowledgeInvocation(a); return err }},
		{"lsp", func(a []string) error { _, _, err := parseLSPInvocation(a); return err }},
		{"memory", func(a []string) error { _, _, err := parseMemoryInvocation(a); return err }},
		{"park", func(a []string) error { _, _, err := parseParkInvocation(a); return err }},
		{"scheduler", func(a []string) error { _, _, err := parseSchedulerInvocation(a); return err }},
		{"view", func(a []string) error { _, err := parseViewInvocation(a); return err }},
		{"http", func(a []string) error { _, err := parseHTTPInvocation(a); return err }},
	}
	for _, p := range parsers {
		for _, argv := range [][]string{nil, {}, {""}, {"  "}} {
			assert.NotPanics(t, func() {
				assert.Error(t, p.fn(argv), "%s with argv %q must report the missing cmd", p.name, argv)
			}, "%s with argv %q", p.name, argv)
		}
	}
	// The two that return no error report through the empty value.
	for _, argv := range [][]string{nil, {}} {
		assert.NotPanics(t, func() {
			out, err := parseFetchArgs(argv)
			require.NoError(t, err)
			assert.Empty(t, out.URL)
			q, _ := parseWebSearchArgs(argv)
			assert.Empty(t, q)
		})
	}
	// The capability probes on the plugin that panicked in the field.
	proc := &BuiltinProcPlugin{}
	assert.NotPanics(t, func() {
		assert.NotEmpty(t, DescribeCall(proc, nil))
		assert.False(t, IsReadOnly(proc, nil), "an unparsable call is not read-only (fail closed)")
	})
}

// panickyPlugin stands in for a plugin whose capability probes crash on an
// argv shape they never expected.
type panickyPlugin struct{}

func (panickyPlugin) Name() string        { return "@panicky" }
func (panickyPlugin) Description() string { return "static description" }
func (panickyPlugin) Usage() string       { return "" }
func (panickyPlugin) Version() string     { return "test" }
func (panickyPlugin) Path() string        { return "" }
func (panickyPlugin) Schema() string      { return "" }
func (panickyPlugin) Execute(_ context.Context, _ []string) (string, error) {
	return "", nil
}
func (panickyPlugin) ExecuteWithStream(_ context.Context, _ []string, _ func(string)) (string, error) {
	return "", nil
}
func (panickyPlugin) IsReadOnly(_ []string) bool        { panic("read-only probe") }
func (panickyPlugin) IsConcurrencySafe(_ []string) bool { panic("concurrency probe") }
func (panickyPlugin) DescribeCall(_ []string) string    { panic("describe probe") }

// A probe that panics degrades to the fail-closed answer: the static
// description, not read-only, not concurrency-safe. The session goes on and
// the plugin's Execute reports the real parse error.
func TestCapabilityProbes_PanicDegradesToFailClosed(t *testing.T) {
	p := panickyPlugin{}
	assert.NotPanics(t, func() {
		assert.Equal(t, "static description", DescribeCall(p, nil))
		assert.False(t, IsReadOnly(p, nil))
		assert.False(t, IsConcurrencySafe(p, nil))
	})
}
