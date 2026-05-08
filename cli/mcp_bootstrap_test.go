package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestResolveMCPConfigPathHonorsEnvOverride pins the env override
// rule so a deployment that points at a custom location continues
// to work without code changes.
func TestResolveMCPConfigPathHonorsEnvOverride(t *testing.T) {
	t.Setenv("CHATCLI_MCP_CONFIG", "/custom/path/servers.json")
	if got := resolveMCPConfigPath(); got != "/custom/path/servers.json" {
		t.Errorf("got %q, want /custom/path/servers.json", got)
	}
}

func TestResolveMCPConfigPathFallsBackToDefault(t *testing.T) {
	t.Setenv("CHATCLI_MCP_CONFIG", "")
	got := resolveMCPConfigPath()
	if got == "" {
		t.Fatal("default path was empty")
	}
	if filepath.Base(got) != "mcp_servers.json" {
		t.Errorf("default path basename = %q, want mcp_servers.json", filepath.Base(got))
	}
}

func TestShouldAutoEnableMCPFileExists(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mcp_servers.json")
	if err := os.WriteFile(cfg, []byte(`{"mcpServers":[]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if !shouldAutoEnableMCP(cfg) {
		t.Error("expected auto-enable when file exists")
	}
}

func TestShouldAutoEnableMCPDirExistsFileMissing(t *testing.T) {
	// This is the case the hot-reload fix unlocks: directory present
	// but no file yet. Without auto-enable, the user can never get
	// hot-reload after creating mcp_servers.json mid-session.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mcp_servers.json") // file deliberately absent
	if !shouldAutoEnableMCP(cfg) {
		t.Error("expected auto-enable when only the parent dir exists")
	}
}

func TestShouldAutoEnableMCPNothingExists(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "missing-subdir", "mcp_servers.json")
	if shouldAutoEnableMCP(cfg) {
		t.Error("expected no auto-enable when neither file nor parent dir exist")
	}
}

func TestBootstrapMCPSkipsWhenDisabled(t *testing.T) {
	// CHATCLI_MCP_ENABLED unset + a fully-bogus config path means
	// bootstrap should skip the entire subsystem. Important so users
	// who don't use MCP don't pay the cost of a manager + watcher.
	t.Setenv("CHATCLI_MCP_ENABLED", "")
	t.Setenv("CHATCLI_MCP_CONFIG", filepath.Join(t.TempDir(), "missing-subdir", "mcp_servers.json"))
	cli := &ChatCLI{}
	cli.bootstrapMCP(zap.NewNop())
	if cli.mcpManager != nil {
		t.Error("manager should not be initialized when MCP is disabled and no path exists")
	}
	if cli.mcpWatcher != nil {
		t.Error("watcher should not start when MCP is disabled")
	}
}

func TestBootstrapMCPInitsManagerWhenDirExists(t *testing.T) {
	// Mirrors the chatcli first-run experience: ~/.chatcli/ exists
	// but mcp_servers.json doesn't. bootstrapMCP must still set up
	// the manager + watcher so a later file creation triggers Reload.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp_servers.json")
	t.Setenv("CHATCLI_MCP_ENABLED", "")
	t.Setenv("CHATCLI_MCP_CONFIG", cfgPath)
	cli := &ChatCLI{}
	cli.bootstrapMCP(zap.NewNop())
	defer cli.stopMCPConfigWatcher()
	if cli.mcpManager == nil {
		t.Fatal("manager should be initialized when parent dir exists")
	}
	if cli.mcpConfigPath != cfgPath {
		t.Errorf("mcpConfigPath = %q, want %q", cli.mcpConfigPath, cfgPath)
	}
	if cli.mcpWatcher == nil {
		t.Fatal("watcher should start so hot-reload works once the file appears")
	}
	// Allow the goroutine started by bootstrapMCP (StartAll on an
	// empty server set) to finish so the test doesn't race against it.
	time.Sleep(50 * time.Millisecond)
}

func TestBootstrapMCPSurvivesMalformedConfig(t *testing.T) {
	// 0-byte / malformed mcp_servers.json must not abort init.
	// Without this, a typo in the JSON file would force the user
	// to restart chatcli to recover — exactly the scenario the
	// hot-reload fix is supposed to handle.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mcp_servers.json")
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_MCP_ENABLED", "true")
	t.Setenv("CHATCLI_MCP_CONFIG", cfgPath)
	cli := &ChatCLI{}
	cli.bootstrapMCP(zap.NewNop())
	defer cli.stopMCPConfigWatcher()
	if cli.mcpManager == nil {
		t.Error("manager must still be set up despite LoadConfig failure")
	}
	if cli.mcpWatcher == nil {
		t.Error("watcher must still start so the user can save the fix")
	}
	time.Sleep(50 * time.Millisecond)
}
