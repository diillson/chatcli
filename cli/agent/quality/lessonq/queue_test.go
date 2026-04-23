package lessonq

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
)

func makeJob(id string, task string, nextAt time.Time) LessonJob {
	return LessonJob{
		ID:            JobID(id),
		Request:       quality.LessonRequest{Task: task, Trigger: "error", Attempt: "a"},
		EnqueuedAt:    time.Now(),
		NextAttemptAt: nextAt,
	}
}

func TestQueue_BasicEnqueueDequeue(t *testing.T) {
	q := NewQueue(10, OverflowBlock, time.Second, nil)
	defer q.Close()

	now := time.Now()
	if err := q.Enqueue(context.Background(), makeJob("a", "t1", now)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	job, ok, err := q.Dequeue(ctx)
	if err != nil || !ok {
		t.Fatalf("dequeue: err=%v ok=%v", err, ok)
	}
	if job.ID != "a" {
		t.Fatalf("wrong id: %s", job.ID)
	}
}

func TestQueue_DeduplicatesByID(t *testing.T) {
	q := NewQueue(10, OverflowBlock, time.Second, nil)
	defer q.Close()

	job := makeJob("same", "x", time.Now())
	if err := q.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("first enqueue: %v", err)
	}
	err := q.Enqueue(context.Background(), job)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second enqueue should be ErrDuplicate; got %v", err)
	}
	if q.Len() != 1 {
		t.Fatalf("len should stay at 1; got %d", q.Len())
	}
}

func TestQueue_BackPressureBlocks(t *testing.T) {
	q := NewQueue(2, OverflowBlock, 100*time.Millisecond, nil)
	defer q.Close()

	_ = q.Enqueue(context.Background(), makeJob("a", "t", time.Now()))
	_ = q.Enqueue(context.Background(), makeJob("b", "t", time.Now()))

	start := time.Now()
	err := q.Enqueue(context.Background(), makeJob("c", "t", time.Now()))
	elapsed := time.Since(start)

	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull; got %v", err)
	}
	if elapsed < 90*time.Millisecond {
		t.Fatalf("expected to block ~100ms; elapsed %s", elapsed)
	}
}

func TestQueue_DropOldestPolicy(t *testing.T) {
	q := NewQueue(2, OverflowDropOldest, time.Second, nil)
	defer q.Close()

	a := makeJob("a", "t1", time.Now())
	a.EnqueuedAt = time.Now().Add(-time.Minute) // oldest
	_ = q.Enqueue(context.Background(), a)
	_ = q.Enqueue(context.Background(), makeJob("b", "t2", time.Now()))
	if err := q.Enqueue(context.Background(), makeJob("c", "t3", time.Now())); err != nil {
		t.Fatalf("drop-oldest enqueue should succeed; got %v", err)
	}
	// 'a' is the oldest — should have been evicted.
	snap := q.Snapshot()
	for _, j := range snap {
		if j.ID == "a" {
			t.Fatalf("oldest job should have been dropped; still present: %+v", snap)
		}
	}
}

func TestQueue_RespectsNextAttemptAt(t *testing.T) {
	q := NewQueue(10, OverflowBlock, time.Second, nil)
	defer q.Close()

	future := time.Now().Add(50 * time.Millisecond)
	_ = q.Enqueue(context.Background(), makeJob("later", "t", future))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	job, ok, err := q.Dequeue(ctx)
	elapsed := time.Since(start)
	if err != nil || !ok {
		t.Fatalf("dequeue: err=%v ok=%v", err, ok)
	}
	if job.ID != "later" {
		t.Fatalf("wrong id: %s", job.ID)
	}
	if elapsed < 40*time.Millisecond {
		t.Fatalf("should have waited ~50ms; got %s", elapsed)
	}
}

func TestQueue_DequeueReturnsEarliest(t *testing.T) {
	q := NewQueue(10, OverflowBlock, time.Second, nil)
	defer q.Close()

	now := time.Now()
	_ = q.Enqueue(context.Background(), makeJob("c", "t", now.Add(100*time.Millisecond)))
	_ = q.Enqueue(context.Background(), makeJob("a", "t", now.Add(-10*time.Millisecond)))
	_ = q.Enqueue(context.Background(), makeJob("b", "t", now.Add(50*time.Millisecond)))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	j1, _, _ := q.Dequeue(ctx)
	j2, _, _ := q.Dequeue(ctx)
	j3, _, _ := q.Dequeue(ctx)
	if j1.ID != "a" || j2.ID != "b" || j3.ID != "c" {
		t.Fatalf("wrong order: %s %s %s (want a b c)", j1.ID, j2.ID, j3.ID)
	}
}

func TestQueue_CloseUnblocksDequeue(t *testing.T) {
	q := NewQueue(10, OverflowBlock, time.Second, nil)

	done := make(chan error, 1)
	go func() {
		_, _, err := q.Dequeue(context.Background())
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	q.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrQueueClosed) {
			t.Fatalf("expected ErrQueueClosed; got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Dequeue did not unblock on Close")
	}
}

func TestQueue_CtxCancelUnblocksDequeue(t *testing.T) {
	q := NewQueue(10, OverflowBlock, time.Second, nil)
	defer q.Close()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, _, err := q.Dequeue(ctx)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled; got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Dequeue did not honor ctx cancel")
	}
}

func TestQueue_ConcurrentEnqueueDequeue(t *testing.T) {
	q := NewQueue(100, OverflowBlock, time.Second, nil)
	defer q.Close()

	const N = 500
	var producers sync.WaitGroup
	producers.Add(10)
	for p := 0; p < 10; p++ {
		go func(p int) {
			defer producers.Done()
			for i := 0; i < N/10; i++ {
				id := JobID("p" + string(rune('0'+p)) + "-" + padInt(i, 3))
				_ = q.Enqueue(context.Background(), makeJob(string(id), "t", time.Now()))
			}
		}(p)
	}

	var consumers sync.WaitGroup
	consumers.Add(5)
	var gotMu sync.Mutex
	got := make(map[JobID]bool)
	for c := 0; c < 5; c++ {
		go func() {
			defer consumers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for {
				j, ok, err := q.Dequeue(ctx)
				if err != nil || !ok {
					return
				}
				gotMu.Lock()
				got[j.ID] = true
				if len(got) == N {
					gotMu.Unlock()
					return
				}
				gotMu.Unlock()
			}
		}()
	}
	producers.Wait()

	// Give consumers a moment to drain.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		gotMu.Lock()
		n := len(got)
		gotMu.Unlock()
		if n == N {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	gotMu.Lock()
	defer gotMu.Unlock()
	if len(got) != N {
		t.Fatalf("delivered %d / %d unique jobs", len(got), N)
	}
}
