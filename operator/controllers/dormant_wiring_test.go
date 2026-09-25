/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func gatingClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(
		&platformv1alpha1.RemediationPlan{}, &platformv1alpha1.Issue{}, &platformv1alpha1.AIInsight{},
		&platformv1alpha1.ApprovalRequest{}, &platformv1alpha1.ApprovalPolicy{},
	).WithObjects(objs...).Build()
}

func failedPlan(name string, at time.Time, stampCompleted bool) *platformv1alpha1.RemediationPlan {
	p := newRemediationPlan(name, "default")
	p.UID = types.UID(name)
	p.CreationTimestamp = metav1.NewTime(at)
	p.Status.State = platformv1alpha1.RemediationStateFailed
	if stampCompleted {
		t := metav1.NewTime(at)
		p.Status.CompletedAt = &t
	}
	return p
}

// The breaker counts failures whether or not the failure path stamped
// CompletedAt: a failed plan with only a creation time inside the window
// still counts, one outside it does not.
func TestDecisionEngine_CircuitBreakerCountsUnstampedFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	c := gatingClient(
		failedPlan("f1", now.Add(-10*time.Minute), true),
		failedPlan("f2", now.Add(-20*time.Minute), false),
		failedPlan("f3", now.Add(-30*time.Minute), false),
		failedPlan("old", now.Add(-3*time.Hour), false),
	)
	open, reason, err := (&DecisionEngine{}).IsCircuitBreakerOpen(ctx, c, "default")
	if err != nil || !open || !strings.Contains(reason, "3 remediations failed") {
		t.Fatalf("open=%v reason=%q err=%v; want open with 3 failures", open, reason, err)
	}
	c2 := gatingClient(failedPlan("f1", now.Add(-10*time.Minute), true), failedPlan("old", now.Add(-3*time.Hour), false))
	if open, _, _ := (&DecisionEngine{}).IsCircuitBreakerOpen(ctx, c2, "default"); open {
		t.Fatal("one recent failure must not open the breaker")
	}
}

func TestDecisionEngine_ModesFollowConfidenceAndSeverity(t *testing.T) {
	ctx := context.Background()
	c := gatingClient()
	de := &DecisionEngine{}
	cases := []struct {
		sev        platformv1alpha1.IssueSeverity
		confidence float32
		mode       string
		approval   bool
	}{
		{platformv1alpha1.IssueSeverityLow, 0.99, DecisionModeAuto, false},
		{platformv1alpha1.IssueSeverityMedium, 0.95, DecisionModeAutoNotify, false},
		{platformv1alpha1.IssueSeverityHigh, 0.99, DecisionModeApproval, true},
		{platformv1alpha1.IssueSeverityCritical, 0.99, DecisionModeManual, true},
		{platformv1alpha1.IssueSeverityLow, 0.2, DecisionModeManual, true},
	}
	for _, tc := range cases {
		issue := newIssue("i", "default")
		issue.Spec.Severity = tc.sev
		insight := &platformv1alpha1.AIInsight{Status: platformv1alpha1.AIInsightStatus{Confidence: float64(tc.confidence)}}
		res, err := de.ShouldAutoRemediate(ctx, c, issue, insight, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.Mode != tc.mode || res.RequiresApproval != tc.approval {
			t.Errorf("%s @ %.2f: mode=%s approval=%v; want %s/%v (%s)", tc.sev, tc.confidence, res.Mode, res.RequiresApproval, tc.mode, tc.approval, res.Reason)
		}
	}
	if ClusterTierRequiresApproval("auto", platformv1alpha1.IssueSeverityCritical) ||
		!ClusterTierRequiresApproval("auto-medium-low", platformv1alpha1.IssueSeverityHigh) ||
		ClusterTierRequiresApproval("auto-medium-low", platformv1alpha1.IssueSeverityLow) ||
		!ClusterTierRequiresApproval("manual", platformv1alpha1.IssueSeverityLow) ||
		!ClusterTierRequiresApproval("weird", platformv1alpha1.IssueSeverityLow) {
		t.Fatal("tier mapping does not match the documented policy table")
	}
}

// With the engine on, a critical issue's plan parks under the synthetic
// decision-engine policy with the verdict annotated, and a confident
// medium issue's plan runs with the verdict annotated. With the engine
// off nothing changes.
func TestRemediation_DecisionEngineParksAndAnnotates(t *testing.T) {
	ctx := context.Background()
	mk := func(sev platformv1alpha1.IssueSeverity, confidence float64, engine bool) (*RemediationReconciler, client.Client) {
		issue := newIssue("test-issue", "default")
		issue.Spec.Severity = sev
		insight := &platformv1alpha1.AIInsight{
			ObjectMeta: metav1.ObjectMeta{Name: "test-issue-insight", Namespace: "default"},
			Status:     platformv1alpha1.AIInsightStatus{Confidence: confidence, Analysis: "known cause"},
		}
		plan := newRemediationPlan("plan-1", "default")
		c := gatingClient(issue, insight, plan, newDeployment("web", "default", 2))
		r := &RemediationReconciler{Client: c, Scheme: newScheme(), AuditRecorder: NewAuditRecorder(c, newScheme())}
		if engine {
			r.DecisionEngine = &DecisionEngine{}
		}
		return r, c
	}
	reconcile := func(r *RemediationReconciler) {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "plan-1", Namespace: "default"}}); err != nil {
			t.Fatal(err)
		}
	}
	load := func(c client.Client) platformv1alpha1.RemediationPlan {
		var p platformv1alpha1.RemediationPlan
		if err := c.Get(ctx, types.NamespacedName{Name: "plan-1", Namespace: "default"}, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	r, c := mk(platformv1alpha1.IssueSeverityCritical, 0.99, true)
	reconcile(r)
	p := load(c)
	if p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("critical plan state = %s, want WaitingApproval (%s)", p.Status.State, p.Status.Result)
	}
	if p.Annotations[annotationDecisionMode] != DecisionModeManual || p.Annotations[annotationDecisionConfidence] == "" {
		t.Fatalf("verdict annotations missing: %v", p.Annotations)
	}
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(ctx, types.NamespacedName{Name: "approval-plan-1", Namespace: "default"}, &ar); err != nil {
		t.Fatal(err)
	}
	if ar.Spec.PolicyRef != DecisionEnginePolicyName || ar.Spec.TimeoutMinutes != 30 || ar.Status.State != platformv1alpha1.ApprovalStatePending {
		t.Fatalf("approval request = %+v", ar.Spec)
	}
	var events platformv1alpha1.AuditEventList
	if err := c.List(ctx, &events); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events.Items {
		if e.Spec.EventType == "approval_requested" {
			found = true
		}
	}
	if !found {
		t.Fatal("approval_requested audit event was not recorded")
	}
	// A second reconcile must not fail the plan: the request already exists.
	reconcile(r)
	if p = load(c); p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("re-reconcile moved the plan to %s", p.Status.State)
	}

	r, c = mk(platformv1alpha1.IssueSeverityMedium, 0.95, true)
	reconcile(r)
	p = load(c)
	if p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("confident medium plan state = %s, want Executing (%s)", p.Status.State, p.Status.Result)
	}
	if p.Annotations[annotationDecisionMode] != DecisionModeAutoNotify {
		t.Fatalf("auto-notify verdict not annotated: %v", p.Annotations)
	}

	r, c = mk(platformv1alpha1.IssueSeverityCritical, 0.99, false)
	reconcile(r)
	if p = load(c); p.Status.State != platformv1alpha1.RemediationStateExecuting || p.Annotations[annotationDecisionMode] != "" {
		t.Fatalf("engine off must leave the plan alone: state=%s ann=%v", p.Status.State, p.Annotations)
	}
}

// The approval controller applies the built-in rule to a synthetic
// request: it expires after the rule's timeout instead of requeueing
// forever on a policy that does not exist.
func TestApproval_SyntheticPolicyExpires(t *testing.T) {
	ctx := context.Background()
	ar := &platformv1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "approval-plan-1", Namespace: "default", CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour))},
		Spec:       platformv1alpha1.ApprovalRequestSpec{RemediationPlanRef: "plan-1", PolicyRef: DecisionEnginePolicyName, RuleName: "decision-engine", TimeoutMinutes: 30},
		Status:     platformv1alpha1.ApprovalRequestStatus{State: platformv1alpha1.ApprovalStatePending},
	}
	c := gatingClient(ar)
	r := &ApprovalReconciler{Client: c, Scheme: newScheme()}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: ar.Name, Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.ApprovalRequest
	if err := c.Get(ctx, types.NamespacedName{Name: ar.Name, Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.State != platformv1alpha1.ApprovalStateExpired {
		t.Fatalf("state = %s, want Expired", got.Status.State)
	}
}

// A loop whose last three observations are identical is stopped by the
// convergence detector before it burns its remaining steps.
func TestAgentic_ConvergenceStopsRepeatingLoop(t *testing.T) {
	issue := newIssue("conv-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	plan := newAgenticPlan("conv-plan", "default", "conv-issue")
	started := metav1.NewTime(time.Now().Add(-time.Minute))
	plan.Status.AgenticStartedAt = &started
	for i := 0; i < 3; i++ {
		plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
			Action: &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionScaleDeployment}, Observation: "SUCCESS: replicas already 3",
		})
	}
	mock := &mockAgenticStepper{}
	r, c := setupAgenticReconciler(mock, issue, plan)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "conv-plan", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	if mock.called {
		t.Fatal("AgenticStep must not be called once the loop converged")
	}
	var got platformv1alpha1.RemediationPlan
	if err := c.Get(context.Background(), types.NamespacedName{Name: "conv-plan", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.State != platformv1alpha1.RemediationStateFailed || !strings.Contains(got.Status.Result, "Converged") || !strings.Contains(got.Status.Result, "progress") {
		t.Fatalf("plan = %s %q", got.Status.State, got.Status.Result)
	}
}

// The Issue reconciler survives a nil federation and, with one attached
// and no clusters registered, a detected Issue is left untouched.
func TestIssue_FederationHookIsNilSafeAndQuietWithoutClusters(t *testing.T) {
	ctx := context.Background()
	for _, withFederation := range []bool{false, true} {
		issue := newIssue("fed-issue", "default")
		r, c := setupFakeIssueReconciler(issue)
		if withFederation {
			r.Federation = &FederationReconciler{Client: c, Scheme: newScheme()}
		}
		for i := 0; i < 2; i++ {
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fed-issue", Namespace: "default"}}); err != nil {
				t.Fatal(err)
			}
		}
		var got platformv1alpha1.Issue
		if err := c.Get(ctx, types.NamespacedName{Name: "fed-issue", Namespace: "default"}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.State != platformv1alpha1.IssueStateAnalyzing {
			t.Fatalf("federation=%v: state = %s, want Analyzing", withFederation, got.Status.State)
		}
		if _, ok := got.Annotations["platform.chatcli.io/cross-cluster-correlation"]; ok {
			t.Fatalf("no clusters registered, yet the Issue was correlated")
		}
	}
}

// SLA breaches and notification deliveries land in the audit trail, which
// is what the dashboard's sla_breach and notification_sent filters read.
func TestAudit_SLABreachAndNotificationAreRecorded(t *testing.T) {
	ctx := context.Background()
	s := newScheme()
	issue := newIssue("sla-issue", "default")
	sla := &platformv1alpha1.IncidentSLA{ObjectMeta: metav1.ObjectMeta{Name: "gold", Namespace: "default"}, Spec: platformv1alpha1.IncidentSLASpec{Severity: platformv1alpha1.IssueSeverityHigh, ResponseTime: "1m", ResolutionTime: "1h"}}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&platformv1alpha1.IncidentSLA{}, &platformv1alpha1.Issue{}).WithObjects(issue, sla).Build()
	rec := NewAuditRecorder(c, s)

	(&SLAReconciler{Client: c, Scheme: s, AuditRecorder: rec}).recordViolation(ctx, sla, issue, "resolution", 2*time.Hour, time.Hour)
	(&NotificationReconciler{Client: c, Scheme: s, AuditRecorder: rec}).auditDelivery(ctx, "slack-oncall", issue, false, "webhook 500")

	var events platformv1alpha1.AuditEventList
	if err := c.List(ctx, &events); err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, e := range events.Items {
		types[e.Spec.EventType] = true
	}
	if !types["sla_breach"] || !types["notification_sent"] {
		t.Fatalf("audit events = %v, want sla_breach and notification_sent", types)
	}
}

// The cluster tier parks a high-severity plan on a critical-tier cluster
// under the cluster-tier policy, lets a non-critical cluster run it, and
// proceeds when the registration cannot be found.
func TestRemediation_ClusterTierGate(t *testing.T) {
	ctx := context.Background()
	run := func(tier, clusterName string) (platformv1alpha1.RemediationPlan, client.Client) {
		t.Helper()
		issue := newIssue("test-issue", "default")
		issue.Spec.Severity = platformv1alpha1.IssueSeverityHigh
		plan := newRemediationPlan("plan-1", "default")
		reg := &platformv1alpha1.ClusterRegistration{
			ObjectMeta: metav1.ObjectMeta{Name: "prod-east", Namespace: "default"},
			Spec:       platformv1alpha1.ClusterRegistrationSpec{DisplayName: "Prod East", Environment: "prod", Tier: tier},
			Status:     platformv1alpha1.ClusterRegistrationStatus{Connected: true},
		}
		c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(
			&platformv1alpha1.RemediationPlan{}, &platformv1alpha1.Issue{}, &platformv1alpha1.ApprovalRequest{}, &platformv1alpha1.ClusterRegistration{},
		).WithObjects(issue, plan, reg, newDeployment("web", "default", 2)).Build()
		r := &RemediationReconciler{Client: c, Scheme: newScheme(), ClusterTier: clusterName, Federation: &FederationReconciler{Client: c, Scheme: newScheme()}}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "plan-1", Namespace: "default"}}); err != nil {
			t.Fatal(err)
		}
		var p platformv1alpha1.RemediationPlan
		if err := c.Get(ctx, types.NamespacedName{Name: "plan-1", Namespace: "default"}, &p); err != nil {
			t.Fatal(err)
		}
		return p, c
	}

	p, c := run("critical", "prod-east")
	if p.Status.State != platformv1alpha1.RemediationStateWaitingApproval || p.Annotations[annotationDecisionMode] != DecisionModeApproval {
		t.Fatalf("critical tier: state=%s ann=%v", p.Status.State, p.Annotations)
	}
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(ctx, types.NamespacedName{Name: "approval-plan-1", Namespace: "default"}, &ar); err != nil || ar.Spec.PolicyRef != ClusterTierPolicyName {
		t.Fatalf("cluster-tier request = %+v (err=%v)", ar.Spec, err)
	}
	if p, _ = run("non-critical", "prod-east"); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("non-critical tier: state=%s", p.Status.State)
	}
	if p, _ = run("critical", "somewhere-else"); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("unknown registration must not block: state=%s", p.Status.State)
	}
}

// Leaving WaitingApproval on a rejection records the decision and, because
// the plan fails, the remediation_failed event; an expiry does the same.
func TestRemediation_DecisionAndFailureAreAudited(t *testing.T) {
	ctx := context.Background()
	for _, state := range []platformv1alpha1.ApprovalRequestState{platformv1alpha1.ApprovalStateRejected, platformv1alpha1.ApprovalStateExpired} {
		issue := newIssue("test-issue", "default")
		plan := newRemediationPlan("plan-1", "default")
		plan.Status.State = platformv1alpha1.RemediationStateWaitingApproval
		ar := &platformv1alpha1.ApprovalRequest{
			ObjectMeta: metav1.ObjectMeta{Name: "approval-plan-1", Namespace: "default"},
			Spec:       platformv1alpha1.ApprovalRequestSpec{RemediationPlanRef: "plan-1", PolicyRef: DecisionEnginePolicyName},
			Status:     platformv1alpha1.ApprovalRequestStatus{State: state, Decisions: []platformv1alpha1.ApprovalDecision{{Approver: "sre", Decision: string(state), Timestamp: metav1.Now()}}},
		}
		c := gatingClient(issue, plan, ar)
		s := newScheme()
		r := &RemediationReconciler{Client: c, Scheme: s, AuditRecorder: NewAuditRecorder(c, s)}
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "plan-1", Namespace: "default"}}); err != nil {
			t.Fatal(err)
		}
		var events platformv1alpha1.AuditEventList
		if err := c.List(ctx, &events); err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, e := range events.Items {
			got[e.Spec.EventType] = true
		}
		want := "approval_" + strings.ToLower(string(state))
		if !got[want] || !got["remediation_failed"] {
			t.Fatalf("%s: audit events = %v, want %s and remediation_failed", state, got, want)
		}
	}
}

func TestConvergenceStopReasonAndFailedAt(t *testing.T) {
	for in, want := range map[string]string{"Converged: x": "converged", "Oscillating: a/b": "oscillating", "Approaching timeout: 8m": "timeout", "Last 5 actions all failed": "failures"} {
		if got := convergenceStopReason(in); got != want {
			t.Errorf("convergenceStopReason(%q) = %q, want %q", in, got, want)
		}
	}
	created := time.Now().Add(-3 * time.Hour)
	started := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	completed := metav1.NewTime(time.Now().Add(-time.Hour))
	p := &platformv1alpha1.RemediationPlan{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created)}}
	if !failedAt(p).Equal(created) {
		t.Error("no stamps: creation time expected")
	}
	p.Status.StartedAt = &started
	if !failedAt(p).Equal(started.Time) {
		t.Error("started only: start time expected")
	}
	p.Status.CompletedAt = &completed
	if !failedAt(p).Equal(completed.Time) {
		t.Error("completed: completion time expected")
	}
}

// Raising the same ApprovalRequest twice reuses the existing one instead
// of failing, so a retried gate never proceeds without approval.
func TestCreateApprovalRequest_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	issue := newIssue("test-issue", "default")
	plan := newRemediationPlan("plan-1", "default")
	c := gatingClient(issue, plan)
	s := newScheme()
	for i := 0; i < 2; i++ {
		if err := CreateApprovalRequest(ctx, c, s, plan, issue, &platformv1alpha1.AIInsight{}, SyntheticApprovalPolicy(DecisionEnginePolicyName), DecisionEngineRule()); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	var list platformv1alpha1.ApprovalRequestList
	if err := c.List(ctx, &list); err != nil || len(list.Items) != 1 || list.Items[0].Status.State != platformv1alpha1.ApprovalStatePending {
		t.Fatalf("requests = %d (err=%v)", len(list.Items), err)
	}
	if plan.Annotations[annotationApprovalPending] != "approval-plan-1" {
		t.Fatalf("plan not annotated: %v", plan.Annotations)
	}
}

// A NotificationPolicy whose channel cannot be configured still leaves a
// notification_sent audit event with the failure, through the real
// reconcile path.
func TestNotification_FailedDeliveryIsAudited(t *testing.T) {
	ctx := context.Background()
	s := newScheme()
	issue := newIssue("notify-issue", "default")
	issue.Spec.Severity = platformv1alpha1.IssueSeverityHigh
	issue.Status.State = platformv1alpha1.IssueStateDetected
	policy := &platformv1alpha1.NotificationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "oncall", Namespace: "default"},
		Spec: platformv1alpha1.NotificationPolicySpec{
			Enabled:  true,
			Channels: []platformv1alpha1.NotificationChannel{{Name: "hook", Type: "webhook", Config: map[string]string{}, SecretRef: &platformv1alpha1.SecretRefSpec{Name: "missing-secret"}}},
			Rules:    []platformv1alpha1.NotificationRule{{Name: "high", Severities: []platformv1alpha1.IssueSeverity{platformv1alpha1.IssueSeverityHigh}, Channels: []string{"hook"}}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&platformv1alpha1.Issue{}, &platformv1alpha1.NotificationPolicy{}).WithObjects(issue, policy).Build()
	r := &NotificationReconciler{Client: c, Scheme: s, AuditRecorder: NewAuditRecorder(c, s)}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "notify-issue", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var events platformv1alpha1.AuditEventList
	if err := c.List(ctx, &events); err != nil {
		t.Fatal(err)
	}
	for _, e := range events.Items {
		if e.Spec.EventType == "notification_sent" {
			return
		}
	}
	t.Fatalf("no notification_sent audit event among %d events", len(events.Items))
}
