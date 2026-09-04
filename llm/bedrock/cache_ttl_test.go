/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockruntimetypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

func TestSupportsExtendedCacheTTL_Gate(t *testing.T) {
	yes := []string{"anthropic.claude-sonnet-4-6", "anthropic.claude-opus-4-8", "anthropic.claude-sonnet-5", "anthropic.claude-fable-5-1", "anthropic.claude-haiku-4-5-20251001-v1:0", "us.anthropic.claude-opus-4-5-20251101-v1:0"}
	no := []string{"anthropic.claude-3-7-sonnet-20250219-v1:0", "anthropic.claude-3-5-sonnet-20241022-v2:0", "amazon.nova-pro-v1:0", "meta.llama4", "openai.gpt-5.6-terra"}
	for _, m := range yes {
		if !extendedCacheTTLModel(m) {
			t.Fatalf("%s must support the 1h TTL", m)
		}
	}
	for _, m := range no {
		if extendedCacheTTLModel(m) {
			t.Fatalf("%s must not get a 1h TTL", m)
		}
	}
}

func TestConverseCacheTTL_FollowsEnvAndModel(t *testing.T) {
	t.Setenv(client.PromptCacheTTLEnv, "1h")
	if converseCacheTTL("anthropic.claude-sonnet-5") != bedrockruntimetypes.CacheTTLOneHour {
		t.Fatal("Claude 5 on Converse must carry the 1h ttl when configured")
	}
	if converseCacheTTL("amazon.nova-pro-v1:0") != "" {
		t.Fatal("Nova keeps the wire default")
	}
	t.Setenv(client.PromptCacheTTLEnv, "5m")
	if converseCacheTTL("anthropic.claude-sonnet-5") != "" {
		t.Fatal("5m stays the wire default (no ttl field)")
	}
	sys := []bedrockruntimetypes.SystemContentBlock{&bedrockruntimetypes.SystemContentBlockMemberText{Value: "sys"}}
	msgs := []bedrockruntimetypes.Message{{Role: bedrockruntimetypes.ConversationRoleUser, Content: []bedrockruntimetypes.ContentBlock{&bedrockruntimetypes.ContentBlockMemberText{Value: "hi"}}}}
	sys, msgs = applyConverseCachePoints(sys, msgs, bedrockruntimetypes.CacheTTLOneHour)
	cp, ok := sys[len(sys)-1].(*bedrockruntimetypes.SystemContentBlockMemberCachePoint)
	if !ok || cp.Value.Ttl != bedrockruntimetypes.CacheTTLOneHour {
		t.Fatalf("system cachePoint must carry the ttl: %#v", sys[len(sys)-1])
	}
	last := msgs[0].Content[len(msgs[0].Content)-1].(*bedrockruntimetypes.ContentBlockMemberCachePoint)
	if last.Value.Ttl != bedrockruntimetypes.CacheTTLOneHour {
		t.Fatal("message cachePoint must carry the ttl")
	}
}

func TestCaptureConverseUsage_SplitsTheHourWriteShare(t *testing.T) {
	c := &BedrockClient{logger: zap.NewNop()}
	out := &bedrockruntime.ConverseOutput{Usage: &bedrockruntimetypes.TokenUsage{
		InputTokens: aws.Int32(10), OutputTokens: aws.Int32(2), CacheWriteInputTokens: aws.Int32(500), CacheReadInputTokens: aws.Int32(100),
		CacheDetails: []bedrockruntimetypes.CacheDetail{{InputTokens: aws.Int32(300), Ttl: bedrockruntimetypes.CacheTTLOneHour}, {InputTokens: aws.Int32(200), Ttl: bedrockruntimetypes.CacheTTLFiveMinutes}},
	}}
	c.captureConverseUsage(out)
	u := c.LastUsage()
	if u == nil || u.CacheCreationInputTokens != 500 || u.CacheCreation1hInputTokens != 300 || u.CacheReadInputTokens != 100 {
		t.Fatalf("usage = %+v", u)
	}
}
