/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seqEvents(from, to uint64) []Event {
	out := make([]Event, 0, to-from+1)
	for s := from; s <= to; s++ {
		out = append(out, Event{Seq: s, Kind: KindTool, Phase: PhasePoint, ID: "e", Name: strings.Repeat("x", 40)})
	}
	return out
}

func TestSpoolAppendAndReadSince(t *testing.T) {
	root := t.TempDir()
	s, err := OpenSpool(root, Meta{Instance: "inst-a", PID: 42, Surface: "repl"})
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	if err := s.Append(seqEvents(1, 5)...); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, next, err := ReadSince(root, "inst-a", 0, 0)
	if err != nil || len(got) != 5 || next != 5 {
		t.Fatalf("ReadSince(0) = %d events, next %d, err %v", len(got), next, err)
	}
	got, next, _ = ReadSince(root, "inst-a", 3, 0)
	if len(got) != 2 || got[0].Seq != 4 || next != 5 {
		t.Fatalf("ReadSince(3) = %+v next %d", got, next)
	}
	got, next, _ = ReadSince(root, "inst-a", 5, 0)
	if len(got) != 0 || next != 5 {
		t.Fatalf("caught-up read returned %d events, next %d", len(got), next)
	}
	got, next, _ = ReadSince(root, "inst-a", 0, 2)
	if len(got) != 2 || next != 2 {
		t.Fatalf("limit not honored: %d events, next %d", len(got), next)
	}
}

// Rotation must bound the spool and a cursor must keep working across
// segment boundaries, including when its segment was already deleted.
func TestSpoolRotationBoundsDiskAndKeepsCursor(t *testing.T) {
	root := t.TempDir()
	s, err := OpenSpool(root, Meta{Instance: "inst-r"})
	if err != nil {
		t.Fatalf("OpenSpool: %v", err)
	}
	s.SetLimits(400, 3) // ~4 events per segment
	s.SetLimits(0, 0)   // non-positive keeps what was set
	for seq := uint64(1); seq <= 40; seq++ {
		if err := s.Append(seqEvents(seq, seq)...); err != nil {
			t.Fatalf("Append(%d): %v", seq, err)
		}
	}

	segs := listSegments(filepath.Join(root, "inst-r"))
	if len(segs) != 3 {
		t.Fatalf("segments on disk = %d, want 3 (bounded)", len(segs))
	}

	tail, next, _ := ReadSince(root, "inst-r", 38, 0)
	if len(tail) != 2 || tail[0].Seq != 39 || next != 40 {
		t.Fatalf("tail read = %+v next %d", tail, next)
	}
	// Cursor pointing into a deleted segment: resume from the oldest kept.
	all, _, _ := ReadSince(root, "inst-r", 1, 0)
	if len(all) == 0 || all[0].Seq != segs[0].firstSeq || all[len(all)-1].Seq != 40 {
		t.Fatalf("resume from deleted segment: first=%d last=%d, oldest kept=%d", all[0].Seq, all[len(all)-1].Seq, segs[0].firstSeq)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq != all[i-1].Seq+1 {
			t.Fatalf("gap or reorder at %d -> %d", all[i-1].Seq, all[i].Seq)
		}
	}
}

func TestSpoolRejectsPathLikeInstance(t *testing.T) {
	for _, bad := range []string{"", "../escape", "a/b", "/abs"} {
		if _, err := OpenSpool(t.TempDir(), Meta{Instance: bad}); err == nil {
			t.Errorf("OpenSpool accepted instance %q", bad)
		}
	}
}

func TestReadSinceNeverBuildsPathFromInput(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root))
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(outside) }()
	if s, err := OpenSpool(filepath.Dir(outside), Meta{Instance: filepath.Base(outside)}); err == nil {
		_ = s.Append(seqEvents(1, 1)...)
	}
	for _, hostile := range []string{"../" + filepath.Base(outside), "nope", ""} {
		if got, _, err := ReadSince(root, hostile, 0, 0); err == nil || len(got) != 0 {
			t.Errorf("ReadSince(%q) = %d events, err %v; want a not-found error", hostile, len(got), err)
		}
	}
}

// A reader can catch the writer mid-line on the live segment; the torn line
// is skipped this time and read whole on the next poll.
func TestReadSinceSkipsTornLine(t *testing.T) {
	root := t.TempDir()
	s, _ := OpenSpool(root, Meta{Instance: "inst-t"})
	_ = s.Append(seqEvents(1, 2)...)
	seg := listSegments(filepath.Join(root, "inst-t"))[0]
	f, err := os.OpenFile(filepath.Join(root, "inst-t", seg.name), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":3,"kind":"to`)
	_ = f.Close()

	got, next, err := ReadSince(root, "inst-t", 0, 0)
	if err != nil || len(got) != 2 || next != 2 {
		t.Fatalf("got %d events next %d err %v; want the 2 whole lines", len(got), next, err)
	}
}

func TestListInstancesOrdersLiveFirstAndSkipsForeignDirs(t *testing.T) {
	root := t.TempDir()
	dead, _ := OpenSpool(root, Meta{Instance: "dead"})
	_ = dead.Close()
	live, _ := OpenSpool(root, Meta{Instance: "live", Surface: "acp"})
	_ = live.Beat()

	_ = os.MkdirAll(filepath.Join(root, "no-meta"), 0o750)
	_ = os.MkdirAll(filepath.Join(root, "renamed"), 0o750)
	_ = os.WriteFile(filepath.Join(root, "renamed", metaFileName), []byte(`{"instance":"someone-else"}`), 0o600)
	_ = os.WriteFile(filepath.Join(root, "stray-file"), []byte("x"), 0o600)

	got := ListInstances(root)
	if len(got) != 2 || got[0].Instance != "live" || got[1].Instance != "dead" {
		t.Fatalf("ListInstances = %+v", got)
	}
	now := time.Now()
	if !got[0].Alive(now) || got[1].Alive(now) {
		t.Fatalf("liveness wrong: live=%v dead=%v", got[0].Alive(now), got[1].Alive(now))
	}
	if got[0].Surface != "acp" || got[0].StartedAt.IsZero() {
		t.Fatalf("meta not round-tripped: %+v", got[0])
	}
	if ListInstances(filepath.Join(root, "missing")) != nil {
		t.Fatal("missing root must list as empty")
	}
}

func TestPruneInstancesKeepsLiveRecentAndSelf(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, ended bool, age time.Duration) {
		s, err := OpenSpool(root, Meta{Instance: name})
		if err != nil {
			t.Fatal(err)
		}
		s.meta.Ended = ended
		s.meta.Heartbeat = time.Now().Add(-age)
		if err := s.writeMeta(); err != nil {
			t.Fatal(err)
		}
	}
	mk("old-dead", true, 48*time.Hour)
	mk("old-crashed", false, 48*time.Hour) // never marked ended, heartbeat stale
	mk("recent-dead", true, time.Hour)
	mk("live", false, 0)
	mk("self", true, 48*time.Hour)

	if n := PruneInstances(root, 24*time.Hour, "self"); n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	left := map[string]bool{}
	for _, m := range ListInstances(root) {
		left[m.Instance] = true
	}
	if len(left) != 3 || !left["recent-dead"] || !left["live"] || !left["self"] {
		t.Fatalf("survivors = %v", left)
	}
}

func TestLeaseLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pulse") // RenewLease creates it
	now := time.Now()
	if LeaseActive(root, now) {
		t.Fatal("no lease file must read as inactive")
	}
	if err := RenewLease(root, "dash-1", 0); err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if !LeaseActive(root, now) {
		t.Fatal("fresh lease must be active")
	}
	if LeaseActive(root, now.Add(DefaultLeaseTTL+time.Second)) {
		t.Fatal("lease must expire after its TTL")
	}
	ReleaseLease(root)
	if LeaseActive(root, now) {
		t.Fatal("released lease must be inactive")
	}
	_ = os.WriteFile(filepath.Join(root, leaseFileName), []byte("not json"), 0o600)
	if LeaseActive(root, now) {
		t.Fatal("corrupt lease must read as inactive")
	}
}

func TestDefaultRootIsUnderChatcliHome(t *testing.T) {
	root, err := DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if !strings.HasSuffix(root, filepath.Join(".chatcli", "pulse")) {
		t.Fatalf("DefaultRoot = %q", root)
	}
}
