/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/diillson/chatcli/models"
)

func TestConverseSupportsCachePoint_Gate(t *testing.T) {
	for _, m := range []string{"amazon.nova-pro-v1:0", "us.amazon.nova-premier-v1:0", "anthropic.claude-sonnet-5", "global.anthropic.claude-opus-5"} {
		if !converseSupportsCachePoint(m) {
			t.Fatalf("%s must honor cachePoint", m)
		}
	}
	for _, m := range []string{"meta.llama4-maverick", "mistral.mistral-large", "deepseek.r1-v1:0", "cohere.command-r"} {
		if converseSupportsCachePoint(m) {
			t.Fatalf("%s rejects cachePoint and must not receive it", m)
		}
	}
}

func TestApplyConverseCachePoints_SystemAndLastUser(t *testing.T) {
	history := []models.Message{
		{Role: "system", Content: "stable prefix"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "second"},
	}
	messages, system := buildConverseMessages("second", history)
	system, messages = applyConverseCachePoints(system, messages)

	if len(system) != 2 {
		t.Fatalf("expected text + cachePoint system blocks, got %d", len(system))
	}
	if _, ok := system[1].(*bedrockruntimetypes.SystemContentBlockMemberCachePoint); !ok {
		t.Fatalf("last system block must be a cachePoint, got %T", system[1])
	}
	last := messages[len(messages)-1]
	if last.Role != bedrockruntimetypes.ConversationRoleUser {
		t.Fatalf("last message role = %s", last.Role)
	}
	if _, ok := last.Content[len(last.Content)-1].(*bedrockruntimetypes.ContentBlockMemberCachePoint); !ok {
		t.Fatalf("last user content must end with a cachePoint, got %T", last.Content[len(last.Content)-1])
	}
	// Earlier messages untouched.
	for _, blk := range messages[0].Content {
		if _, ok := blk.(*bedrockruntimetypes.ContentBlockMemberCachePoint); ok {
			t.Fatal("only the last user message may carry the cachePoint")
		}
	}
}
