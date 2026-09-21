/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package lsp

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// Language servers are pooled and outlive turns. Each one is a span that
// stays open from spawn until its stdout closes.
func TestLanguageServerIsReportedFromSpawnToExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses cat")
	}
	catPath, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("cat not available")
	}
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(32)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	// cat echoes nothing valid and exits when killed: enough to exercise the
	// spawn and the read loop ending. --secret stands for a flag from the
	// override env, which must not be shown.
	c, err := Spawn(context.Background(), ServerSpec{Command: []string{catPath, "--secret-flag"}, LanguageID: "go"}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()

	var got []pulse.Event
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case ev := <-ch:
			if ev.Name == "lsp" {
				got = append(got, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of 2 events", len(got))
		}
	}
	if got[0].Phase != pulse.PhaseStart || got[1].Phase != pulse.PhaseEnd {
		t.Fatalf("events = %+v", got)
	}
	end := got[1].Attrs
	if end["server"] != "cat" || end["language"] != "go" || end["state"] != "server exited" {
		t.Fatalf("end attrs = %+v", end)
	}
	for _, v := range end {
		if v == "--secret-flag" || v == catPath {
			t.Fatalf("server flags or full path leaked: %+v", end)
		}
	}
}
