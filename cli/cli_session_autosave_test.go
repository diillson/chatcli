/*
 * ChatCLI - Session autosave-on-exit tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newAutosaveCLI(t *testing.T, msgs int) *ChatCLI {
	t.Helper()
	cli := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	for i := 0; i < msgs; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		cli.history = append(cli.history, models.Message{Role: role, Content: fmt.Sprintf("m%d", i)})
	}
	return cli
}

func autosaveNames(t *testing.T, cli *ChatCLI) []string {
	t.Helper()
	names, err := cli.sessionManager.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	var autos []string
	for _, n := range names {
		if strings.HasPrefix(n, autosavePrefix) {
			autos = append(autos, n)
		}
	}
	return autos
}

func TestAutosaveOnExit_SavesConversation(t *testing.T) {
	cli := newAutosaveCLI(t, 4)
	cli.autosaveSessionOnExit()

	autos := autosaveNames(t, cli)
	if len(autos) != 1 {
		t.Fatalf("expected 1 autosave, got %v", autos)
	}
	sd, err := cli.sessionManager.LoadSessionV2(autos[0])
	if err != nil || len(sd.ChatHistory) != 4 {
		t.Fatalf("autosave content wrong: %v / %+v", err, sd)
	}

	// Idempotent: cleanup can be reached twice.
	cli.autosaveSessionOnExit()
	if autos = autosaveNames(t, cli); len(autos) != 1 {
		t.Errorf("second call must be a no-op, got %v", autos)
	}
}

func TestAutosaveOnExit_SkipsTrivialAndDisabled(t *testing.T) {
	// Trivial: fewer than 2 non-system messages.
	cli := newAutosaveCLI(t, 1)
	cli.history = append([]models.Message{{Role: "system", Content: "sys"}}, cli.history...)
	cli.autosaveSessionOnExit()
	if autos := autosaveNames(t, cli); len(autos) != 0 {
		t.Errorf("trivial session must not autosave, got %v", autos)
	}

	// Disabled by env.
	t.Setenv("CHATCLI_SESSION_AUTOSAVE", "false")
	cli2 := newAutosaveCLI(t, 4)
	cli2.autosaveSessionOnExit()
	if autos := autosaveNames(t, cli2); len(autos) != 0 {
		t.Errorf("disabled autosave must not save, got %v", autos)
	}
}

func TestPruneAutosaves_KeepsNewest(t *testing.T) {
	// The keep-count backstop is env-tunable; a small value keeps the test
	// cheap and covers the override path.
	t.Setenv("CHATCLI_SESSION_AUTOSAVE_KEEP", "10")
	keep := sessionAutosaveKeep()
	cli := newAutosaveCLI(t, 2)
	for i := 0; i < keep+3; i++ {
		name := fmt.Sprintf("%s202601%02d-120000", autosavePrefix, i+1)
		if err := cli.sessionManager.SaveSessionV2(name, cli.buildSessionData()); err != nil {
			t.Fatal(err)
		}
	}
	cli.pruneAutosaves()

	autos := autosaveNames(t, cli)
	if len(autos) != keep {
		t.Fatalf("expected %d autosaves after prune, got %d", keep, len(autos))
	}
	for _, n := range autos {
		if n == autosavePrefix+"20260101-120000" || n == autosavePrefix+"20260102-120000" || n == autosavePrefix+"20260103-120000" {
			t.Errorf("oldest autosave %s must have been pruned", n)
		}
	}
}

func TestSessionAutosaveKeep_DefaultAndOverride(t *testing.T) {
	t.Setenv("CHATCLI_SESSION_AUTOSAVE_KEEP", "")
	if got := sessionAutosaveKeep(); got != autosaveKeepDefault {
		t.Errorf("default keep must be %d, got %d", autosaveKeepDefault, got)
	}
	t.Setenv("CHATCLI_SESSION_AUTOSAVE_KEEP", "500")
	if got := sessionAutosaveKeep(); got != 500 {
		t.Errorf("override must apply, got %d", got)
	}
	t.Setenv("CHATCLI_SESSION_AUTOSAVE_KEEP", "banana")
	if got := sessionAutosaveKeep(); got != autosaveKeepDefault {
		t.Errorf("malformed override must fall back, got %d", got)
	}
}

// TestAutosaveOnExit_BoundSessionIsNotDuplicated: a terminal bound to a
// named session already persists every turn; exit flushes that session
// once more and writes no autosave- copy. Without write-through, and when
// the bound file vanished, the autosave still keeps the conversation.
func TestAutosaveOnExit_BoundSessionIsNotDuplicated(t *testing.T) {
	cli := newAutosaveCLI(t, 4)
	if err := cli.sessionManager.SaveSessionV2("minha", cli.buildSessionData()); err != nil {
		t.Fatal(err)
	}
	cli.currentSessionName = "minha"
	cli.markBoundSessionSynced("minha")
	cli.history = append(cli.history, models.Message{Role: "user", Content: "último turno"})

	cli.autosaveSessionOnExit()
	if autos := autosaveNames(t, cli); len(autos) != 0 {
		t.Fatalf("bound session must not be duplicated as autosave, got %v", autos)
	}
	sd, err := cli.sessionManager.LoadSessionV2("minha")
	if err != nil || len(sd.ChatHistory) != 5 {
		t.Fatalf("exit must flush the last turn into the bound session: %v / %d", err, len(sd.ChatHistory))
	}

	// Write-through off: the binding is inert, so the autosave applies.
	t.Setenv("CHATCLI_SESSION_WRITETHROUGH", "off")
	cli = newAutosaveCLI(t, 4)
	cli.currentSessionName = "minha"
	cli.autosaveSessionOnExit()
	if autos := autosaveNames(t, cli); len(autos) != 1 {
		t.Fatalf("without write-through the autosave must still happen, got %v", autos)
	}
	t.Setenv("CHATCLI_SESSION_WRITETHROUGH", "")

	// Bound file deleted by another surface: the flush detaches and the
	// autosave keeps the conversation.
	cli = newAutosaveCLI(t, 4)
	if err := cli.sessionManager.SaveSessionV2("sumiu", cli.buildSessionData()); err != nil {
		t.Fatal(err)
	}
	cli.currentSessionName = "sumiu"
	cli.markBoundSessionSynced("sumiu")
	if err := cli.sessionManager.DeleteSession("sumiu"); err != nil {
		t.Fatal(err)
	}
	cli.autosaveSessionOnExit()
	if autos := autosaveNames(t, cli); len(autos) != 1 {
		t.Fatalf("a vanished bound session must fall back to autosave, got %v", autos)
	}
}
