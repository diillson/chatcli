/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
)

// budgetModel registers a Bedrock model whose entry claims task_budget,
// the way a catalog entry will once AWS serves the feature. No shipped
// entry does.
func budgetModel(t *testing.T) string {
	t.Helper()
	const id = "anthropic.claude-task-budget-probe"
	catalog.Register(catalog.ModelMeta{
		ID:           id,
		Aliases:      []string{id},
		Provider:     catalog.ProviderBedrock,
		PreferredAPI: catalog.APIAnthropicMessages,
		Capabilities: []string{"tools", "adaptive_thinking", "output_effort", "task_budget"},
	})
	return id
}

// TestBedrockTaskBudget_NotSentOnShippedMirrors pins the platform fact:
// Anthropic does not list Amazon Bedrock among the platforms that serve
// task budgets, so no shipped Claude mirror may send the field or the
// beta flag — a run under a spending ceiling used to send both.
func TestBedrockTaskBudget_NotSentOnShippedMirrors(t *testing.T) {
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudgetFor(400000, 120000))
	for _, model := range []string{
		"global.anthropic.claude-fable-5-2", "anthropic.claude-fable-5-1", "anthropic.claude-fable-5",
		"global.anthropic.claude-mythos-5-2", "anthropic.claude-mythos-5-1", "anthropic.claude-mythos-5",
		"anthropic.claude-opus-5-5", "anthropic.claude-opus-5", "anthropic.claude-sonnet-5",
		"global.anthropic.claude-sonnet-5-5", "global.anthropic.claude-haiku-5-5",
		"global.anthropic.claude-opus-4-8", "global.anthropic.claude-opus-4-7",
		"claude-opus-5-5", "us.anthropic.claude-opus-5-5",
	} {
		if catalog.HasCapability(catalog.ProviderBedrock, model, "task_budget") {
			t.Errorf("%s must not advertise task_budget on Bedrock", model)
		}
		body := map[string]interface{}{"max_tokens": 4096}
		sent := applyBedrockTaskBudget(body, model, ctx)
		applyBedrockTaskBudgetBeta(body, sent)
		if sent {
			t.Errorf("%s: a task budget was attached", model)
		}
		if _, ok := body["output_config"]; ok {
			t.Errorf("%s: output_config created: %+v", model, body["output_config"])
		}
		if _, ok := body["anthropic_beta"]; ok {
			t.Errorf("%s: beta flag sent: %v", model, body["anthropic_beta"])
		}
	}
}

// TestBedrockTaskBudget_TravelsOnTheMirrors keeps the wire path
// working for the day an entry carries the capability: the budget goes in
// output_config and the beta flag in the body, once.
func TestBedrockTaskBudget_TravelsOnTheMirrors(t *testing.T) {
	model := budgetModel(t)
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudgetFor(400000, 120000))
	body := map[string]interface{}{"max_tokens": 4096}

	if !applyBedrockTaskBudget(body, model, ctx) {
		t.Fatal("an entry that claims task_budget must send it")
	}
	cfg, ok := body["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("output_config missing: %+v", body["output_config"])
	}
	budget, ok := cfg["task_budget"].(*client.TaskBudget)
	if !ok || budget.Total != 400000 || budget.Remaining != 120000 {
		t.Fatalf("task_budget = %+v", cfg["task_budget"])
	}

	// InvokeModel takes beta flags in the body, and only on a request that
	// carries one.
	applyBedrockTaskBudgetBeta(body, true)
	betas, _ := body["anthropic_beta"].([]string)
	if len(betas) != 1 || betas[0] != client.TaskBudgetBeta {
		t.Fatalf("anthropic_beta = %v", body["anthropic_beta"])
	}
	applyBedrockTaskBudgetBeta(body, true)
	if betas, _ = body["anthropic_beta"].([]string); len(betas) != 1 {
		t.Fatalf("beta duplicated: %v", betas)
	}

	empty := map[string]interface{}{}
	applyBedrockTaskBudgetBeta(empty, false)
	if _, ok := empty["anthropic_beta"]; ok {
		t.Fatal("a request carrying no budget must carry no beta")
	}
}

// TestBedrockTaskBudget_TravelsWithoutAnEffort mirrors the first-party
// regression: the ceiling is not a reasoning setting, and the effort
// routing it used to live inside returns early when no effort was chosen.
func TestBedrockTaskBudget_TravelsWithoutAnEffort(t *testing.T) {
	model := budgetModel(t)
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudget(64000))
	body := map[string]interface{}{"max_tokens": 4096}
	if applyAnthropicThinkingForEffort(body, model, ctx) {
		t.Fatal("precondition: no effort means no thinking block")
	}
	if !applyBedrockTaskBudget(body, model, ctx) {
		t.Fatal("a spending ceiling must travel whether or not an effort was chosen")
	}
}

// TestBedrockTaskBudget_OnlyForModelsThatReadIt keeps the field off a
// model whose entry does not claim it.
func TestBedrockTaskBudget_OnlyForModelsThatReadIt(t *testing.T) {
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudget(64000))
	body := map[string]interface{}{"max_tokens": 4096}
	if applyBedrockTaskBudget(body, "anthropic.claude-3-5-sonnet-20241022-v2:0", ctx) {
		t.Fatal("a model that does not read the field must not be sent it")
	}
	if _, ok := body["output_config"]; ok {
		t.Fatal("no output_config may be created for it")
	}
}
