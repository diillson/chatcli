/*
 * ChatCLI - VerifyHook tests (Phase 6 / CoVe).
 */
package quality

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/workers"
)

func TestVerifyHook_DisabledSkips(t *testing.T) {
	cfg := Defaults()
	cfg.Verify.Enabled = false
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "x", Config: cfg}
	res := &workers.AgentResult{Output: "draft"}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		t.Fatal("disabled verify must not dispatch")
		return workers.AgentResult{}
	}}
	_ = NewVerifyHook(cd.handle, nil).PostRun(context.Background(), hc, res)
}

func TestVerifyHook_CleanResultMarksMetadata(t *testing.T) {
	cfg := Defaults()
	cfg.Verify.Enabled = true
	cfg.Verify.NumQuestions = 3
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	res := &workers.AgentResult{Output: "draft answer"}

	cd := &captureDispatch{response: func(call workers.AgentCall, _ int) workers.AgentResult {
		if call.Agent != workers.AgentTypeVerifier {
			t.Errorf("expected verifier dispatch; got %s", call.Agent)
		}
		if !strings.Contains(call.Task, workers.VerifyDirective) {
			t.Errorf("expected VerifyDirective in task")
		}
		// Simulate a clean verification.
		return workers.AgentResult{Output: "<status>verified-clean</status>\n<questions>\n- q1\n</questions>\n<answers>\n- a1\n</answers>\n<discrepancies>\nnone\n</discrepancies>\n<final>\ndraft answer\n</final>"}
	}}
	_ = NewVerifyHook(cd.handle, nil).PostRun(context.Background(), hc, res)
	if !res.MetadataFlag("verified_clean") {
		t.Errorf("expected verified_clean=true; metadata=%v", res.Metadata)
	}
	if res.MetadataFlag("verified_with_discrepancy") {
		t.Errorf("clean run should not flag discrepancy")
	}
	if res.Output != "draft answer" {
		t.Errorf("clean run should not rewrite; got %q", res.Output)
	}
}

func TestVerifyHook_DiscrepancyRewritesOutputAndFlags(t *testing.T) {
	cfg := Defaults()
	cfg.Verify.Enabled = true
	cfg.Verify.RewriteOnDiscrepancy = true
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	res := &workers.AgentResult{Output: "incorrect draft"}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		return workers.AgentResult{Output: "<status>verified-with-corrections</status>\n<questions>\n- q1\n</questions>\n<answers>\n- a1\n</answers>\n<discrepancies>\nq1 contradicts the draft\n</discrepancies>\n<final>\ncorrected answer\n</final>"}
	}}
	_ = NewVerifyHook(cd.handle, nil).PostRun(context.Background(), hc, res)
	if !res.MetadataFlag("verified_with_discrepancy") {
		t.Errorf("expected verified_with_discrepancy; metadata=%v", res.Metadata)
	}
	if res.Output != "corrected answer" {
		t.Errorf("expected rewrite; got %q", res.Output)
	}
}

func TestVerifyHook_DiscrepancyButRewriteOff(t *testing.T) {
	cfg := Defaults()
	cfg.Verify.Enabled = true
	cfg.Verify.RewriteOnDiscrepancy = false
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	res := &workers.AgentResult{Output: "original"}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		return workers.AgentResult{Output: "<status>verified-with-corrections</status>\n<discrepancies>\nfoo\n</discrepancies>\n<final>\nrewritten\n</final>"}
	}}
	_ = NewVerifyHook(cd.handle, nil).PostRun(context.Background(), hc, res)
	if res.Output != "original" {
		t.Errorf("RewriteOnDiscrepancy=false must keep original; got %q", res.Output)
	}
	if !res.MetadataFlag("verified_with_discrepancy") {
		t.Errorf("metadata should still flag discrepancy")
	}
}

func TestVerifyHook_AgentErrorSkips(t *testing.T) {
	cfg := Defaults()
	cfg.Verify.Enabled = true
	hc := &HookContext{Agent: &agentWithType{t: "coder"}, Task: "task", Config: cfg}
	res := &workers.AgentResult{Output: "draft", Error: errBoom}

	cd := &captureDispatch{response: func(_ workers.AgentCall, _ int) workers.AgentResult {
		t.Fatal("errored worker must skip verify")
		return workers.AgentResult{}
	}}
	_ = NewVerifyHook(cd.handle, nil).PostRun(context.Background(), hc, res)
}
