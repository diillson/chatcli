package client

import (
	"context"
	"errors"
	"github.com/diillson/chatcli/llm/session"
	"github.com/diillson/chatcli/models"
	"testing"
)

func TestSessionClient_GetModelName(t *testing.T) {
	mockSession := &session.MockSessionManager{
		GetModelNameFunc: func() string {
			return "test-model"
		},
	}

	client := NewSessionClient(mockSession)

	if client.GetModelName() != "test-model" {
		t.Errorf("Expected model name to be 'test-model', got '%s'", client.GetModelName())
	}
}

func TestSessionClient_SendPrompt_Success(t *testing.T) {
	mockSession := &session.MockSessionManager{
		IsSessionActiveFunc: func() bool {
			return true
		},
		SendMessageFunc: func(ctx context.Context, message string) (string, error) {
			return "Test response", nil
		},
	}

	client := NewSessionClient(mockSession)

	response, err := client.SendPrompt(context.Background(), "Test prompt", []models.Message{})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if response != "Test response" {
		t.Errorf("Expected response to be 'Test response', got '%s'", response)
	}
}

func TestSessionClient_SendPrompt_InitializeSession(t *testing.T) {
	initializeCalled := false

	mockSession := &session.MockSessionManager{
		IsSessionActiveFunc: func() bool {
			return false
		},
		InitializeFunc: func(ctx context.Context) error {
			initializeCalled = true
			return nil
		},
		SendMessageFunc: func(ctx context.Context, message string) (string, error) {
			return "Test response", nil
		},
	}

	client := NewSessionClient(mockSession)

	_, err := client.SendPrompt(context.Background(), "Test prompt", []models.Message{})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if !initializeCalled {
		t.Error("Expected InitializeSession to be called, but it wasn't")
	}
}

func TestSessionClient_SendPrompt_ErrorAndRetry(t *testing.T) {
	initializeCount := 0

	mockSession := &session.MockSessionManager{
		IsSessionActiveFunc: func() bool {
			return true
		},
		InitializeFunc: func(ctx context.Context) error {
			initializeCount++
			return nil
		},
		SendMessageFunc: func(ctx context.Context, message string) (string, error) {
			if initializeCount == 0 {
				return "", errors.New("test error")
			}
			return "Test response after retry", nil
		},
	}

	client := NewSessionClient(mockSession)

	response, err := client.SendPrompt(context.Background(), "Test prompt", []models.Message{})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if initializeCount != 1 {
		t.Errorf("Expected InitializeSession to be called once, but it was called %d times", initializeCount)
	}

	if response != "Test response after retry" {
		t.Errorf("Expected response to be 'Test response after retry', got '%s'", response)
	}
}
