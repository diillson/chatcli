/*
 * ChatCLI - Episode-derived rollup tests.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 */
package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// pastMonday returns the Monday of a week ~n weeks back, safely elapsed.
func pastMonday(n int) time.Time {
	return startOfISOWeek(time.Now().AddDate(0, 0, -7*n))
}

func TestRollup_WeeklyFromEpisodesOnly(t *testing.T) {
	dir := t.TempDir()
	rs := NewRollupStore(dir, testLogger())
	es := NewEpisodeStore(dir, 100, zap.NewNop())
	rs.SetEpisodes(es)

	// A week with NO daily notes (they expired) but with episodes — the old
	// pipeline produced nothing for it.
	monday := pastMonday(10)
	es.Add(Episode{Date: monday.AddDate(0, 0, 1), Project: "chatcli",
		Summary: "Shipped the OAuth refresh fix", Outcome: "merged as PR 1047"})
	es.Add(Episode{Date: monday.AddDate(0, 0, 3),
		Summary: "Decided on the in-memory queue design"})

	written, err := rs.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if written < 1 {
		t.Fatalf("expected digests from episodes alone, wrote %d", written)
	}

	year, week := monday.ISOWeek()
	data, err := os.ReadFile(filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", year, week)))
	if err != nil {
		t.Fatalf("weekly digest missing for episodes-only week: %v", err)
	}
	for _, want := range []string{"OAuth refresh fix", "in-memory queue"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("weekly digest missing %q:\n%s", want, data)
		}
	}

	// The elapsed month must roll from episodes too.
	if monday.Format("2006-01") != time.Now().Format("2006-01") {
		monthly := filepath.Join(dir, "monthly", monday.Format("2006-01")+".md")
		mdata, err := os.ReadFile(monthly)
		if err != nil {
			t.Fatalf("monthly digest missing for episodes-only month: %v", err)
		}
		if !strings.Contains(string(mdata), "OAuth refresh fix") {
			t.Errorf("monthly digest must carry the episode: %s", mdata)
		}
	}
}

func TestRollup_EpisodesLeadDailyNotesFollow(t *testing.T) {
	dir := t.TempDir()
	rs := NewRollupStore(dir, testLogger())
	es := NewEpisodeStore(dir, 100, zap.NewNop())
	rs.SetEpisodes(es)

	monday := pastMonday(8)
	es.Add(Episode{Date: monday, Summary: "Hardened the episode store", Outcome: "quarantine covered"})
	writeDailyNoteAt(t, dir, monday, "leu arquivos de configuração")

	if _, err := rs.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	year, week := monday.ISOWeek()
	data, err := os.ReadFile(filepath.Join(dir, "weekly", fmt.Sprintf("%04d-W%02d.md", year, week)))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	epIdx := strings.Index(body, "Hardened the episode store")
	noteIdx := strings.Index(body, "leu arquivos")
	if epIdx < 0 || noteIdx < 0 {
		t.Fatalf("digest must carry episode AND note content:\n%s", body)
	}
	if epIdx > noteIdx {
		t.Errorf("episodes must lead the digest (deterministic fallback keeps source order):\n%s", body)
	}
}

func TestRollup_NilEpisodesKeepsLegacyBehavior(t *testing.T) {
	dir := t.TempDir()
	rs := NewRollupStore(dir, testLogger()) // SetEpisodes never called

	monday := pastMonday(6)
	writeDailyNoteAt(t, dir, monday, "nota antiga")

	written, err := rs.Run(context.Background(), nil)
	if err != nil || written < 1 {
		t.Fatalf("legacy daily-note rollup must keep working: %d / %v", written, err)
	}
}

func TestManager_RollupsWiredToEpisodes(t *testing.T) {
	m := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	monday := pastMonday(5)
	m.Episodes.Add(Episode{Date: monday, Summary: "Wired the rollup pipeline"})

	written, err := m.RunRollups(context.Background(), nil)
	if err != nil || written < 1 {
		t.Fatalf("manager-wired rollups must digest episodes: %d / %v", written, err)
	}
}
