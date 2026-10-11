/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/coder"
	"go.uber.org/zap"
)

const unattendedExecTouch = `{"cmd":"exec","args":{"cmd":"touch built.txt"}}`

// TestUnattendedToolPolicy_AskDeniedByDefault walks the orchestrator's
// policy path for an unattended run with no permission dialog: the verdict
// stays "ask" and the ask is refused.
func TestUnattendedToolPolicy_AskDeniedByDefault(t *testing.T) {
	t.Setenv(coder.PolicyAutoModeEnv, "")
	pm := newSandboxedPolicyManager(t)
	a, c := newUnattendedAgent(&sinkRecorder{})
	c.seedPolicyAutoModeFromEnv()

	if got := a.toolPolicyVerdict(pm, "@coder", unattendedExecTouch); got != coder.ActionAsk {
		t.Fatalf("verdict = %q, want ask", got)
	}
	if !a.unattendedAskBlocked("@coder", unattendedExecTouch, pm, func(string) {}) {
		t.Fatal("an unattended ask without the opt-in must be refused")
	}
}

// TestUnattendedToolPolicy_AllowedWithOptIn: CHATCLI_POLICY_AUTOMODE=true
// seeds session automode, which approves the ask before the unattended
// refusal is reached.
func TestUnattendedToolPolicy_AllowedWithOptIn(t *testing.T) {
	t.Setenv(coder.PolicyAutoModeEnv, "true")
	pm := newSandboxedPolicyManager(t)
	a, c := newUnattendedAgent(&sinkRecorder{})
	c.seedPolicyAutoModeFromEnv()

	if !c.PolicyAutoMode() {
		t.Fatal("the env opt-in must seed session automode")
	}
	if got := a.toolPolicyVerdict(pm, "@coder", unattendedExecTouch); got != coder.ActionAllow {
		t.Fatalf("verdict = %q, want allow under the opt-in", got)
	}
}

// TestUnattendedToolPolicy_DenyRuleBlocksWithOptIn: the opt-in widens "ask"
// only; an explicit deny rule keeps blocking.
func TestUnattendedToolPolicy_DenyRuleBlocksWithOptIn(t *testing.T) {
	t.Setenv(coder.PolicyAutoModeEnv, "true")
	pm := newSandboxedPolicyManager(t)
	if err := pm.AddRule("@coder exec --cmd touch", coder.ActionDeny); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	a, c := newUnattendedAgent(&sinkRecorder{})
	c.seedPolicyAutoModeFromEnv()

	if got := a.toolPolicyVerdict(pm, "@coder", unattendedExecTouch); got != coder.ActionDeny {
		t.Fatalf("verdict = %q, want deny even with the opt-in", got)
	}
}

// TestUnattendedToolPolicy_ImmuneRefusedWithOptIn: safety-immune operations
// never ride the opt-in; unattended, they are refused.
func TestUnattendedToolPolicy_ImmuneRefusedWithOptIn(t *testing.T) {
	t.Setenv(coder.PolicyAutoModeEnv, "true")
	pm := newSandboxedPolicyManager(t)
	a, c := newUnattendedAgent(&sinkRecorder{})
	c.seedPolicyAutoModeFromEnv()

	immune := `{"cmd":"exec","args":{"cmd":"git push --force origin main"}}`
	if got := a.toolPolicyVerdict(pm, "@coder", immune); got != coder.ActionAsk {
		t.Fatalf("verdict = %q, want ask for a safety-immune call", got)
	}
	if !a.unattendedAskBlocked("@coder", immune, pm, func(string) {}) {
		t.Fatal("a safety-immune call must be refused unattended")
	}
}

func TestSeedPolicyAutoModeFromEnv_DefaultOff(t *testing.T) {
	t.Setenv(coder.PolicyAutoModeEnv, "")
	c := &ChatCLI{logger: zap.NewNop()}
	c.seedPolicyAutoModeFromEnv()
	if c.PolicyAutoMode() {
		t.Fatal("automode must be off unless the operator opts in")
	}
}

// TestWorkerPolicyAdapter_UnattendedAsk: parallel workers follow the same
// contract — refused by default, approved under automode, deny rules win.
func TestWorkerPolicyAdapter_UnattendedAsk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pa, err := newWorkerPolicyAdapter(zap.NewNop())
	if err != nil {
		t.Fatalf("newWorkerPolicyAdapter: %v", err)
	}
	c := &ChatCLI{unattended: true}
	a := NewAgentMode(c, zap.NewNop())
	pa.unattended = true
	pa.autoApprove = a.askAutoApproved

	ok, msg := pa.CheckAndPrompt(context.Background(), "@coder", unattendedExecTouch)
	if ok || !strings.Contains(msg, coder.PolicyAutoModeEnv) {
		t.Fatalf("default must refuse and name the opt-in, got ok=%v msg=%q", ok, msg)
	}

	c.policyAutoMode.Store(true)
	if ok, _ := pa.CheckAndPrompt(context.Background(), "@coder", unattendedExecTouch); !ok {
		t.Fatal("automode must approve the worker's ask")
	}

	if err := pa.pm.AddRule("@coder exec --cmd touch", coder.ActionDeny); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if ok, _ := pa.CheckAndPrompt(context.Background(), "@coder", unattendedExecTouch); ok {
		t.Fatal("a deny rule must block the worker even under automode")
	}
}

// TestPolicyUnavailable_Denies: a policy that fails to load is never
// permission — not for the orchestrator, not for workers.
func TestPolicyUnavailable_Denies(t *testing.T) {
	a, c := newUnattendedAgent(&sinkRecorder{})
	loadErr := errors.New("home unavailable")
	if !a.policyUnavailableBlocked(loadErr, "@coder", unattendedExecTouch, func(string) {}) {
		t.Fatal("a policy load failure must block the tool call")
	}
	if len(c.history) == 0 || !strings.Contains(c.history[len(c.history)-1].Content, "SECURITY BLOCK") {
		t.Fatal("the model must be told why the call was blocked")
	}
	if a.policyUnavailableBlocked(nil, "@coder", unattendedExecTouch, func(string) {}) {
		t.Fatal("no error, no block")
	}
	if ok, _ := (policyUnavailableChecker{err: loadErr}).CheckAndPrompt(context.Background(), "@coder", `{"cmd":"read"}`); ok {
		t.Fatal("the fallback worker checker must deny every call")
	}
}

// TestNewPolicyManagerFailure_DeniesEveryPath forces NewPolicyManager to fail
// (no home directory) and checks each non-interactive entry denies.
func TestNewPolicyManagerFailure_DeniesEveryPath(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := coder.NewPolicyManager(zap.NewNop()); err == nil {
		t.Skip("platform resolves a home directory without HOME")
	}
	c := &ChatCLI{logger: zap.NewNop()}
	c.policyAutoMode.Store(true)
	if err := c.rpcToolPolicyGate("@coder", "read --file x"); err == nil {
		t.Fatal("RPC tool gate must deny when the policy cannot load")
	}
	tg := &taskGraphAdapter{cli: c, logger: zap.NewNop()}
	if ok, _ := tg.gatePolicyChecker()(context.Background(), "@coder", gateExecPolicyArgs("true")); ok {
		t.Fatal("taskgraph gate check must deny when the policy cannot load")
	}
}

// TestRunAnyRPCToolArgv_AskRefusedWithoutOptIn: the RPC tool surface (MCP
// passthrough, gRPC CallTool, chatcli tool) applies the policy; an ask is
// refused unless the session is in automode, and the tool never runs.
func TestRunAnyRPCToolArgv_AskRefusedWithoutOptIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_MCP_TOOLS", "all")
	rec := &argvRecorderPlugin{}
	c := &ChatCLI{pluginManager: newTestPluginManagerWith(t, rec)}

	_, err := c.RunAnyRPCToolArgv(context.Background(), rec.Name(), []string{"--term", "x"})
	if err == nil || !strings.Contains(err.Error(), coder.PolicyAutoModeEnv) {
		t.Fatalf("an ask must be refused with the opt-in named, got %v", err)
	}
	if rec.gotArgs != nil {
		t.Fatal("a refused tool must not run")
	}
}

// TestRunAnyRPCTool_DenyRuleBlocksWithOptIn: a deny rule blocks the RPC tool
// surface even in automode.
func TestRunAnyRPCTool_DenyRuleBlocksWithOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHATCLI_MCP_TOOLS", "all")
	rec := &argvRecorderPlugin{}
	c := &ChatCLI{pluginManager: newTestPluginManagerWith(t, rec)}
	c.policyAutoMode.Store(true)
	pm, err := coder.NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.AddRule(rec.Name(), coder.ActionDeny); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunAnyRPCTool(context.Background(), rec.Name(), "--term x"); err == nil {
		t.Fatal("a deny rule must block the RPC tool even in automode")
	}
	if rec.gotArgs != nil {
		t.Fatal("a denied tool must not run")
	}
}

// TestCoderGateRunner_AsksPolicy: a taskgraph gate command passes the policy
// as an @coder exec before it runs; a refusal stops it.
func TestCoderGateRunner_AsksPolicy(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "gate-ran")
	cmd := "touch " + marker

	var gotTool, gotArgs string
	deny := func(_ context.Context, tool, args string) (bool, string) {
		gotTool, gotArgs = tool, args
		return false, "needs approval"
	}
	r := &coderGateRunner{workspace: dir, check: deny}
	if _, err := r.RunGateStep(context.Background(), dir, cmd, nil); err == nil {
		t.Fatal("a refused gate command must error")
	}
	if gotTool != "@coder" || !strings.Contains(gotArgs, `"exec"`) || !strings.Contains(gotArgs, marker) {
		t.Fatalf("the policy must see the @coder exec, got tool=%q args=%q", gotTool, gotArgs)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused gate command must not run")
	}

	nilCheck := &coderGateRunner{workspace: dir}
	if _, err := nilCheck.RunGateStep(context.Background(), dir, cmd, nil); err == nil {
		t.Fatal("a gate runner without a policy check must refuse")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a gate runner without a policy check must not run the command")
	}
}

// TestTaskGraphGatePolicyChecker_Fallback: without a live worker adapter the
// gate check resolves the policy non-interactively — exec asks, so it is
// refused by default and approved under automode.
func TestTaskGraphGatePolicyChecker_Fallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &ChatCLI{logger: zap.NewNop()}
	tg := &taskGraphAdapter{cli: c, logger: zap.NewNop()}
	args := gateExecPolicyArgs("touch built.txt")
	if ok, _ := tg.gatePolicyChecker()(context.Background(), "@coder", args); ok {
		t.Fatal("an exec gate must be refused without the opt-in")
	}
	c.policyAutoMode.Store(true)
	if ok, _ := tg.gatePolicyChecker()(context.Background(), "@coder", args); !ok {
		t.Fatal("an exec gate must be approved under automode")
	}
	if ok, _ := tg.gatePolicyChecker()(context.Background(), "@coder", gateExecPolicyArgs("git reset --hard HEAD~1")); ok {
		t.Fatal("a safety-immune gate must be refused even under automode")
	}
}

// TestIsCoderExecDangerous_FlagForms: the shell line is found in every form
// Go's flag parser accepts, and every occurrence is checked (the parser keeps
// the last one).
func TestIsCoderExecDangerous_FlagForms(t *testing.T) {
	a := NewAgentMode(&ChatCLI{}, zap.NewNop())
	cases := [][]string{
		{"exec", "--cmd=rm -rf /"},
		{"exec", "-cmd=rm -rf /"},
		{"exec", "--cmd", "rm -rf /"},
		{"exec", "--cmd", "ls", "--cmd", "rm -rf /"},
		{"exec", "--cmd=ls", "--cmd=rm -rf /"},
		{"test", "--cmd=rm -rf /"},
	}
	for _, argv := range cases {
		if dangerous, _ := a.isCoderExecDangerous(argv); !dangerous {
			t.Errorf("%q must be recognized as dangerous", argv)
		}
	}
	if dangerous, cmd := a.isCoderExecDangerous([]string{"exec", "--cmd=ls -la"}); dangerous || cmd != "ls -la" {
		t.Errorf("a safe --cmd= value must pass and be returned, got dangerous=%v cmd=%q", dangerous, cmd)
	}
}
