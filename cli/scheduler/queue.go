/*
 * ChatCLI - Scheduler: priority queue.
 *
 * The scheduling index is a min-heap keyed on NextFireAt. Fronted by a
 * map[JobID]*heapEntry for O(1) dedupe, Remove, and Requeue. Lazy
 * deletion keeps Remove off the heap's critical path — entries are
 * marked dead and skipped on Pop.
 *
 * Invariants:
 *   - Each JobID appears at most once live in the heap.
 *   - Dead entries sort after live ones so Pop reliably returns work.
 *   - The queue is the *scheduling index* only; the WAL is the truth
 *     of what jobs exist. Drain (on boot) re-Enqueue-s from the WAL.
 *
 * Thread-safety: single mutex + cond var (same pattern as lessonq,
 * already proven in production).
 */
package scheduler

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// scheduleQueue is the in-memory scheduling index.
type scheduleQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	heap   *scheduleHeap
	byID   map[JobID]*queueEntry
	closed bool
}

// newScheduleQueue builds a scheduleQueue.
func newScheduleQueue() *scheduleQueue {
	q := &scheduleQueue{
		heap: &scheduleHeap{},
		byID: make(map[JobID]*queueEntry),
	}
	q.cond = sync.NewCond(&q.mu)
	heap.Init(q.heap)
	return q
}

// Len returns the live queue depth (dead entries excluded).
func (q *scheduleQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byID)
}

// Close signals shutdown; pop-ers wake up and return false.
func (q *scheduleQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Enqueue registers (or re-registers) a job with the given fireAt.
// Idempotent — re-enqueueing the same ID updates the fireAt without
// duplicating the heap entry.
func (q *scheduleQueue) Enqueue(id JobID, fireAt time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if existing, ok := q.byID[id]; ok && !existing.dead {
		existing.fireAt = fireAt
		heap.Fix(q.heap, existing.index)
		q.cond.Broadcast()
		return
	}
	entry := &queueEntry{id: id, fireAt: fireAt, index: -1}
	heap.Push(q.heap, entry)
	q.byID[id] = entry
	q.cond.Broadcast()
}

// Remove drops the entry for id. No-op if not present or already dead.
func (q *scheduleQueue) Remove(id JobID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e, ok := q.byID[id]; ok {
		e.dead = true
		delete(q.byID, id)
		q.cond.Broadcast()
	}
}

// PopReady blocks until the earliest live entry's fireAt is reached,
// the queue closes, or ctx is cancelled. Returns the JobID; the caller
// uses Scheduler.jobs.Get(id) to resolve the full Job record.
//
// Unlike lessonq.Queue.Dequeue, PopReady does NOT remove the entry
// from the map — the scheduler marks it dead after it decides whether
// to re-enqueue (recurring jobs keep a fresh entry).
func (q *scheduleQueue) PopReady(ctx context.Context) (JobID, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.closed {
			return "", false, ErrSchedulerClosed
		}
		for q.heap.Len() > 0 {
			peek := (*q.heap)[0]
			if peek.dead {
				heap.Pop(q.heap)
				continue
			}
			wait := time.Until(peek.fireAt)
			if wait <= 0 {
				id := peek.id
				peek.dead = true
				heap.Pop(q.heap)
				delete(q.byID, id)
				return id, true, nil
			}
			q.sleepUntilLocked(ctx, wait)
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			continue
		}
		// Empty heap — wait for Enqueue or close/cancel.
		if err := q.waitLocked(ctx); err != nil {
			return "", false, err
		}
	}
}

// Peek returns the earliest live fireAt without removing it. Used by
// the UI status line to show "next fire in 2m13s".
func (q *scheduleQueue) Peek() (JobID, time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.heap.Len() > 0 {
		e := (*q.heap)[0]
		if !e.dead {
			return e.id, e.fireAt, true
		}
		// Strip dead heads so subsequent Peeks don't have to skim.
		heap.Pop(q.heap)
	}
	return "", time.Time{}, false
}

// sleepUntilLocked releases the lock, sleeps for at most d or until
// wake, reacquires the lock. Responsive to ctx cancellation and
// internal signals.
//
// Copied from lessonq.Queue — the two share the pattern deliberately.
func (q *scheduleQueue) sleepUntilLocked(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	done := make(chan struct{})
	go func() {
		select {
		case <-timer.C:
		case <-ctx.Done():
		case <-done:
			return
		}
		q.mu.Lock()
		q.cond.Broadcast()
		q.mu.Unlock()
	}()
	q.cond.Wait()
	close(done)
}

// waitLocked blocks until Enqueue or close/cancel.
func (q *scheduleQueue) waitLocked(ctx context.Context) error {
	cancelWatcher := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-cancelWatcher:
		}
	}()
	q.cond.Wait()
	close(cancelWatcher)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.closed {
		return ErrSchedulerClosed
	}
	return nil
}

// ─── heap wiring ──────────────────────────────────────────────

type queueEntry struct {
	id     JobID
	fireAt time.Time
	index  int
	dead   bool
}

type scheduleHeap []*queueEntry

func (h scheduleHeap) Len() int { return len(h) }
func (h scheduleHeap) Less(i, j int) bool {
	if h[i].dead != h[j].dead {
		return !h[i].dead
	}
	return h[i].fireAt.Before(h[j].fireAt)
}
func (h scheduleHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *scheduleHeap) Push(x any) {
	e := x.(*queueEntry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *scheduleHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	e.index = -1
	*h = old[:n-1]
	return e
}
