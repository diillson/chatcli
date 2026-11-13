/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A record is read back whole whatever its size. The reader took one Read
// of a buffered reader for "the payload", which returns at most what the
// buffer holds: past about 4 KB the payload came back short and its CRC
// did not match.
func TestWAL_RoundTripsRecordsOfAnySize(t *testing.T) {
	dir := t.TempDir()
	for _, size := range []int{100, 4084, 4085, 5000, 20000, 1 << 20} {
		payload := []byte(strings.Repeat("x", size))
		path := filepath.Join(dir, "raw.wal")
		if err := writeBytesToFile(path, frameWALRecord(payload)); err != nil {
			t.Fatal(err)
		}
		if _, err := readWALRecord(path); err == nil || strings.Contains(err.Error(), "crc") || strings.Contains(err.Error(), "read ") {
			t.Errorf("a %d-byte payload must reach the JSON decoder intact, got %v", size, err)
		}
	}

	wal, err := newSchedulerWAL(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	for _, size := range []int{100, 4084, 4085, 5000, 20000, 1 << 20} {
		j := NewJob("big", Owner{Kind: OwnerUser, ID: "u"}, Schedule{Kind: ScheduleRelative, Relative: time.Minute}, Action{Type: ActionNoop})
		j.ID = JobID("size" + strings.Repeat("0", 3))
		j.Description = strings.Repeat("d", size)
		if err := wal.Write(j); err != nil {
			t.Fatal(err)
		}
		got, err := wal.Read(j.ID)
		if err != nil || got.Description != j.Description {
			t.Errorf("a job with a %d-byte field did not read back: %v", size, err)
		}
	}
}

func bigJobScheduler(t *testing.T, dir string) *Scheduler {
	t.Helper()
	cfg := DefaultConfig()
	cfg.DataDir = dir
	cfg.AuditEnabled = false
	cfg.SnapshotInterval = 0
	cfg.WALGCInterval = 0
	cfg.DaemonAutoConnect = false
	s, err := New(cfg, NewNoopBridge(), SchedulerDeps{}, nil)
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return s
}

// A job with a long prompt survives a restart, and one an earlier chatcli
// already quarantined for being "corrupt" — it was only long — comes back.
func TestSchedulerRestartKeepsAJobWithALongPrompt(t *testing.T) {
	dir := t.TempDir()
	prompt := strings.Repeat("please check the deploy and report. ", 240) // ~8 KB
	s := bigJobScheduler(t, dir)
	mk := func(name string) *Job {
		j := NewJob(name, Owner{Kind: OwnerUser, ID: "u"}, Schedule{Kind: ScheduleRelative, Relative: 10 * time.Hour},
			Action{Type: ActionLLMPrompt, Payload: map[string]any{"prompt": prompt}})
		created, err := s.Enqueue(context.Background(), j)
		if err != nil {
			t.Fatalf("enqueue %s: %v", name, err)
		}
		return created
	}
	kept, lost := mk("long-prompt"), mk("quarantined-by-the-old-reader")
	s.DrainAndShutdown(2 * time.Second)

	// The process crashed rather than shut down: no snapshot was written,
	// and the WAL alone is what the restart has.
	if err := os.Remove(filepath.Join(dir, "snapshot.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// What the old reader did to the second one on some earlier start.
	walDir := filepath.Join(dir, "wal")
	lostPath := filepath.Join(walDir, recordName(lost.ID))
	if err := os.Rename(lostPath, lostPath+".corrupt"); err != nil {
		t.Fatal(err)
	}
	// And a record that really is corrupt stays quarantined.
	junk := filepath.Join(walDir, "junk.wal.corrupt")
	if err := writeBytesToFile(junk, []byte("not a frame")); err != nil {
		t.Fatal(err)
	}

	s2 := bigJobScheduler(t, dir)
	defer s2.DrainAndShutdown(2 * time.Second)
	for name, id := range map[string]JobID{"the long job": kept.ID, "the quarantined job": lost.ID} {
		j, err := s2.Query(id)
		if err != nil {
			t.Fatalf("%s is gone after the restart: %v", name, err)
		}
		if got, _ := j.Action.Payload["prompt"].(string); got != prompt || j.Status.IsTerminal() {
			t.Fatalf("%s came back changed (status %s, %d bytes of prompt)", name, j.Status, len(got))
		}
	}
	if _, err := os.Stat(lostPath); err != nil {
		t.Errorf("the recovered record must be back under its own name: %v", err)
	}
	if _, err := os.Stat(lostPath + ".corrupt"); !os.IsNotExist(err) {
		t.Errorf("the recovered record must not stay quarantined too: %v", err)
	}
	if _, err := os.Stat(junk); err != nil {
		t.Errorf("a record that does not read back stays quarantined, untouched: %v", err)
	}
}
