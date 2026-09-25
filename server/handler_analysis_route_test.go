/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package server

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The operator's analysis RPCs go through the same router as the prompt
// RPCs: attribution and usage come back on the wire, and with nothing
// named in the request the fallback chain answers.
func TestAnalyzeIssue_RoutesThroughChainAndReportsUsage(t *testing.T) {
	down := &routeClient{model: "model-a", errs: []error{assert.AnError}}
	up := &routeClient{model: "model-b", replies: []string{`{"analysis":"OOM kill","confidence":0.9,"recommendations":["raise limits"],"actions":[]}`},
		usage: &models.UsageInfo{PromptTokens: 40, CompletionTokens: 12, IsReal: true}}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{}})
	h.SetFallbackChain(chainOf(down, up))

	resp, err := h.AnalyzeIssue(context.Background(), &pb.AnalyzeIssueRequest{IssueName: "oom-1", Namespace: "prod", Severity: "high"})
	require.NoError(t, err)
	assert.Equal(t, "OOM kill", resp.Analysis)
	assert.Equal(t, "PB", resp.Provider, "the entry that answered")
	assert.Equal(t, "model-b", resp.Model)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, int32(40), resp.Usage.PromptTokens)
	assert.False(t, resp.Usage.Estimated)

	_, err = h.AnalyzeIssue(context.Background(), &pb.AnalyzeIssueRequest{})
	assert.Error(t, err, "issue name is required")
}

func TestAgenticStep_ExplicitModelBypassesChainAndReportsUsage(t *testing.T) {
	step := &routeClient{model: "gpt-6-astra", replies: []string{`{"reasoning":"restart it","resolved":false,"next_action":{"name":"restart","action":"RestartDeployment"}}`},
		usage: &models.UsageInfo{PromptTokens: 9, CompletionTokens: 4, IsReal: true}}
	chainOnly := &routeClient{model: "chain-model", replies: []string{"never"}}
	h := newRouteHandler(&routeManager{byProvider: map[string]client.LLMClient{"OPENAI": step}})
	h.SetFallbackChain(chainOf(chainOnly))

	resp, err := h.AgenticStep(context.Background(), &pb.AgenticStepRequest{IssueName: "crash-2", Provider: "OPENAI", Model: "gpt-6-astra", CurrentStep: 1, MaxSteps: 3})
	require.NoError(t, err)
	assert.Equal(t, "restart it", resp.Reasoning)
	assert.Equal(t, "OPENAI", resp.Provider)
	assert.Equal(t, "gpt-6-astra", resp.Model)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, int32(4), resp.Usage.CompletionTokens)
	assert.Equal(t, int32(0), chainOnly.calls, "an explicit provider and model never touch the chain")

	// A route the manager cannot build surfaces as an error, not a panic.
	_, err = h.AgenticStep(context.Background(), &pb.AgenticStepRequest{IssueName: "x", Provider: "NOPE"})
	assert.Error(t, err)
}
