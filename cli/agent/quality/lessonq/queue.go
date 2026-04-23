/*
 * ChatCLI - Lesson Queue: in-memory bounded queue.
 *
 * The in-memory layer is a min-heap keyed on NextAttemptAt so workers
 * naturally pick up ready-to-retry jobs in scheduled order. A
 * map[JobID]*heapEntry fronts the heap for O(1) dedupe + removal.
 *
 * Invariants:
 *   - Each JobID appears at most once in the heap at any time.
 *   - Removing a job requires invalidating the heap entry (we use the
 *     "lazy deletion" trick: entry.dead=true, Pop skips dead entries).
 *   - The WAL is the source of truth; the heap is a scheduling index
 *     over it. On crash/reboot, Drain rebuilds the heap from the WAL.
 */
package lessonq

import (
	"container/heap"
	"context"
	"errors"
	"sync"
	"time"
)

// ErrQueueClosed is returned from Enqueue after Close.
var ErrQueueClosed = errors.New("lessonq: queue closed")

// ErrQueueFull is returned when OverflowBlock times out without a slot
// opening up and OverflowDropOldest is not in effect.
var ErrQueueFull = errors.New("lessonq: queue full")

// ErrDuplicate is returned when Enqueue is called with a JobID that
// is already in the queue (or a job with the same key that is in-
// flight or in the WAL). Caller should treat this as success — the
// work is already scheduled.
var ErrDuplicate = errors.New("lessonq: duplicate job")

// Queue is a bounded, priority-scheduled (by NextAttemptAt) job queue.
// Safe for concurrent use.
type Queue struct {
	mu           sync.Mutex
	cond         *sync.Cond // signals on Enqueue or shutdown
	heap         *scheduledHeap
	byID         map[JobID]*heapEntry
	cap          int
	policy       OverflowPolicy
	blockTimeout time.Duration // max wait on OverflowBlock
	closed       bool
	m            *Metrics
}

// NewQueue builds a queue with the given capacity and overflow policy.
// cap ≤ 0 disables the bound (unlimited); used mainly in tests.
func NewQueue(cap int, policy OverflowPolicy, blockTimeout time.Duration, metrics *Metrics) *Queue {
	q := &Queue{
		heap:         &scheduledHeap{},
		byID:         make(map[JobID]*heapEntry),
		cap:          cap,
		policy:       policy,
		blockTimeout: blockTimeout,
		m:            metrics,
	}
	q.cond = sync.NewCond(&q.mu)
	heap.Init(q.heap)
	return q
}

// Len returns the current queue depth (excluding dead entries).
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byID)
}

// Close signals shutdown. Dequeue-ers wake up and return ctx.Err().
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Enqueue adds a job. Returns ErrDuplicate if the JobID is already
// known (call site should treat as success — idempotent re-submit).
// Returns ErrQueueFull on OverflowBlock timeout; never on DropOldest.
func (q *Queue) Enqueue(ctx context.Context, job LessonJob) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrQueueClosed
	}
	if _, exists := q.byID[job.ID]; exists {
		q.recordEnqueue("deduped")
		return ErrDuplicate
	}

	if q.cap > 0 && len(q.byID) >= q.cap {
		switch q.policy {
		case OverflowDropOldest:
			q.dropOldestLocked()
			q.recordEnqueue("dropped_oldest")
		case OverflowBlock:
			fallthrough
		default:
			if err := q.waitForSlotLocked(ctx); err != nil {
				q.recordEnqueue("rejected_full")
				return err
			}
		}
	}

	entry := &heapEntry{job: job, index: -1}
	heap.Push(q.heap, entry)
	q.byID[job.ID] = entry
	q.recordEnqueue("accepted")
	q.updateDepthLocked()
	q.cond.Broadcast()
	return nil
}

// Remove drops a job by ID (used when DLQ moves a permanently-failed
// entry out of the active queue). No-op if the ID isn't present.
// Safe to call from the same goroutine that Dequeue returned the
// entry on.
func (q *Queue) Remove(id JobID) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if e, ok := q.byID[id]; ok {
		e.dead = true
		delete(q.byID, id)
		q.updateDepthLocked()
	}
}

// Dequeue blocks until a job is ready (NextAttemptAt ≤ now), the
// queue is closed, or ctx is canceled. When a job comes up, the
// entry is removed from the active set (it's in-flight) and returned
// with an Ack callback. The caller MUST invoke ack — otherwise the
// entry is lost from the in-memory view (WAL still has it, so it'd
// resurface on reboot, but that's a degraded path).
//
// Ack semantics are encoded in ProcessResult: Success/Skipped → WAL
// delete; Transient → WAL update + re-enqueue with new NextAttemptAt;
// Permanent → WAL move to DLQ (caller's responsibility, Ack just
// drops from the active queue).
func (q *Queue) Dequeue(ctx context.Context) (LessonJob, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.closed {
			return LessonJob{}, false, ErrQueueClosed
		}
		// Find the earliest live entry.
		for q.heap.Len() > 0 {
			peek := (*q.heap)[0]
			if peek.dead {
				heap.Pop(q.heap)
				continue
			}
			wait := time.Until(peek.job.NextAttemptAt)
			if wait <= 0 {
				// Ready to process.
				heap.Pop(q.heap)
				delete(q.byID, peek.job.ID)
				q.updateDepthLocked()
				return peek.job, true, nil
			}
			// Earliest job isn't ready yet — sleep until its time.
			// We unlock, sleep, and reacquire. If the queue is
			// mutated meanwhile we'll re-evaluate from the top.
			q.sleepUntilLocked(ctx, wait)
			if ctx.Err() != nil {
				return LessonJob{}, false, ctx.Err()
			}
			continue
		}
		// Empty queue — wait for Enqueue or close/cancel.
		if err := q.waitLocked(ctx); err != nil {
			return LessonJob{}, false, err
		}
	}
}

// Snapshot returns a read-only copy of all active jobs. Used by the
// DLQ/list commands. Stable ordering (by NextAttemptAt asc).
func (q *Queue) Snapshot() []LessonJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]LessonJob, 0, len(q.byID))
	for _, e := range q.byID {
		if !e.dead {
			out = append(out, e.job)
		}
	}
	sortByNextAttempt(out)
	return out
}

// ─── internals ─────────────────────────────────────────────────────────────

func (q *Queue) recordEnqueue(outcome string) {
	if q.m == nil {
		return
	}
	q.m.EnqueueTotal.WithLabelValues(outcome).Inc()
}

func (q *Queue) updateDepthLocked() {
	if q.m == nil {
		return
	}
	q.m.QueueDepth.Set(float64(len(q.byID)))
}

func (q *Queue) dropOldestLocked() {
	var oldest *heapEntry
	for _, e := range q.byID {
		if e.dead {
			continue
		}
		if oldest == nil || e.job.EnqueuedAt.Before(oldest.job.EnqueuedAt) {
			oldest = e
		}
	}
	if oldest != nil {
		oldest.dead = true
		delete(q.byID, oldest.job.ID)
	}
}

// waitForSlotLocked blocks (with mu held on entry/exit) until the
// queue has capacity again, ctx is done, or blockTimeout elapses.
func (q *Queue) waitForSlotLocked(ctx context.Context) error {
	deadline := time.Now().Add(q.blockTimeout)
	if q.blockTimeout <= 0 {
		return ErrQueueFull
	}
	for q.cap > 0 && len(q.byID) >= q.cap && !q.closed {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ErrQueueFull
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		q.sleepUntilLocked(ctx, remaining)
	}
	if q.closed {
		return ErrQueueClosed
	}
	return nil
}

// sleepUntilLocked releases the lock, sleeps for d or until wake-up,
// reacquires the lock. Handles ctx cancellation via a channel so the
// wait is responsive to both enqueue signals and cancellation.
func (q *Queue) sleepUntilLocked(ctx context.Context, d time.Duration) {
	// cond.Wait can't observe a timeout. Emulate it by spawning a
	// timer goroutine that broadcasts; release the lock during the
	// wait via Wait() itself.
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

// waitLocked blocks (with mu held) until Enqueue signals or ctx/close.
func (q *Queue) waitLocked(ctx context.Context) error {
	done := make(chan struct{})
	cancelWatcher := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.cond.Broadcast()
			q.mu.Unlock()
		case <-cancelWatcher:
			return
		}
		close(done)
	}()
	q.cond.Wait()
	close(cancelWatcher)
	select {
	case <-done:
	default:
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if q.closed {
		return ErrQueueClosed
	}
	return nil
}

// ─── heap wiring ──────────────────────────────────────────────────────────

type heapEntry struct {
	job   LessonJob
	index int
	dead  bool
}

type scheduledHeap []*heapEntry

func (h scheduledHeap) Len() int { return len(h) }
func (h scheduledHeap) Less(i, j int) bool {
	// Dead entries sort to the back so Pop finds live ones first.
	if h[i].dead != h[j].dead {
		return !h[i].dead
	}
	return h[i].job.NextAttemptAt.Before(h[j].job.NextAttemptAt)
}
func (h scheduledHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *scheduledHeap) Push(x any) {
	e := x.(*heapEntry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *scheduledHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	e.index = -1
	*h = old[:n-1]
	return e
}

func sortByNextAttempt(jobs []LessonJob) {
	// Simple bubble-free sort for small lists; for larger ones,
	// sort.Slice would be fine but we avoid the allocation.
	for i := 1; i < len(jobs); i++ {
		for j := i; j > 0 && jobs[j].NextAttemptAt.Before(jobs[j-1].NextAttemptAt); j-- {
			jobs[j], jobs[j-1] = jobs[j-1], jobs[j]
		}
	}
}
