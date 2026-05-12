/*
 * ChatCLI - Tests for /mcp status metadata renderers
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The full renderMCPServerMetadata writes to stdout. The two pure
 * helpers it delegates to — mcpCategoryTagsLine and
 * mcpHiddenToolCount — are unit-tested here so the row-by-row
 * conditional logic is exercised without capturing TTY output.
 */
package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
)

// ansiEscape matches CSI sequences ("\x1b[...m"). Used to strip
// color codes before substring assertions so an ANSI bracket does
// not get confused with a visible "[category]" badge.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripAnsi(s string) string {
	return ansiEscape.ReplaceAllString(s, "")
}

func TestMcpCategoryTagsLine_BothFieldsPresent(t *testing.T) {
	got := stripAnsi(mcpCategoryTagsLine(mcp.ServerConfig{
		Category: "aws",
		Tags:     []string{"prod", "cluster"},
	}))
	if !strings.Contains(got, "[aws]") {
		t.Errorf("missing category badge: %q", got)
	}
	if !strings.Contains(got, "#prod") || !strings.Contains(got, "#cluster") {
		t.Errorf("missing tag prefixes: %q", got)
	}
}

func TestMcpCategoryTagsLine_OnlyCategory(t *testing.T) {
	got := stripAnsi(mcpCategoryTagsLine(mcp.ServerConfig{Category: "io"}))
	if !strings.Contains(got, "[io]") {
		t.Errorf("missing category badge: %q", got)
	}
	if strings.Contains(got, "#") {
		t.Errorf("tag prefix leaked when no tags configured: %q", got)
	}
}

func TestMcpCategoryTagsLine_OnlyTags(t *testing.T) {
	got := stripAnsi(mcpCategoryTagsLine(mcp.ServerConfig{Tags: []string{"experimental"}}))
	if strings.Contains(got, "[") {
		t.Errorf("category badge leaked when no category configured: %q", got)
	}
	if !strings.Contains(got, "#experimental") {
		t.Errorf("missing tag: %q", got)
	}
}

func TestMcpCategoryTagsLine_EmptyCfg(t *testing.T) {
	if got := mcpCategoryTagsLine(mcp.ServerConfig{}); got != "" {
		t.Errorf("empty config should produce empty line; got %q", got)
	}
}

func TestMcpCategoryTagsLine_BlankTagsSkipped(t *testing.T) {
	// Whitespace-only entries must not become "#" or "# " in output.
	got := stripAnsi(mcpCategoryTagsLine(mcp.ServerConfig{Tags: []string{" ", "", "good"}}))
	if !strings.Contains(got, "#good") {
		t.Errorf("real tag missing: %q", got)
	}
	if strings.Contains(got, "# ") || strings.Contains(got, "##") {
		t.Errorf("blank tag leaked: %q", got)
	}
}

func TestMcpHiddenToolCount_DisabledBlocklist(t *testing.T) {
	got := mcpHiddenToolCount(mcp.ServerConfig{
		DisabledTools: []string{"a", "b", " ", ""},
	})
	if got != 2 {
		t.Errorf("got %d, want 2 (blank entries skipped)", got)
	}
}

func TestMcpHiddenToolCount_EnabledAllowlistAlwaysZero(t *testing.T) {
	// When EnabledTools is set we don't know the total tool count
	// from here, so the renderer treats hidden = 0 and lets the
	// "auto-approve: N" / regular tool count carry the meaning.
	got := mcpHiddenToolCount(mcp.ServerConfig{
		EnabledTools:  []string{"read_file"},
		DisabledTools: []string{"write_file"}, // ignored when allowlist active
	})
	if got != 0 {
		t.Errorf("EnabledTools active → hidden count is 0; got %d", got)
	}
}

func TestMcpHiddenToolCount_EmptyConfig(t *testing.T) {
	if got := mcpHiddenToolCount(mcp.ServerConfig{}); got != 0 {
		t.Errorf("empty config → 0 hidden; got %d", got)
	}
}
