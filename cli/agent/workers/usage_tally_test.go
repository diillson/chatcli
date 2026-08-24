/*
 * ChatCLI - worker usage recording tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// fakeWorkerClient is UsageAware + ToolAware with canned responses.
type fakeWorkerClient struct {
	usage *models.UsageInfo
	tools bool
}

func (f *fakeWorkerClient) GetModelName() string { return "fake-model" }
func (f *fakeWorkerClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return "answer", nil
}
func (f *fakeWorkerClient) SendPromptWithTools(_ context.Context, _ string, _ []models.Message, _ []models.ToolDefinition, _ int) (*models.LLMResponse, error) {
	return &models.LLMResponse{Content: "tool answer", Usage: f.usage}, nil
}
func (f *fakeWorkerClient) SupportsNativeTools() bool    { return f.tools }
func (f *fakeWorkerClient) LastUsage() *models.UsageInfo { return f.usage }
func (f *fakeWorkerClient) LastStopReason() string       { return "end_turn" }

// TestRecordingClientRecordsEveryCallSeparately: two rounds through the
// wrapped client must land as TWO records — per-call recording is what
// keeps the tracker's provider-billed accounting correct (a merged total
// would let one billed call mark all tokens as billed).
func TestRecordingClientRecordsEveryCallSeparately(t *testing.T) {
	inner := &fakeWorkerClient{
		usage: &models.UsageInfo{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, IsReal: true},
		tools: true,
	}
	var recorded []*models.UsageInfo
	wrapped := wrapWithUsageRecording(inner, func(u *models.UsageInfo) { recorded = append(recorded, u) }, nil)

	if _, err := wrapped.SendPrompt(context.Background(), "p", nil, 0); err != nil {
		t.Fatal(err)
	}
	tac, ok := client.AsToolAware(wrapped)
	if !ok || !tac.SupportsNativeTools() {
		t.Fatal("wrapper lost the native-tools capability")
	}
	if _, err := tac.SendPromptWithTools(context.Background(), "p", nil, nil, 0); err != nil {
		t.Fatal(err)
	}

	if len(recorded) != 2 {
		t.Fatalf("recorded %d calls, want 2 (per-call recording)", len(recorded))
	}
	for i, u := range recorded {
		if u.PromptTokens != 100 || u.CompletionTokens != 20 || !u.IsReal {
			t.Fatalf("call %d usage = %+v, want 100/20 real", i, u)
		}
	}
}

// TestRecordingClientEstimatesWhenInnerSilent: a usage-less inner client
// still produces a character estimate instead of zero.
func TestRecordingClientEstimatesWhenInnerSilent(t *testing.T) {
	inner := &fakeWorkerClient{usage: nil}
	var recorded []*models.UsageInfo
	wrapped := wrapWithUsageRecording(inner, func(u *models.UsageInfo) { recorded = append(recorded, u) }, nil)

	if _, err := wrapped.SendPrompt(context.Background(), "a 40-character prompt string goes here!!", nil, 0); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0].PromptTokens == 0 || recorded[0].IsReal {
		t.Fatalf("estimate fallback broken: %+v", recorded)
	}
}

// TestRecordingClientBudgetGateRefusesCalls: once the gate errors, no
// provider call happens — the in-flight dispatch wave stops mid-run.
func TestRecordingClientBudgetGateRefusesCalls(t *testing.T) {
	inner := &fakeWorkerClient{tools: true}
	blocked := errors.New("budget exhausted")
	calls := 0
	wrapped := wrapWithUsageRecording(inner, func(*models.UsageInfo) { calls++ }, func() error { return blocked })

	if _, err := wrapped.SendPrompt(context.Background(), "p", nil, 0); !errors.Is(err, blocked) {
		t.Fatalf("SendPrompt not gated: %v", err)
	}
	tac, _ := client.AsToolAware(wrapped)
	if _, err := tac.SendPromptWithTools(context.Background(), "p", nil, nil, 0); !errors.Is(err, blocked) {
		t.Fatalf("SendPromptWithTools not gated: %v", err)
	}
	if calls != 0 {
		t.Fatalf("gated calls still recorded usage: %d", calls)
	}
}

// TestRecordingClientHidesToolsWhenInnerLacksThem: capability parity.
func TestRecordingClientHidesToolsWhenInnerLacksThem(t *testing.T) {
	inner := &fakeWorkerClient{tools: false}
	wrapped := wrapWithUsageRecording(inner, func(*models.UsageInfo) {}, nil)
	if tac, ok := client.AsToolAware(wrapped); ok && tac.SupportsNativeTools() {
		t.Fatal("wrapper claims native tools the inner client lacks")
	}
}
