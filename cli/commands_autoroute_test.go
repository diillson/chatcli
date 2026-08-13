/*
 * ChatCLI - Slash command chat→coder auto-route tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"context"
	"errors"
	"testing"
)

func TestPeekSlashCommand_MatchMissAndNilSafe(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")

	cmd, args, ok := cli.peekSlashCommand("/deploy prod")
	if !ok || cmd == nil || cmd.Name != "deploy" || args != "prod" {
		t.Fatalf("peek must resolve the invocation: ok=%v cmd=%v args=%q", ok, cmd, args)
	}
	if _, _, ok := cli.peekSlashCommand("/nope"); ok {
		t.Error("unknown token must not peek")
	}
	if _, _, ok := cli.peekSlashCommand("plain text"); ok {
		t.Error("plain text must never peek (chat stays chat)")
	}

	bare := &ChatCLI{} // no catalog: nil-safe
	if _, _, ok := bare.peekSlashCommand("/deploy"); ok {
		t.Error("peek must be nil-safe without a catalog")
	}
}

func TestPeekSlashCommand_HasNoSideEffects(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md",
		"---\nmode: coder\nmodel: claude-sonnet-5\nallowed-tools: exec_command\n---\nbody")

	if _, _, ok := cli.peekSlashCommand("/deploy"); !ok {
		t.Fatal("peek must resolve")
	}
	if m, e := cli.consumePendingCommandHints(); m != "" || e != "" {
		t.Errorf("peek must not stage model/effort hints: %q/%q", m, e)
	}
	if scope := cli.consumePendingCommandToolScope(); scope != nil {
		t.Errorf("peek must not stage the tool scope: %v", scope)
	}
}

func TestCommandsAutorouteEnabled_EnvValues(t *testing.T) {
	cases := map[string]bool{
		"": true, "true": true, "1": true, "on": true, "weird": true,
		"false": false, "0": false, "off": false, "no": false, "disabled": false, " OFF ": false,
	}
	for value, want := range cases {
		t.Setenv(commandsAutorouteEnv, value)
		if got := commandsAutorouteEnabled(); got != want {
			t.Errorf("CHATCLI_COMMANDS_AUTOROUTE=%q: got %v, want %v", value, got, want)
		}
	}
}

func TestMaybeAutorouteCoderCommand_PanicsWithSentinelAndStagesInput(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")
	cli.replActive = true

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("coder command on the interactive REPL must unwind via the sentinel panic")
		}
		err, ok := r.(error)
		if !ok || !errors.Is(err, errCoderModeRequest) {
			t.Fatalf("panic value must be errCoderModeRequest, got %v", r)
		}
		if cli.pendingCoderCommandInput != "/deploy prod" {
			t.Errorf("raw invocation must be staged, got %q", cli.pendingCoderCommandInput)
		}
		if cli.pendingAction != "coder" {
			t.Errorf("pendingAction fallback (Windows panic clobber) must be set, got %q", cli.pendingAction)
		}
	}()
	cli.commandHandler.maybeAutorouteCoderCommand("/deploy prod")
}

func TestMaybeAutorouteCoderCommand_InferredFromAllowedTools(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "audit.md", "---\nallowed-tools: exec_command\n---\naudit the repo")
	cli.replActive = true

	defer func() {
		if recover() == nil {
			t.Fatal("allowed-tools without mode must infer coder and unwind")
		}
	}()
	cli.commandHandler.maybeAutorouteCoderCommand("/audit")
}

func TestMaybeAutorouteCoderCommand_HeadlessKeepsChatPath(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")

	cli.replActive = false // scheduler/ACP/MCP surfaces
	if cli.commandHandler.maybeAutorouteCoderCommand("/deploy") {
		t.Error("headless dispatch must keep today's chat path (no sentinel panic)")
	}
	cli.replActive, cli.unattended = true, true
	if cli.commandHandler.maybeAutorouteCoderCommand("/deploy") {
		t.Error("unattended dispatch must keep today's chat path")
	}
	if cli.pendingCoderCommandInput != "" {
		t.Error("nothing may be staged on the headless path")
	}
}

func TestMaybeAutorouteCoderCommand_BusyRefusesWithoutQueueing(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")
	cli.replActive = true
	cli.isExecuting.Store(true)

	if !cli.commandHandler.maybeAutorouteCoderCommand("/deploy") {
		t.Fatal("busy REPL must consume the invocation (refusal), not fall through to chat")
	}
	cli.messageQueueMu.Lock()
	queued := len(cli.messageQueue)
	cli.messageQueueMu.Unlock()
	if queued != 0 {
		t.Error("refusal must not enqueue: messageQueue replays entries as chat turns")
	}
	if cli.pendingCoderCommandInput != "" {
		t.Error("refusal must not stage a pending task")
	}
}

func TestMaybeAutorouteCoderCommand_ChatModeCommandUntouched(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "summary.md", "---\ndescription: plain chat prompt\n---\nsummarize")
	writeCommandFile(t, project, "vetoed.md", "---\nmode: chat\nallowed-tools: exec_command\n---\nbody")
	cli.replActive = true

	if cli.commandHandler.maybeAutorouteCoderCommand("/summary") {
		t.Error("chat-mode command must follow the normal chat expansion path")
	}
	if cli.commandHandler.maybeAutorouteCoderCommand("/vetoed") {
		t.Error("explicit mode: chat must veto the allowed-tools inference")
	}
}

func TestMaybeAutorouteCoderCommand_DisabledEnvKeepsChat(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")
	cli.replActive = true
	t.Setenv(commandsAutorouteEnv, "off")

	if cli.commandHandler.maybeAutorouteCoderCommand("/deploy") {
		t.Error("with autoroute off the invocation must fall through to the chat path")
	}
	if cli.pendingCoderCommandInput != "" {
		t.Error("nothing may be staged when autoroute is off")
	}
}

func TestResolveOneShotCommand_CoderRoute(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\ndeploy $ARGUMENTS now")

	expanded, coderRoute := cli.resolveOneShotCommand(context.Background(), "/deploy prod")
	if !coderRoute {
		t.Fatal("mode:coder command must route -p through the coder engine")
	}
	if expanded != "deploy prod now" {
		t.Errorf("expansion mismatch: %q", expanded)
	}
}

func TestResolveOneShotCommand_ChatRouteAndPassthrough(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "summary.md", "---\ndescription: chat prompt\n---\nsummarize $ARGUMENTS")

	expanded, coderRoute := cli.resolveOneShotCommand(context.Background(), "/summary today")
	if coderRoute {
		t.Error("chat-mode command must keep the chat route")
	}
	if expanded != "summarize today" {
		t.Errorf("chat command must still expand: %q", expanded)
	}

	passthrough, coderRoute := cli.resolveOneShotCommand(context.Background(), "plain question")
	if coderRoute || passthrough != "plain question" {
		t.Errorf("non-command input must pass through unchanged: %q (coder=%v)", passthrough, coderRoute)
	}
}

func TestResolveOneShotCommand_DisabledEnvKeepsChatRoute(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\ndeploy")
	t.Setenv(commandsAutorouteEnv, "off")

	if _, coderRoute := cli.resolveOneShotCommand(context.Background(), "/deploy"); coderRoute {
		t.Error("with autoroute off, -p must keep today's routing")
	}
}

// TestHandleCommand_AutoroutesCoderCommand walks the FULL dispatch path the
// executor takes (palette trigger → sentinel switch → catalog dispatch) to
// prove a bare coder command reaches the auto-route and unwinds.
func TestHandleCommand_AutoroutesCoderCommand(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")
	cli.replActive = true

	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok || !errors.Is(err, errCoderModeRequest) {
			t.Fatalf("full dispatch must unwind with errCoderModeRequest, got %v", r)
		}
		if cli.pendingCoderCommandInput != "/deploy prod" {
			t.Errorf("raw invocation must be staged, got %q", cli.pendingCoderCommandInput)
		}
	}()
	cli.commandHandler.HandleCommand(context.Background(), "/deploy prod")
}

// Same walk with a BARE invocation (no args): the palette trigger inspects
// bare single-token commands, and must not swallow a catalog coder command.
func TestHandleCommand_AutoroutesBareCoderCommand(t *testing.T) {
	cli, project := newCommandsTestCLI(t)
	writeCommandFile(t, project, "deploy.md", "---\nmode: coder\n---\nrun the deploy")
	cli.replActive = true

	defer func() {
		r := recover()
		err, ok := r.(error)
		if !ok || !errors.Is(err, errCoderModeRequest) {
			t.Fatalf("bare invocation must reach the auto-route, got %v (paletteRequested=%v)",
				r, cli.paletteRequested)
		}
	}()
	cli.commandHandler.HandleCommand(context.Background(), "/deploy")
}

func TestRunPendingCoderCommand_ExpandFailureConsumesInputOnce(t *testing.T) {
	cli, _ := newCommandsTestCLI(t)
	cli.pendingCoderCommandInput = "/ghost prod" // not in the catalog anymore

	cli.runPendingCoderCommand(context.Background())
	if cli.pendingCoderCommandInput != "" {
		t.Error("pending input must be consumed exactly once, even on expand failure")
	}
}
