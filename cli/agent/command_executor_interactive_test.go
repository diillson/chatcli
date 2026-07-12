package agent

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestExecuteInteractiveRendersTitledRules runs a trivially-exiting command
// through the interactive path and asserts the entry/exit frames render as
// responsive titled rules instead of the legacy embedded dash strings.
func TestExecuteInteractiveRendersTitledRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("interactive path shells through /bin/sh")
	}
	e := NewCommandExecutor(zap.NewNop())

	out := captureStdout(t, func() {
		res, err := e.executeInteractive(context.Background(), "/bin/sh", "-c", "true")
		if err != nil || res == nil {
			t.Errorf("executeInteractive: res=%v err=%v", res, err)
		}
	})

	plain := stripANSI(out)
	if !strings.Contains(plain, "──") {
		t.Errorf("interactive frames must draw rules, got:\n%s", plain)
	}
	if strings.Contains(plain, "----") {
		t.Errorf("legacy raw dash rule survived:\n%s", plain)
	}
}
