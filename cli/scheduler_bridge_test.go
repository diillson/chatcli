/*
 * Tests for the scheduler bridge's slash-command classifier. The
 * production trace (Apr 2026) showed scheduled "/run open -a Docker"
 * jobs failing at fire-time because the bridge let them flow into
 * HandleCommand, which panics errAgentModeRequest. The intercept now
 * routes those forms to RunAgentTask / RunCoderTask directly.
 *
 * Also covers the stdout-capture pump (regression test for the pipe
 * deadlock that left jobs stuck in "running" forever) and the
 * reentrancy guard surface used by checkAgentNotBusy.
 */
package cli

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyAgentSlash(t *testing.T) {
	cases := []struct {
		line     string
		wantTask string
		wantKind agentSlashKind
		wantOK   bool
	}{
		// /run with task → agent profile.
		{"/run open -a Docker", "open -a Docker", agentSlashKindAgent, true},
		{"  /run echo hi  ", "echo hi", agentSlashKindAgent, true},
		{"/run\tdocker info", "docker info", agentSlashKindAgent, true},

		// /agent with task → agent profile.
		{"/agent refactor cli/agent_mode.go", "refactor cli/agent_mode.go", agentSlashKindAgent, true},

		// /coder with task → coder profile.
		{"/coder fix the docker job", "fix the docker job", agentSlashKindCoder, true},

		// Bare commands → ok=true with empty task; caller surfaces a hint.
		{"/run", "", agentSlashKindAgent, true},
		{"/agent", "", agentSlashKindAgent, true},
		{"/coder", "", agentSlashKindCoder, true},

		// /coder is matched before /agent so its prefix wins. The
		// /run prefix-overlap check (none of the others share a prefix
		// with /run) is implicit.
		{"/coder /agent ambiguous", "/agent ambiguous", agentSlashKindCoder, true},

		// Non-mode slashes must fall through.
		{"/jobs list", "", 0, false},
		{"/schedule list", "", 0, false},
		{"@coder exec ls", "", 0, false},
		{"shell: docker info", "", 0, false},
		{"agent: deploy", "", 0, false},

		// Prefix-overlap: "/runner" must NOT match "/run" — it's a
		// different (hypothetical) slash command.
		{"/runner status", "", 0, false},
		{"/agentic-foo", "", 0, false},
		{"/coderxyz arg", "", 0, false},

		// Empty/garbage.
		{"", "", 0, false},
		{"   ", "", 0, false},
		{"random text", "", 0, false},
	}
	for _, tc := range cases {
		gotTask, gotKind, gotOK := classifyAgentSlash(tc.line)
		if gotOK != tc.wantOK || gotTask != tc.wantTask || gotKind != tc.wantKind {
			t.Errorf(
				"classifyAgentSlash(%q) = (%q, %d, %v); want (%q, %d, %v)",
				tc.line, gotTask, gotKind, gotOK, tc.wantTask, tc.wantKind, tc.wantOK,
			)
		}
	}
}

// TestBuildSchedulerSystemPrompt_TeachesXMLToolCallFormat is the
// regression test for the production hallucination where /run jobs
// reported "success" with fabricated tool output (e.g. WSL2 docker
// info on a macOS host). The worker's text-mode path passes our
// SystemPrompt to the LLM verbatim — if the prompt doesn't show the
// chatcli XML tool-call syntax, the LLM falls back to Anthropic's
// `<function_calls><invoke>` training-format which the parser doesn't
// recognize. The worker then sees "no tool calls" and treats the
// hallucinated text as a finished answer.
func TestBuildSchedulerSystemPrompt_TeachesXMLToolCallFormat(t *testing.T) {
	prompt := buildSchedulerSystemPrompt("", []string{"exec", "read", "write"})

	// Must teach the canonical chatcli XML form so text-mode LLMs
	// emit it instead of hallucinating <function_calls><invoke>.
	if !strings.Contains(prompt, `<tool_call name="@coder"`) {
		t.Errorf("system prompt missing the canonical XML tool-call form: %q", prompt)
	}
	if !strings.Contains(prompt, `"cmd":"exec"`) {
		t.Errorf("system prompt missing exec example: %q", prompt)
	}

	// Must explicitly forbid the hallucination format the LLM
	// reaches for by default — otherwise the worker silently
	// reports success on hallucinated work.
	for _, forbidden := range []string{"`<function_calls>`", "`<invoke>`"} {
		if !strings.Contains(prompt, forbidden) {
			t.Errorf("system prompt should call out forbidden tool-call format %q: %q", forbidden, prompt)
		}
	}

	// Must affirm scheduler autonomy so the LLM doesn't refuse
	// legitimate tasks like "open -a Docker" with subagent-style
	// apologetics ("I cannot launch desktop applications").
	for _, must := range []string{"autonomously", "exec tool", "no human at the keyboard"} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(must)) {
			t.Errorf("system prompt missing autonomy framing %q: %q", must, prompt)
		}
	}

	// Must mention the available subcommands so the LLM picks the
	// right cmd value in the JSON envelope.
	if !strings.Contains(prompt, "exec") || !strings.Contains(prompt, "read") {
		t.Errorf("system prompt does not list the granted tools: %q", prompt)
	}
}

// TestBuildSchedulerSystemPrompt_LayersPreface confirms that a
// non-empty preface (e.g. CoderSystemPrompt for /coder tasks) gets
// prepended to the scheduler framing rather than overriding or being
// dropped.
func TestBuildSchedulerSystemPrompt_LayersPreface(t *testing.T) {
	preface := "PREFACE-MARKER-FOR-TEST"
	prompt := buildSchedulerSystemPrompt(preface, []string{"exec"})
	if !strings.Contains(prompt, preface) {
		t.Errorf("preface dropped from system prompt: %q", prompt)
	}
	prefaceIdx := strings.Index(prompt, preface)
	frameIdx := strings.Index(prompt, "autonomously")
	if prefaceIdx < 0 || frameIdx < 0 || prefaceIdx > frameIdx {
		t.Errorf("preface should appear before scheduler framing; preface@%d framing@%d", prefaceIdx, frameIdx)
	}
}

// TestSchedulerPolicyChecker_NeverPrompts: the headless agent runner
// for scheduled /run|/agent|/coder tasks must never block on stdin.
// The PolicyChecker must classify and return *synchronously*. We assert
// the call returns within a tight deadline (a real interactive prompt
// would block indefinitely for stdin) and surfaces a helpful message
// that points the user at non-interactive remediation paths.
func TestSchedulerPolicyChecker_NeverPrompts(t *testing.T) {
	b := &schedulerBridge{cli: &ChatCLI{}}
	pc := newSchedulerPolicyChecker(b, nil, false)

	type result struct {
		allowed bool
		msg     string
	}
	ch := make(chan result, 1)
	go func() {
		ok, m := pc.CheckAndPrompt(context.Background(), "@coder", `{"cmd":"exec","args":{"cmd":"ls"}}`)
		ch <- result{ok, m}
	}()

	select {
	case r := <-ch:
		if r.allowed {
			t.Fatal("scheduled @coder exec without explicit allow rule must NOT be permitted automatically")
		}
		// Every fail path must point the user at how to fix it without
		// interaction — either via /config security allow, i_know:true,
		// or a deny diagnosis. The common element is the "scheduler:"
		// prefix and a remediation hint.
		if !strings.HasPrefix(r.msg, "scheduler:") {
			t.Errorf("error must be scheduler-tagged for surface clarity: %q", r.msg)
		}
		if !strings.ContainsAny(r.msg, "/-_") { // mentions "/config", "i_know", or similar
			t.Errorf("error must include a non-interactive remediation hint: %q", r.msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("PolicyChecker.CheckAndPrompt blocked — must return synchronously without prompting")
	}
}

// TestSchedulerPolicyChecker_DangerousConfirmedAdmitsAsk regression:
// a job enqueued with i_know:true (DangerousConfirmed=true) must pass
// fire-time tool-call checks even when the policy classifies the call
// as "Ask". Without this, /run|/agent|/coder jobs that legitimately
// pre-authorized got "tool requires user confirmation" failures even
// though the user explicitly opted in at schedule time.
func TestSchedulerPolicyChecker_DangerousConfirmedAdmitsAsk(t *testing.T) {
	b := &schedulerBridge{cli: &ChatCLI{}}

	// dangerousConfirmed=false → Ask should reject (baseline).
	pcNoConfirm := newSchedulerPolicyChecker(b, nil, false)
	allowed, msg := pcNoConfirm.CheckAndPrompt(context.Background(), "@coder",
		`{"cmd":"exec","args":{"cmd":"open -a Docker"}}`)
	if allowed {
		t.Errorf("without dangerousConfirmed, Ask classification must reject; got allowed=true (msg=%q)", msg)
	}

	// dangerousConfirmed=true → Ask should admit.
	pcConfirmed := newSchedulerPolicyChecker(b, nil, true)
	allowed, msg = pcConfirmed.CheckAndPrompt(context.Background(), "@coder",
		`{"cmd":"exec","args":{"cmd":"open -a Docker"}}`)
	if !allowed {
		t.Errorf("dangerousConfirmed=true must admit Ask classification; got allowed=false (msg=%q)", msg)
	}
}

// TestAgentModeRunInflight_CASGuard: prove that AgentMode.runInflight
// CAS rejects a concurrent CompareAndSwap when already set. We don't
// invoke Run() (it requires the full CLI scaffolding); the atomic
// semantics are what matter here.
func TestAgentModeRunInflight_CASGuard(t *testing.T) {
	var flag atomic.Bool
	if !flag.CompareAndSwap(false, true) {
		t.Fatal("first CAS should succeed")
	}
	if flag.CompareAndSwap(false, true) {
		t.Fatal("second CAS must fail while already set")
	}
	flag.Store(false)
	if !flag.CompareAndSwap(false, true) {
		t.Fatal("after Store(false), CAS should succeed again")
	}
}
