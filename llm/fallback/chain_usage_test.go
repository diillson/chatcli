/*
 * ChatCLI - fallback chain usage forwarding tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package fallback

import (
	"context"
	"errors"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// fakeUsageClient implements client.UsageAwareClient with canned data.
type fakeUsageClient struct {
	name  string
	fail  bool
	usage *models.UsageInfo
}

func (f *fakeUsageClient) GetModelName() string { return f.name }
func (f *fakeUsageClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	if f.fail {
		return "", errors.New("boom")
	}
	return "ok from " + f.name, nil
}
func (f *fakeUsageClient) LastUsage() *models.UsageInfo { return f.usage }

var _ client.UsageAwareClient = (*fakeUsageClient)(nil)

// TestChainForwardsUsageFromServedEntry: the chain must expose the usage of
// the entry that actually answered — including after a failover — so cost
// tracking never degrades to estimates behind a chain.
func TestChainForwardsUsageFromServedEntry(t *testing.T) {
	primary := &fakeUsageClient{name: "primary-model", fail: true}
	secondary := &fakeUsageClient{name: "secondary-model",
		usage: &models.UsageInfo{PromptTokens: 42, CompletionTokens: 7, IsReal: true}}

	chain := NewChain(zap.NewNop(), []FallbackEntry{
		{Provider: "P1", Model: "m1", Client: primary},
		{Provider: "P2", Model: "m2", Client: secondary},
	}, WithMaxRetries(0))

	if u := chain.LastUsage(); u != nil {
		t.Fatalf("usage before any call: %+v", u)
	}

	out, err := chain.SendPrompt(context.Background(), "hi", nil, 0)
	if err != nil || out == "" {
		t.Fatalf("chain send: %v", err)
	}

	provider, model, ok := chain.LastServedEntry()
	if !ok || provider != "P2" || model != "m2" {
		t.Fatalf("served entry = %s/%s ok=%v, want P2/m2", provider, model, ok)
	}
	u := chain.LastUsage()
	if u == nil || u.PromptTokens != 42 || !u.IsReal {
		t.Fatalf("chain did not forward served usage: %+v", u)
	}
}
