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
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// /park-note --token must not report success for a note nobody will read:
// a park that already resumed (its snapshot is kept for forensics), one
// whose job is finished, and one another live process owns — whose
// snapshot this process must not rewrite under it.
func TestParkNoteRefusesAParkThatWillNotReadIt(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	note := func(token string) string {
		return captureStdout(t, func() { cli.handleParkNoteCommand("--token " + token + " please read this") })
	}

	resumed := saveParkAged(t, time.Hour, "")
	snap, err := park.Load(resumed)
	if err != nil {
		t.Fatal(err)
	}
	snap.LastResumeAt, snap.ResumeState = time.Now().Add(-time.Minute), park.ResumeDone
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	finished := saveParkAged(t, time.Hour, enqueueParkJob(t, cli, true))
	remote := saveParkAged(t, time.Hour, "job-in-another-process")
	setRemoteParkProvider(&remoteParkProvider{owner: func(token string) (remoteParkOwner, bool) {
		return remoteParkOwner{Instance: "abcd", Origin: "gateway"}, token == remote
	}})
	t.Cleanup(func() { setRemoteParkProvider(nil) })
	waiting := saveParkAged(t, time.Hour, enqueueParkJob(t, cli, false))

	for name, token := range map[string]string{"resumed": resumed, "finished": finished, "remote": remote} {
		out := note(token)
		if strings.Contains(out, i18n.T("leftovers.park_note.queued", parkShort(token), parkShort(token))) {
			t.Errorf("%s park: the note was reported as queued: %q", name, out)
		}
		if got := parkDirectives(t, token); got != "" {
			t.Errorf("%s park: a note was written to its snapshot: %q", name, got)
		}
	}
	if out := note(remote); !strings.Contains(out, "gateway") {
		t.Errorf("the refusal must name the surface that owns the park: %q", out)
	}
	// The @park note tool follows the same rules, from flattened argv.
	plugins.SetParkCanceller(newLiveParkAdapter(cli))
	t.Cleanup(func() { plugins.SetParkCanceller(nil) })
	tool := plugins.NewBuiltinParkPlugin()
	for name, token := range map[string]string{"resumed": resumed, "finished": finished, "remote": remote} {
		out, err := tool.Execute(context.Background(), []string{"note", "--text", "from the model", "--token", token[:12]})
		if err == nil || !strings.Contains(err.Error(), "othing was recorded") || parkDirectives(t, token) != "" {
			t.Errorf("%s park: the tool's note must be refused and not written: %q %v", name, out, err)
		}
	}
	if _, err := tool.Execute(context.Background(), []string{"note", "--text", "from the model", "--token", waiting[:12]}); err != nil || parkDirectives(t, waiting) != "from the model" {
		t.Errorf("the tool's note for a waiting park must be recorded: %v", err)
	}
	if err := park.Delete(waiting); err != nil {
		t.Fatal(err)
	}
	waiting = saveParkAged(t, time.Hour, enqueueParkJob(t, cli, false))

	// A park that is still waiting, and that nobody else holds, takes it.
	if out := note(waiting); !strings.Contains(out, i18n.T("leftovers.park_note.queued", parkShort(waiting), parkShort(waiting))) || parkDirectives(t, waiting) != "please read this" {
		t.Errorf("a waiting park must take the note: %q", out)
	}
}
