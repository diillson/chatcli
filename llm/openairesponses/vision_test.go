/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

func TestHistoryHasImages(t *testing.T) {
	none := []models.Message{{Role: "user", Content: "hi"}}
	if historyHasImages(none) {
		t.Error("text-only history must report no images")
	}
	withImg := []models.Message{
		{Role: "user", Content: "look", Images: []models.ImageContent{
			{MediaType: "image/png", Data: []byte("x")},
		}},
	}
	if !historyHasImages(withImg) {
		t.Error("history with an image must report images")
	}
}

func TestBuildOAuthPayload_WithImage(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "be helpful"},
		{Role: "user", Content: "describe", Images: []models.ImageContent{
			{MediaType: "image/png", Data: []byte("x")},
		}},
	}
	instructions, input := buildOAuthPayload(history, "describe")
	if !strings.Contains(instructions, "be helpful") {
		t.Errorf("system text should become instructions, got %q", instructions)
	}
	if len(input) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(input))
	}
	// The user content must serialize to a parts array carrying input_image.
	b, _ := json.Marshal(input[0]["content"])
	if !strings.Contains(string(b), "input_image") {
		t.Errorf("expected input_image part in content, got %s", b)
	}
}
