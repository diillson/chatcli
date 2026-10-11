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

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func TestParseRemediationMode(t *testing.T) {
	for in, want := range map[string]RemediationMode{
		"": RemediationModeApprove, "approve": RemediationModeApprove, " AUTO ": RemediationModeAuto, "observe": RemediationModeObserve,
	} {
		if got, err := ParseRemediationMode(in); err != nil || got != want {
			t.Errorf("ParseRemediationMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseRemediationMode("yolo"); err == nil {
		t.Error("an unknown mode must be refused")
	}
	if got := RemediationMode("").effective(); got != RemediationModeApprove {
		t.Errorf("unset mode = %q, want approve", got)
	}
}

// modePlanFixture is a Pending plan scaling the Issue's Deployment, with
// whatever else the test adds.
func modePlanFixture(actions []platformv1alpha1.RemediationAction, objs ...client.Object) (client.Client, *platformv1alpha1.RemediationPlan) {
	issue := newIssue("test-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	plan := newRemediationPlan("plan-1", "default")
	if actions != nil {
		plan.Spec.Actions = actions
	}
	objs = append(objs, issue, plan, newDeployment("web", "default", 2))
	return gatingClient(objs...), plan
}

func reconcileModePlan(t *testing.T, r *RemediationReconciler, c client.Client) platformv1alpha1.RemediationPlan {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "plan-1", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var p platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "plan-1", Namespace: "default"}, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertReplicas(t *testing.T, c client.Client, name string, want int32) {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Replicas == nil || *d.Spec.Replicas != want {
		t.Fatalf("deployment %s replicas = %v, want %d: the plan must not have run", name, d.Spec.Replicas, want)
	}
}

func autoPolicy(actionTypes ...platformv1alpha1.RemediationActionType) *platformv1alpha1.ApprovalPolicy {
	return &platformv1alpha1.ApprovalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "auto-all", Namespace: "default"},
		Spec: platformv1alpha1.ApprovalPolicySpec{Enabled: true, Rules: []platformv1alpha1.ApprovalRule{{
			Name: "auto", Mode: platformv1alpha1.ApprovalModeAuto,
			Match: platformv1alpha1.ApprovalMatch{ActionTypes: actionTypes},
		}}},
	}
}

// With no ApprovalPolicy, no cluster tier and no decision engine (the
// chart defaults), a plan no longer runs: the default approve mode parks it
// in WaitingApproval through an ApprovalRequest naming its actions.
func TestRemediation_NoGateParksPlanInApproveMode(t *testing.T) {
	c, plan := modePlanFixture(nil)
	r := &RemediationReconciler{Client: c, Scheme: newScheme()} // mode unset: approve
	p := reconcileModePlan(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("state = %s (%s), want WaitingApproval", p.Status.State, p.Status.Result)
	}
	if !strings.Contains(p.Status.Result, RemediationModeEnv) {
		t.Errorf("result %q must say which setting lets plans run", p.Status.Result)
	}
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: "approval-plan-1", Namespace: "default"}, &ar); err != nil {
		t.Fatal(err)
	}
	if ar.Spec.PolicyRef != RemediationModePolicyName || len(ar.Spec.RequestedActions) != 1 || ar.Spec.RequestedActions[0].Type != plan.Spec.Actions[0].Type {
		t.Fatalf("request = %+v", ar.Spec)
	}
	if !IsSyntheticApprovalPolicy(ar.Spec.PolicyRef) {
		t.Error("the remediation-mode policy must be synthetic so the approval controller applies the built-in rule")
	}
	assertReplicas(t, c, "web", 2)
}

// An explicit allow lets the plan through in approve mode: an
// ApprovalPolicy auto rule, or the decision engine.
func TestRemediation_ExplicitAllowRunsInApproveMode(t *testing.T) {
	c, _ := modePlanFixture(nil, autoPolicy())
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	if p := reconcileModePlan(t, r, c); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("auto rule: state = %s (%s), want Executing", p.Status.State, p.Status.Result)
	}
	c, _ = modePlanFixture(nil)
	insight := &platformv1alpha1.AIInsight{ObjectMeta: metav1.ObjectMeta{Name: "test-issue-insight", Namespace: "default"}}
	if err := c.Create(context.Background(), insight); err != nil {
		t.Fatal(err)
	}
	insight.Status.Confidence = 0.95
	if err := c.Status().Update(context.Background(), insight); err != nil {
		t.Fatal(err)
	}
	var issue platformv1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: "test-issue", Namespace: "default"}, &issue); err != nil {
		t.Fatal(err)
	}
	issue.Spec.Severity = platformv1alpha1.IssueSeverityMedium
	if err := c.Update(context.Background(), &issue); err != nil {
		t.Fatal(err)
	}
	r = &RemediationReconciler{Client: c, Scheme: newScheme(), DecisionEngine: &DecisionEngine{}}
	if p := reconcileModePlan(t, r, c); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("decision engine allow: state = %s (%s), want Executing", p.Status.State, p.Status.Result)
	}
}

// A high-risk action waits for a human outside auto mode even when a
// policy auto rule would let it through.
func TestRemediation_HighRiskActionAlwaysParksOutsideAuto(t *testing.T) {
	drain := []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionDrainNode, Params: map[string]string{"node": "node-a"}}}
	c, _ := modePlanFixture(drain, autoPolicy())
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	p := reconcileModePlan(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateWaitingApproval || !strings.Contains(p.Status.Result, "high risk") {
		t.Fatalf("state = %s (%s), want WaitingApproval for a high-risk action", p.Status.State, p.Status.Result)
	}
	c, _ = modePlanFixture(drain, autoPolicy())
	r = &RemediationReconciler{Client: c, Scheme: newScheme(), RemediationMode: RemediationModeAuto}
	if p := reconcileModePlan(t, r, c); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("auto mode: state = %s (%s), want Executing", p.Status.State, p.Status.Result)
	}
}

// Observe mode never runs a plan, and says how to change that.
func TestRemediation_ObserveModeNeverRuns(t *testing.T) {
	c, _ := modePlanFixture(nil, autoPolicy())
	r := &RemediationReconciler{Client: c, Scheme: newScheme(), RemediationMode: RemediationModeObserve}
	p := reconcileModePlan(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateFailed || !strings.Contains(p.Status.Result, RemediationModeEnv+"=approve") {
		t.Fatalf("state = %s (%s), want Failed naming the setting", p.Status.State, p.Status.Result)
	}
	assertReplicas(t, c, "web", 2)
}

// The safety validator runs before the approval gate: an approved plan goes
// straight to Executing, so a plan that would only be validated after the
// gate would run unvalidated once a human approved it.
func TestRemediation_SafetyValidatedBeforeApproval(t *testing.T) {
	zero := []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{"replicas": "0"}}}
	c, _ := modePlanFixture(zero)
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	p := reconcileModePlan(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateFailed || !strings.Contains(p.Status.Result, "Safety constraint violation") {
		t.Fatalf("state = %s (%s), want Failed by the safety validator before any approval", p.Status.State, p.Status.Result)
	}
}

// In observe mode an analyzed Issue is escalated to a human and no plan is
// created.
func TestIssue_ObserveModeEscalatesWithoutPlan(t *testing.T) {
	issue := newIssue("obs", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	insight := &platformv1alpha1.AIInsight{ObjectMeta: metav1.ObjectMeta{Name: "obs-insight", Namespace: "default"}}
	insight.Status.Analysis = "root cause found"
	insight.Status.SuggestedActions = []platformv1alpha1.SuggestedAction{{Name: "scale", Action: "ScaleDeployment", Params: map[string]string{"replicas": "4"}}}
	r, c := setupFakeIssueReconciler(issue, insight)
	r.RemediationMode = RemediationModeObserve
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "obs", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "obs", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.State != platformv1alpha1.IssueStateEscalated {
		t.Fatalf("issue state = %s, want Escalated", got.Status.State)
	}
	var plans platformv1alpha1.RemediationPlanList
	if err := c.List(ctx, &plans); err != nil || len(plans.Items) != 0 {
		t.Fatalf("observe mode created %d plan(s) (err=%v)", len(plans.Items), err)
	}
}
