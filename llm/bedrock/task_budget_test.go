/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package bedrock

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/llm/client"
)

// TestBedrockTaskBudget_TravelsOnTheMirrors covers a capability the
// catalog advertised and the package never implemented: task_budget is on
// the Bedrock mirror of every model that reads it, and this package did
// not mention the field anywhere. A run under a spending ceiling was
// paced on the first-party API and not here.
func TestBedrockTaskBudget_TravelsOnTheMirrors(t *testing.T) {
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudgetFor(400000, 120000))
	body := map[string]interface{}{"max_tokens": 4096}

	if !applyBedrockTaskBudget(body, "anthropic.claude-opus-5", ctx) {
		t.Fatal("the Bedrock mirror advertises task_budget and must send it")
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
	ctx := client.WithTaskBudget(context.Background(), client.AnthropicTaskBudget(64000))
	body := map[string]interface{}{"max_tokens": 4096}
	if applyAnthropicThinkingForEffort(body, "anthropic.claude-opus-5", ctx) {
		t.Fatal("precondition: no effort means no thinking block")
	}
	if !applyBedrockTaskBudget(body, "anthropic.claude-opus-5", ctx) {
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
