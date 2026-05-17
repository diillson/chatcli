/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package agent

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// defaultMaxToolConcurrency caps the number of read-only / concurrency-safe
// tools that run in parallel within a single batch. Anthropic's reference
// implementation uses 10; we mirror that as a sane default. Operators can
// override via CHATCLI_AGENT_MAX_TOOL_CONCURRENCY.
const defaultMaxToolConcurrency = 10

// MaxToolConcurrency returns the active concurrency budget, reading the
// CHATCLI_AGENT_MAX_TOOL_CONCURRENCY environment variable on every call so a
// /config security mutation takes effect immediately. Values <=0 fall back
// to the default; the upper bound is intentionally not enforced — operators
// who want 64 concurrent fetches can have them.
func MaxToolConcurrency() int {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_MAX_TOOL_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxToolConcurrency
}

// ParallelToolsEnabled reports whether the orchestrator should partition
// the batch and run concurrency-safe tools in parallel. Opt-in via
// CHATCLI_AGENT_PARALLEL_TOOLS=true while the feature bakes in production
// usage. When false, every tool runs sequentially regardless of its
// capability flags — preserving the legacy behavior bit-for-bit.
//
// Default ON for new installs after Fase 7 acceptance; until then the
// env var gates the rollout.
func ParallelToolsEnabled() bool {
	v := strings.TrimSpace(os.Getenv("CHATCLI_AGENT_PARALLEL_TOOLS"))
	if v == "" {
		return false
	}
	return strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "on") || strings.EqualFold(v, "yes")
}

// ToolBatch represents a contiguous run of tool calls that share the same
// execution policy (all concurrency-safe or all serial). The orchestrator
// partitions a turn's calls into a sequence of batches and runs each one
// to completion before moving on, preserving the relative order of serial
// steps relative to surrounding parallel batches.
type ToolBatch struct {
	// Concurrent is true when every call in the batch is concurrency-safe
	// and read-only-or-network-only (no shared-state mutations). The batch
	// runs through errgroup with a semaphore-bounded fan-out.
	Concurrent bool

	// Calls are the per-tool invocations in their original order. The
	// orchestrator returns results in the same order so the caller can
	// keep its existing index-keyed accounting (batchOutputBuilder etc).
	Calls []ToolCall
}

// PartitionPolicy lets the caller inject capability lookups without
// pulling cli/plugins or cli/mcp into the agent package (cycle). The
// orchestrator asks: "is this call safe to parallelize?". The caller
// implements that by inspecting its plugin / MCP registry.
type PartitionPolicy interface {
	// IsConcurrencySafe returns true when the given tool call can run
	// in parallel with other safe calls. Implementations should be
	// pure functions over the (name, args) inputs.
	IsConcurrencySafe(call ToolCall) bool
}

// PartitionPolicyFunc adapts a plain function into the PartitionPolicy
// interface for callers that don't want to define a type.
type PartitionPolicyFunc func(call ToolCall) bool

// IsConcurrencySafe satisfies PartitionPolicy.
func (f PartitionPolicyFunc) IsConcurrencySafe(call ToolCall) bool { return f(call) }

// PartitionToolCalls splits a turn's tool calls into a sequence of batches
// where each batch is either fully concurrent-safe or fully serial.
// Consecutive concurrent-safe calls are coalesced into one batch (up to
// MaxToolConcurrency() — larger groups are split into multiple back-to-back
// concurrent batches to keep memory bounded). A serial call always opens a
// fresh batch and any following calls stay in their own batches until the
// next concurrency-safe run.
//
// The algorithm is deterministic and order-preserving: a serial call
// between two safe calls is NEVER folded into either neighbor's parallel
// batch — that would change the observable execution order.
func PartitionToolCalls(calls []ToolCall, policy PartitionPolicy) []ToolBatch {
	if len(calls) == 0 {
		return nil
	}
	if policy == nil {
		// No policy → everything is serial (fail-closed).
		return []ToolBatch{{Concurrent: false, Calls: append([]ToolCall(nil), calls...)}}
	}

	maxN := MaxToolConcurrency()
	if maxN <= 0 {
		maxN = defaultMaxToolConcurrency
	}

	var batches []ToolBatch
	var current ToolBatch
	flush := func() {
		if len(current.Calls) > 0 {
			batches = append(batches, current)
			current = ToolBatch{}
		}
	}
	for _, call := range calls {
		safe := policy.IsConcurrencySafe(call)
		if safe {
			if !current.Concurrent {
				flush()
				current.Concurrent = true
			}
			if len(current.Calls) >= maxN {
				// Split into another concurrent batch — both run in
				// parallel internally; sequentially relative to each other.
				flush()
				current.Concurrent = true
			}
			current.Calls = append(current.Calls, call)
		} else {
			if current.Concurrent || len(current.Calls) > 0 {
				flush()
			}
			current.Concurrent = false
			current.Calls = append(current.Calls, call)
			flush()
		}
	}
	flush()
	return batches
}

// ExecuteFunc is the callback the orchestrator invokes per tool call. It
// returns the structured ToolResult; the second return is an
// infrastructure error that aborts the batch (context.Canceled, network
// down, plugin binary missing) — business errors stay inside the result
// with IsError=true.
type ExecuteFunc func(ctx context.Context, call ToolCall) (ToolResult, error)

// BatchOptions configures a single RunBatch invocation.
type BatchOptions struct {
	// MaxConcurrency caps parallelism for this batch. Zero or negative
	// falls back to MaxToolConcurrency().
	MaxConcurrency int

	// CancelSiblings, when true, cancels in-flight siblings via the
	// batch ctx when any tool returns an infra error. The legacy serial
	// loop fails fast on first error; we mirror that for the concurrent
	// path so users don't pay for trailing operations after one already
	// signals "stop".
	CancelSiblings bool

	// Logger is used for batch-level diagnostics. Nil → no-op.
	Logger *zap.Logger
}

// RunBatch executes the calls in batch with the given policy. The returned
// slice is index-aligned with batch.Calls — entry i corresponds to call i,
// preserving order even when the batch ran in parallel. An error is
// returned only for infrastructure failures that aborted the batch; per-
// tool business errors live inside ToolResult.IsError.
//
// Concurrency semantics:
//
//   - Concurrent batch: errgroup with semaphore-bounded fan-out. Each
//     goroutine populates its slot; the main goroutine waits for Wait().
//     CancelSiblings=true means the first infra error cancels the batch
//     ctx so the others observe context.Canceled and bail.
//   - Serial batch: linear for-loop with the same ctx (no errgroup
//     overhead). Errors cause an early return preserving prior results.
//
// The function never panics — a panic in the callback is recovered and
// surfaced as an error result.
func RunBatch(ctx context.Context, batch ToolBatch, exec ExecuteFunc, opts BatchOptions) ([]ToolResult, error) {
	if len(batch.Calls) == 0 {
		return nil, nil
	}
	results := make([]ToolResult, len(batch.Calls))
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	if !batch.Concurrent {
		for i, call := range batch.Calls {
			start := time.Now()
			res, err := safeCall(ctx, call, exec)
			res.Duration = time.Since(start)
			results[i] = res
			if err != nil {
				logger.Debug("serial batch aborted by infra error",
					zap.String("tool", call.Name),
					zap.Int("index", i),
					zap.Error(err))
				return results[:i+1], err
			}
		}
		return results, nil
	}

	// Concurrent batch.
	maxN := opts.MaxConcurrency
	if maxN <= 0 {
		maxN = MaxToolConcurrency()
	}
	if maxN <= 0 {
		maxN = defaultMaxToolConcurrency
	}
	sem := make(chan struct{}, maxN)

	g, gctx := errgroup.WithContext(ctx)
	if !opts.CancelSiblings {
		// Decouple from errgroup's auto-cancel by using the parent ctx
		// inside each callback. errgroup still awaits all goroutines.
		gctx = ctx
	}

	var infraErrMu sync.Mutex
	var firstInfraErr error
	logger.Debug("starting concurrent tool batch",
		zap.Int("size", len(batch.Calls)),
		zap.Int("max_concurrency", maxN),
		zap.Bool("cancel_siblings", opts.CancelSiblings))

	for i, call := range batch.Calls {
		i, call := i, call
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				results[i] = ToolResult{
					IsError:   true,
					ErrorCode: ClassifyErrorCode(gctx.Err()),
					Output:    gctx.Err().Error(),
				}
				return nil
			}
			defer func() { <-sem }()

			start := time.Now()
			res, err := safeCall(gctx, call, exec)
			res.Duration = time.Since(start)
			results[i] = res
			if err != nil && opts.CancelSiblings {
				infraErrMu.Lock()
				if firstInfraErr == nil {
					firstInfraErr = err
				}
				infraErrMu.Unlock()
				return err
			}
			if err != nil {
				infraErrMu.Lock()
				if firstInfraErr == nil {
					firstInfraErr = err
				}
				infraErrMu.Unlock()
			}
			return nil
		})
	}

	_ = g.Wait()
	infraErrMu.Lock()
	finalErr := firstInfraErr
	infraErrMu.Unlock()
	return results, finalErr
}

// safeCall wraps the user-supplied ExecuteFunc with panic recovery and
// nil-ctx defense so the orchestrator never crashes on a bad callback.
func safeCall(ctx context.Context, call ToolCall, exec ExecuteFunc) (res ToolResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("tool execution panicked")
			res = ToolResult{
				IsError:   true,
				ErrorCode: "PanicError",
				Output:    "tool execution panicked",
			}
		}
	}()
	return exec(ctx, call)
}
