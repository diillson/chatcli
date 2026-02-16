package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/diillson/chatcli/models"
)

// mockLLMClient is a test double for LLMClient.
type mockLLMClient struct {
	model    string
	response string
	err      error
}

func (m *mockLLMClient) GetModelName() string { return m.model }
func (m *mockLLMClient) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	return m.response, m.err
}

// mockRecorder captures metrics calls for assertion.
type mockRecorder struct {
	requests []recordedRequest
	errors   []recordedError
}

type recordedRequest struct {
	provider, model, status string
	duration                time.Duration
}

type recordedError struct {
	provider, model, errorType string
}

func (r *mockRecorder) RecordRequest(provider, model, status string, duration time.Duration) {
	r.requests = append(r.requests, recordedRequest{provider, model, status, duration})
}
func (r *mockRecorder) RecordError(provider, model, errorType string) {
	r.errors = append(r.errors, recordedError{provider, model, errorType})
}

func TestInstrumentedClient_Success(t *testing.T) {
	inner := &mockLLMClient{model: "gpt-4", response: "Hello!"}
	rec := &mockRecorder{}
	ic := NewInstrumentedClient(inner, rec, "OPENAI")

	if ic.GetModelName() != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", ic.GetModelName())
	}

	resp, err := ic.SendPrompt(context.Background(), "hi", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "Hello!" {
		t.Errorf("expected 'Hello!', got %q", resp)
	}

	if len(rec.requests) != 1 {
		t.Fatalf("expected 1 request recorded, got %d", len(rec.requests))
	}
	if rec.requests[0].status != "success" {
		t.Errorf("expected status 'success', got %q", rec.requests[0].status)
	}
	if rec.requests[0].provider != "OPENAI" {
		t.Errorf("expected provider 'OPENAI', got %q", rec.requests[0].provider)
	}
	if len(rec.errors) != 0 {
		t.Errorf("expected no errors, got %d", len(rec.errors))
	}
}

func TestInstrumentedClient_Error(t *testing.T) {
	inner := &mockLLMClient{model: "claude-3", err: fmt.Errorf("rate limit exceeded (429)")}
	rec := &mockRecorder{}
	ic := NewInstrumentedClient(inner, rec, "CLAUDEAI")

	_, err := ic.SendPrompt(context.Background(), "hi", nil, 0)
	if err == nil {
		t.Fatal("expected error")
	}

	if len(rec.requests) != 1 || rec.requests[0].status != "error" {
		t.Errorf("expected 1 error request, got %v", rec.requests)
	}
	if len(rec.errors) != 1 || rec.errors[0].errorType != "rate_limit" {
		t.Errorf("expected rate_limit error type, got %v", rec.errors)
	}
}

func TestInstrumentedClient_LLMError(t *testing.T) {
	inner := &mockLLMClient{model: "gpt-4", err: &LLMError{Code: 500, Message: "internal server error"}}
	rec := &mockRecorder{}
	ic := NewInstrumentedClient(inner, rec, "OPENAI")

	_, err := ic.SendPrompt(context.Background(), "hi", nil, 0)
	if err == nil {
		t.Fatal("expected error")
	}

	if len(rec.errors) != 1 || rec.errors[0].errorType != "server_error" {
		t.Errorf("expected server_error, got %v", rec.errors)
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"nil", nil, ""},
		{"rate limit text", fmt.Errorf("rate limit exceeded"), "rate_limit"},
		{"timeout text", fmt.Errorf("request timeout"), "timeout"},
		{"deadline exceeded", context.DeadlineExceeded, "timeout"},
		{"cancelled", context.Canceled, "cancelled"},
		{"unauthorized text", fmt.Errorf("unauthorized access"), "auth_error"},
		{"server error text", fmt.Errorf("500 internal server error"), "server_error"},
		{"unknown", fmt.Errorf("something weird happened"), "unknown"},
		{"LLMError 429", &LLMError{Code: 429, Message: "too many requests"}, "rate_limit"},
		{"LLMError 401", &LLMError{Code: 401, Message: "invalid key"}, "auth_error"},
		{"LLMError 500", &LLMError{Code: 500, Message: "server error"}, "server_error"},
		{"LLMError 408", &LLMError{Code: 408, Message: "request timeout"}, "timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyError(tt.err)
			if got != tt.expected {
				t.Errorf("classifyError(%v) = %q, want %q", tt.err, got, tt.expected)
			}
		})
	}
}
