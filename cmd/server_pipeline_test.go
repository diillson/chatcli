/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

type recordingPipelineSink struct{ set server.PipelineBackend }

func (r *recordingPipelineSink) SetPipelineBackend(b server.PipelineBackend) { r.set = b }

func TestInitPipelineBackend_OffByDefaultAndExclusiveWithGateway(t *testing.T) {
	t.Setenv(serverPipelineEnv, "")
	t.Setenv("CHATCLI_GATEWAY_IN_SERVER", "")
	sink := &recordingPipelineSink{}
	assert.Nil(t, initPipelineBackend(nil, sink, zap.NewNop()), "off unless asked")
	assert.Nil(t, sink.set)

	t.Setenv(serverPipelineEnv, "true")
	t.Setenv("CHATCLI_GATEWAY_IN_SERVER", "true")
	assert.Nil(t, initPipelineBackend(nil, sink, zap.NewNop()), "two engines in one process are refused")
	assert.Nil(t, sink.set)
	assert.True(t, serverPipelineEnabled())
	t.Setenv(serverPipelineEnv, "TRUE")
	assert.True(t, serverPipelineEnabled())
	t.Setenv(serverPipelineEnv, "yes")
	assert.False(t, serverPipelineEnabled(), "only true enables")
}

func TestPipelineAdapter_DegradesWithoutChatCLI(t *testing.T) {
	a := &pipelineAdapter{b: &rpcBackend{mgr: &fakeLLMManager{}, sessions: map[string][]models.Message{}}}
	ctx := context.Background()
	_, err := a.ChatTurn(ctx, "s", "hi", server.PipelineRunOpts{})
	assert.Error(t, err, "no provider configured, no chat")
	_, err = a.RunCoder(ctx, "s", "task", server.PipelineRunOpts{}, func(string) {})
	assert.Error(t, err, "no ChatCLI, no coder loop")
	_, err = a.RunAgent(ctx, "s", "task", server.PipelineRunOpts{}, nil)
	assert.Error(t, err)
	assert.Empty(t, a.Tools())
	_, err = a.CallTool(ctx, "@git", "status")
	assert.Error(t, err)

	opts := a.runOpts(server.PipelineRunOpts{Provider: "P", Model: "m", Plain: true, Quality: map[string]string{"k": "v"}}, nil)
	assert.Equal(t, "P", opts.Provider)
	assert.Equal(t, "m", opts.Model)
	assert.True(t, opts.Plain)
	assert.Equal(t, "v", opts.Quality["k"])
}
