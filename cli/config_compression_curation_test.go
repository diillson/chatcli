/*
 * ChatCLI - coverage for CCR curation display + commands.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/compress"
)

func TestFormatAgeCompact(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := formatAgeCompact(c.d); got != c.want {
			t.Errorf("formatAgeCompact(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestCCRStoreSummary(t *testing.T) {
	// Clean footprint: no stale/oldest suffixes.
	bare := ccrStoreSummary(compress.StoreStats{Entries: 3, TotalBytes: 1200})
	if strings.Contains(bare, "·") {
		t.Errorf("clean store summary should carry no suffix: %q", bare)
	}
	// Curation visibility: stale count + oldest age appended.
	rich := ccrStoreSummary(compress.StoreStats{
		Entries: 5, TotalBytes: 2048, StaleEntries: 2, OldestAge: 49 * time.Hour,
	})
	for _, want := range []string{"5 entries", "stale", "oldest 2d"} {
		if !strings.Contains(rich, want) {
			t.Errorf("rich summary %q missing %q", rich, want)
		}
	}
}

func TestPruneCompressionStore(t *testing.T) {
	// nil layer → unavailable branch (must not panic).
	(&ChatCLI{}).pruneCompressionStore()

	// A real (clean) store exercises the prune + report path end to end.
	dir := t.TempDir()
	ds, err := compress.NewDiskStore(dir, 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cli := &ChatCLI{compressionLayer: compress.NewLayer(
		compress.Config{Mode: compress.ModeLossyWithCCR, Store: ds})}
	cli.pruneCompressionStore()
}
