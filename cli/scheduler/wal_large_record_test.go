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

// A job with a long prompt survives a restart from the WAL alone.
func TestSchedulerRestartKeepsAJobWithALongPrompt(t *testing.T) {
	dir := t.TempDir()
	prompt := strings.Repeat("please check the deploy and report. ", 240) // ~8 KB
	s := bigJobScheduler(t, dir)
	j := NewJob("long-prompt", Owner{Kind: OwnerUser, ID: "u"}, Schedule{Kind: ScheduleRelative, Relative: 10 * time.Hour},
		Action{Type: ActionLLMPrompt, Payload: map[string]any{"prompt": prompt}})
	kept, err := s.Enqueue(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	s.DrainAndShutdown(2 * time.Second)
	// The process crashed rather than shut down: no snapshot was written,
	// and the WAL alone is what the restart has.
	if err := os.Remove(filepath.Join(dir, "snapshot.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s2 := bigJobScheduler(t, dir)
	defer s2.DrainAndShutdown(2 * time.Second)
	got, err := s2.Query(kept.ID)
	if err != nil {
		t.Fatalf("the long job is gone after the restart: %v", err)
	}
	if p, _ := got.Action.Payload["prompt"].(string); p != prompt || got.Status.IsTerminal() {
		t.Fatalf("the long job came back changed (status %s, %d bytes of prompt)", got.Status, len(p))
	}
}

// A quarantined record has no effect at all, as before. Released versions
// quarantined every record over ~4 KB while the job itself lived on in the
// snapshot: the ".wal.corrupt" copy is a STALE picture of a job that was
// later cancelled, or ran. Once that job is finished and its record acked,
// the stale copy is all that is left on disk — reading it back would fire
// a cancelled job again. So it is never read: not on replay, not by the
// read-only view of the store.
func TestQuarantinedRecordsAreNeverBroughtBack(t *testing.T) {
	dir := t.TempDir()
	prompt := strings.Repeat("please check the deploy and report. ", 240) // ~8 KB
	s := bigJobScheduler(t, dir)
	owner := Owner{Kind: OwnerUser, ID: "u"}
	j := NewJob("cancelled-long-ago", owner, Schedule{Kind: ScheduleRelative, Relative: 10 * time.Hour},
		Action{Type: ActionLLMPrompt, Payload: map[string]any{"prompt": prompt}})
	job, err := s.Enqueue(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	walDir := filepath.Join(dir, "wal")
	record := filepath.Join(walDir, recordName(job.ID))
	pending, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	// The old reader quarantined the record while the job was pending...
	stale := record + ".corrupt"
	if err := os.WriteFile(stale, pending, 0o600); err != nil {
		t.Fatal(err)
	}
	// ...the user cancelled the job, and after its TTL the record was acked.
	if err := s.Cancel(job.ID, "not needed", owner); err != nil {
		t.Fatal(err)
	}
	if err := s.wal.Ack(job.ID); err != nil {
		t.Fatal(err)
	}
	s.DrainAndShutdown(2 * time.Second)
	if err := os.Remove(filepath.Join(dir, "snapshot.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	s2 := bigJobScheduler(t, dir)
	defer s2.DrainAndShutdown(2 * time.Second)
	if got, err := s2.Query(job.ID); err == nil {
		t.Fatalf("a job the user cancelled came back as %s from its quarantined copy", got.Status)
	}
	if _, ok := s2.FindByName("cancelled-long-ago"); ok {
		t.Fatal("the cancelled job is live again")
	}
	if jobs, err := s2.StoredJobs(nil); err != nil || len(jobs) != 0 {
		t.Fatalf("the read-only view of the store must not count a quarantined file: %d job(s) %v", len(jobs), err)
	}
	if _, err := s2.StoredJob(job.ID); err == nil {
		t.Fatal("a quarantined file must not answer for its job id")
	}
	after, err := os.ReadFile(stale)
	if err != nil || string(after) != string(pending) {
		t.Fatalf("the quarantined file must be left exactly as it was: %v", err)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("nothing may be restored under the record's name: %v", err)
	}
}
