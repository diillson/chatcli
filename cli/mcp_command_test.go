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
