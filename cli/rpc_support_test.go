package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

func TestCaptureRPCStdout(t *testing.T) {
	out, err := captureRPCStdout(func() error {
		fmt.Print("hello\x1b[31m world\x1b[0m") // includes ANSI codes
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Errorf("expected ANSI-stripped capture, got %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Error("ANSI escapes should be stripped")
	}
}

func TestRunBuiltinTool_Guards(t *testing.T) {
	c := &ChatCLI{} // no plugin manager
	if _, err := c.RunBuiltinTool(context.Background(), "read", "x"); err == nil {
		t.Error("expected error when plugins unavailable")
	}
	if tools := c.ListBuiltinTools(); tools != nil {
		t.Errorf("expected nil tools without a plugin manager, got %v", tools)
	}
}

func TestBuiltinTools_WithManager(t *testing.T) {
	mgr, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinReadPlugin())
	c := &ChatCLI{pluginManager: mgr}

	found := false
	for _, tl := range c.ListBuiltinTools() {
		if tl.Name == "read" {
			found = true
		}
	}
	if !found {
		t.Error("expected the read tool to be listed")
	}

	// Not in the exposed allowlist.
	if _, err := c.RunBuiltinTool(context.Background(), "coder", "x"); err == nil {
		t.Error("coder must not be exposed over MCP")
	}
	// Allowed but not registered in this manager.
	if _, err := c.RunBuiltinTool(context.Background(), "search", "x"); err == nil {
		t.Error("search is not registered -> should error")
	}
	// Allowed + registered: executes (bad path errors, but the code path runs).
	_, _ = c.RunBuiltinTool(context.Background(), "read", `{"path":"/nonexistent-xyz"}`)
}
