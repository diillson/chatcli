/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openairesponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// From GPT-6.2 on the Responses API appends a top-level `instructions`
// after `input`; earlier generations keep it in front.
func TestInstructionsTrailInput(t *testing.T) {
	for model, want := range map[string]bool{
		"gpt-6.2-sol":              true,
		"gpt-6.2-astra":            true,
		"GPT-6.2-Sol":              true,
		"gpt-6.2-sol-2026-11-17":   true,
		"gpt-6.3-luna":             true,
		"gpt-6.10-sol":             true,
		"gpt-7":                    true,
		"gpt-7-astra":              true,
		"gpt-6.1-sol":              false,
		"gpt-6.1-terra":            false,
		"gpt-6-astra":              false,
		"gpt-6-sol":                false,
		"gpt-5.6-sol":              false,
		"gpt-5.5":                  false,
		"gpt-4o":                   false,
		"o3":                       false,
		"":                         false,
		"some-custom-model":        false,
		"gpt-oss-120b":             false,
		"openai/gpt-6.2-sol-proxy": false,
	} {
		if got := instructionsTrailInput(model); got != want {
			t.Errorf("instructionsTrailInput(%q) = %v, want %v", model, got, want)
		}
	}
}

// structuredBody sends one image turn through SendPrompt and returns the
// request body the API received.
func structuredBody(t *testing.T, model string) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"ok","status":"completed"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_RESPONSES_API_URL", srv.URL)

	c := NewOpenAIResponsesClient(testProvider("k"), model, zap.NewNop(), 1, 0)
	history := []models.Message{
		{Role: "system", Content: "be terse"},
		{Role: "user", Content: "describe", Images: []models.ImageContent{{MediaType: "image/png", Data: []byte("x")}}},
	}
	if _, err := c.SendPrompt(context.Background(), "describe", history, 256); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if body == nil {
		t.Fatal("the API received no request")
	}
	return body
}

// On GPT-6.2 the system prompt travels as a developer message heading the
// input, and the top-level field is not sent at all.
func TestStructuredRequest_GPT62LeadsWithDeveloperMessage(t *testing.T) {
	body := structuredBody(t, "gpt-6.2-sol")
	if _, sent := body["instructions"]; sent {
		t.Errorf("gpt-6.2 must not carry top-level instructions: %v", body["instructions"])
	}
	input, _ := body["input"].([]interface{})
	if len(input) != 2 {
		t.Fatalf("want developer + user items, got %d: %v", len(input), body["input"])
	}
	lead, _ := input[0].(map[string]interface{})
	if lead["role"] != "developer" || lead["content"] != "be terse" {
		t.Errorf("first input item = %v, want the developer message with the system prompt", lead)
	}
	if user, _ := input[1].(map[string]interface{}); user["role"] != "user" {
		t.Errorf("second input item = %v, want the user turn", user)
	}
}

// Up to GPT-6.1 the request is unchanged: top-level instructions, and an
// input that starts at the conversation.
func TestStructuredRequest_GPT61KeepsInstructions(t *testing.T) {
	body := structuredBody(t, "gpt-6.1-sol")
	if body["instructions"] != "be terse" {
		t.Errorf("instructions = %v, want the system prompt", body["instructions"])
	}
	input, _ := body["input"].([]interface{})
	if len(input) != 1 {
		t.Fatalf("want the user item alone, got %d: %v", len(input), body["input"])
	}
	if user, _ := input[0].(map[string]interface{}); user["role"] != "user" {
		t.Errorf("input item = %v, want the user turn", user)
	}
}
