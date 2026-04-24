package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/coder"
	"go.uber.org/zap"
)

// withCoderHomeDir redirects HOME so PolicyManager.configPath ends
// up inside a tmp dir. Returns the configPath for assertions. Uses
// HOME because coder.NewPolicyManager calls utils.GetHomeDir().
func withCoderHomeDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp) // windows
	return filepath.Join(tmp, ".chatcli", "coder_policy.json")
}

// captureStdout and minimalCLI are reused from config_sections_test.go
// (same package). Keeping the helpers in one place avoids drift.

func TestConfigSecurity_AllowDenyForget_RoundTrip(t *testing.T) {
	configPath := withCoderHomeDir(t)
	cli := minimalCLI(t)

	// allow (not broad → no confirmation prompt, no --yes needed)
	out := captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"allow", "@coder exec my-custom-tool"})
	})
	if !strings.Contains(out, "ALLOW") {
		t.Fatalf("expected ALLOW success, got %q", out)
	}

	// File exists + contains the rule.
	pm, err := coder.NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("reload policy: %v", err)
	}
	if !ruleExists(pm, "@coder exec my-custom-tool", coder.ActionAllow) {
		t.Errorf("allow rule did not persist to %s", configPath)
	}

	// deny requires confirmation → use --yes to skip.
	out = captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"deny", "@coder exec rm -rf /", "--yes"})
	})
	if !strings.Contains(out, "DENY") {
		t.Fatalf("expected DENY success, got %q", out)
	}

	// forget requires confirmation → --yes
	out = captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"forget", "@coder exec my-custom-tool", "--yes"})
	})
	if !strings.Contains(out, "my-custom-tool") {
		t.Fatalf("expected forget confirmation mentioning the pattern, got %q", out)
	}

	// forget of missing pattern reports nomatch.
	out = captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"forget", "@coder exec does-not-exist", "--yes"})
	})
	if !strings.Contains(out, "nenhuma rule") && !strings.Contains(out, "no rule") {
		t.Errorf("expected nomatch message, got %q", out)
	}
}

func TestConfigSecurity_BroadAllowNeedsYes(t *testing.T) {
	withCoderHomeDir(t)
	cli := minimalCLI(t)

	// "@coder exec" is broad — isBroadPattern returns true.
	// Without --yes, the handler would prompt. In a captured-stdout
	// test with no stdin input, readYesNo returns false, so the rule
	// must NOT be added.
	out := captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"allow", "@coder exec"})
	})
	if strings.Contains(out, "ALLOW rule added") || strings.Contains(out, "rule ALLOW adicionada") {
		t.Fatalf("broad allow without --yes should be cancelled, got %q", out)
	}
	// Re-read — rule must not be persisted.
	pm, err := coder.NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("reload policy: %v", err)
	}
	if ruleExists(pm, "@coder exec", coder.ActionAllow) {
		t.Error("broad allow persisted without confirmation")
	}
}

func TestConfigSecurity_PatternRequired(t *testing.T) {
	withCoderHomeDir(t)
	cli := minimalCLI(t)
	out := captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"allow"})
	})
	if !strings.Contains(out, "required") && !strings.Contains(out, "obrigatório") {
		t.Errorf("expected 'pattern required' error, got %q", out)
	}
}

func TestConfigSecurity_UnknownSub(t *testing.T) {
	withCoderHomeDir(t)
	cli := minimalCLI(t)
	out := captureStdout(t, func() {
		cli.routeConfigSecurity([]string{"explode"})
	})
	if !strings.Contains(out, "explode") {
		t.Errorf("expected unknown-sub error mentioning the sub, got %q", out)
	}
}

// ruleExists returns true iff pm has an exact pattern match with the
// given action.
func ruleExists(pm *coder.PolicyManager, pattern string, action coder.Action) bool {
	for _, r := range pm.RulesSnapshot() {
		if r.Pattern == pattern && r.Action == action {
			return true
		}
	}
	return false
}
