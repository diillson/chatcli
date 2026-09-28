/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func availabilitySLO(name, ns string) *platformv1alpha1.ServiceLevelObjective {
	return &platformv1alpha1.ServiceLevelObjective{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: "slo-uid"},
		Spec: platformv1alpha1.ServiceLevelObjectiveSpec{
			ServiceName: "checkout",
			Enabled:     true,
			Indicator:   platformv1alpha1.SLOIndicator{Type: platformv1alpha1.SLOIndicatorAvailability, MetricSource: platformv1alpha1.SLOSourceIssues},
			Target:      platformv1alpha1.SLOTarget{Percentage: 99.9, Window: "1d"},
			AlertPolicy: platformv1alpha1.SLOAlertPolicy{PageOnBudgetExhausted: true},
		},
	}
}

// outage is an unresolved Issue of the checkout service detected ago.
func outage(name string, ago time.Duration) *platformv1alpha1.Issue {
	detected := metav1.NewTime(time.Now().Add(-ago))
	return &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: map[string]string{"platform.chatcli.io/service": "checkout"}},
		Spec:       platformv1alpha1.IssueSpec{Severity: platformv1alpha1.IssueSeverityHigh, SignalType: "pod_not_ready", Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "checkout", Namespace: "shop"}},
		Status:     platformv1alpha1.IssueStatus{State: platformv1alpha1.IssueStateAnalyzing, DetectedAt: &detected},
	}
}

func sloFixture(objs ...client.Object) (*SLOReconciler, client.Client) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.ServiceLevelObjective{}, &platformv1alpha1.Issue{}).
		WithObjects(objs...).Build()
	return &SLOReconciler{Client: c, Scheme: s}, c
}

func reconcileSLO(t *testing.T, r *SLOReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "shop"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func exhaustionIssues(t *testing.T, c client.Client) []platformv1alpha1.Issue {
	t.Helper()
	var list platformv1alpha1.IssueList
	if err := c.List(context.Background(), &list, client.InNamespace("shop")); err != nil {
		t.Fatal(err)
	}
	var out []platformv1alpha1.Issue
	for _, iss := range list.Items {
		if iss.Annotations["platform.chatcli.io/slo-window"] == sloBudgetExhaustedWindow {
			out = append(out, iss)
		}
	}
	return out
}

// Budget exhaustion pages once, not once per reconcile, and the page is
// tracked on the SLO until budget comes back.
func TestSLO_BudgetExhaustionPagesOnce(t *testing.T) {
	r, c := sloFixture(availabilitySLO("checkout-availability", "shop"), outage("checkout-down", 2*time.Hour))
	for i := 0; i < 3; i++ {
		reconcileSLO(t, r, "checkout-availability")
		// Issue names carry the second: space the reconciles so a
		// regression would create distinct Issues.
		if i < 2 {
			time.Sleep(1100 * time.Millisecond)
		}
	}
	if got := len(exhaustionIssues(t, c)); got != 1 {
		t.Fatalf("budget-exhausted Issues = %d, want 1", got)
	}
	var slo platformv1alpha1.ServiceLevelObjective
	if err := c.Get(context.Background(), types.NamespacedName{Name: "checkout-availability", Namespace: "shop"}, &slo); err != nil {
		t.Fatal(err)
	}
	if slo.Status.ErrorBudgetRemaining != 0 || slo.Status.ErrorBudgetRemainingPercentage != 0 {
		t.Fatalf("remaining = %v / %v%%, want 0", slo.Status.ErrorBudgetRemaining, slo.Status.ErrorBudgetRemainingPercentage)
	}
	tracked := false
	for _, a := range slo.Status.ActiveAlerts {
		tracked = tracked || a.Window == sloBudgetExhaustedWindow
	}
	if !tracked {
		t.Fatalf("active alerts %+v do not track the exhaustion page", slo.Status.ActiveAlerts)
	}
}

// A status write that lost the alert must not page again while the page's
// Issue is still open.
func TestSLO_OpenExhaustionIssuePreventsSecondPage(t *testing.T) {
	r, c := sloFixture(availabilitySLO("checkout-availability", "shop"), outage("checkout-down", 2*time.Hour))
	reconcileSLO(t, r, "checkout-availability")
	var slo platformv1alpha1.ServiceLevelObjective
	key := types.NamespacedName{Name: "checkout-availability", Namespace: "shop"}
	if err := c.Get(context.Background(), key, &slo); err != nil {
		t.Fatal(err)
	}
	slo.Status.ActiveAlerts = nil
	if err := c.Status().Update(context.Background(), &slo); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	reconcileSLO(t, r, "checkout-availability")
	if got := len(exhaustionIssues(t, c)); got != 1 {
		t.Fatalf("budget-exhausted Issues = %d, want the one still open", got)
	}
}

// The page re-arms when the budget recovers.
func TestSLO_ExhaustionAlertClearsWhenBudgetReturns(t *testing.T) {
	slo := availabilitySLO("checkout-availability", "shop")
	slo.Status.ActiveAlerts = []platformv1alpha1.SLOAlert{{Window: sloBudgetExhaustedWindow, Severity: platformv1alpha1.IssueSeverityCritical}}
	r, c := sloFixture(slo)
	reconcileSLO(t, r, "checkout-availability")
	var got platformv1alpha1.ServiceLevelObjective
	if err := c.Get(context.Background(), types.NamespacedName{Name: "checkout-availability", Namespace: "shop"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Status.ActiveAlerts) != 0 {
		t.Fatalf("active alerts = %+v, want the exhaustion alert cleared", got.Status.ActiveAlerts)
	}
	if got.Status.ErrorBudgetRemainingPercentage != 100 {
		t.Fatalf("remaining%% = %v, want 100", got.Status.ErrorBudgetRemainingPercentage)
	}
}

// The SLO's own violation Issues are not downtime of the service.
func TestSLO_OwnViolationIssuesAreNotDowntime(t *testing.T) {
	own := outage("slo-checkout-availability-budget-exhausted-1", 2*time.Hour)
	own.Labels["platform.chatcli.io/signal"] = "slo_violation"
	own.Spec.SignalType = "slo_violation"
	r, c := sloFixture(availabilitySLO("checkout-availability", "shop"), own)
	reconcileSLO(t, r, "checkout-availability")
	var slo platformv1alpha1.ServiceLevelObjective
	if err := c.Get(context.Background(), types.NamespacedName{Name: "checkout-availability", Namespace: "shop"}, &slo); err != nil {
		t.Fatal(err)
	}
	if slo.Status.CurrentValue != 1 || !slo.Status.TargetMet {
		t.Fatalf("SLI = %v (met %v): the SLO counted its own page as downtime", slo.Status.CurrentValue, slo.Status.TargetMet)
	}
}

// metricSource=prometheus is reported as not evaluated instead of being
// silently computed from Issues.
func TestSLO_PrometheusSourceIsReportedUnsupported(t *testing.T) {
	slo := availabilitySLO("checkout-latency", "shop")
	slo.Spec.Indicator = platformv1alpha1.SLOIndicator{Type: platformv1alpha1.SLOIndicatorLatency, MetricSource: platformv1alpha1.SLOSourcePrometheus, PrometheusQuery: "histogram_quantile(0.99, rate(http_duration_seconds_bucket[5m]))"}
	r, c := sloFixture(slo)
	reconcileSLO(t, r, "checkout-latency")
	var got platformv1alpha1.ServiceLevelObjective
	if err := c.Get(context.Background(), types.NamespacedName{Name: "checkout-latency", Namespace: "shop"}, &got); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, "MetricSourceSupported")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "PrometheusNotEvaluated" {
		t.Fatalf("MetricSourceSupported = %+v, want False/PrometheusNotEvaluated", cond)
	}
}
