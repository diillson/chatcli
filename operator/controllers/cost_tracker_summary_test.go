/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// The summary aggregates one namespace or, with none given, every ledger
// the tracker created; entries booked before the window are left out and
// entries from before RecordedAt existed still count.
func TestCostTracker_SummaryAcrossNamespacesAndWindow(t *testing.T) {
	ctx := context.Background()
	old, _ := json.Marshal(IncidentCost{IssueName: "old", LLMCosts: LLMCostBreakdown{EstimatedCostUSD: 5}, TotalCostUSD: 5, RecordedAt: time.Now().Add(-48 * time.Hour)})
	legacy, _ := json.Marshal(IncidentCost{IssueName: "legacy", LLMCosts: LLMCostBreakdown{EstimatedCostUSD: 1}, TotalCostUSD: 1})
	staging := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: costLedgerCM, Namespace: "staging", Labels: map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator"}},
		Data:       map[string]string{"old": string(old), "legacy": string(legacy)},
	}
	ct := &CostTracker{client: fake.NewClientBuilder().WithObjects(staging).Build()}

	ref := platformv1alpha1.IssueRef{Name: "fresh"}
	if err := ct.RecordLLMCost(ctx, ref, "production", "CLAUDEAI", "claude-sonnet-5", 1_000_000, 0); err != nil {
		t.Fatal(err)
	}
	fresh, err := ct.GetIncidentCost(ctx, "fresh", "production")
	if err != nil || fresh.RecordedAt.IsZero() {
		t.Fatalf("booking must stamp RecordedAt (err=%v, cost=%+v)", err, fresh)
	}

	prod, err := ct.GetCostSummary(ctx, "production", 24*time.Hour)
	if err != nil || prod.IncidentCount != 1 || !near(prod.TotalLLMCost, fresh.TotalCostUSD) {
		t.Fatalf("production summary = %+v (err=%v), want the fresh booking only", prod, err)
	}

	all, err := ct.GetCostSummary(ctx, "", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// fresh (production) + legacy (staging, no stamp); old is outside the window.
	if all.IncidentCount != 2 || !near(all.TotalLLMCost, fresh.TotalCostUSD+1) {
		t.Fatalf("all-namespace summary = %+v, want fresh + legacy", all)
	}
	if !near(all.CostPerIncident, all.TotalLLMCost/2) {
		t.Fatalf("cost per incident = %v", all.CostPerIncident)
	}

	wide, err := ct.GetCostSummary(ctx, "", 30*24*time.Hour)
	if err != nil || wide.IncidentCount != 3 {
		t.Fatalf("30-day summary = %+v (err=%v), want every entry", wide, err)
	}
}
