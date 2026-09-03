/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// summarizerStub is the embedded summarizer: it records whether it ran.
type summarizerStub struct{ calls int }

func (s *summarizerStub) GetModelName() string { return "stub" }
func (s *summarizerStub) SendPrompt(context.Context, string, []models.Message, int) (string, error) {
	s.calls++
	return "EMBEDDED SUMMARY", nil
}

func compactableHistory() []models.Message {
	var hist []models.Message
	for i := 0; i < 16; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		hist = append(hist, models.Message{Role: role, Content: strings.Repeat("turn text ", 40)})
	}
	return hist
}

func TestStructuredSummarize_ContextEngineReplacesAndFallsBack(t *testing.T) {
	hc := NewHistoryCompactor(zap.NewNop())
	stub := &summarizerStub{}
	cfg := DefaultCompactConfig("CLAUDEAI", "claude-sonnet-5")
	cfg.MinKeepRecent = 4

	var gotSegment string
	cfg.ExternalSummarizer = func(_ context.Context, segment string, budget int, instruction string) (string, error) {
		gotSegment = segment
		if budget <= 0 || instruction != "" {
			t.Fatalf("budget=%d instruction=%q", budget, instruction)
		}
		return "ENGINE SUMMARY", nil
	}
	out, err := hc.structuredSummarize(context.Background(), compactableHistory(), stub, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if stub.calls != 0 || !strings.Contains(out[0].Content, "ENGINE SUMMARY") || !strings.Contains(gotSegment, "[user]:") {
		t.Fatalf("engine must produce the summary without the embedded call: calls=%d first=%q", stub.calls, out[0].Content[:40])
	}

	cfg.ExternalSummarizer = func(context.Context, string, int, string) (string, error) { return "", errors.New("engine down") }
	out, err = hc.structuredSummarize(context.Background(), compactableHistory(), stub, cfg)
	if err != nil || stub.calls != 1 || !strings.Contains(out[0].Content, "EMBEDDED SUMMARY") {
		t.Fatalf("engine failure must fall back: err=%v calls=%d", err, stub.calls)
	}
}
