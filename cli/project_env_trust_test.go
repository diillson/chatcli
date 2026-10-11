/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/diillson/chatcli/cli/coder"
	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/pkg/workspacetrust"
	"go.uber.org/zap"
)

// The project a client announces over ACP is attacker-controlled: its .env
// is not applied until the folder is trusted, whatever CHATCLI_PROJECT_ENV.
func TestApplyProjectEnvSkipsUntrustedProject(t *testing.T) {
	withCoderHomeDir(t)
	t.Setenv(config.ProjectDotenvEnv, "all")
	reset := func() {
		workspacetrust.ResetSessionForTest()
		config.ResetProjectDotenvForTest()
		ResetProjectEnvOnceForTest()
	}
	reset()
	t.Cleanup(reset)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("CHATCLI_MCP_CONFIG=/tmp/x.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_MCP_CONFIG", "")
	_ = os.Unsetenv("CHATCLI_MCP_CONFIG")

	c := &ChatCLI{logger: zap.NewNop()}
	mgr, rep := c.ApplyProjectEnv(project)
	if mgr != nil || !rep.Untrusted || len(rep.Applied) != 0 || os.Getenv("CHATCLI_MCP_CONFIG") != "" {
		t.Fatalf("an untrusted project .env must not apply: %+v", rep)
	}
}

// /policy status says why a workspace coder_policy.json is not in effect.
func TestLocalPolicyNotes(t *testing.T) {
	withCoderHomeDir(t)
	t.Setenv(coder.PolicyOverlayEnv, "")
	workspacetrust.ResetSessionForTest()
	t.Cleanup(workspacetrust.ResetSessionForTest)
	project := t.TempDir()
	t.Chdir(project)
	if err := os.WriteFile(filepath.Join(project, "coder_policy.json"), []byte(`{"rules":[{"pattern":"@coder","action":"allow"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pm, err := coder.NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if notes := localPolicyNotes(pm); len(notes) != 1 {
		t.Fatalf("untrusted: %v", notes)
	}
	ins, _ := workspacetrust.Inspect(project)
	if err := workspacetrust.Trust(ins); err != nil {
		t.Fatal(err)
	}
	pm, _ = coder.NewPolicyManager(zap.NewNop())
	if notes := localPolicyNotes(pm); len(notes) != 1 || pm.LocalIgnoredAllows() != 1 {
		t.Fatalf("trusted: %v", notes)
	}
}
