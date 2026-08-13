/*
 * ChatCLI - Slash command integration tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/commands"
	"go.uber.org/zap"
)

// newCommandsTestCLI builds a ChatCLI with a real CommandHandler (reserved
// names derive from its live routes) and a catalog rooted at temp dirs.
func newCommandsTestCLI(t *testing.T) (*ChatCLI, string) {
	t.Helper()
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.commandHandler = NewCommandHandler(cli)
	project := t.TempDir()
	cli.slashCommands = commands.NewCatalog(project, t.TempDir(), cli.commandHandler.isReservedSlashName, zap.NewNop())
	cli.slashCommands.SetHomeDir(t.TempDir()) // hermetic: no real ~/.codex, ~/.gemini, …
	return cli, project
}

func writeCommandFile(t *testing.T, project, name, content string) {
	t.Helper()
	path := filepath.Join(project, ".chatcli", "commands", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeriveReservedNames_CoversFullDispatchSurface(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	ch := NewCommandHandler(cli)

	// The old hand-maintained list had drifted: these are commands it did
	// NOT protect. The derived set must cover every route root + sentinel.
	for _, name := range []string{
		"board", "mail", "jobs", "policy", "schedule", "parked", "graph",
		"moa", "update", "export", "gateway", "lsp", "thinking", "refine",
		"verify", "reflect", "wait", "resume", "cancel-park", "plan",
		"agents", "websearch", "max-tokens", "model-image",
		// classics
		"session", "config", "help", "run", "coder", "agent", "exit",
	} {
		if !ch.isReservedSlashName(name) {
			t.Errorf("%q must be reserved (derived from live routes)", name)
		}
	}
	if ch.isReservedSlashName("review-pr") {
		t.Error("non-builtin names must not be reserved")
	}
}

func TestExpandSlashCommandInput_ExpandsAndStagesHints(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "review-pr.md",
		"---\ndescription: review\nmodel: claude-sonnet-5\neffort: high\nallowed-tools: read, search\n---\nReview PR $1 with focus on $ARGUMENTS.")

	expanded, ok := cli.expandSlashCommandInput(context.Background(), "/review-pr 1326 security", false)
	if !ok {
		t.Fatal("known command must expand")
	}
	if !strings.Contains(expanded, "Review PR 1326") || !strings.Contains(expanded, "1326 security") {
		t.Errorf("interpolation failed: %q", expanded)
	}
	if m, e := cli.consumePendingCommandHints(); m != "claude-sonnet-5" || e != "high" {
		t.Errorf("hints not staged: model=%q effort=%q", m, e)
	}
	if m, e := cli.consumePendingCommandHints(); m != "" || e != "" {
		t.Error("hints must be single-consumption")
	}
	if scope := cli.consumePendingCommandToolScope(); len(scope) != 2 {
		t.Errorf("tool scope not staged: %v", scope)
	}
	if scope := cli.consumePendingCommandToolScope(); scope != nil {
		t.Error("tool scope must be single-consumption")
	}
}

func TestExpandSlashCommandInput_UnknownAndDisabled(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "known.md", "body")

	if _, ok := cli.expandSlashCommandInput(context.Background(), "/unknown-xyz", false); ok {
		t.Error("unknown token must not expand")
	}
	if _, ok := cli.expandSlashCommandInput(context.Background(), "plain text", false); ok {
		t.Error("non-slash input must not expand")
	}
	t.Setenv("CHATCLI_COMMANDS", "off")
	if _, ok := cli.expandSlashCommandInput(context.Background(), "/known", false); ok {
		t.Error("master switch off must disable expansion")
	}
}

func TestExpandSlashCommandInput_ReservedNeverDispatches(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	// A hostile project file trying to hijack /session.
	writeCommandFile(t, project, "session.md", "HIJACKED $ARGUMENTS")

	if _, ok := cli.expandSlashCommandInput(context.Background(), "/session list", false); ok {
		t.Fatal("a command shadowing a built-in must never dispatch")
	}
	if _, refused := cli.slashCommands.Refused()["session"]; !refused {
		t.Error("shadow attempt must be recorded")
	}
}

// isolatePolicyHome points the policy manager at an empty HOME so the
// developer's real ~/.chatcli/coder_policy.json can never leak into the
// gate assertions.
func isolatePolicyHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir()) // windows
}

func TestPreExecGate_UnattendedFailSafe(t *testing.T) {
	isolatePolicyHome(t)
	cli, project := newCommandsTestCLI(t)
	cli.unattended = true
	writeCommandFile(t, project, "ctx.md", "Context:\n! echo should-not-run\nEnd.")

	// Unattended + no automode: "ask" resolves to DENY, marker replaces
	// the line, nothing executes.
	expanded, ok := cli.expandSlashCommandInput(context.Background(), "/ctx", false)
	if !ok {
		t.Fatal("command must still expand (with denial markers)")
	}
	if strings.Contains(expanded, "should-not-run\n") && !strings.Contains(expanded, "denied") {
		t.Errorf("pre-exec must not run unattended without automode: %q", expanded)
	}
	if !strings.Contains(expanded, "denied by the security gate") {
		t.Errorf("denial must be explicit to the model: %q", expanded)
	}
}

func TestPreExecGate_AutomodeRunsButImmuneStaysBlocked(t *testing.T) {
	isolatePolicyHome(t)
	cli, project := newCommandsTestCLI(t)
	cli.unattended = true
	cli.SetPolicyAutoMode(true)
	t.Cleanup(func() { cli.SetPolicyAutoMode(false) })

	writeCommandFile(t, project, "ok.md", "! echo automode-ran")
	expanded, ok := cli.expandSlashCommandInput(context.Background(), "/ok", false)
	if !ok || !strings.Contains(expanded, "automode-ran") {
		t.Errorf("automode must approve a benign pre-exec: %q", expanded)
	}

	writeCommandFile(t, project, "danger.md", "! rm -rf /tmp/x && sudo reboot")
	expanded, ok = cli.expandSlashCommandInput(context.Background(), "/danger", false)
	if !ok {
		t.Fatal("expansion itself must succeed")
	}
	if !strings.Contains(expanded, "denied by the security gate") {
		t.Errorf("safety-immune commands must stay blocked even under automode: %q", expanded)
	}
}

func TestCommandScopeAllows(t *testing.T) {
	a := &AgentMode{}
	if !a.commandScopeAllows("@coder") {
		t.Error("empty scope must allow everything")
	}
	a.commandToolScope = []string{"read", "@search"}
	if !a.commandScopeAllows("@read") || !a.commandScopeAllows("search") {
		t.Error("scope matching must tolerate the @ prefix on either side")
	}
	if a.commandScopeAllows("@coder") {
		t.Error("tool outside the scope must not pass")
	}
}

func TestRenderCommandPromptRPC(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "triage.md",
		"---\ndescription: triage\nargument-hint: <issue>\n---\nTriage issue $1.")

	content, ok := cli.RenderCommandPromptRPC(context.Background(), "triage", "42")
	if !ok || !strings.Contains(content, "Triage issue 42.") {
		t.Errorf("MCP prompt render failed: %q ok=%v", content, ok)
	}
	if _, ok := cli.RenderCommandPromptRPC(context.Background(), "nope", ""); ok {
		t.Error("unknown prompt must return ok=false")
	}
	prompts := cli.ListCommandPromptsRPC()
	if len(prompts) != 1 || prompts[0][0] != "triage" || prompts[0][2] != "<issue>" {
		t.Errorf("prompt listing mismatch: %v", prompts)
	}
}

func TestOneShotExpansion_GoldenPath(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "greet.md", "Say hello to $1 politely.")

	// The one-shot surface goes through the same chokepoint; interactive
	// false because stdin is not a TTY under tests.
	expanded, ok := cli.expandSlashCommandInput(context.Background(), "/greet maria", false)
	if !ok || expanded != "Say hello to maria politely." {
		t.Errorf("one-shot expansion mismatch: %q ok=%v", expanded, ok)
	}
}
