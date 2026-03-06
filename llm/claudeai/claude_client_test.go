package claudeai

import (
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

func newTestClient() *ClaudeClient {
	logger, _ := zap.NewDevelopment()
	return &ClaudeClient{
		apiKey: "test-key",
		model:  "claude-sonnet-4-20250514",
		logger: logger,
	}
}

func TestBuildMessagesAndSystem_PlainSystemString(t *testing.T) {
	c := newTestClient()
	history := []models.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
	}

	messages, systemObj := c.buildMessagesAndSystem("Hello", history)

	// System should be a plain string (no SystemParts)
	systemStr, ok := systemObj.(string)
	if !ok {
		t.Fatalf("expected system to be a string, got %T", systemObj)
	}
	if systemStr != "You are a helpful assistant." {
		t.Errorf("unexpected system string: %q", systemStr)
	}

	// Should have exactly one user message (deduplicated since prompt == last user content)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0]["role"] != "user" {
		t.Errorf("expected user role, got %q", messages[0]["role"])
	}
}

func TestBuildMessagesAndSystem_StructuredSystemBlocks(t *testing.T) {
	c := newTestClient()
	history := []models.Message{
		{
			Role: "system",
			SystemParts: []models.ContentBlock{
				{
					Type: "text",
					Text: "Base system prompt",
				},
				{
					Type: "text",
					Text: "Attached context data with lots of code...",
					CacheControl: &models.CacheControl{
						Type: "ephemeral",
					},
				},
			},
		},
		{Role: "user", Content: "Explain the codebase"},
	}

	messages, systemObj := c.buildMessagesAndSystem("Explain the codebase", history)

	// System should be a slice of structured blocks
	blocks, ok := systemObj.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected system to be []map[string]interface{}, got %T", systemObj)
	}

	if len(blocks) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(blocks))
	}

	// First block: no cache_control
	if blocks[0]["type"] != "text" {
		t.Errorf("block 0: expected type 'text', got %v", blocks[0]["type"])
	}
	if blocks[0]["text"] != "Base system prompt" {
		t.Errorf("block 0: unexpected text: %v", blocks[0]["text"])
	}
	if blocks[0]["cache_control"] != nil {
		t.Errorf("block 0: expected no cache_control, got %v", blocks[0]["cache_control"])
	}

	// Second block: has cache_control
	if blocks[1]["type"] != "text" {
		t.Errorf("block 1: expected type 'text', got %v", blocks[1]["type"])
	}
	if blocks[1]["text"] != "Attached context data with lots of code..." {
		t.Errorf("block 1: unexpected text: %v", blocks[1]["text"])
	}
	cc, ok := blocks[1]["cache_control"].(map[string]string)
	if !ok {
		t.Fatalf("block 1: expected cache_control to be map[string]string, got %T", blocks[1]["cache_control"])
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("block 1: expected cache_control type 'ephemeral', got %q", cc["type"])
	}

	// Messages: one user message
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
}

func TestBuildMessagesAndSystem_MixedSystemPartsAndPlain(t *testing.T) {
	c := newTestClient()
	history := []models.Message{
		{
			Role:    "system",
			Content: "Plain system instruction",
		},
		{
			Role: "system",
			SystemParts: []models.ContentBlock{
				{
					Type: "text",
					Text: "Structured context",
					CacheControl: &models.CacheControl{
						Type: "ephemeral",
					},
				},
			},
		},
		{Role: "user", Content: "What does this code do?"},
	}

	_, systemObj := c.buildMessagesAndSystem("What does this code do?", history)

	// When structured blocks exist, ALL system parts (including plain) become blocks
	blocks, ok := systemObj.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected system to be []map[string]interface{}, got %T", systemObj)
	}

	// Should have 2 blocks: 1 structured + 1 plain converted to block
	if len(blocks) != 2 {
		t.Fatalf("expected 2 system blocks, got %d", len(blocks))
	}

	// The structured block comes first (processed in history order)
	if blocks[0]["text"] != "Structured context" {
		t.Errorf("block 0: expected structured context first, got %v", blocks[0]["text"])
	}

	// The plain block is appended after structured blocks
	if blocks[1]["text"] != "Plain system instruction" {
		t.Errorf("block 1: expected plain instruction, got %v", blocks[1]["text"])
	}
	// Plain-converted block should not have cache_control
	if blocks[1]["cache_control"] != nil {
		t.Errorf("block 1: plain block should not have cache_control")
	}
}

func TestBuildMessagesAndSystem_NoSystem(t *testing.T) {
	c := newTestClient()
	history := []models.Message{
		{Role: "user", Content: "Hi"},
	}

	messages, systemObj := c.buildMessagesAndSystem("Hi", history)

	if systemObj != nil {
		t.Errorf("expected nil system, got %v", systemObj)
	}

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
}

func TestBuildMessagesAndSystem_PromptAppendedWhenNotInHistory(t *testing.T) {
	c := newTestClient()
	history := []models.Message{
		{Role: "user", Content: "First message"},
		{Role: "assistant", Content: "Reply"},
	}

	messages, _ := c.buildMessagesAndSystem("Second message", history)

	// Should have 3 messages: first user, assistant, new prompt
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[2]["content"] != "Second message" {
		t.Errorf("expected last message to be the prompt, got %q", messages[2]["content"])
	}
}
