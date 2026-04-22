/*
 * ChatCLI - Pipeline immutable snapshot.
 *
 * A snapshot bundles the hook slices and config that a single Run()
 * invocation should see. Snapshots are never mutated after creation:
 * AddPre/AddPost build a new snapshot with the added hook and swap it
 * atomically against the Pipeline's pointer.
 *
 * This gives enterprise-grade guarantees without RWMutex contention on
 * the Run() hot path:
 *
 *   - Concurrent AddPre + Run: Run uses the snapshot it grabbed on
 *     entry, AddPre builds a new one. No partial visibility.
 *
 *   - Hot reload: SwapConfig constructs a new snapshot with the new
 *     Config + existing hooks and CAS-swaps it. In-flight Run() calls
 *     still see the old Config — correct, because a turn should run
 *     under one consistent config from start to finish.
 *
 *   - Generation tracking: each snapshot carries a monotonically
 *     increasing Generation counter. Useful for observability and for
 *     detecting "hook added mid-session" in logs.
 */
package quality

import (
	"sort"
)

// snapshot is the immutable view of the Pipeline a single Run uses.
type snapshot struct {
	cfg        Config
	pre        []PreHook
	post       []PostHook
	generation uint64
}

// withPre returns a new snapshot with h appended, preserving
// priority-based ordering (stable — ties use insertion order).
func (s *snapshot) withPre(h PreHook) *snapshot {
	pre := make([]PreHook, len(s.pre)+1)
	copy(pre, s.pre)
	pre[len(s.pre)] = h
	sortPreByPriority(pre)
	return &snapshot{
		cfg:        s.cfg,
		pre:        pre,
		post:       s.post,
		generation: s.generation + 1,
	}
}

// withPost returns a new snapshot with h appended.
func (s *snapshot) withPost(h PostHook) *snapshot {
	post := make([]PostHook, len(s.post)+1)
	copy(post, s.post)
	post[len(s.post)] = h
	sortPostByPriority(post)
	return &snapshot{
		cfg:        s.cfg,
		pre:        s.pre,
		post:       post,
		generation: s.generation + 1,
	}
}

// withConfig returns a new snapshot with cfg replaced (hooks unchanged).
// Used by Pipeline.SwapConfig for hot reload.
func (s *snapshot) withConfig(cfg Config) *snapshot {
	return &snapshot{
		cfg:        cfg,
		pre:        s.pre,
		post:       s.post,
		generation: s.generation + 1,
	}
}

// sortPreByPriority is a stable sort that respects Prioritized
// interface on elements. Equal priorities preserve insertion order —
// which is important for backward compat with the original
// registration-order semantics.
func sortPreByPriority(pre []PreHook) {
	sort.SliceStable(pre, func(i, j int) bool {
		return priorityOf(pre[i]) < priorityOf(pre[j])
	})
}

func sortPostByPriority(post []PostHook) {
	sort.SliceStable(post, func(i, j int) bool {
		return priorityOf(post[i]) < priorityOf(post[j])
	})
}
