/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

type usageStub struct{ usage *models.UsageInfo }

func (u *usageStub) GetModelName() string { return "m" }
func (u *usageStub) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	return "ok", nil
}
func (u *usageStub) LastUsage() *models.UsageInfo { return u.usage }

type tokenRecorderStub struct {
	requests int
	tokens   map[string]int64
}

func (r *tokenRecorderStub) RecordRequest(string, string, string, time.Duration) { r.requests++ }
func (r *tokenRecorderStub) RecordError(string, string, string)                  {}
func (r *tokenRecorderStub) RecordTokens(_, _, kind string, n int64) {
	if r.tokens == nil {
		r.tokens = map[string]int64{}
	}
	r.tokens[kind] += n
}

func TestInstrumentedClient_ExportsTokenKinds(t *testing.T) {
	inner := &usageStub{usage: &models.UsageInfo{PromptTokens: 100, CompletionTokens: 20,
		CacheReadInputTokens: 60, CacheCreationInputTokens: 30}}
	rec := &tokenRecorderStub{}
	ic := NewInstrumentedClient(inner, rec, "CLAUDEAI")
	if _, err := ic.SendPrompt(context.Background(), "p", nil, 10); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"input": 100, "output": 20, "cache_read": 60, "cache_write": 30}
	for k, v := range want {
		if rec.tokens[k] != v {
			t.Fatalf("%s = %d, want %d (all: %v)", k, rec.tokens[k], v, rec.tokens)
		}
	}
	if rec.requests != 1 {
		t.Fatalf("request counter must still fire once, got %d", rec.requests)
	}
}

type plainRecorder struct{}

func (plainRecorder) RecordRequest(string, string, string, time.Duration) {}
func (plainRecorder) RecordError(string, string, string)                  {}

func TestInstrumentedClient_RecorderWithoutTokensIsFine(t *testing.T) {
	inner := &usageStub{usage: &models.UsageInfo{PromptTokens: 1}}
	ic := NewInstrumentedClient(inner, plainRecorder{}, "X")
	if _, err := ic.SendPrompt(context.Background(), "p", nil, 10); err != nil {
		t.Fatal(err)
	}
}
