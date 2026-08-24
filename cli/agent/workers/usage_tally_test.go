/*
 * ChatCLI - worker usage tally tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
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

// TestTallyClientSumsEveryCall: two rounds through the wrapped client must
// accumulate into one total the recorder can attribute.
func TestTallyClientSumsEveryCall(t *testing.T) {
	inner := &fakeWorkerClient{
		usage: &models.UsageInfo{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, IsReal: true},
		tools: true,
	}
	tally := &usageTally{}
	wrapped := wrapWithUsageTally(inner, tally)

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

	total := tally.take()
	if total == nil || total.PromptTokens != 200 || total.CompletionTokens != 40 || !total.IsReal {
		t.Fatalf("tally = %+v, want 200/40 real", total)
	}
}

// TestTallyClientEstimatesWhenInnerSilent: a usage-less inner client still
// produces a character estimate instead of zero.
func TestTallyClientEstimatesWhenInnerSilent(t *testing.T) {
	inner := &fakeWorkerClient{usage: nil}
	tally := &usageTally{}
	wrapped := wrapWithUsageTally(inner, tally)

	if _, err := wrapped.SendPrompt(context.Background(), "a 40-character prompt string goes here!!", nil, 0); err != nil {
		t.Fatal(err)
	}
	total := tally.take()
	if total == nil || total.PromptTokens == 0 || total.IsReal {
		t.Fatalf("estimate fallback broken: %+v", total)
	}
}

// TestTallyClientHidesToolsWhenInnerLacksThem: capability parity.
func TestTallyClientHidesToolsWhenInnerLacksThem(t *testing.T) {
	inner := &fakeWorkerClient{tools: false}
	wrapped := wrapWithUsageTally(inner, &usageTally{})
	if tac, ok := client.AsToolAware(wrapped); ok && tac.SupportsNativeTools() {
		t.Fatal("wrapper claims native tools the inner client lacks")
	}
}
