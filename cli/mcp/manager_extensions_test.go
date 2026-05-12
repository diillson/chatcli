/*
 * ChatCLI - Tests for Manager APIs added by the config-extensions PR
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Exercises the manager-level surface that consumes the new
 * ServerConfig fields:
 *   - GetTools / GetToolsSummary honoring EnabledTools/DisabledTools
 *   - ToolCount reporting only visible tools
 *   - ShouldAutoApprove walking tool→server→config
 *   - GetServerConfig snapshot for /mcp status rendering
 *
 * The fixture builds a Manager with two servers and four tools, all
 * in-memory, so the tests do not spawn processes or open sockets.
 */
package mcp

import (
	"sort"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// newMgrWithFixture returns a Manager prepopulated with:
//
//	server "fs"   exposes tools: read_file, write_file, list_files
//	server "db"   exposes tools: query, exec
//
// Per-server config knobs are set by the caller via overrides so each
// test focuses on the field it actually exercises.
func newMgrWithFixture(t *testing.T, fsCfg, dbCfg ServerConfig) *Manager {
	t.Helper()
	if fsCfg.Name == "" {
		fsCfg.Name = "fs"
	}
	if dbCfg.Name == "" {
		dbCfg.Name = "db"
	}
	m := &Manager{
		servers: map[string]*ServerConnection{
			fsCfg.Name: {Config: fsCfg, Status: ServerStatus{Name: fsCfg.Name, Connected: true}},
			dbCfg.Name: {Config: dbCfg, Status: ServerStatus{Name: dbCfg.Name, Connected: true}},
		},
		tools: map[string]*MCPTool{
			"read_file":  {Name: "read_file", Description: "read", ServerName: fsCfg.Name},
			"write_file": {Name: "write_file", Description: "write", ServerName: fsCfg.Name},
			"list_files": {Name: "list_files", Description: "ls", ServerName: fsCfg.Name},
			"query":      {Name: "query", Description: "select", ServerName: dbCfg.Name},
			"exec":       {Name: "exec", Description: "ddl/dml", ServerName: dbCfg.Name},
		},
		logger: zap.NewNop(),
	}
	return m
}

// toolNames extracts the bare tool name (mcp_ prefix stripped) from
// each ToolDefinition, sorted for stable assertions.
func toolNames(defs []toolDef) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, strings.TrimPrefix(d.name, "mcp_"))
	}
	sort.Strings(out)
	return out
}

// toolDef is a tiny shim so the test can ignore models.ToolDefinition's
// internal shape (we only care about Name).
type toolDef struct{ name string }

func defsToNames(defs []struct{ name string }) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.name)
	}
	return out
}

func TestManager_GetTools_NoFiltersExposesEverything(t *testing.T) {
	m := newMgrWithFixture(t, ServerConfig{}, ServerConfig{})
	got := make([]string, 0)
	for _, d := range m.GetTools() {
		got = append(got, strings.TrimPrefix(d.Function.Name, "mcp_"))
	}
	sort.Strings(got)
	want := []string{"exec", "list_files", "query", "read_file", "write_file"}
	if !sliceEq(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestManager_GetTools_HonorsDisabledTools(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{DisabledTools: []string{"write_file"}},
		ServerConfig{},
	)
	got := make(map[string]bool)
	for _, d := range m.GetTools() {
		got[strings.TrimPrefix(d.Function.Name, "mcp_")] = true
	}
	if got["write_file"] {
		t.Errorf("disabled tool 'write_file' should be hidden; got=%v", got)
	}
	if !got["read_file"] {
		t.Errorf("non-disabled tool 'read_file' should still be visible; got=%v", got)
	}
	if !got["query"] || !got["exec"] {
		t.Errorf("other server's tools must not be affected; got=%v", got)
	}
}

func TestManager_GetToolsSummary_HonorsEnabledToolsAllowlist(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{EnabledTools: []string{"read_file"}},
		ServerConfig{},
	)
	got := make(map[string]bool)
	for _, d := range m.GetToolsSummary() {
		got[strings.TrimPrefix(d.Function.Name, "mcp_")] = true
	}
	if !got["read_file"] || got["write_file"] || got["list_files"] {
		t.Errorf("enabledTools allowlist should hide unlisted; got=%v", got)
	}
	if !got["query"] || !got["exec"] {
		t.Errorf("untouched server must keep all tools; got=%v", got)
	}
}

func TestManager_GetTools_EnabledToolsTrumpsDisabledTools(t *testing.T) {
	// Both lists name the same tool — allowlist wins.
	m := newMgrWithFixture(t,
		ServerConfig{
			EnabledTools:  []string{"read_file"},
			DisabledTools: []string{"read_file"},
		},
		ServerConfig{},
	)
	visible := false
	for _, d := range m.GetTools() {
		if d.Function.Name == "mcp_read_file" {
			visible = true
		}
	}
	if !visible {
		t.Errorf("EnabledTools must take precedence over DisabledTools")
	}
}

func TestManager_ToolCount_MatchesVisibleToolCount(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{DisabledTools: []string{"write_file", "list_files"}},
		ServerConfig{},
	)
	if got, want := m.ToolCount(), 3; got != want {
		t.Errorf("ToolCount = %d, want %d (read_file + query + exec)", got, want)
	}
}

func TestManager_ShouldAutoApprove_TrustBypassesEverything(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{Trust: true},
		ServerConfig{},
	)
	if !m.ShouldAutoApprove("read_file") {
		t.Errorf("Trust=true should auto-approve every tool on the server")
	}
	if !m.ShouldAutoApprove("mcp_write_file") {
		t.Errorf("mcp_ prefix should be stripped for lookup; want auto-approve")
	}
	if m.ShouldAutoApprove("query") {
		t.Errorf("Trust on fs must not leak to the db server's tools")
	}
}

func TestManager_ShouldAutoApprove_WildcardAndNamedLists(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{AutoApprove: []string{"read_file", "list_files"}},
		ServerConfig{AlwaysAllow: []string{"*"}},
	)
	for _, tc := range []struct {
		tool string
		want bool
	}{
		{"read_file", true},
		{"list_files", true},
		{"write_file", false}, // not in allowlist
		{"query", true},       // db server has wildcard
		{"exec", true},
		{"mcp_read_file", true}, // prefix-stripped lookup
	} {
		if got := m.ShouldAutoApprove(tc.tool); got != tc.want {
			t.Errorf("ShouldAutoApprove(%q) = %v, want %v", tc.tool, got, tc.want)
		}
	}
}

func TestManager_ShouldAutoApprove_UnknownToolReturnsFalse(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{AutoApprove: []string{"*"}}, // even with wildcard …
		ServerConfig{},
	)
	if m.ShouldAutoApprove("ghost") {
		t.Errorf("unknown tool name must never be auto-approved (no owner to consult)")
	}
}

func TestManager_GetServerConfig_RetrievesByName(t *testing.T) {
	m := newMgrWithFixture(t,
		ServerConfig{Description: "filesystem ops", Category: "io", Tags: []string{"local"}},
		ServerConfig{},
	)
	got, ok := m.GetServerConfig("fs")
	if !ok {
		t.Fatal("GetServerConfig('fs') should return ok=true")
	}
	if got.Description != "filesystem ops" || got.Category != "io" {
		t.Errorf("metadata not echoed back: %+v", got)
	}
	if _, ok := m.GetServerConfig("does-not-exist"); ok {
		t.Errorf("unknown server should return ok=false")
	}
}

// sliceEq is a tiny equality helper kept private to this file —
// reflect.DeepEqual works too, but for sorted []string the manual
// loop is faster to read in failure messages.
func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Unused glue — kept so the toolDef shim is referenced. The active
// tests use models.ToolDefinition directly via m.GetTools(), but the
// shim documents what we're filtering on.
var _ = defsToNames
var _ = toolNames
