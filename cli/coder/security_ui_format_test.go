/*
 * ChatCLI - formatActionDetails sub-formatter tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the table-driven dispatch added by the cyclo-30 refactor:
 * each helper formatter is exercised independently, plus the parent
 * dispatcher's three branches (non-coder, MCP, @coder + fallback).
 */

package coder

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/stretchr/testify/assert"
)

func init() {
	// Pin to English so assertions on i18n-rendered labels stay stable.
	_ = i18n.Init
	i18n.Init()
}

func TestParseToolCallArgs_NestedAndFlat(t *testing.T) {
	// Nested @coder shape: {"cmd":"read","args":{...}}
	sub, args := parseToolCallArgs(`{"cmd":"read","args":{"file":"main.go"}}`)
	assert.Equal(t, "read", sub)
	assert.Equal(t, "main.go", args["file"])

	// Flat plugin shape: {"url":"..."}
	sub, args = parseToolCallArgs(`{"url":"https://example.com"}`)
	assert.Empty(t, sub, "flat shape carries no cmd")
	assert.Equal(t, "https://example.com", args["url"])

	// Garbage → both zero values, no panic.
	sub, args = parseToolCallArgs("not json")
	assert.Empty(t, sub)
	assert.Nil(t, args)
}

func TestFormatNonCoderTool_WebFetchAndSearch(t *testing.T) {
	label, details, ok := formatNonCoderTool("@webfetch",
		map[string]interface{}{"url": "https://example.com", "raw": "true"},
		`{"url":"https://example.com","raw":"true"}`)
	assert.True(t, ok)
	assert.Equal(t, "Web Fetch", label)
	assert.Contains(t, strings.Join(details, "\n"), "URL: https://example.com")
	assert.Contains(t, strings.Join(details, "\n"), "raw HTML")

	label, details, ok = formatNonCoderTool("@websearch",
		map[string]interface{}{"query": "golang lipgloss"},
		`{"query":"golang lipgloss"}`)
	assert.True(t, ok)
	assert.Equal(t, "Web Search", label)
	assert.Contains(t, details[0], "golang lipgloss")

	_, _, ok = formatNonCoderTool("@unknown", nil, "{}")
	assert.False(t, ok, "unknown tools must return ok=false so the parent dispatcher can try other paths")
}

func TestFormatMCPTool(t *testing.T) {
	label, details := formatMCPTool("mcp_filesystem_list", `{"path":"/tmp"}`)
	assert.Equal(t, "MCP: filesystem_list", label)
	assert.Equal(t, []string{`{"path":"/tmp"}`}, details)

	// Empty args → no detail row (nothing useful to show).
	label, details = formatMCPTool("mcp_noop", "")
	assert.Equal(t, "MCP: noop", label)
	assert.Empty(t, details)

	// Long args get truncated at 150 chars.
	long := strings.Repeat("x", 200)
	_, details = formatMCPTool("mcp_dump", long)
	assert.Equal(t, 150+3, len(details[0]),
		"raw args longer than 150 chars must be cut + ellipsis")
}

func TestFormatFallback_ToolNameVsSubcmd(t *testing.T) {
	// Tool name present (not @coder) → use as label.
	label, details := formatFallback("", "@something", "@something", "raw")
	assert.Equal(t, "@something", label)
	assert.NotEmpty(t, details)

	// No tool name but a subcmd → use subcmd.
	label, _ = formatFallback("delegate", "", "", "raw")
	assert.Equal(t, "delegate", label)

	// Neither → i18n unknown.
	label, _ = formatFallback("", "", "", "raw")
	assert.NotEmpty(t, label, "unknown action must still have SOME label")
}

func TestTruncate150_BoundaryBehavior(t *testing.T) {
	// Exact-150: untouched.
	in := strings.Repeat("a", 150)
	assert.Equal(t, in, truncate150(in))

	// 151: truncated + ellipsis.
	in = strings.Repeat("a", 151)
	out := truncate150(in)
	assert.Equal(t, 153, len(out), "result is exactly 150 chars + 3 of ellipsis")
	assert.True(t, strings.HasSuffix(out, "..."))

	// Empty: untouched.
	assert.Equal(t, "", truncate150(""))
}

func TestDetailsExec_CommandAndDir(t *testing.T) {
	out := detailsExec(map[string]interface{}{"cmd": "go test", "cwd": "/tmp/proj"}, "")
	assert.Contains(t, strings.Join(out, "\n"), "$ go test")
	assert.Contains(t, strings.Join(out, "\n"), "dir: /tmp/proj")

	// No cmd, no dir → empty (caller adds raw-args fallback).
	assert.Empty(t, detailsExec(nil, ""))
}

func TestDetailsFile_PicksFirstAvailableKey(t *testing.T) {
	for _, key := range []string{"file", "path", "filepath"} {
		t.Run(key, func(t *testing.T) {
			out := detailsFile(map[string]interface{}{key: "main.go"}, "")
			assert.NotEmpty(t, out)
			assert.Contains(t, out[0], "main.go")
		})
	}

	// Missing → empty result.
	assert.Empty(t, detailsFile(nil, ""))
}

func TestDetailsSearch_TermAndOptionalDir(t *testing.T) {
	out := detailsSearch(map[string]interface{}{"term": "TODO", "dir": "./src"}, "")
	joined := strings.Join(out, "\n")
	assert.Contains(t, joined, "TODO")
	assert.Contains(t, joined, "dir: ./src")

	// Search without dir: only term row.
	out = detailsSearch(map[string]interface{}{"pattern": "FIXME"}, "")
	assert.Len(t, out, 1)
}

func TestDetailsTree_DirOnly(t *testing.T) {
	out := detailsTree(map[string]interface{}{"dir": "./cmd"}, "")
	assert.Equal(t, []string{"dir: ./cmd"}, out)

	// Missing dir: nothing emitted.
	assert.Empty(t, detailsTree(nil, ""))
}

func TestFormatActionDetails_Integration(t *testing.T) {
	cases := []struct {
		name       string
		toolName   string
		rawArgs    string
		wantLabel  string
		wantDetail string
	}{
		{"webfetch", "@webfetch", `{"url":"https://x"}`, "Web Fetch", "URL: https://x"},
		{"mcp", "mcp_list", `{"a":1}`, "MCP: list", `{"a":1}`},
		{"coder exec", "@coder", `{"cmd":"exec","args":{"cmd":"ls"}}`, "", "$ ls"},
		{"coder read", "@coder", `{"cmd":"read","args":{"file":"main.go"}}`, "", "main.go"},
		{"unknown", "weirdtool", `{}`, "weirdtool", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, details := formatActionDetails("", tc.toolName, tc.rawArgs)
			assert.NotEmpty(t, label, "label must always be non-empty")
			if tc.wantLabel != "" {
				assert.Equal(t, tc.wantLabel, label)
			}
			if tc.wantDetail != "" {
				assert.Contains(t, strings.Join(details, "\n"), tc.wantDetail)
			}
		})
	}
}
