package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/mcp"
	"go.uber.org/zap"
)

// withConfiguredServers returns a ChatCLI carrying an MCP manager
// preloaded with the given server names via a temp config file.
// Using LoadConfig instead of poking unexported fields keeps the
// test against the public contract — if the loader changes shape,
// these tests catch it.
func withConfiguredServers(t *testing.T, names ...string) *ChatCLI {
	t.Helper()
	type cfgEntry struct {
		Name      string `json:"name"`
		Command   string `json:"command"`
		Transport string `json:"transport"`
		Enabled   bool   `json:"enabled"`
	}
	entries := make([]cfgEntry, 0, len(names))
	for _, n := range names {
		entries = append(entries, cfgEntry{
			Name:      n,
			Command:   "true", // never actually executed in these tests
			Transport: "stdio",
			Enabled:   true,
		})
	}
	body, err := json.Marshal(struct {
		Servers []cfgEntry `json:"mcpServers"`
	}{entries})
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(cfgPath, body, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	m := mcp.NewManager(zap.NewNop())
	if err := m.LoadConfig(cfgPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return &ChatCLI{mcpManager: m}
}

func TestMCPCompleterSubcommandSuggestionsAtRoot(t *testing.T) {
	cli := withConfiguredServers(t)
	got := suggestTexts(cli.getMCPSuggestions(docFor("/mcp ")))
	want := []string{"status", "tools", "restart", "start", "stop", "reload", "logs"}
	if len(got) != len(want) {
		t.Fatalf("got %d suggestions, want %d (%v)", len(got), len(want), got)
	}
	for _, sub := range want {
		if !contains(got, sub) {
			t.Errorf("subcommand %q missing from /mcp completer; got %v", sub, got)
		}
	}
}

func TestMCPCompleterFiltersByPrefix(t *testing.T) {
	cli := withConfiguredServers(t)
	got := suggestTexts(cli.getMCPSuggestions(docFor("/mcp re")))
	if !contains(got, "restart") || !contains(got, "reload") {
		t.Errorf("expected restart+reload, got %v", got)
	}
	for _, s := range got {
		if !strings.HasPrefix(s, "re") {
			t.Errorf("suggestion %q does not match prefix 're'", s)
		}
	}
}

func TestMCPCompleterServerNamesAfterStart(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem", "github")
	got := suggestTexts(cli.getMCPSuggestions(docFor("/mcp start ")))
	if len(got) != 2 {
		t.Fatalf("got %d server suggestions, want 2 (%v)", len(got), got)
	}
	if !contains(got, "filesystem") || !contains(got, "github") {
		t.Errorf("expected filesystem+github, got %v", got)
	}
}

func TestMCPCompleterFiltersServerNamesByPrefix(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem", "github", "fetch")
	got := suggestTexts(cli.getMCPSuggestions(docFor("/mcp logs f")))
	if contains(got, "github") {
		t.Errorf("github should be filtered out, got %v", got)
	}
	if !contains(got, "filesystem") || !contains(got, "fetch") {
		t.Errorf("expected filesystem+fetch, got %v", got)
	}
}

func TestMCPCompleterReloadHasNoServerArg(t *testing.T) {
	// reload doesn't take a server name — second-position completion
	// must return nil so the user isn't lured into typing one.
	cli := withConfiguredServers(t, "filesystem")
	got := suggestTexts(cli.getMCPSuggestions(docFor("/mcp reload ")))
	if len(got) != 0 {
		t.Errorf("reload should not suggest a server arg, got %v", got)
	}
}

// captureStdout is shared with config_sections_test.go (same package).

// All assertions below match against the English locale, since
// TestMain in config_sections_test.go forces CHATCLI_LANG=en before
// running. Tests therefore check against the user-visible strings
// (the contract that matters) rather than against raw i18n keys.

func TestHandleMCPCommandWithoutManagerShowsHint(t *testing.T) {
	cli := &ChatCLI{} // no manager
	out := captureStdout(t, func() { cli.handleMCPCommand("/mcp") })
	if !strings.Contains(out, "MCP is not enabled") {
		t.Errorf("expected not_enabled hint, got:\n%s", out)
	}
	if !strings.Contains(out, "mcpServers") {
		t.Errorf("expected JSON template hint, got:\n%s", out)
	}
}

func TestMcpStartUsageWhenNoName(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpStart("") })
	if !strings.Contains(out, "Usage: /mcp start") {
		t.Errorf("expected usage hint, got:\n%s", out)
	}
}

func TestMcpStartUnknownServerTranslatesToI18nKey(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpStart("ghost") })
	if !strings.Contains(out, `"ghost" is not configured`) {
		t.Errorf("expected translated unknown_server, got:\n%s", out)
	}
	// The library error wraps the sentinel as `<sentinel>: "ghost"`.
	// translateMCPError must strip that and pass only the name back
	// to i18n — so the full library message must NOT appear.
	if strings.Contains(out, "MCP server is not configured: ") {
		t.Errorf("raw sentinel error leaked through translation:\n%s", out)
	}
}

func TestMcpStopUsageWhenNoName(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpStop("") })
	if !strings.Contains(out, "Usage: /mcp stop") {
		t.Errorf("expected usage hint, got:\n%s", out)
	}
}

func TestMcpStopUnknownServerTranslated(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpStop("ghost") })
	if !strings.Contains(out, `"ghost" is not configured`) {
		t.Errorf("expected unknown_server translation, got:\n%s", out)
	}
}

func TestMcpRestartOneUnknownServerTranslated(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpRestart("ghost") })
	if !strings.Contains(out, `"ghost" is not configured`) {
		t.Errorf("expected unknown_server translation, got:\n%s", out)
	}
}

func TestMcpReloadWithoutPathHintsAtMissingConfig(t *testing.T) {
	cli := withConfiguredServers(t)
	// mcpConfigPath is intentionally empty here — reload should
	// short-circuit with a friendly hint instead of attempting an
	// empty-path Read.
	out := captureStdout(t, func() { cli.mcpReload() })
	if !strings.Contains(out, "config path is not set") {
		t.Errorf("expected reload_no_path hint, got:\n%s", out)
	}
}

func TestMcpLogsUsageWhenNoName(t *testing.T) {
	cli := withConfiguredServers(t)
	out := captureStdout(t, func() { cli.mcpLogs("") })
	if !strings.Contains(out, "Usage: /mcp logs") {
		t.Errorf("expected usage hint, got:\n%s", out)
	}
}

func TestMcpLogsUnknownServer(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpLogs("ghost") })
	if !strings.Contains(out, `"ghost" is not configured`) {
		t.Errorf("expected unknown_server translation, got:\n%s", out)
	}
}

func TestMcpLogsEmptyBufferShowsHint(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpLogs("filesystem") })
	if !strings.Contains(out, "No log lines captured yet") {
		t.Errorf("expected no_logs hint, got:\n%s", out)
	}
}

func TestTranslateMCPErrorPassesThroughForUnknownErr(t *testing.T) {
	// Plain error must come back as its own message — we don't want
	// to silently swallow unexpected failures behind a generic key.
	got := translateMCPError(errSentinel("unexpected boom"))
	if !strings.Contains(got, "unexpected boom") {
		t.Errorf("plain error was masked: %q", got)
	}
}

// errSentinel is a tiny inline error type for the pass-through test
// — using errors.New here would also work but stays self-documenting.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }

func TestMcpShowStatusUnknownFilter(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpShowStatus("ghost") })
	if !strings.Contains(out, `"ghost" is not configured`) {
		t.Errorf("expected unknown_server when filter doesn't match, got:\n%s", out)
	}
}

func TestMcpShowToolsUnknownFilter(t *testing.T) {
	cli := withConfiguredServers(t, "filesystem")
	out := captureStdout(t, func() { cli.mcpShowTools("ghost") })
	if !strings.Contains(out, `"ghost" is not configured`) {
		t.Errorf("expected unknown_server when filter doesn't match, got:\n%s", out)
	}
}

func TestHandleMCPCommandDispatchesToSubcommands(t *testing.T) {
	// Drive the dispatcher across every branch so the switch shows
	// up as covered. We don't assert exact wording — the per-handler
	// tests above already pin that — only that the right handler was
	// selected (a distinctive fragment of its English output).
	cli := withConfiguredServers(t, "filesystem")
	cases := []struct {
		input    string
		expectIn string
	}{
		{"/mcp status", "MCP SERVERS"},
		{"/mcp tools", "MCP TOOLS"},
		{"/mcp start", "Usage: /mcp start"},
		{"/mcp stop", "Usage: /mcp stop"},
		{"/mcp logs", "Usage: /mcp logs"},
		{"/mcp reload", "config path is not set"},
		{"/mcp totally-unknown", "Usage: /mcp [status"},
	}
	for _, c := range cases {
		out := captureStdout(t, func() { cli.handleMCPCommand(c.input) })
		if !strings.Contains(out, c.expectIn) {
			t.Errorf("input %q: expected %q in output:\n%s", c.input, c.expectIn, out)
		}
	}
}

// TestMCPI18nKeysPresentInBundle pins the new /mcp subcommand keys
// to all three locale bundles so a forgotten translation surfaces in
// CI rather than at runtime as a raw key in the user's terminal.
func TestMCPI18nKeysPresentInBundle(t *testing.T) {
	keys := []string{
		"mcp.cmd.sug_start",
		"mcp.cmd.sug_stop",
		"mcp.cmd.sug_reload",
		"mcp.cmd.sug_logs",
		"mcp.cmd.unknown_server",
		"mcp.cmd.already_running",
		"mcp.cmd.no_tools_for_server",
		"mcp.cmd.restarting_one",
		"mcp.cmd.restart_one_success",
		"mcp.cmd.usage_start",
		"mcp.cmd.start_error",
		"mcp.cmd.start_success",
		"mcp.cmd.usage_stop",
		"mcp.cmd.stop_error",
		"mcp.cmd.stop_success",
		"mcp.cmd.reload_no_path",
		"mcp.cmd.reload_error",
		"mcp.cmd.reload_no_changes",
		"mcp.cmd.reload_summary",
		"mcp.cmd.usage_logs",
		"mcp.cmd.box_logs_title",
		"mcp.cmd.no_logs",
	}
	for _, locale := range []string{"pt-BR", "en-US", "en"} {
		path := "../i18n/locales/" + locale + ".json"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var bundle map[string]string
		if err := json.Unmarshal(data, &bundle); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, k := range keys {
			v, ok := bundle[k]
			if !ok {
				t.Errorf("locale %q missing key %q", locale, k)
				continue
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("locale %q has empty value for key %q", locale, k)
			}
		}
	}
}
