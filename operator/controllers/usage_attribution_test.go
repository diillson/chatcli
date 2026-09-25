/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUsageTokens_PrefersTheServerReport(t *testing.T) {
	in, out := usageTokens(&pb.TokenUsage{PromptTokens: 1200, CompletionTokens: 80}, 9, 9)
	if in != 1200 || out != 80 {
		t.Errorf("reported tokens expected, got %d/%d", in, out)
	}
	in, out = usageTokens(nil, 500, 25)
	if in != 500 || out != 25 {
		t.Errorf("fallback expected without usage, got %d/%d", in, out)
	}
}

func TestServedProviderModel_FallsBackToTheRequest(t *testing.T) {
	p, m := servedProviderModel("PB", "model-b", "OPENAI", "gpt-6-astra")
	if p != "PB" || m != "model-b" {
		t.Errorf("served attribution wins, got %s/%s", p, m)
	}
	p, m = servedProviderModel("", "", "OPENAI", "gpt-6-astra")
	if p != "OPENAI" || m != "gpt-6-astra" {
		t.Errorf("request is the fallback, got %s/%s", p, m)
	}
}

func TestRecordAgenticStepCost_BooksTheReportedTokens(t *testing.T) {
	issue := &platformv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "crash-9", Namespace: "prod"}}
	r, c := setupAgenticReconciler(&mockAgenticStepper{}, issue)
	r.CostTracker = NewCostTracker(c)

	r.recordAgenticStepCost(context.Background(), issue, &pb.AgenticStepResponse{
		Reasoning: "restart it",
		Provider:  "CLAUDEAI", Model: "claude-sonnet-5",
		Usage: &pb.TokenUsage{PromptTokens: 4000, CompletionTokens: 150},
	})
	cost, err := r.CostTracker.GetIncidentCost(context.Background(), "crash-9", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if cost.LLMCosts.AgenticSteps != 1 || cost.LLMCosts.TotalInputTokens != 4000 || cost.LLMCosts.TotalOutputTokens != 150 {
		t.Errorf("step not booked from the reported usage: %+v", cost.LLMCosts)
	}
	if cost.LLMCosts.Provider != "CLAUDEAI" || cost.LLMCosts.Model != "claude-sonnet-5" {
		t.Errorf("attribution must follow the server: %+v", cost.LLMCosts)
	}
	if cost.LLMCosts.EstimatedCostUSD <= 0 {
		t.Error("a priced model yields a cost")
	}

	// No tracker, nil response: nothing happens, nothing panics.
	r.CostTracker = nil
	r.recordAgenticStepCost(context.Background(), issue, nil)
	r.CostTracker = NewCostTracker(c)
	r.recordAgenticStepCost(context.Background(), issue, nil)
}
