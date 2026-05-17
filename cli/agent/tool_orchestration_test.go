/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPartitionToolCalls_AllSafe verifies that a run of fully-safe calls
// is coalesced into a single concurrent batch (or split across multiple
// batches when the budget is exceeded).
func TestPartitionToolCalls_AllSafe(t *testing.T) {
	safe := PartitionPolicyFunc(func(_ ToolCall) bool { return true })
	calls := []ToolCall{
		{Name: "@websearch"},
		{Name: "@webfetch"},
		{Name: "@coder", Args: "read"},
	}
	batches := PartitionToolCalls(calls, safe)
	if assert.Len(t, batches, 1) {
		assert.True(t, batches[0].Concurrent)
		assert.Len(t, batches[0].Calls, 3)
	}
}

// TestPartitionToolCalls_AllSerial covers the conservative case: every
// call is serial. The partitioner emits one batch per call (each with
// Concurrent=false) so the orchestrator runs them strictly in sequence.
func TestPartitionToolCalls_AllSerial(t *testing.T) {
	serial := PartitionPolicyFunc(func(_ ToolCall) bool { return false })
	calls := []ToolCall{
		{Name: "@coder", Args: "exec"},
		{Name: "@coder", Args: "write"},
	}
	batches := PartitionToolCalls(calls, serial)
	assert.Len(t, batches, 2)
	for _, b := range batches {
		assert.False(t, b.Concurrent)
		assert.Len(t, b.Calls, 1)
	}
}

// TestPartitionToolCalls_MixedPreservesOrder ensures the interleaved
// case never reorders calls and that every transition between safe and
// unsafe opens a fresh batch. The order safe → unsafe → safe → unsafe
// emits exactly four batches; preserving the user-visible execution
// causality is the whole point of partitioning per-step rather than
// merging non-contiguous safe calls.
func TestPartitionToolCalls_MixedPreservesOrder(t *testing.T) {
	policy := PartitionPolicyFunc(func(c ToolCall) bool {
		return c.Name == "@websearch"
	})
	calls := []ToolCall{
		{Name: "@websearch"},
		{Name: "@coder", Args: "exec"},
		{Name: "@websearch"},
		{Name: "@webfetch"}, // not safe per policy
	}
	batches := PartitionToolCalls(calls, policy)
	if assert.Len(t, batches, 4, "each safe<->unsafe transition opens a new batch") {
		assert.True(t, batches[0].Concurrent)
		assert.Equal(t, "@websearch", batches[0].Calls[0].Name)
		assert.False(t, batches[1].Concurrent)
		assert.Equal(t, "@coder", batches[1].Calls[0].Name)
		assert.True(t, batches[2].Concurrent)
		assert.Equal(t, "@websearch", batches[2].Calls[0].Name)
		assert.False(t, batches[3].Concurrent)
		assert.Equal(t, "@webfetch", batches[3].Calls[0].Name)
	}
}

// TestPartitionToolCalls_ContiguousSafeMerged is the complement: two
// adjacent concurrent-safe calls coalesce into one batch even when the
// policy returns false for an unrelated tool not present here.
func TestPartitionToolCalls_ContiguousSafeMerged(t *testing.T) {
	policy := PartitionPolicyFunc(func(_ ToolCall) bool { return true })
	calls := []ToolCall{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	}
	batches := PartitionToolCalls(calls, policy)
	if assert.Len(t, batches, 1) {
		assert.True(t, batches[0].Concurrent)
		assert.Len(t, batches[0].Calls, 3)
	}
}

// TestPartitionToolCalls_SplitsAtMaxConcurrency confirms that more
// concurrent-safe calls than the budget allows are split into multiple
// back-to-back concurrent batches rather than running unbounded.
func TestPartitionToolCalls_SplitsAtMaxConcurrency(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY", "3")
	safe := PartitionPolicyFunc(func(_ ToolCall) bool { return true })

	var calls []ToolCall
	for i := 0; i < 7; i++ {
		calls = append(calls, ToolCall{Name: "@websearch"})
	}
	batches := PartitionToolCalls(calls, safe)
	// 7 calls / budget 3 = 3 batches of sizes 3 + 3 + 1
	if assert.Len(t, batches, 3) {
		assert.Equal(t, 3, len(batches[0].Calls))
		assert.Equal(t, 3, len(batches[1].Calls))
		assert.Equal(t, 1, len(batches[2].Calls))
	}
}

// TestPartitionToolCalls_NilPolicyFailsClosed confirms that calling
// the partitioner without a policy treats everything as serial in one
// catch-all batch. This is the safe default for early bootstrap paths.
func TestPartitionToolCalls_NilPolicyFailsClosed(t *testing.T) {
	batches := PartitionToolCalls([]ToolCall{
		{Name: "a"}, {Name: "b"},
	}, nil)
	if assert.Len(t, batches, 1) {
		assert.False(t, batches[0].Concurrent)
		assert.Len(t, batches[0].Calls, 2)
	}
}

// TestRunBatch_SerialReturnsResultsInOrder is the baseline: a serial
// batch runs callbacks in order, returns slice index-aligned with calls.
func TestRunBatch_SerialReturnsResultsInOrder(t *testing.T) {
	batch := ToolBatch{
		Concurrent: false,
		Calls: []ToolCall{
			{Name: "first"}, {Name: "second"}, {Name: "third"},
		},
	}
	exec := func(_ context.Context, c ToolCall) (ToolResult, error) {
		return ToolResult{Output: c.Name}, nil
	}
	results, err := RunBatch(context.Background(), batch, exec, BatchOptions{})
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, "first", results[0].Output)
	assert.Equal(t, "second", results[1].Output)
	assert.Equal(t, "third", results[2].Output)
}

// TestRunBatch_ConcurrentRunsInParallel uses synthetic delays to
// demonstrate the speedup vs. serial. Three 100ms tools must complete
// in < 200ms (parallel) instead of > 300ms (serial).
func TestRunBatch_ConcurrentRunsInParallel(t *testing.T) {
	batch := ToolBatch{
		Concurrent: true,
		Calls: []ToolCall{
			{Name: "a"}, {Name: "b"}, {Name: "c"},
		},
	}
	exec := func(ctx context.Context, c ToolCall) (ToolResult, error) {
		select {
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		}
		return ToolResult{Output: c.Name}, nil
	}
	start := time.Now()
	results, err := RunBatch(context.Background(), batch, exec, BatchOptions{MaxConcurrency: 3})
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Less(t, elapsed, 250*time.Millisecond,
		"three 120ms tools must finish in well under 360ms when run in parallel")
}

// TestRunBatch_ResultsIndexAlignedDespiteParallelism guarantees that
// even with parallel execution and out-of-order completion, the result
// slice maps 1:1 to the input order. Tests use varying durations to
// force a different completion order than declaration order.
func TestRunBatch_ResultsIndexAlignedDespiteParallelism(t *testing.T) {
	batch := ToolBatch{
		Concurrent: true,
		Calls: []ToolCall{
			{Name: "slow"}, // 80ms
			{Name: "fast"}, // 10ms
			{Name: "mid"},  // 40ms
		},
	}
	exec := func(_ context.Context, c ToolCall) (ToolResult, error) {
		switch c.Name {
		case "slow":
			time.Sleep(80 * time.Millisecond)
		case "fast":
			time.Sleep(10 * time.Millisecond)
		case "mid":
			time.Sleep(40 * time.Millisecond)
		}
		return ToolResult{Output: c.Name}, nil
	}
	results, err := RunBatch(context.Background(), batch, exec, BatchOptions{MaxConcurrency: 3})
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, "slow", results[0].Output)
	assert.Equal(t, "fast", results[1].Output)
	assert.Equal(t, "mid", results[2].Output)
}

// TestRunBatch_RespectsSemaphore caps simultaneous executions at the
// configured concurrency. We track high-watermark via atomic counter.
func TestRunBatch_RespectsSemaphore(t *testing.T) {
	var inflight, highWater int32
	batch := ToolBatch{
		Concurrent: true,
		Calls: []ToolCall{
			{}, {}, {}, {}, {}, {}, {}, {},
		},
	}
	exec := func(_ context.Context, _ ToolCall) (ToolResult, error) {
		cur := atomic.AddInt32(&inflight, 1)
		defer atomic.AddInt32(&inflight, -1)
		for {
			hw := atomic.LoadInt32(&highWater)
			if cur <= hw || atomic.CompareAndSwapInt32(&highWater, hw, cur) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		return ToolResult{}, nil
	}
	_, err := RunBatch(context.Background(), batch, exec, BatchOptions{MaxConcurrency: 3})
	require.NoError(t, err)
	assert.LessOrEqual(t, atomic.LoadInt32(&highWater), int32(3),
		"never more than 3 tools in flight when MaxConcurrency=3")
}

// TestRunBatch_CancelSiblingsAbortsOnInfraError is the contract for the
// fail-fast option. When one tool returns an infra error and
// CancelSiblings=true, the others observe ctx cancellation and bail.
func TestRunBatch_CancelSiblingsAbortsOnInfraError(t *testing.T) {
	batch := ToolBatch{
		Concurrent: true,
		Calls: []ToolCall{
			{Name: "fail"},
			{Name: "ok-1"},
			{Name: "ok-2"},
		},
	}
	var canceledCount int32
	exec := func(ctx context.Context, c ToolCall) (ToolResult, error) {
		if c.Name == "fail" {
			return ToolResult{}, errors.New("kaboom")
		}
		select {
		case <-time.After(500 * time.Millisecond):
			return ToolResult{Output: c.Name}, nil
		case <-ctx.Done():
			atomic.AddInt32(&canceledCount, 1)
			return ToolResult{}, ctx.Err()
		}
	}
	start := time.Now()
	_, err := RunBatch(context.Background(), batch, exec, BatchOptions{
		MaxConcurrency: 3,
		CancelSiblings: true,
	})
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Less(t, elapsed, 300*time.Millisecond,
		"siblings must abort early on first infra error — not run to their full 500ms timeout")
	assert.GreaterOrEqual(t, atomic.LoadInt32(&canceledCount), int32(1),
		"at least one sibling observed ctx cancellation")
}

// TestRunBatch_CancelSiblingsFalse keeps siblings running even when one
// fails. This is the default — useful when independent fetches should
// all complete even if one was rate-limited.
func TestRunBatch_CancelSiblingsFalse(t *testing.T) {
	batch := ToolBatch{
		Concurrent: true,
		Calls: []ToolCall{
			{Name: "fail"},
			{Name: "ok-1"},
			{Name: "ok-2"},
		},
	}
	var completed sync.Map
	exec := func(_ context.Context, c ToolCall) (ToolResult, error) {
		if c.Name == "fail" {
			return ToolResult{IsError: true, Output: "no"}, errors.New("kaboom")
		}
		time.Sleep(50 * time.Millisecond)
		completed.Store(c.Name, true)
		return ToolResult{Output: c.Name}, nil
	}
	results, err := RunBatch(context.Background(), batch, exec, BatchOptions{
		MaxConcurrency: 3,
		CancelSiblings: false,
	})
	assert.Error(t, err, "RunBatch surfaces the first infra error even when not canceling siblings")
	require.Len(t, results, 3)
	_, ok1 := completed.Load("ok-1")
	_, ok2 := completed.Load("ok-2")
	assert.True(t, ok1)
	assert.True(t, ok2)
}

// TestRunBatch_PanicRecovery confirms a bug in a tool callback doesn't
// take down the orchestrator. The panicking call is reported as an
// error result; siblings keep running.
func TestRunBatch_PanicRecovery(t *testing.T) {
	batch := ToolBatch{
		Concurrent: true,
		Calls: []ToolCall{
			{Name: "panicky"},
			{Name: "well-behaved"},
		},
	}
	exec := func(_ context.Context, c ToolCall) (ToolResult, error) {
		if c.Name == "panicky" {
			panic("deliberate panic for test")
		}
		return ToolResult{Output: "ok"}, nil
	}
	results, _ := RunBatch(context.Background(), batch, exec, BatchOptions{MaxConcurrency: 2})
	require.Len(t, results, 2)
	assert.True(t, results[0].IsError)
	assert.Equal(t, "PanicError", results[0].ErrorCode)
	assert.Equal(t, "ok", results[1].Output)
}

// TestRunBatch_DurationPopulated checks that each result has a non-zero
// Duration filled in by the orchestrator, regardless of whether the
// callback set one.
func TestRunBatch_DurationPopulated(t *testing.T) {
	batch := ToolBatch{
		Concurrent: false,
		Calls:      []ToolCall{{Name: "a"}},
	}
	exec := func(_ context.Context, _ ToolCall) (ToolResult, error) {
		time.Sleep(20 * time.Millisecond)
		return ToolResult{Output: "ok"}, nil
	}
	results, err := RunBatch(context.Background(), batch, exec, BatchOptions{})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.GreaterOrEqual(t, results[0].Duration, 20*time.Millisecond)
}

// TestRunBatch_EmptyCallsReturnsNil keeps the helper safe to call
// unconditionally — useful when the partitioner returned an empty batch
// at the end (no-op).
func TestRunBatch_EmptyCallsReturnsNil(t *testing.T) {
	results, err := RunBatch(context.Background(), ToolBatch{}, nil, BatchOptions{})
	assert.Nil(t, results)
	assert.NoError(t, err)
}

// TestMaxToolConcurrency_EnvOverride confirms the env-var knob.
func TestMaxToolConcurrency_EnvOverride(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY", "42")
	assert.Equal(t, 42, MaxToolConcurrency())
}

// TestMaxToolConcurrency_InvalidFallsBack documents that bogus values
// fall back to the default rather than panicking or yielding 0.
func TestMaxToolConcurrency_InvalidFallsBack(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY", "abc")
	assert.Equal(t, defaultMaxToolConcurrency, MaxToolConcurrency())
	t.Setenv("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY", "0")
	assert.Equal(t, defaultMaxToolConcurrency, MaxToolConcurrency())
	t.Setenv("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY", "-5")
	assert.Equal(t, defaultMaxToolConcurrency, MaxToolConcurrency())
}

// TestParallelToolsEnabled_OffByDefault is the rollout safety net: the
// feature must default to OFF until Fase 7 acceptance.
func TestParallelToolsEnabled_OffByDefault(t *testing.T) {
	t.Setenv("CHATCLI_AGENT_PARALLEL_TOOLS", "")
	assert.False(t, ParallelToolsEnabled())
}

// TestParallelToolsEnabled_OnVariants checks that true / 1 / on / yes
// all enable the feature (case-insensitive).
func TestParallelToolsEnabled_OnVariants(t *testing.T) {
	for _, v := range []string{"true", "TRUE", "1", "on", "yes", "YES"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("CHATCLI_AGENT_PARALLEL_TOOLS", v)
			assert.True(t, ParallelToolsEnabled())
		})
	}
}
