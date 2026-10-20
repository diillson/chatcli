/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// as_of moves "3 months ago" for one call: anchored three months ahead, the
// window lands on yesterday's episode instead of the old one.
func TestMemoryAdapter_TimelineAt_AnchorsRelativeExpressions(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	seedEpisodes(t, cli)
	a := &memoryPluginAdapter{cli: cli}
	asOf := time.Now().AddDate(0, 3, 0).Format("2006-01-02")

	out, err := a.TimelineAt("", "", "", "o que fizemos há 3 meses", 0, asOf)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if !strings.Contains(out, "tool tag alias") || strings.Contains(out, "OAuth refresh loop") {
		t.Errorf("anchored window must hold yesterday's episode only, got: %s", out)
	}
	// The anchor is per call: the next call without it is back on today.
	if out, _ := a.Timeline("", "", "", "o que fizemos há 3 meses", 0); !strings.Contains(out, "OAuth refresh loop") {
		t.Errorf("an unanchored call must use the current date, got: %s", out)
	}
	if _, err := a.TimelineAt("", "", "", "x", 0, "next tuesday"); err == nil {
		t.Error("an unparseable as_of must be an error, not a silent fallback")
	}
}

func TestMemoryAdapter_RecallAt_AnchorsTheTimelineSlice(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	seedEpisodes(t, cli)
	a := &memoryPluginAdapter{cli: cli}
	out, err := a.RecallAt("o que fizemos há 3 meses", time.Now().AddDate(0, 3, 0).Format("2006-01-02"))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(out, "tool tag alias") || strings.Contains(out, "OAuth refresh loop") {
		t.Errorf("anchored recall must slice around the anchor, got: %s", out)
	}
}

func seedDatedSessions(t *testing.T) *ChatCLI {
	t.Helper()
	c := &ChatCLI{sessionManager: newTestSessionManager(t), logger: zap.NewNop()}
	for name, when := range map[string]time.Time{
		"airbnb-booking": time.Date(2022, 12, 28, 10, 0, 0, 0, time.Local),
		"ibotta-signup":  time.Date(2023, 5, 9, 10, 0, 0, 0, time.Local),
		"gym-plan":       time.Date(2023, 5, 27, 10, 0, 0, 0, time.Local),
	} {
		if err := c.sessionManager.SaveSessionV2(name, &SessionData{Version: 2, ChatHistory: []models.Message{
			{Role: "user", Content: "notes about " + strings.ReplaceAll(name, "-", " ") + " and the app"},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(c.sessionManager.getSessionPath(name), when, when); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestSessionAdapter_SearchWindow(t *testing.T) {
	a := &sessionPluginAdapter{cli: seedDatedSessions(t)}
	ctx := context.Background()

	// Every hit carries its save date and its age from the anchor.
	out, err := a.SearchWindow(ctx, "ibotta app", 2, "", "", "2023-05-30 10:00")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ibotta-signup") || !strings.Contains(out, "2023-05-09") || !strings.Contains(out, "21d ago") {
		t.Errorf("hit must show its date and its age from as_of: %s", out)
	}

	// A to-only window used to be ignored; it must bound the results.
	out, err = a.SearchWindow(ctx, "app", 2, "", "2023-01", "2023-05-30")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "airbnb-booking") || strings.Contains(out, "gym-plan") || strings.Contains(out, "ibotta-signup") {
		t.Errorf("to=2023-01 must keep only the December session: %s", out)
	}

	// No query: list the window, with "3 weeks ago" counted from as_of.
	out, err = a.SearchWindow(ctx, "", 2, "3 weeks ago", "", "2023-05-30")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "gym-plan") || strings.Contains(out, "airbnb-booking") {
		t.Errorf("from=3 weeks ago as of 2023-05-30: %s", out)
	}

	// A time expression in the query sets the window.
	out, err = a.SearchWindow(ctx, "app há 3 semanas", 2, "", "", "2023-05-30")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ibotta-signup") || strings.Contains(out, "airbnb-booking") {
		t.Errorf("query window: %s", out)
	}

	for _, bad := range [][3]string{{"someday", "", ""}, {"", "soon", ""}, {"", "", "tomorrowish"}} {
		if _, err := a.SearchWindow(ctx, "app", 2, bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("bad bound %v must be an error", bad)
		}
	}
}

// Sessions saved by earlier versions are found by the dated search with no
// migration: the format did not change, and the date is the file's own
// modification time. Covers the legacy plain-array format and a v2 file
// written before titles existed.
func TestSessionAdapter_SearchWindow_FindsSessionsSavedBeforeTheChange(t *testing.T) {
	sm := newTestSessionManager(t)
	c := &ChatCLI{sessionManager: sm, logger: zap.NewNop()}
	files := map[string]struct {
		body string
		when time.Time
	}{
		"legacy-array": {`[{"role":"user","content":"planning the kyoto trip itinerary"}]`, time.Date(2023, 3, 2, 9, 0, 0, 0, time.Local)},
		"v2-untitled":  {`{"version":2,"chat_history":[{"role":"user","content":"kyoto hotel booked near gion"}]}`, time.Date(2023, 4, 20, 9, 0, 0, 0, time.Local)},
	}
	for name, f := range files {
		p := sm.getSessionPath(name)
		if err := os.WriteFile(p, []byte(f.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.when, f.when); err != nil {
			t.Fatal(err)
		}
	}
	a := &sessionPluginAdapter{cli: c}

	out, err := a.SearchWindow(context.Background(), "kyoto", 2, "", "", "2023-05-01 09:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"legacy-array", "2023-03-02", "60d ago", "v2-untitled", "2023-04-20", "11d ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in: %s", want, out)
		}
	}
	// The plain search still works on them, now with dates too.
	if out, err := a.Search(context.Background(), "kyoto", 2); err != nil || !strings.Contains(out, "2023-03-02") {
		t.Errorf("plain search: %v %s", err, out)
	}
	// And a window still filters them.
	if out, _ := a.SearchWindow(context.Background(), "kyoto", 2, "2023-04", "", "2023-05-01"); strings.Contains(out, "legacy-array") || !strings.Contains(out, "v2-untitled") {
		t.Errorf("window over old files: %s", out)
	}
}
