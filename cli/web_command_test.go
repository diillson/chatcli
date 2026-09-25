/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/diillson/chatcli/models"
)

type fakeWebProcess struct{ killed bool }

func (p *fakeWebProcess) PID() int    { return 4242 }
func (p *fakeWebProcess) Kill() error { p.killed = true; return nil }

func newWebCLI(t *testing.T) (*ChatCLI, *[]string, *fakeWebProcess) {
	t.Helper()
	var spawned []string
	proc := &fakeWebProcess{}
	prevSpawn, prevLaunch, prevTimeout := webSpawner, webBrowserLauncher, webStartTimeout
	webSpawner = func(_ context.Context, session, urlFile string) (webProcess, error) {
		spawned = append(spawned, session)
		if err := os.WriteFile(urlFile, []byte("http://127.0.0.1:60000/?t=deadbeef\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return proc, nil
	}
	webBrowserLauncher = func(string) error { t.Fatal("the browser must not open under go test"); return nil }
	webStartTimeout = 2 * time.Second
	t.Cleanup(func() { webSpawner, webBrowserLauncher, webStartTimeout = prevSpawn, prevLaunch, prevTimeout })
	c := &ChatCLI{logger: zap.NewNop(), sessionManager: newTestSessionManager(t)}
	c.history = []models.Message{{Role: "user", Content: "from the terminal"}}
	return c, &spawned, proc
}

// /web binds an unbound terminal to a fresh saved session, starts one
// child bound to it, reuses it on the next open, reports it and stops it.
func TestWebCommand_BindsSpawnsReusesAndStops(t *testing.T) {
	c, spawned, proc := newWebCLI(t)
	ctx := context.Background()

	out := captureStdout(t, func() { c.handleWebCommand(ctx, "/web") })
	if !strings.Contains(out, "http://127.0.0.1:60000/?t=deadbeef") {
		t.Fatalf("open must print the address: %s", out)
	}
	if len(*spawned) != 1 || !strings.HasPrefix((*spawned)[0], "web-") {
		t.Fatalf("spawned = %v, want one child bound to a fresh web- session", *spawned)
	}
	bound := (*spawned)[0]
	if c.currentSessionName != bound || !c.sessionManager.SessionExists(bound) {
		t.Fatalf("terminal not bound to %q (current=%q)", bound, c.currentSessionName)
	}
	saved, err := c.sessionManager.LoadSession(bound)
	if err != nil || len(saved) != 1 || saved[0].Content != "from the terminal" {
		t.Fatalf("the terminal history must be in the shared session: %v %v", saved, err)
	}

	out = captureStdout(t, func() { c.handleWebCommand(ctx, "/web url") })
	if len(*spawned) != 1 || !strings.Contains(out, "60000") {
		t.Fatalf("a second open must reuse the child: spawned=%d out=%s", len(*spawned), out)
	}
	out = captureStdout(t, func() { c.handleWebCommand(ctx, "/web status") })
	if !strings.Contains(out, "4242") || !strings.Contains(out, bound) {
		t.Fatalf("status = %s", out)
	}
	out = captureStdout(t, func() { c.handleWebCommand(ctx, "/web off") })
	if !proc.killed || strings.Contains(out, "60000") {
		t.Fatalf("off must kill the child: killed=%v out=%s", proc.killed, out)
	}
	out = captureStdout(t, func() { c.handleWebCommand(ctx, "/web status") })
	if strings.Contains(out, "4242") {
		t.Fatalf("status after off still shows the child: %s", out)
	}
	if c.shutdownWeb(ctx) {
		t.Fatal("nothing to shut down after off")
	}
	out = captureStdout(t, func() { c.handleWebCommand(ctx, "/web bogus") })
	if !strings.Contains(out, "/web") {
		t.Fatalf("usage expected: %s", out)
	}
}

// An already bound terminal shares that session, and a child that fails or
// never publishes its address leaves nothing running.
func TestWebCommand_BoundSessionAndFailures(t *testing.T) {
	c, spawned, _ := newWebCLI(t)
	ctx := context.Background()
	if err := c.sessionManager.SaveSession("work", c.history); err != nil {
		t.Fatal(err)
	}
	c.currentSessionName = "work"
	_ = captureStdout(t, func() { c.handleWebCommand(ctx, "/web url") })
	if len(*spawned) != 1 || (*spawned)[0] != "work" {
		t.Fatalf("spawned = %v, want the bound session", *spawned)
	}
	c.shutdownWeb(ctx)

	webSpawner = func(context.Context, string, string) (webProcess, error) { return nil, errors.New("no binary") }
	out := captureStdout(t, func() { c.handleWebCommand(ctx, "/web") })
	if !strings.Contains(out, "no binary") || c.web.proc != nil {
		t.Fatalf("spawn failure must be reported and leave no child: %s", out)
	}

	webStartTimeout = 150 * time.Millisecond
	silent := &fakeWebProcess{}
	webSpawner = func(context.Context, string, string) (webProcess, error) { return silent, nil }
	out = captureStdout(t, func() { c.handleWebCommand(ctx, "/web") })
	if !silent.killed || c.web.proc != nil || !strings.Contains(out, webLogPath()) {
		t.Fatalf("a child that never publishes must be killed and the log pointed at: killed=%v out=%s", silent.killed, out)
	}
}

func TestWebCompleter(t *testing.T) {
	c := &ChatCLI{logger: zap.NewNop()}
	all := c.getWebSuggestions(docAt("/web "))
	if len(all) != 4 {
		t.Fatalf("subcommands = %d", len(all))
	}
	some := c.getWebSuggestions(docAt("/web st"))
	if len(some) != 1 || some[0].Text != "status" {
		t.Fatalf("prefix filter = %+v", some)
	}
	if c.getWebSuggestions(docAt("/web status x")) != nil {
		t.Fatal("nothing to complete past the subcommand")
	}
}
