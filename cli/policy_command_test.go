/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestParsePolicyModeArg pins the lenient vocabulary of the mode switch:
// strict parsing here would make the command fail for users (and models)
// typing near-synonyms, for zero safety benefit.
func TestParsePolicyModeArg(t *testing.T) {
	cases := []struct {
		arg  string
		auto bool
		ok   bool
	}{
		{"auto", true, true},
		{"automode", true, true},
		{"AUTO", true, true},
		{"on", true, true},
		{"interactive", false, true},
		{"ask", false, true},
		{"manual", false, true},
		{"off", false, true},
		{"", false, false},
		{"yolo", false, false},
	}
	for _, tc := range cases {
		auto, ok := parsePolicyModeArg(tc.arg)
		if auto != tc.auto || ok != tc.ok {
			t.Errorf("parsePolicyModeArg(%q) = (%v, %v), want (%v, %v)", tc.arg, auto, ok, tc.auto, tc.ok)
		}
	}
}

// TestHandlePolicyCommand_TogglesSessionMode: the command must flip the
// session-scoped mode, accept the shorthand without the mode token, ignore
// unknown arguments, and default to interactive on a fresh session.
func TestHandlePolicyCommand_TogglesSessionMode(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	if c.PolicyAutoMode() {
		t.Fatal("a fresh session must start in interactive mode")
	}

	c.handlePolicyCommand("/policy mode auto")
	if !c.PolicyAutoMode() {
		t.Fatal("/policy mode auto must enable automode")
	}
	c.handlePolicyCommand("/policy mode interactive")
	if c.PolicyAutoMode() {
		t.Fatal("/policy mode interactive must disable automode")
	}

	c.handlePolicyCommand("/policy automode") // shorthand without "mode"
	if !c.PolicyAutoMode() {
		t.Fatal("/policy automode shorthand must enable automode")
	}
	c.handlePolicyCommand("/policy mode yolo") // unknown → unchanged
	if !c.PolicyAutoMode() {
		t.Fatal("an unknown mode argument must not change the session mode")
	}
	c.handlePolicyCommand("/policy off")
	if c.PolicyAutoMode() {
		t.Fatal("/policy off shorthand must disable automode")
	}

	// Status forms must not crash nor mutate.
	c.handlePolicyCommand("/policy")
	c.handlePolicyCommand("/policy status")
	c.handlePolicyCommand("/policy mode")
	if c.PolicyAutoMode() {
		t.Fatal("status forms must not change the session mode")
	}
}

// TestAskAutoApproved_Gate pins the automode contract: only policy "ask"
// verdicts auto-approve, and NEVER for safety-immune operations — those keep
// prompting exactly as in interactive mode. Deny rules never reach this gate
// (deny wins before ask in the policy check).
func TestAskAutoApproved_Gate(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	a := NewAgentMode(c, zap.NewNop())

	if a.askAutoApproved("@coder", `{"cmd":"write","args":{"file":"x"}}`) {
		t.Fatal("interactive mode must never auto-approve")
	}
	c.SetPolicyAutoMode(true)
	if !a.askAutoApproved("@coder", `{"cmd":"write","args":{"file":"x"}}`) {
		t.Fatal("automode must auto-approve a plain ask verdict")
	}
	if !a.askAutoApproved("@coder", `{"cmd":"exec","args":{"cmd":"go test ./..."}}`) {
		t.Fatal("automode must auto-approve non-immune exec commands")
	}
	if a.askAutoApproved("@coder", `{"cmd":"exec","args":{"cmd":"rm -rf /tmp/x"}}`) {
		t.Fatal("safety-immune operations must keep prompting even in automode")
	}
	if a.askAutoApproved("@coder", `{"cmd":"exec","args":{"cmd":"sudo apt install x"}}`) {
		t.Fatal("privilege escalation must keep prompting even in automode")
	}

	// Nil receiver paths must stay safe.
	orphan := &AgentMode{}
	if orphan.askAutoApproved("@coder", "{}") {
		t.Fatal("an AgentMode without a ChatCLI must never auto-approve")
	}
}

// TestListACPCommands_IncludesPolicyWithRealHint: the ACP command listing
// must advertise /policy, and every allowlisted command's input hint must
// come from the live completer (its real subcommands), not the generic
// args placeholder — the IDE input hint is the only completion surface the
// ACP protocol offers past the command name.
func TestListACPCommands_IncludesPolicyWithRealHint(t *testing.T) {
	c := newCompleterTestCLI(t)
	cmds := c.ListACPCommands()

	var policy, session *ACPCommandInfo
	for i := range cmds {
		switch cmds[i].Name {
		case "policy":
			policy = &cmds[i]
		case "session":
			session = &cmds[i]
		}
	}
	if policy == nil {
		t.Fatal("/policy must be advertised over ACP")
	}
	if !strings.Contains(policy.InputHint, "mode") {
		t.Fatalf("policy hint must surface its real subcommands, got %q", policy.InputHint)
	}
	if session == nil {
		t.Fatal("/session must remain advertised over ACP")
	}
	if !strings.Contains(session.InputHint, "save") {
		t.Fatalf("session hint must surface its real subcommands, got %q", session.InputHint)
	}
}
