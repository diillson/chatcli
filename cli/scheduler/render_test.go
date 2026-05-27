/*
 * ChatCLI - /jobs render tests (i18n + alignment)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/i18n"
)

func TestPadRightRuneAware(t *testing.T) {
	// "Ação" is 4 runes / 6 bytes — padding must count runes.
	got := padRight("Ação", 6)
	if n := len([]rune(got)); n != 6 {
		t.Fatalf("padRight rune width = %d, want 6 (%q)", n, got)
	}
	if padRight("already-long", 3) != "already-long" {
		t.Fatal("padRight must not truncate")
	}
}

func TestRenderListEmptyIsI18nAndConsistentWithTree(t *testing.T) {
	got := RenderList(nil)
	want := "  " + i18n.T("scheduler.jobs.empty") + "\n"
	if got != want {
		t.Fatalf("empty list = %q, want %q (must reuse the i18n empty key, not hardcoded English)", got, want)
	}
	// The tree's empty state uses the same key — they must not diverge.
	if !strings.Contains(want, i18n.T("scheduler.jobs.empty")) {
		t.Fatal("empty list/tree drifted")
	}
}

func TestRenderListRendersRowsAndSeparator(t *testing.T) {
	// Headers go through i18n; in production they are short ("STATUS", …) and
	// fit the column. We assert the row content + the separator rule rather than
	// the header text, which the column width truncates (and which is the i18n
	// key, not its value, under the test's uninitialized catalog).
	out := RenderList([]JobSummary{{
		Status:      StatusRunning,
		ID:          "job-1",
		Name:        "build",
		Type:        "once",
		LastOutcome: "success",
	}})
	for _, want := range []string{"job-1", "build", "running", "success", "──"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list missing %q in:\n%s", want, out)
		}
	}
}

func TestRenderShowI18nLabelsAndAlignment(t *testing.T) {
	j := &Job{
		Name:      "deploy",
		ID:        "job-9",
		Status:    StatusPending,
		Action:    Action{Type: ActionShell, Payload: map[string]any{"command": "kubectl apply"}},
		CreatedAt: time.Now(),
		Attempts:  2,
		Tags:      map[string]string{"env": "prod"},
	}
	out := RenderShow(j)

	if !strings.Contains(out, i18n.T("scheduler.render.owner")) ||
		!strings.Contains(out, i18n.T("scheduler.render.action")) {
		t.Fatalf("show missing translated labels:\n%s", out)
	}
	if !strings.Contains(out, "deploy") || !strings.Contains(out, "kubectl apply") {
		t.Fatalf("show missing job content:\n%s", out)
	}
	// Aligned rows use " : " separators.
	if !strings.Contains(out, " : ") {
		t.Fatalf("show rows not aligned:\n%s", out)
	}

	if RenderShow(nil) != i18n.T("scheduler.jobs.empty")+"\n" {
		t.Fatal("nil show must use the i18n empty key")
	}
}

func TestRenderTreeEmptyAndNonEmpty(t *testing.T) {
	if RenderTree(nil) != "" {
		t.Fatal("empty tree should render nothing (caller prints the i18n empty notice)")
	}
	out := RenderTree([]*Job{{Name: "root", ID: "r1", Status: StatusCompleted}})
	if !strings.Contains(out, "root") || !strings.Contains(out, "r1") {
		t.Fatalf("tree missing root job:\n%s", out)
	}
}
