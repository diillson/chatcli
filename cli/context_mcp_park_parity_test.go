/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

func completeLine(t *testing.T, line string, fn func(prompt.Document) []prompt.Suggest) string {
	t.Helper()
	b := prompt.NewBuffer()
	b.InsertText(line, false, true)
	return strings.Join(suggestTexts(fn(*b.Document())), " ")
}

func TestContextAdapterParityWithCommand(t *testing.T) {
	cli, corpus := newContextTestCLI(t)
	a := &contextPluginAdapter{cli: cli}
	mgr := cli.contextHandler.GetManager()
	src := t.TempDir()
	note := filepath.Join(src, "note.md")
	if err := os.WriteFile(note, []byte("# Note\nfirst version"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := a.CreateTagged("notes", "full", []string{src}, "d", []string{"api", "go"}, false); err != nil {
		t.Fatalf("CreateTagged: %v", err)
	}
	if fc, _ := mgr.GetContextByName("notes"); !reflect.DeepEqual(fc.Tags, []string{"api", "go"}) {
		t.Fatalf("tags = %v", fc.Tags)
	}

	// With embeddings on, a plain attach is upgraded to retrieval; full is
	// the only way to get the whole content.
	mgr.AttachEmbeddingProvider(stubEmbeddingProvider{})
	if out, err := a.Attach("notes", 0, 0); err != nil || !strings.Contains(out, "semantic top-8") {
		t.Fatalf("auto-RAG attach = %q, err %v", out, err)
	}
	if _, err := a.Detach("notes"); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if out, err := a.AttachWith("notes", plugins.ContextAttachOptions{Full: true, Weight: 2}); err != nil || !strings.Contains(out, "full content") {
		t.Fatalf("full attach = %q, err %v", out, err)
	}
	for name, opts := range map[string]plugins.ContextAttachOptions{
		"weight out of range": {Weight: 11},
		"rag with chunks":     {RagTopK: 4, Chunks: []int{1}},
		"full with rag":       {Full: true, RagTopK: 4},
		"chunks, not chunked": {Chunks: []int{1}},
	} {
		if _, err := a.AttachWith("notes", opts); err == nil {
			t.Errorf("%s must be refused, as /context attach refuses it", name)
		}
	}

	if out, err := a.Refresh(context.Background(), "notes"); err != nil || !strings.Contains(out, "up to date") {
		t.Fatalf("Refresh unchanged = %q, err %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(src, "new.md"), []byte("# New\nadded later"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := a.Refresh(context.Background(), "notes"); err != nil || !strings.Contains(out, "1 added") {
		t.Fatalf("Refresh after add = %q, err %v", out, err)
	}
	if _, err := a.Refresh(context.Background(), "ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Refresh of a missing context: %v", err)
	}

	t.Cleanup(cli.contextHandler.Close)
	if out, _ := a.Watch(""); !strings.Contains(out, "No context") {
		t.Fatalf("empty watch list = %q", out)
	}
	if out, err := a.Watch("notes"); err != nil || !strings.Contains(out, src) {
		t.Fatalf("Watch = %q, err %v", out, err)
	}
	if out, _ := a.Watch(""); !strings.Contains(out, "notes") {
		t.Fatalf("watch list = %q", out)
	}
	if _, err := a.Unwatch("notes"); err != nil {
		t.Fatalf("Unwatch: %v", err)
	}
	if _, err := a.Unwatch("notes"); err == nil {
		t.Fatal("Unwatch twice must report it is not watched")
	}

	// Same merge policy as /context merge: the merged tag is the visible part.
	for _, n := range []string{"left", "right"} {
		if _, err := a.Create(n, "knowledge", []string{corpus}, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Merge("both", []string{"left", "right"}, ""); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if fc, _ := mgr.GetContextByName("both"); !reflect.DeepEqual(fc.Tags, []string{"merged"}) {
		t.Fatalf("merged tags = %v", fc.Tags)
	}
}

func TestContextCommandReportsBadInput(t *testing.T) {
	cli, corpus := newContextTestCLI(t)
	h := cli.contextHandler
	for _, bad := range []string{
		"/context list --since yesterday", "/context list --before 2026/01/02",
		"/context list --tga api", "/context list --tag", "/context list --mode --tag x",
		"/context list --mode bogus", `/context create x --description "never closed ` + corpus,
	} {
		if err := h.HandleContextCommand(context.Background(), "s", bad); err == nil {
			t.Errorf("%q must be reported", bad)
		}
	}
	f, err := parseContextListFilter([]string{"--tag", "api", "--mode", "FULL", "--name", "x", "--since", "2026-01-02", "--before", "2026-02-03"})
	if err != nil || f.Mode != "full" || f.NamePattern != "x" || f.CreatedAfter == nil || f.CreatedBefore == nil || len(f.Tags) != 1 {
		t.Fatalf("filter = %+v, err %v", f, err)
	}
	for _, n := range []string{"0", "-1"} {
		if _, err := parseAttachFlags([]string{"x", "--rag", n}); err == nil {
			t.Errorf("--rag %s must be rejected", n)
		}
	}

	cmd := `/context create quoted ` + corpus + ` --mode knowledge --description "two words"`
	if err := h.HandleContextCommand(context.Background(), "s", cmd); err != nil {
		t.Fatalf("create with a quoted description: %v", err)
	}
	if fc, _ := h.GetManager().GetContextByName("quoted"); fc == nil || fc.Description != "two words" {
		t.Fatalf("description = %+v", fc)
	}
}

func TestContextAndParkCompleterParity(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli, corpus := newContextTestCLI(t)
	if _, err := (&contextPluginAdapter{cli: cli}).Create("docs", "knowledge", []string{corpus}, "", false); err != nil {
		t.Fatal(err)
	}
	if got := completeLine(t, "/context list --", cli.getContextSuggestions); got != "--tag --mode --name --since --before" {
		t.Errorf("list flags = %q", got)
	}
	if got := completeLine(t, "/context list --mode ", cli.getContextSuggestions); !strings.Contains(got, "knowledge") {
		t.Errorf("list --mode values = %q", got)
	}
	for _, verb := range []string{"refresh", "watch", "unwatch", "export"} {
		if got := completeLine(t, "/context "+verb+" ", cli.getContextSuggestions); !strings.Contains(got, "docs") {
			t.Errorf("/context %s must suggest context names, got %q", verb, got)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "out.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := completeLine(t, "/context export docs "+dir+string(filepath.Separator), cli.getContextSuggestions); !strings.Contains(got, "out.json") {
		t.Errorf("export must complete the path, got %q", got)
	}

	snap := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	if got := completeLine(t, "/parked ", cli.getParkedSuggestions); !strings.Contains(got, "wake") || !strings.Contains(got, "prune") {
		t.Errorf("/parked verbs = %q", got)
	}
	if got := completeLine(t, "/parked wake ", cli.getParkedSuggestions); !strings.Contains(got, snap.Token[:12]) {
		t.Errorf("/parked wake must suggest tokens, got %q", got)
	}
}

func TestMCPLoginAdapterRestartAndTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := withConfiguredServers(t, "srv1")
	cli.mcpCtx = context.Background()
	a := &mcpLoginAdapter{cli: cli}

	if _, err := a.Restart("ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("a server outside the config must be refused: %v", err)
	}
	// The user's /mcp stop wins over the model's restart, until /mcp start.
	if err := cli.mcpSetRunning("srv1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Restart("srv1"); err == nil || !strings.Contains(err.Error(), "/mcp start srv1") {
		t.Fatalf("a user-stopped server must stay stopped: %v", err)
	}
	cli.mcpUserStops.Delete("srv1")
	// "true" exits at once: the failed start is reported, not papered over.
	if out, err := a.Restart("srv1"); err == nil {
		t.Fatalf("restart of a server that cannot start must fail, got %q", out)
	}
	if _, err := a.Tools("ghost"); err == nil {
		t.Fatal("tools of an unknown server must fail")
	}
	if out, err := a.Tools("srv1"); err != nil || strings.TrimSpace(out) == "" {
		t.Fatalf("Tools = %q, err %v", out, err)
	}
}

func TestParkHumanAndToolHalves(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	cli.schedulerBridge = &schedulerBridge{cli: cli}
	adapter := newLiveParkAdapter(cli)
	var _ plugins.ParkManager = adapter
	woken := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if err := woken.Save(); err != nil {
		t.Fatal(err)
	}

	// /parked wake: the agent is resumed as cancelled, with the reason.
	ch := cli.registerParkWaiter(woken.Token)
	out := captureStdout(t, func() { cli.handleParkedCommand("/parked wake " + woken.Token[:8] + " CI is green") })
	select {
	case w := <-ch:
		if w.Outcome != parkCancelOutcome || w.Detail != "CI is green" {
			t.Fatalf("wake = %+v", w)
		}
	default:
		t.Fatalf("/parked wake did not wake the agent: %s", out)
	}
	if _, err := park.Load(woken.Token); err != nil {
		t.Fatal("wake must keep the snapshot for the resume")
	}
	for _, bad := range []string{"/parked wake", "/parked wake zzzzzzzz"} {
		if got := captureStdout(t, func() { cli.handleParkedCommand(bad) }); strings.Contains(got, "✓") {
			t.Errorf("%q must not report success: %s", bad, got)
		}
	}

	// @park resume: same wake, outcome manual.
	if _, err := adapter.ResumePark(woken.Token); err != nil {
		t.Fatalf("ResumePark: %v", err)
	}
	if w := <-ch; w.Outcome != parkManualOutcome {
		t.Fatalf("resume outcome = %q", w.Outcome)
	}

	// @park discard: gone without a resume.
	if _, err := adapter.DiscardPark(woken.Token); err != nil {
		t.Fatalf("DiscardPark: %v", err)
	}
	if _, err := park.Load(woken.Token); err == nil {
		t.Fatal("discard must delete the snapshot")
	}
	if _, err := adapter.DiscardPark(woken.Token); err == nil {
		t.Fatal("discarding twice must fail")
	}
}

func TestSplitContextArgsQuotesAndWhitespace(t *testing.T) {
	for in, want := range map[string][]string{
		`/context create docs "C:\dir one\" --description "my desc"`: {"/context", "create", "docs", `C:\dir one\`, "--description", "my desc"},
		`/context create docs "C:\My Docs\" --mode full`:             {"/context", "create", "docs", `C:\My Docs\`, "--mode", "full"},
		`create d C:\a\b "C:\My Docs\a" ""`:                          {"create", "d", `C:\a\b`, `C:\My Docs\a`, ""},
		`-d 'say "hi" twice' don't 'tis --tags "a, b"`:               {"-d", `say "hi" twice`, "don't", "'tis", "--tags", "a, b"},
		"/context\u00a0list\u00a0--tag\tx":                           {"/context", "list", "--tag", "x"},
	} {
		if got, err := splitContextArgs(in); err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("split(%q) = %q, err %v; want %q", in, got, err, want)
		}
	}
	if _, err := splitContextArgs(`create d --description "never closed`); err == nil {
		t.Error("an unclosed double quote must be an error")
	}
}

// A discard from another turn must end the turn blocked on the park.
func TestParkDiscardEndsInTurnWaiter(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop(), unattended: true}
	snap := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	ch := cli.registerParkWaiter(snap.Token)
	cli.parkOutcomes = map[string]parkOutcome{snap.Token: {Outcome: "elapsed"}}
	if _, err := newLiveParkAdapter(cli).DiscardPark(snap.Token); err != nil {
		t.Fatalf("DiscardPark: %v", err)
	}
	select {
	case w := <-ch:
		if w.Outcome != parkDiscardOutcome {
			t.Fatalf("outcome = %q", w.Outcome)
		}
	default:
		t.Fatal("the in-turn waiter was left blocked")
	}
	if cli.parkWaiter(snap.Token) != nil || len(cli.ownedParkTokens()) != 0 || len(cli.parkOutcomes) != 0 {
		t.Fatal("a discarded park must leave no waiter, no owned token and no outcome")
	}
}

func TestParkDiscardRefusedForLiveRemoteOwner(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := &ChatCLI{logger: zap.NewNop()}
	snap := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	setRemoteParkProvider(&remoteParkProvider{owner: func(string) (remoteParkOwner, bool) {
		return remoteParkOwner{Instance: "gw-1", Origin: "gateway"}, true
	}})
	t.Cleanup(func() { setRemoteParkProvider(nil) })
	if _, err := newLiveParkAdapter(cli).DiscardPark(snap.Token); err == nil || !strings.Contains(err.Error(), "another live process") {
		t.Fatalf("a park with a live remote owner must be refused: %v", err)
	}
	if _, err := park.Load(snap.Token); err != nil {
		t.Fatal("a refused discard must keep the snapshot")
	}
}

func TestMCPUserStopMarkHasOneMeaning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := withConfiguredServers(t, "srv1", "srv2")
	cli.mcpCtx = context.Background()
	cli.costTracker = NewCostTrackerAt(t.TempDir())
	a := &mcpLoginAdapter{cli: cli}
	stop := func(name string) { _ = cli.mcpSetRunning(name, false) }

	// The model's login is a start too, and gets the same refusal.
	stop("srv1")
	if _, err := a.Login(context.Background(), "srv1"); err == nil || !strings.Contains(err.Error(), "/mcp start srv1") {
		t.Fatalf("tool login on a user-stopped server: %v", err)
	}
	// The user's own /mcp login ends the stop, whatever its result.
	captureStdout(t, func() { cli.mcpLogin(context.Background(), "srv1") })
	if cli.mcpStoppedByUser("srv1") {
		t.Fatal("/mcp login must clear the user's stop")
	}
	// A reload that restarted or removed a server drops its mark.
	stop("srv1")
	stop("srv2")
	cli.mcpForgetUserStops(mcp.ReloadDiff{Updated: []string{"srv1"}, Stopped: []string{"srv2"}})
	if cli.mcpStoppedByUser("srv1") || cli.mcpStoppedByUser("srv2") {
		t.Fatal("a reload must clear the marks of the servers it touched")
	}
	// A mark on a server that is no longer configured does not survive.
	cli.mcpUserStops.Store("gone", true)
	stopped := cli.mcpStoppedByUser("gone")
	if _, kept := cli.mcpUserStops.Load("gone"); stopped || kept {
		t.Fatal("a mark for a server outside the config must be dropped")
	}

	// A restart whose start half fails still changed the tool catalog.
	if _, err := a.Restart("srv1"); err == nil {
		t.Fatal("the server cannot start in this fixture")
	}
	if !cli.costTracker.cache.rebuildPending {
		t.Fatal("a failed restart must still note the prefix change")
	}
}

func TestMCPRestartSerializedPerServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cli := withConfiguredServers(t, "srv1", "srv2")
	cli.mcpCtx = context.Background()
	held := cli.mcpServerLock("srv1")
	held.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cli.mcpRestartServer("srv1")
	}()
	select {
	case <-done:
		t.Fatal("a restart ran while the same server was being stopped/started")
	case <-time.After(100 * time.Millisecond):
	}
	held.Unlock()
	<-done
	var wg sync.WaitGroup // /mcp restart swaps mcpCtx while tool calls read it
	for range 4 {
		wg.Go(func() { _ = cli.mcpRestartServer("srv1") })
		wg.Go(func() {
			cli.mcpCtxMu.Lock()
			cli.mcpCtx = context.Background()
			cli.mcpCtxMu.Unlock()
		})
	}
	wg.Wait()
}
