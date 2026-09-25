/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"math"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestCostTracker_ConfigMapPricingOutranksEngine pins the precedence: an
// explicit chatcli-cost-config entry is the cluster operator's word and
// wins over the shared engine, which still prices every other provider.
func TestCostTracker_ConfigMapPricingOutranksEngine(t *testing.T) {
	t.Setenv("CHATCLI_MODEL_PRICING", "")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: costConfigCM, Namespace: "default"},
		Data:       map[string]string{"pricing": `{"OPENAI":{"InputPerMillion":1,"OutputPerMillion":2}}`},
	}
	ct := &CostTracker{client: fake.NewClientBuilder().WithObjects(cm).Build()}
	ctx := context.Background()

	got := ct.getTokenPricing(ctx, "default", "OPENAI", "gpt-5.6-sol")
	if !near(got.InputPerMillion, 1) || !near(got.OutputPerMillion, 2) {
		t.Fatalf("configmap entry must win, got %+v", got)
	}
	got = ct.getTokenPricing(ctx, "default", "CLAUDEAI", "claude-opus-5-5")
	if !near(got.InputPerMillion, 4) || !near(got.OutputPerMillion, 20) {
		t.Fatalf("engine must price providers the configmap does not list, got %+v", got)
	}
}

// TestCostTracker_LedgerPricesThroughEngine records two calls of one
// incident and checks the ledger carries the engine's list price for the
// model, not a per-provider guess.
func TestCostTracker_LedgerPricesThroughEngine(t *testing.T) {
	t.Setenv("CHATCLI_MODEL_PRICING", "")
	ct := &CostTracker{client: fake.NewClientBuilder().Build()}
	ctx := context.Background()
	ref := platformv1alpha1.IssueRef{Name: "issue-1"}

	// Sonnet 5: 1M input x $2 + 100K output x $10.
	if err := ct.RecordLLMCost(ctx, ref, "default", "CLAUDEAI", "claude-sonnet-5", 1_000_000, 100_000); err != nil {
		t.Fatalf("RecordLLMCost: %v", err)
	}
	cost, err := ct.GetIncidentCost(ctx, "issue-1", "default")
	if err != nil {
		t.Fatalf("GetIncidentCost: %v", err)
	}
	if !near(cost.LLMCosts.EstimatedCostUSD, 3.0) || cost.LLMCosts.AnalysisCalls != 1 {
		t.Fatalf("after analysis call: %+v", cost.LLMCosts)
	}
	if cost.LLMCosts.Model != "claude-sonnet-5" || cost.LLMCosts.Provider != "CLAUDEAI" {
		t.Fatalf("ledger must remember provider and model: %+v", cost.LLMCosts)
	}

	// An agentic step adds 500K input: the cumulative ledger re-prices.
	if err := ct.RecordAgenticStep(ctx, ref, "default", "CLAUDEAI", "claude-sonnet-5", 500_000, 0); err != nil {
		t.Fatalf("RecordAgenticStep: %v", err)
	}
	cost, err = ct.GetIncidentCost(ctx, "issue-1", "default")
	if err != nil {
		t.Fatalf("GetIncidentCost: %v", err)
	}
	if !near(cost.LLMCosts.EstimatedCostUSD, 4.0) || cost.LLMCosts.AgenticSteps != 1 {
		t.Fatalf("after agentic step: %+v", cost.LLMCosts)
	}
	if !near(cost.TotalCostUSD, 4.0) {
		t.Fatalf("total must equal the LLM cost with no downtime: %v", cost.TotalCostUSD)
	}
}
