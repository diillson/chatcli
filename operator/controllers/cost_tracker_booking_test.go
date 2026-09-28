/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Each call is priced at its own model's rate when booked; a later call on
// another model does not reprice the earlier ones.
func TestCostTracker_BookingsKeepTheirOwnPrice(t *testing.T) {
	t.Setenv("CHATCLI_MODEL_PRICING", "")
	ct := &CostTracker{client: fake.NewClientBuilder().Build()}
	ctx := context.Background()
	ref := platformv1alpha1.IssueRef{Name: "mixed"}
	if err := ct.RecordLLMCost(ctx, ref, "default", "CLAUDEAI", "claude-sonnet-5", 1_000_000, 0); err != nil {
		t.Fatal(err)
	}
	first, _ := ct.GetIncidentCost(ctx, "mixed", "default")
	cheap := ct.getTokenPricing(ctx, "default", "MINIMAX", "unknown-model")
	if err := ct.RecordAgenticStep(ctx, ref, "default", "MINIMAX", "unknown-model", 1_000_000, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := ct.GetIncidentCost(ctx, "mixed", "default")
	want := first.LLMCosts.EstimatedCostUSD + cheap.InputPerMillion
	if !near(got.LLMCosts.EstimatedCostUSD, want) {
		t.Fatalf("cost = %v, want %v (first call at its own rate + second at its own)", got.LLMCosts.EstimatedCostUSD, want)
	}
	if got.LLMCosts.Model != "unknown-model" || got.LLMCosts.TotalInputTokens != 2_000_000 {
		t.Fatalf("ledger = %+v", got.LLMCosts)
	}
}

// A conflicting write is retried, so concurrent bookings are not lost.
func TestCostTracker_ConflictIsRetried(t *testing.T) {
	ctx := context.Background()
	conflicts := 1
	c := fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if conflicts > 0 {
				conflicts--
				return apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, obj.GetName(), errors.New("stale"))
			}
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	ct := &CostTracker{client: c}
	for _, name := range []string{"one", "two"} {
		if err := ct.RecordLLMCost(ctx, platformv1alpha1.IssueRef{Name: name}, "default", "CLAUDEAI", "claude-sonnet-5", 1000, 10); err != nil {
			t.Fatalf("booking %s: %v", name, err)
		}
	}
	for _, name := range []string{"one", "two"} {
		if _, err := ct.GetIncidentCost(ctx, name, "default"); err != nil {
			t.Fatalf("booking %s lost: %v", name, err)
		}
	}
}

// A ledger that cannot be read is an error, not an all-zero summary; a
// namespace with no ledger yet is an empty summary.
func TestCostTracker_SummaryErrors(t *testing.T) {
	ctx := context.Background()
	empty := &CostTracker{client: fake.NewClientBuilder().Build()}
	if s, err := empty.GetCostSummary(ctx, "nowhere", time.Hour); err != nil || s.IncidentCount != 0 {
		t.Fatalf("no ledger: %+v, %v; want an empty summary", s, err)
	}
	broken := &CostTracker{client: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return errors.New("etcd unavailable")
		},
	}).Build()}
	if _, err := broken.GetCostSummary(ctx, "default", time.Hour); err == nil {
		t.Fatal("a failed ledger read must surface")
	}
	if err := broken.RecordLLMCost(ctx, platformv1alpha1.IssueRef{Name: "x"}, "default", "CLAUDEAI", "claude-sonnet-5", 1, 1); err == nil {
		t.Fatal("a booking whose ledger cannot be read must fail, not overwrite it")
	}
}

// Summaries honor both ends of an absolute period.
func TestCostTracker_SummaryForPeriod(t *testing.T) {
	ctx := context.Background()
	ct := &CostTracker{client: fake.NewClientBuilder().Build()}
	if err := ct.RecordLLMCost(ctx, platformv1alpha1.IssueRef{Name: "now"}, "default", "CLAUDEAI", "claude-sonnet-5", 1000, 0); err != nil {
		t.Fatal(err)
	}
	past, err := ct.GetCostSummaryForPeriod(ctx, "default", time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	if err != nil || past.IncidentCount != 0 {
		t.Fatalf("past period = %+v, %v; want nothing booked", past, err)
	}
	current, err := ct.GetCostSummaryForPeriod(ctx, "default", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil || current.IncidentCount != 1 {
		t.Fatalf("current period = %+v, %v; want the booking", current, err)
	}
}
