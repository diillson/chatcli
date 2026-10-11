/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package coder

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/evals"
	"github.com/diillson/chatcli/pkg/workspacetrust"
	"go.uber.org/zap"
)

// policyWorkspace isolates the global policy (HOME) and the trust store,
// and enters a project directory holding the given local policy.
func policyWorkspace(t *testing.T, local string) (home, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv(PolicyOverlayEnv, "")
	workspacetrust.ResetSessionForTest()
	t.Cleanup(workspacetrust.ResetSessionForTest)
	project = t.TempDir()
	t.Chdir(project)
	if local != "" {
		if err := os.WriteFile(filepath.Join(project, "coder_policy.json"), []byte(local), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, project
}

const hostileLocalPolicy = `{"merge":false,"rules":[
	{"pattern":"@coder","action":"allow"},
	{"pattern":"@coder read","action":"deny"},
	{"pattern":"@coder git-status","action":"ask"}]}`

// An untrusted workspace policy is not loaded at all: the global defaults
// stay in force.
func TestLocalPolicyIgnoredWhenUntrusted(t *testing.T) {
	_, project := policyWorkspace(t, hostileLocalPolicy)
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got := pm.Check("@coder", `{"cmd":"write","args":{"file":"x"}}`); got != ActionAsk {
		t.Fatalf("an untrusted local allow must not apply: %s", got)
	}
	if got := pm.Check("@coder", `{"cmd":"read","args":{"file":"x"}}`); got != ActionAllow {
		t.Fatalf("the global defaults must stay in force: %s", got)
	}
	if filepath.Base(pm.UntrustedLocalPolicyPath()) != "coder_policy.json" || !strings.Contains(pm.UntrustedLocalPolicyPath(), filepath.Base(project)) {
		t.Fatalf("the ignored file must be reported: %q", pm.UntrustedLocalPolicyPath())
	}
	if pm.LocalPolicyPath() != "" {
		t.Fatal("no local policy was loaded")
	}
}

// Even trusted, a workspace policy can only tighten: allow is dropped, deny
// and ask apply, and the global rules are kept rather than replaced.
func TestTrustedLocalPolicyOnlyTightens(t *testing.T) {
	home, project := policyWorkspace(t, hostileLocalPolicy)
	if ins, err := workspacetrust.Inspect(project); err != nil || workspacetrust.Trust(ins) != nil {
		t.Fatal("trust the workspace")
	}
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if got := pm.Check("@coder", `{"cmd":"write","args":{"file":"x"}}`); got != ActionAsk {
		t.Fatalf("a local allow must be ignored: %s", got)
	}
	if got := pm.Check("@coder", `{"cmd":"read","args":{"file":"x"}}`); got != ActionDeny {
		t.Fatalf("a local deny must be honored: %s", got)
	}
	if got := pm.Check("@coder", `{"cmd":"git-status"}`); got != ActionAsk {
		t.Fatalf("a local ask must tighten a global allow of the same pattern: %s", got)
	}
	if got := pm.Check("@coder", `{"cmd":"tree"}`); got != ActionAllow {
		t.Fatalf("global rules must be kept (merge, never replace): %s", got)
	}
	if pm.LocalIgnoredAllows() != 1 || pm.LocalPolicyPath() == "" {
		t.Fatalf("ignored allows = %d, local = %q", pm.LocalIgnoredAllows(), pm.LocalPolicyPath())
	}

	// "Allow always" persists to the global policy, never the repository.
	if err := pm.AddRule("@coder write", ActionAllow); err != nil {
		t.Fatal(err)
	}
	if pm.ActivePolicyPath() != filepath.Join(home, ".chatcli", "coder_policy.json") {
		t.Fatalf("decisions must persist globally: %s", pm.ActivePolicyPath())
	}
	local, _ := os.ReadFile(filepath.Join(project, "coder_policy.json"))
	if strings.Contains(string(local), "@coder write") {
		t.Fatal("a decision was written into the workspace policy")
	}
	global, _ := os.ReadFile(pm.ActivePolicyPath())
	if !strings.Contains(string(global), "@coder write") || strings.Contains(string(global), "deny") {
		t.Fatal("the decision must be in the global policy, and workspace rules never")
	}
	if removed, err := pm.DeleteRule("@coder write"); err != nil || !removed {
		t.Fatalf("delete: %v %v", removed, err)
	}
}

// The overlay is the channel for automation that owns the run (the eval
// harness): it merges with full effect, allow included.
func TestPolicyOverlayEnv(t *testing.T) {
	if PolicyOverlayEnv != evals.PolicyEnv {
		t.Fatalf("the eval harness and the policy manager disagree: %s vs %s", evals.PolicyEnv, PolicyOverlayEnv)
	}
	_, _ = policyWorkspace(t, "")
	overlay := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(overlay, []byte(`{"rules":[{"pattern":"@coder write","action":"allow"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PolicyOverlayEnv, overlay)
	pm, _ := NewPolicyManager(zap.NewNop())
	if got := pm.Check("@coder", `{"cmd":"write","args":{"file":"x"}}`); got != ActionAllow {
		t.Fatalf("overlay allow must apply: %s", got)
	}
	_ = pm.AddRule("@coder patch", ActionAllow)
	global, _ := os.ReadFile(pm.ActivePolicyPath())
	if strings.Contains(string(global), "@coder write") {
		t.Fatal("overlay rules must not be persisted")
	}

}
