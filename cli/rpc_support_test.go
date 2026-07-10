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

func TestCaptureStreaming_ForwardsLines(t *testing.T) {
	var got []string
	out, err := captureStreaming(func(s string) { got = append(got, s) }, func() error {
		fmt.Println("first line")
		fmt.Print("\x1b[32msecond\x1b[0m line\n") // ANSI must be stripped
		fmt.Println("   ")                        // whitespace-only: emitted? no
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "first line" || got[1] != "second line" {
		t.Fatalf("expected two ANSI-stripped lines, got %#v", got)
	}
	if !strings.Contains(out, "first line") || !strings.Contains(out, "second line") {
		t.Errorf("transcript missing lines: %q", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Error("transcript should be ANSI-stripped")
	}
}

func TestRunAnyRPCTool_Guards(t *testing.T) {
	c := &ChatCLI{} // no plugin manager
	if _, err := c.RunAnyRPCTool(context.Background(), "read", "x"); err == nil {
		t.Error("expected error when plugins unavailable")
	}
	if tools := c.ListAllRPCTools(); tools != nil {
		t.Errorf("expected nil tools without a plugin manager, got %v", tools)
	}
}

func TestAllRPCTools_PolicyModes(t *testing.T) {
	mgr, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinReadPlugin())
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinCoderPlugin())
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinAskPlugin())
	c := &ChatCLI{pluginManager: mgr}

	names := func() map[string]bool {
		out := map[string]bool{}
		for _, tl := range c.ListAllRPCTools() {
			out[tl.Name] = true
		}
		return out
	}

	// Default policy: everything except interactive tools.
	t.Setenv("CHATCLI_MCP_TOOLS", "")
	got := names()
	if !got["read"] || !got["coder"] {
		t.Errorf("default policy must expose read and coder, got %v", got)
	}
	if got["ask"] {
		t.Error("interactive tools (ask) must never be exposed over stdio RPC")
	}

	// Safe policy: only read-only tools.
	t.Setenv("CHATCLI_MCP_TOOLS", "safe")
	got = names()
	if !got["read"] {
		t.Error("safe policy must keep read-only tools")
	}
	if got["coder"] {
		t.Error("safe policy must drop write-capable tools like coder")
	}
	if _, err := c.RunAnyRPCTool(context.Background(), "coder", "x"); err == nil {
		t.Error("safe policy must refuse invoking coder")
	}

	// Allowlist policy.
	t.Setenv("CHATCLI_MCP_TOOLS", "read")
	got = names()
	if !got["read"] || got["coder"] {
		t.Errorf("allowlist must expose exactly its entries, got %v", got)
	}

	// Default policy again: invoking a registered tool runs it end to end
	// (bad path errors, but the code path executes).
	t.Setenv("CHATCLI_MCP_TOOLS", "all")
	_, _ = c.RunAnyRPCTool(context.Background(), "read", `{"path":"/nonexistent-xyz"}`)
	// Unknown tool errors.
	if _, err := c.RunAnyRPCTool(context.Background(), "nope", "x"); err == nil {
		t.Error("unknown tool must error")
	}
}

// TestRunAnyRPCTool_JSONEnvelopeArgv pins the argv contract with
// subcommand-style tools: a JSON envelope must be parsed exactly like the
// agent loop parses it, not wrapped as one opaque argv element — wrapping
// broke every {"cmd":...} tool (coder, memory, session) over MCP.
func TestRunAnyRPCTool_JSONEnvelopeArgv(t *testing.T) {
	mgr, err := plugins.NewManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mgr.RegisterBuiltinPlugin(plugins.NewBuiltinCoderPlugin())
	c := &ChatCLI{pluginManager: mgr}
	t.Setenv("CHATCLI_MCP_TOOLS", "all")

	// The coder engine enforces a workspace boundary at the process cwd,
	// so read a file that genuinely lives inside it (this package's own
	// source) instead of a temp dir outside the boundary.
	out, err := c.RunAnyRPCTool(context.Background(),
		"coder", `{"cmd":"read","args":{"file":"rpc_support_full.go"}}`)
	if err != nil {
		t.Fatalf("coder read via MCP envelope: %v", err)
	}
	if !strings.Contains(out, "rpcToolPolicy") {
		t.Fatalf("coder must read through the JSON envelope, got: %.120q", out)
	}
}
