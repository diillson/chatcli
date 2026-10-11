/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package coder

import "testing"

// TestCheck_ReadOnlyExecSeesWholeCommand: the read-only auto-allow used to
// classify a reconstruction of the command (cut at the first "--flag",
// newlines folded into spaces), so a read-only prefix let the rest run.
func TestCheck_ReadOnlyExecSeesWholeCommand(t *testing.T) {
	pm := newTempPolicyManager(t)
	// No rule for exec: only the read-only classification could allow.
	if _, err := pm.DeleteRule("@coder exec"); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	for _, args := range []string{
		`{"cmd":"exec","args":{"cmd":"cat a.txt --x ; touch pwned"}}`,
		`{"cmd":"exec","args":{"cmd":"ls\ntouch pwned"}}`,
		"exec --cmd \"ls\ntouch pwned\"",
		`exec --cmd "cat a.txt --x ; touch pwned"`,
		`{"cmd":"exec","args":{"cmd":"ls"},"command":"touch pwned"}`,
		`{"cmd":"exec","args":{"cmd":"ls","command":"touch pwned"}}`,
		`{"cmd":"exec","args":{"cmd":"ls &amp;&amp; touch pwned"}}`,
		`["exec","--cmd","ls","--cmd","touch pwned"]`,
	} {
		if got := pm.Check("@coder", args); got == ActionAllow {
			t.Errorf("%s must not be auto-allowed", args)
		}
	}
}

// TestCheck_ReadOnlyExecStillAllowed: unambiguous read-only commands keep
// their auto-allow in every argument form.
func TestCheck_ReadOnlyExecStillAllowed(t *testing.T) {
	pm := newTempPolicyManager(t)
	if _, err := pm.DeleteRule("@coder exec"); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	for _, args := range []string{
		`{"cmd":"exec","args":{"cmd":"ls -la"}}`,
		`{"cmd":"exec","args":{"cmd":"git status","dir":"."}}`,
		`exec --cmd "ls -la"`,
		`exec --cmd=pwd`,
		`["exec","--cmd","ls -la"]`,
	} {
		if got := pm.Check("@coder", args); got != ActionAllow {
			t.Errorf("%s should stay auto-allowed, got %q", args, got)
		}
	}
}

func TestExecShellCommand_ExactValue(t *testing.T) {
	cases := map[string]string{
		`{"cmd":"exec","args":{"cmd":"cat a.txt --x ; touch pwned"}}`: "cat a.txt --x ; touch pwned",
		`exec --dir . --cmd "ls -la" --timeout 5`:                     "ls -la",
		`exec --cmd=ls --cmd=pwd`:                                     "pwd",
	}
	for args, want := range cases {
		got, ok := execShellCommand(args)
		if !ok || got != want {
			t.Errorf("execShellCommand(%s) = %q, %v; want %q", args, got, ok, want)
		}
	}
	for _, args := range []string{
		`exec --cmd "unbalanced`,
		`exec --bogus x --cmd ls`,
		`exec --cmd`,
		`{"cmd":"exec","args":"{\"cmd\":\"ls\"}"}`,
		`{"cmd":"exec","args":{"cmd":"ls"},"positional":["x"]}`,
	} {
		if got, ok := execShellCommand(args); ok {
			t.Errorf("execShellCommand(%s) = %q; want undetermined", args, got)
		}
	}
}
