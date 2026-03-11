package client

import (
	"context"
	"fmt"
	"testing"

	"github.com/diillson/chatcli/models"
)

// streamTestMock is a simple LLMClient for testing.
type streamTestMock struct {
	response string
	err      error
}

func (m *streamTestMock) GetModelName() string { return "mock-model" }
func (m *streamTestMock) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	return m.response, m.err
}

func TestCollectStream(t *testing.T) {
	ch := make(chan StreamChunk, 3)
	ch <- StreamChunk{Text: "hello "}
	ch <- StreamChunk{Text: "world"}
	ch <- StreamChunk{Done: true, Usage: &UsageInfo{InputTokens: 10, OutputTokens: 5}}
	close(ch)

	text, usage, err := CollectStream(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello world" {
		t.Fatalf("expected 'hello world', got %q", text)
	}
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 5 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestCollectStreamError(t *testing.T) {
	ch := make(chan StreamChunk, 2)
	ch <- StreamChunk{Text: "partial"}
	ch <- StreamChunk{Error: fmt.Errorf("connection lost")}
	close(ch)

	text, _, err := CollectStream(ch)
	if err == nil {
		t.Fatal("expected error")
	}
	if text != "partial" {
		t.Fatalf("expected partial text 'partial', got %q", text)
	}
}

func TestStreamFromSync(t *testing.T) {
	ch := StreamFromSync(context.Background(), func(_ context.Context) (string, error) {
		return "sync result", nil
	})

	text, _, err := CollectStream(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "sync result" {
		t.Fatalf("expected 'sync result', got %q", text)
	}
}

func TestStreamFromSyncError(t *testing.T) {
	ch := StreamFromSync(context.Background(), func(_ context.Context) (string, error) {
		return "", fmt.Errorf("api error")
	})

	_, _, err := CollectStream(ch)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "api error" {
		t.Fatalf("expected 'api error', got %q", err.Error())
	}
}

func TestAsStreamingClient_WithNonStreamingClient(t *testing.T) {
	mock := &streamTestMock{response: "wrapped response"}
	sc := AsStreamingClient(mock)

	if sc.GetModelName() != "mock-model" {
		t.Fatalf("expected 'mock-model', got %q", sc.GetModelName())
	}

	ch, err := sc.SendPromptStream(context.Background(), "test", nil, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text, _, err := CollectStream(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "wrapped response" {
		t.Fatalf("expected 'wrapped response', got %q", text)
	}
}

func TestAsStreamingClient_WithStreamingClient(t *testing.T) {
	mock := &streamTestMock{response: "direct"}
	wrapped := AsStreamingClient(mock)
	// Wrapping a StreamingClient should return it directly
	sc := AsStreamingClient(wrapped)

	ch, err := sc.SendPromptStream(context.Background(), "test", nil, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	text, _, err := CollectStream(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "direct" {
		t.Fatalf("expected 'direct', got %q", text)
	}
}
