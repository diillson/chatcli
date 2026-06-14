package engine

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestHandleExec_Success(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX echo")
	}
	e, out, _ := newWsEngine(t)
	if err := e.Execute(context.Background(), "exec", []string{"--cmd", "echo hello-exec"}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "hello-exec") {
		t.Errorf("missing command output: %q", s)
	}
	if !strings.Contains(s, "Sucesso") {
		t.Errorf("missing success marker: %q", s)
	}
}

func TestHandleExec_Failure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX false")
	}
	e, _, _ := newWsEngine(t)
	err := e.Execute(context.Background(), "exec", []string{"--cmd", "exit 3"})
	if err == nil || !strings.Contains(err.Error(), "command failed") {
		t.Fatalf("expected command failed, got %v", err)
	}
}

func TestHandleExec_Blocked(t *testing.T) {
	e, _, _ := newWsEngine(t)
	// "rm -rf /" should be classified unsafe and rejected.
	err := e.Execute(context.Background(), "exec", []string{"--cmd", "rm -rf /"})
	if err == nil || !strings.Contains(err.Error(), "bloqueado") {
		t.Fatalf("expected blocked error, got %v", err)
	}
}

func TestHandleExec_RequiresCmd(t *testing.T) {
	e, _, _ := newWsEngine(t)
	if err := e.Execute(context.Background(), "exec", []string{}); err == nil {
		t.Error("expected error for missing --cmd")
	}
}
