package lessonq

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
)

func newTestJob(t *testing.T, id JobID, task string) LessonJob {
	t.Helper()
	if id == "" {
		id = DeriveJobID(quality.LessonRequest{Task: task, Trigger: "error", Attempt: "a"})
	}
	return LessonJob{
		ID: id,
		Request: quality.LessonRequest{
			Task: task, Attempt: "a", Outcome: "ERROR: x", Trigger: "error",
		},
		EnqueuedAt:    time.Now(),
		NextAttemptAt: time.Now(),
	}
}

func TestWAL_AppendListAck(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWAL(dir, nil, nil)
	if err != nil {
		t.Fatalf("NewWAL: %v", err)
	}
	defer w.Close()

	job := newTestJob(t, "abc123", "task A")
	if err := w.Append(job); err != nil {
		t.Fatalf("Append: %v", err)
	}

	jobs, err := w.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != "abc123" {
		t.Fatalf("expected [abc123], got %+v", jobs)
	}
	if jobs[0].Request.Task != "task A" {
		t.Fatalf("request roundtrip failed; got task %q", jobs[0].Request.Task)
	}

	if err := w.Ack("abc123"); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	jobs, _ = w.List()
	if len(jobs) != 0 {
		t.Fatalf("expected empty after ack; got %+v", jobs)
	}
}

func TestWAL_AckMissingIsNoop(t *testing.T) {
	w, _ := NewWAL(t.TempDir(), nil, nil)
	defer w.Close()
	if err := w.Ack("never-existed"); err != nil {
		t.Fatalf("Ack of missing id should be no-op; got %v", err)
	}
}

func TestWAL_AppendIdempotent(t *testing.T) {
	w, _ := NewWAL(t.TempDir(), nil, nil)
	defer w.Close()

	job := newTestJob(t, "same-id", "x")
	if err := w.Append(job); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := w.Append(job); err != nil {
		t.Fatalf("second append should be idempotent no-op; got %v", err)
	}
	jobs, _ := w.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 record after idempotent second append; got %d", len(jobs))
	}
}

func TestWAL_DetectCorruption(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWAL(dir, nil, nil)
	defer w.Close()

	job := newTestJob(t, "corrupt-me", "task")
	if err := w.Append(job); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Corrupt the payload of the file: flip a byte in the middle.
	path := filepath.Join(dir, "corrupt-me.wal")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) < 20 {
		t.Fatalf("record too small to corrupt: %d bytes", len(data))
	}
	data[15] ^= 0xFF
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// List should detect the CRC mismatch and delete the file.
	jobs, err := w.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("corrupted record should have been filtered out; got %+v", jobs)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt file should have been removed; stat err=%v", statErr)
	}
}

func TestWAL_DetectTruncation(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWAL(dir, nil, nil)
	defer w.Close()

	job := newTestJob(t, "truncated", "task")
	if err := w.Append(job); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := filepath.Join(dir, "truncated.wal")
	data, _ := os.ReadFile(path)
	// Truncate the trailing CRC — simulates a torn write where the
	// final fsync never completed.
	if err := os.WriteFile(path, data[:len(data)-4], 0o644); err != nil {
		t.Fatalf("write trunc: %v", err)
	}
	jobs, _ := w.List()
	if len(jobs) != 0 {
		t.Fatalf("truncated record should be rejected; got %+v", jobs)
	}
}

func TestWAL_BadMagic(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWAL(dir, nil, nil)
	defer w.Close()
	path := filepath.Join(dir, "bad.wal")
	_ = os.WriteFile(path, bytes.Repeat([]byte{0xAA}, 64), 0o644)
	jobs, _ := w.List()
	if len(jobs) != 0 {
		t.Fatalf("bad-magic file must be rejected; got %+v", jobs)
	}
}

func TestWAL_CleansStaleTmpFiles(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWAL(dir, nil, nil)
	defer w.Close()

	// Drop a file that looks like an interrupted Append.
	stale := filepath.Join(dir, "job.tmp.1234.5")
	if err := os.WriteFile(stale, []byte("partial"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}
	if _, err := w.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale tmp should be cleaned; stat err=%v", err)
	}
}

func TestWAL_Update(t *testing.T) {
	w, _ := NewWAL(t.TempDir(), nil, nil)
	defer w.Close()

	job := newTestJob(t, "upd", "x")
	if err := w.Append(job); err != nil {
		t.Fatalf("append: %v", err)
	}
	job.Attempts = 3
	job.LastError = "transient provider timeout"
	if err := w.Update(job); err != nil {
		t.Fatalf("update: %v", err)
	}
	jobs, _ := w.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 record; got %d", len(jobs))
	}
	if jobs[0].Attempts != 3 || jobs[0].LastError != "transient provider timeout" {
		t.Fatalf("update did not persist; got %+v", jobs[0])
	}
}

func TestWAL_Count(t *testing.T) {
	w, _ := NewWAL(t.TempDir(), nil, nil)
	defer w.Close()
	if n := w.Count(); n != 0 {
		t.Fatalf("initial count should be 0; got %d", n)
	}
	for i := 0; i < 5; i++ {
		job := newTestJob(t, JobID("id-"+string(rune('A'+i))), "x")
		_ = w.Append(job)
	}
	if n := w.Count(); n != 5 {
		t.Fatalf("expected 5 after 5 appends; got %d", n)
	}
}

func TestWAL_ClosedRejectsAppend(t *testing.T) {
	w, _ := NewWAL(t.TempDir(), nil, nil)
	w.Close()
	err := w.Append(newTestJob(t, "x", "t"))
	if err != ErrWALClosed {
		t.Fatalf("expected ErrWALClosed; got %v", err)
	}
}

func TestWAL_ReopenRecoversRecords(t *testing.T) {
	dir := t.TempDir()
	w1, _ := NewWAL(dir, nil, nil)
	for i := 0; i < 3; i++ {
		job := newTestJob(t, JobID("rec-"+string(rune('A'+i))), "t")
		if err := w1.Append(job); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	w1.Close()

	w2, _ := NewWAL(dir, nil, nil)
	defer w2.Close()
	jobs, err := w2.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("records should survive reopen; got %d", len(jobs))
	}
}

func TestWAL_ConcurrentAppends(t *testing.T) {
	w, _ := NewWAL(t.TempDir(), nil, nil)
	defer w.Close()

	const N = 100
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			job := newTestJob(t, JobID("concurrent-"+padInt(i, 3)), "t")
			errs <- w.Append(job)
		}(i)
	}
	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent append %d: %v", i, err)
		}
	}
	jobs, _ := w.List()
	if len(jobs) != N {
		t.Fatalf("expected %d records after concurrent appends; got %d", N, len(jobs))
	}
}

func padInt(n, width int) string {
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	for len(s) < width {
		s = "0" + s
	}
	if s == "" {
		s = "0"
	}
	return s
}
