package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
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
