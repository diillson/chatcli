/*
 * ChatCLI - Tests for the processLLMRequest lifecycle helpers.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The lifecycle helpers — startProcessingLifecycle, endProcessingLifecycle,
 * announceQueueDrain, warnIfHistoryExceedsProxyCap — own state (isExecuting,
 * proxyPayloadWarned, the message queue) that the rest of the chat pipeline
 * relies on. Each test below asserts on that state, not just on the
 * function returning.
 */
package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func TestStartProcessingLifecycle_FlipsIsExecutingAndReturnsIdempotentStop(t *testing.T) {
	cli := &ChatCLI{
		logger:         zap.NewNop(),
		animation:      NewAnimationManager(),
		processingDone: make(chan struct{}),
	}
	stop := cli.startProcessingLifecycle()
	if stop == nil {
		t.Fatal("startProcessingLifecycle must return a non-nil stop closure")
	}
	if !cli.isExecuting.Load() {
		t.Error("isExecuting must flip to true after start")
	}
	// Two consecutive Calls must not panic — stopSpinner is documented as
	// idempotent (defer + mid-turn manual stop both reach it).
	stop()
	stop()
}

func TestEndProcessingLifecycle_EmptyQueueRestoresIdleState(t *testing.T) {
	cli := &ChatCLI{
		logger:         zap.NewNop(),
		animation:      NewAnimationManager(),
		processingDone: make(chan struct{}),
	}
	cli.isExecuting.Store(true)
	cli.interactionState = StateProcessing

	cli.endProcessingLifecycle(func() {}) // empty queue → idle
	if cli.isExecuting.Load() {
		t.Error("isExecuting must be false after end with empty queue")
	}
	if cli.interactionState != StateNormal {
		t.Errorf("interactionState = %v, want StateNormal", cli.interactionState)
	}
}

func TestAnnounceQueueDrain_FlipsInteractionStateAndCountsRemaining(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.messageQueueMu.Lock()
	cli.messageQueue = []string{"queued-1", "queued-2"}
	cli.messageQueueMu.Unlock()

	// Redirect stdout to /dev/null so the printf doesn't noise the test
	// runner. The test's real purpose is the state mutation.
	old := os.Stdout
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if devnull != nil {
		os.Stdout = devnull
		defer func() {
			os.Stdout = old
			devnull.Close()
		}()
	}
	cli.announceQueueDrain()
	if cli.interactionState != StateProcessing {
		t.Errorf("interactionState = %v, want StateProcessing during drain", cli.interactionState)
	}
}

func TestWarnIfHistoryExceedsProxyCap_BelowThresholdDoesNotWarn(t *testing.T) {
	cli := &ChatCLI{
		logger: zap.NewNop(),
		// One small message, way under the 2.5MB ceiling.
		history: []models.Message{{Content: "hello"}},
	}
	cli.warnIfHistoryExceedsProxyCap(CompactConfig{MaxPayloadBytes: 0})
	if cli.proxyPayloadWarned {
		t.Error("proxyPayloadWarned must stay false below the threshold")
	}
}

func TestWarnIfHistoryExceedsProxyCap_AboveThresholdFlipsFlagOnce(t *testing.T) {
	// Build a history with > 2.5MB of content.
	bigChunk := strings.Repeat("x", 100_000)
	history := make([]models.Message, 0, 30)
	for i := 0; i < 30; i++ {
		history = append(history, models.Message{Content: bigChunk})
	}
	cli := &ChatCLI{logger: zap.NewNop(), history: history}

	old := os.Stdout
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if devnull != nil {
		os.Stdout = devnull
		defer func() {
			os.Stdout = old
			devnull.Close()
		}()
	}

	cli.warnIfHistoryExceedsProxyCap(CompactConfig{MaxPayloadBytes: 0})
	if !cli.proxyPayloadWarned {
		t.Fatal("proxyPayloadWarned must flip true once the threshold is crossed")
	}
	// Second call must be a no-op — the warning is once-per-session.
	cli.warnIfHistoryExceedsProxyCap(CompactConfig{MaxPayloadBytes: 0})
	if !cli.proxyPayloadWarned {
		t.Error("flag must remain true on subsequent calls")
	}
}

func TestWarnIfHistoryExceedsProxyCap_ExplicitCapShortCircuits(t *testing.T) {
	// When the user (or DefaultCompactConfig for that provider) has set
	// an explicit MaxPayloadBytes, the warning is irrelevant and must
	// not fire even on huge history.
	bigChunk := strings.Repeat("x", 1_000_000)
	cli := &ChatCLI{
		logger:  zap.NewNop(),
		history: []models.Message{{Content: bigChunk}, {Content: bigChunk}, {Content: bigChunk}},
	}
	cli.warnIfHistoryExceedsProxyCap(CompactConfig{MaxPayloadBytes: 5_000_000})
	if cli.proxyPayloadWarned {
		t.Error("explicit MaxPayloadBytes must suppress the warning entirely")
	}
}

func TestAcquireOperationContext_ReleaseCancelsContextAndClearsSlot(t *testing.T) {
	cli := &ChatCLI{}
	ctx, release := cli.acquireOperationContext()
	if cli.operationCancel == nil {
		t.Fatal("operationCancel must be populated immediately after acquire")
	}
	select {
	case <-ctx.Done():
		t.Fatal("context should not be cancelled before release()")
	default:
	}
	release()
	if cli.operationCancel != nil {
		t.Error("operationCancel must be cleared after release()")
	}
	select {
	case <-ctx.Done():
		// expected — release cancelled the ctx
	default:
		t.Error("release() must cancel the returned context")
	}
}

func TestAcquireOperationContext_DoubleReleaseIsSafe(t *testing.T) {
	cli := &ChatCLI{}
	_, release := cli.acquireOperationContext()
	release()
	release() // must not panic even though cancel was already called
}

func TestApplyChatEffortHint_NoOverridePropagatesSkillEffort(t *testing.T) {
	cli := &ChatCLI{}
	ctx := cli.applyChatEffortHint(context.Background(), client.EffortMedium)
	if got := client.EffortFromContext(ctx); got != client.EffortMedium {
		t.Errorf("effort hint did not propagate; got %q want %q", got, client.EffortMedium)
	}
}

func TestApplyChatEffortHint_ThinkingOverrideTakesPriority(t *testing.T) {
	cli := &ChatCLI{
		thinkingOverride: thinkingOverrideState{set: true, effort: client.EffortHigh},
	}
	ctx := cli.applyChatEffortHint(context.Background(), client.EffortLow)
	if got := client.EffortFromContext(ctx); got != client.EffortHigh {
		t.Errorf("/thinking override must trump skill effort; got %q", got)
	}
}

func TestApplyChatEffortHint_OverrideUnsetDetachesHint(t *testing.T) {
	cli := &ChatCLI{
		thinkingOverride: thinkingOverrideState{set: true, effort: client.EffortUnset},
	}
	ctx := cli.applyChatEffortHint(context.Background(), client.EffortHigh)
	if got := client.EffortFromContext(ctx); got != client.EffortUnset {
		t.Errorf("EffortUnset override must detach the hint; got %q", got)
	}
}
