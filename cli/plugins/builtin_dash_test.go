/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedDashAdapter records the call the plugin made.
type scriptedDashAdapter struct {
	calls []string
	query DashEventsQuery
	note  string
	fail  error
}

func (s *scriptedDashAdapter) Status(context.Context) (string, error) {
	s.calls = append(s.calls, "status")
	return "status-out", s.fail
}
func (s *scriptedDashAdapter) Open(_ context.Context, browser bool) (string, error) {
	if browser {
		s.calls = append(s.calls, "open")
	} else {
		s.calls = append(s.calls, "url")
	}
	return "open-out", s.fail
}
func (s *scriptedDashAdapter) Off(context.Context) (string, error) {
	s.calls = append(s.calls, "off")
	return "off-out", s.fail
}
func (s *scriptedDashAdapter) Summary(_ context.Context, all bool) (string, error) {
	s.calls = append(s.calls, "summary")
	if all {
		s.calls = append(s.calls, "all")
	}
	return "summary-out", s.fail
}
func (s *scriptedDashAdapter) Events(_ context.Context, q DashEventsQuery) (string, error) {
	s.calls = append(s.calls, "events")
	s.query = q
	return "events-out", s.fail
}
func (s *scriptedDashAdapter) Mark(_ context.Context, note string) (string, error) {
	s.calls = append(s.calls, "mark")
	s.note = note
	return "mark-out", s.fail
}

func TestParseDashInvocationShapes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want dashInvocation
	}{
		{"empty is status", nil, dashInvocation{cmd: "status"}},
		{"envelope summary all", []string{`{"cmd":"summary","args":{"all":true}}`}, dashInvocation{cmd: "summary", all: true}},
		{"envelope scope all", []string{`{"cmd":"summary","args":{"scope":"all"}}`}, dashInvocation{cmd: "summary", all: true}},
		{"envelope events", []string{`{"cmd":"events","args":{"kind":"Tool","status":"error","limit":20}}`}, dashInvocation{cmd: "events", kind: "tool", status: "error", limit: 20}},
		{"envelope stringified limit", []string{`{"cmd":"events","args":{"limit":"5"}}`}, dashInvocation{cmd: "events", limit: 5}},
		{"flat envelope mark", []string{`{"cmd":"mark","note":"phase: tests"}`}, dashInvocation{cmd: "mark", note: "phase: tests"}},
		{"note alone implies mark", []string{`{"label":"phase: docs"}`}, dashInvocation{cmd: "mark", note: "phase: docs"}},
		{"alias cmd", []string{`{"cmd":"tail"}`}, dashInvocation{cmd: "events"}},
		{"positional summary all", []string{"summary", "all"}, dashInvocation{cmd: "summary", all: true}},
		{"positional events limit", []string{"events", "40"}, dashInvocation{cmd: "events", limit: 40}},
		{"positional events kind", []string{"events", "llm"}, dashInvocation{cmd: "events", kind: "llm"}},
		{"flag form from the flattener", []string{"events", "--kind", "tool", "--status", "error", "--limit", "20", "--all"}, dashInvocation{cmd: "events", kind: "tool", status: "error", limit: 20, all: true}},
		{"flag cmd", []string{"--cmd", "url"}, dashInvocation{cmd: "url"}},
		{"multi-word note as one argv element", []string{"mark", "--note", "phase: running the suite"}, dashInvocation{cmd: "mark", note: "phase: running the suite"}},
		{"positional note words", []string{"mark", "phase:", "tests", "green"}, dashInvocation{cmd: "mark", note: "phase: tests green"}},
		{"single free text keeps quotes together", []string{`mark --note "phase: two words"`}, dashInvocation{cmd: "mark", note: "phase: two words"}},
		{"unknown flag ignored", []string{"status", "--verbose"}, dashInvocation{cmd: "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDashInvocation(tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	_, err := parseDashInvocation([]string{`{"cmd":`})
	require.Error(t, err)
}

func TestDashPluginDispatch(t *testing.T) {
	s := &scriptedDashAdapter{}
	SetDashAdapter(s)
	t.Cleanup(func() { SetDashAdapter(nil) })
	p := NewBuiltinDashPlugin()
	ctx := context.Background()

	out, err := p.Execute(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, "status-out", out)
	_, err = p.Execute(ctx, []string{`{"cmd":"open"}`})
	require.NoError(t, err)
	_, err = p.Execute(ctx, []string{`{"cmd":"url"}`})
	require.NoError(t, err)
	_, err = p.Execute(ctx, []string{`{"cmd":"off"}`})
	require.NoError(t, err)
	_, err = p.Execute(ctx, []string{`{"cmd":"summary","args":{"all":true}}`})
	require.NoError(t, err)
	_, err = p.Execute(ctx, []string{`{"cmd":"events","args":{"kind":"tool","status":"error","limit":7}}`})
	require.NoError(t, err)
	assert.Equal(t, DashEventsQuery{Limit: 7, Kind: "tool", Status: "error"}, s.query)
	_, err = p.Execute(ctx, []string{`{"cmd":"mark","args":{"note":"phase: tests"}}`})
	require.NoError(t, err)
	assert.Equal(t, "phase: tests", s.note)
	assert.Equal(t, []string{"status", "open", "url", "off", "summary", "all", "events", "mark"}, s.calls)

	_, err = p.Execute(ctx, []string{`{"cmd":"mark","args":{"note":"   "}}`})
	require.Error(t, err, "mark without a note is refused before reaching the adapter")
	_, err = p.Execute(ctx, []string{`{"cmd":"explode"}`})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cmd")

	s.fail = errors.New("boom")
	_, err = p.Execute(ctx, []string{"status"})
	require.Error(t, err)

	SetDashAdapter(nil)
	_, err = p.Execute(ctx, []string{"status"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestDashPluginCapabilitiesAndCatalog(t *testing.T) {
	p := NewBuiltinDashPlugin()
	assert.Equal(t, "@dash", p.Name())
	assert.Equal(t, "[builtin]", p.Path())
	assert.NotEmpty(t, p.Version())
	assert.Contains(t, p.Description(), "summary")
	assert.Contains(t, p.Usage(), `"cmd":"mark"`)

	var schema struct {
		Subcommands []struct {
			Name string `json:"name"`
		} `json:"subcommands"`
	}
	require.NoError(t, json.Unmarshal([]byte(p.Schema()), &schema))
	var names []string
	for _, s := range schema.Subcommands {
		names = append(names, s.Name)
	}
	assert.Equal(t, []string{"status", "open", "url", "off", "summary", "events", "mark"}, names)

	for _, ro := range []string{`{"cmd":"status"}`, `{"cmd":"url"}`, `{"cmd":"summary"}`, `{"cmd":"events"}`} {
		assert.True(t, p.IsReadOnly([]string{ro}), ro)
		assert.True(t, p.IsConcurrencySafe([]string{ro}), ro)
	}
	for _, rw := range []string{`{"cmd":"open"}`, `{"cmd":"off"}`, `{"cmd":"mark","args":{"note":"x"}}`} {
		assert.False(t, p.IsReadOnly([]string{rw}), rw)
		assert.False(t, p.IsConcurrencySafe([]string{rw}), rw)
	}
	assert.False(t, p.IsReadOnly([]string{`{"cmd":`}), "unparseable is not read-only")

	for _, c := range []string{"status", "open", "url", "off", "summary", "events", "mark --note x"} {
		label := p.DescribeCall(strings.Fields(c))
		assert.NotEmpty(t, label, c)
		assert.NotEqual(t, p.Description(), label, c)
	}
	assert.Equal(t, p.Description(), p.DescribeCall([]string{`{"cmd":`}))
}
