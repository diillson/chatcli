/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// A draft sits on the idle prompt's line. A park fires: the bridge queues
// the token and types "/resume <token>" + Enter into the terminal — onto
// the end of the draft. The submitted line is the draft glued to the
// resume. It is that resume: the agent resumes once, the draft goes back
// on the next prompt, and nothing is sent to the model with nobody at the
// keyboard.
func TestInjectedResumeOnTopOfAShownDraftIsThatResume(t *testing.T) {
	resumed := 0
	cli := resumeCLI(t, func(context.Context) error {
		resumed++
		return nil
	})
	cli.commandHandler = NewCommandHandler(cli)
	sentToModel := i18n.T("cli.error.no_provider_configured") // what a chat line prints here: no client
	submit := func(line string) string { return captureStdout(t, func() { cli.executor(line) }) }

	token := firedPark(t, cli)
	cli.offerPromptDraft("y")
	if cli.promptInitialText() != "y" {
		t.Fatal("the test needs the draft shown on the prompt")
	}
	cli.pendingResumeQueue = []string{token}
	cli.promptShouldRebuild("y/", false) // the injected keys are input events on that prompt
	out := submit("y/resume " + token)
	if strings.Contains(out, sentToModel) {
		t.Fatalf("the draft glued to an injected resume was sent on as chat: %q", out)
	}
	if resumed != 1 {
		t.Fatalf("the agent must resume exactly once, resumed %d time(s)", resumed)
	}
	if got := cli.promptInitialText(); got != "y" {
		t.Fatalf("the draft must be back on the next prompt, unchanged; got %q", got)
	}

	// Only that exact shape. With no draft shown, or text that is not the
	// shown draft, or a token nothing queued, the line is what the user
	// typed and goes where it always went.
	cli.promptShouldRebuild("x", false) // the draft above is spent by real input
	if cli.promptInitialText() != "" {  // and the next prompt is built with none
		t.Fatal("a spent draft must not be shown again")
	}
	other := firedPark(t, cli)
	cli.pendingResumeQueue = []string{other}
	for _, line := range []string{"y/resume " + other, "why /resume " + other} {
		if out := submit(line); !strings.Contains(out, sentToModel) {
			t.Errorf("with no draft shown, %q is the user's own line: %q", line, out)
		}
	}
	cli.offerPromptDraft("keep this")
	_ = cli.promptInitialText()
	for _, line := range []string{"keep this/resume 0000000000000000", "keep thiS/resume " + token, "keep this /status"} {
		if out := submit(line); !strings.Contains(out, sentToModel) {
			t.Errorf("%q is not the shown draft plus a queued resume: %q", line, out)
		}
		_ = cli.promptInitialText()
	}
}
