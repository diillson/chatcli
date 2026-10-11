/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package coder

import (
	"fmt"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newTempPolicyManager builds a policy manager over an isolated HOME with
// the default rules (exec asks; read/tree/search allowed).
func newTempPolicyManager(t *testing.T) *PolicyManager {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	pm, err := NewPolicyManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewPolicyManager: %v", err)
	}
	return pm
}

const execTouch = `{"cmd":"exec","args":{"cmd":"touch built.txt"}}`

func TestResolveWithoutApprover_AskDeniedByDefault(t *testing.T) {
	pm := newTempPolicyManager(t)
	if got := ResolveWithoutApprover(pm, "@coder", execTouch, false); got != ActionAsk {
		t.Fatalf("without the opt-in an ask must stay unresolved (refused by callers), got %q", got)
	}
}

func TestResolveWithoutApprover_AutoModeApprovesAsk(t *testing.T) {
	pm := newTempPolicyManager(t)
	if got := ResolveWithoutApprover(pm, "@coder", execTouch, true); got != ActionAllow {
		t.Fatalf("automode must approve a plain ask, got %q", got)
	}
}

func TestResolveWithoutApprover_DenyRuleWinsOverAutoMode(t *testing.T) {
	pm := newTempPolicyManager(t)
	if err := pm.AddRule("@coder exec --cmd touch", ActionDeny); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if got := ResolveWithoutApprover(pm, "@coder", execTouch, true); got != ActionDeny {
		t.Fatalf("a deny rule must block even with automode, got %q", got)
	}
}

func TestResolveWithoutApprover_SafetyImmuneNeverAutoApproved(t *testing.T) {
	pm := newTempPolicyManager(t)
	for _, args := range []string{
		`{"cmd":"exec","args":{"cmd":"git push --force origin main"}}`,
		"exec --cmd git reset --hard HEAD~3",
	} {
		if got := ResolveWithoutApprover(pm, "@coder", args, true); got == ActionAllow {
			t.Fatalf("safety-immune %q must never be auto-approved", args)
		}
	}
}

func TestResolveWithoutApprover_NilManagerDenies(t *testing.T) {
	if got := ResolveWithoutApprover(nil, "@coder", `{"cmd":"read","args":{"file":"x"}}`, true); got != ActionDeny {
		t.Fatalf("an unavailable policy must deny, got %q", got)
	}
}

func TestOperatorPolicyAutoMode_ParsesAndWarnsOnce(t *testing.T) {
	for _, v := range []string{"", "false", "0", "nope"} {
		t.Setenv(PolicyAutoModeEnv, v)
		if OperatorPolicyAutoMode(nil) {
			t.Fatalf("%q must not enable automode", v)
		}
	}
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)
	t.Setenv(PolicyAutoModeEnv, "true")
	policyAutoModeWarnOnce = sync.Once{}
	t.Cleanup(func() { policyAutoModeWarnOnce = sync.Once{} })
	for i := 0; i < 3; i++ {
		if !OperatorPolicyAutoMode(logger) {
			t.Fatal("true must enable automode")
		}
	}
	if n := logs.FilterMessageSnippet("policy automode enabled").Len(); n != 1 {
		t.Fatalf("the opt-in must be logged at warn exactly once, got %d", n)
	}
}

// TestPolicyManagerCheck_ConcurrentNoRace runs Check and LastMatchedRule
// from many goroutines; under -race the old read-locked write of lastRule
// was reported as a data race.
func TestPolicyManagerCheck_ConcurrentNoRace(t *testing.T) {
	pm := newTempPolicyManager(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if i%2 == 0 {
					pm.Check("@coder", fmt.Sprintf(`{"cmd":"read","args":{"file":"f%d"}}`, j))
				} else {
					pm.Check("@coder", fmt.Sprintf(`{"cmd":"write","args":{"file":"f%d"}}`, j))
				}
				_, _ = pm.LastMatchedRule()
			}
		}(i)
	}
	wg.Wait()
}
