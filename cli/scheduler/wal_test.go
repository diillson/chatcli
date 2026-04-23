package scheduler

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWAL_WriteReadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	wal, err := newSchedulerWAL(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()

	j := NewJob("test", Owner{Kind: OwnerUser, ID: "u"}, Schedule{Kind: ScheduleRelative, Relative: time.Minute}, Action{Type: ActionNoop})
	j.ID = "abc123"
	if err := wal.Write(j); err != nil {
		t.Fatal(err)
	}
	got, err := wal.Read(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "test" || got.Owner.ID != "u" || got.Action.Type != ActionNoop {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestWAL_List_SortedByCreatedAt(t *testing.T) {
	dir := t.TempDir()
	wal, err := newSchedulerWAL(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()

	now := time.Now()
	for i, dt := range []time.Duration{2 * time.Minute, 1 * time.Minute, 3 * time.Minute} {
		j := NewJob("t", Owner{Kind: OwnerUser, ID: "u"},
			Schedule{Kind: ScheduleRelative, Relative: time.Minute},
			Action{Type: ActionNoop})
		j.ID = JobID([]byte{byte('A' + i)})
		j.CreatedAt = now.Add(dt)
		if err := wal.Write(j); err != nil {
			t.Fatal(err)
		}
	}
	list, err := wal.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("list len %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if !list[i-1].CreatedAt.Before(list[i].CreatedAt) {
			t.Errorf("not sorted: %v then %v", list[i-1].CreatedAt, list[i].CreatedAt)
		}
	}
}

func TestWAL_Ack_Idempotent(t *testing.T) {
	dir := t.TempDir()
	wal, _ := newSchedulerWAL(dir, nil)
	defer wal.Close()

	j := NewJob("x", Owner{Kind: OwnerUser, ID: "u"},
		Schedule{Kind: ScheduleRelative, Relative: time.Minute},
		Action{Type: ActionNoop})
	j.ID = "to-ack"
	_ = wal.Write(j)
	if err := wal.Ack(j.ID); err != nil {
		t.Fatal(err)
	}
	// Double ack must not error.
	if err := wal.Ack(j.ID); err != nil {
		t.Errorf("double ack: %v", err)
	}
	if _, err := wal.Read(j.ID); err == nil {
		t.Error("expected error reading acked record")
	}
}

func TestWAL_CorruptQuarantined(t *testing.T) {
	dir := t.TempDir()
	// Write a bogus .wal file.
	path := filepath.Join(dir, "bad.wal")
	if err := writeBytesToFile(path, []byte("not a frame")); err != nil {
		t.Fatal(err)
	}
	wal, _ := newSchedulerWAL(dir, nil)
	list, err := wal.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("corrupt record should not appear in list: %v", list)
	}
	// Quarantine rename should have happened.
	matches, _ := filepath.Glob(filepath.Join(dir, "*.corrupt"))
	if len(matches) != 1 {
		t.Errorf("expected 1 .corrupt file, got %d", len(matches))
	}
}

// writeBytesToFile is a tiny test helper to avoid importing os in two
// places in this file.
func writeBytesToFile(path string, data []byte) error {
	return writeAndSyncWAL(path, data)
}
