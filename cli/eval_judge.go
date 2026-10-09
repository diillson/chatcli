/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
)

// EvalJudge is the LLM that grades `chatcli eval` judge checks. It sends
// each grading prompt as a fresh, history-free conversation and books what
// it spends on this session's cost tracker, so judge spend lands in the
// user's real daily total like any other call.
type EvalJudge struct {
	cli      *ChatCLI
	client   client.LLMClient
	provider string
	model    string
	// mu serializes calls: usage is read back from the client after each
	// call, and concurrent calls on one client would swap their figures.
	mu sync.Mutex
}

// NewEvalJudge resolves the judge model: the given provider/model, filling a
// missing model with the provider's default, or the session's active model
// when neither is given.
func (cli *ChatCLI) NewEvalJudge(provider, model string) (*EvalJudge, error) {
	provider = strings.ToUpper(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if provider == "" && model == "" {
		if cli.Client == nil {
			return nil, errors.New(i18n.T("oneshot.error.no_provider"))
		}
		return &EvalJudge{cli: cli, client: cli.Client, provider: cli.Provider, model: cli.Model}, nil
	}
	if provider == "" {
		provider = cli.Provider
	}
	if model == "" {
		if provider == cli.Provider {
			model = cli.Model
		} else {
			model = cli.providerDefaultModel(provider)
		}
	}
	if cli.manager == nil {
		return nil, errors.New(i18n.T("oneshot.error.no_provider"))
	}
	c, err := cli.manager.GetClient(provider, model)
	if err != nil {
		return nil, err
	}
	return &EvalJudge{cli: cli, client: c, provider: provider, model: model}, nil
}

// Provider and Model name the judge.
func (j *EvalJudge) Provider() string { return j.provider }

// Model names the judge model by id (the display name when no id is known).
func (j *EvalJudge) Model() string {
	if j.model != "" {
		return j.model
	}
	return j.client.GetModelName()
}

// Send grades one prompt and returns the reply and what the call cost.
func (j *EvalJudge) Send(ctx context.Context, prompt string) (string, float64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	history := []models.Message{{Role: "user", Content: prompt}}
	reply, err := j.client.SendPrompt(ctx, prompt, history, 0)
	// An expired OAuth token: the session refresh reloads the providers;
	// the judge then rebuilds its own client, which may be another provider.
	if err != nil && j.cli.refreshClientOnAuthError(err) {
		if fresh, cerr := j.cli.manager.GetClient(j.provider, j.model); cerr == nil {
			j.client = fresh
		}
		reply, err = j.client.SendPrompt(ctx, prompt, history, 0)
	}
	if err != nil {
		return "", 0, err
	}
	ct := j.cli.costTracker
	if ct == nil {
		return reply, 0, nil
	}
	before := ct.TotalCost()
	ct.RecordRealUsage(j.provider, j.Model(), client.GetUsageOrEstimate(j.client, len(prompt), len(reply)))
	return reply, ct.TotalCost() - before, nil
}
