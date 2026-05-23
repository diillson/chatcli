/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// =============================================================================
// GAP-07 — handleRemediating Contained branch persists the typed status fields
// =============================================================================

// TestHandleRemediating_PersistsContainedStatusFields is the canonical
// regression guard for GAP-07: after a containment remediation succeeds, the
// Issue's typed status fields MUST be populated, not just the conditions.
// The 1.122.x ship only wrote the conditions, leaving
// `kubectl get issue -o jsonpath='{.status.requiredAction}'` returning null.
func TestHandleRemediating_PersistsContainedStatusFields(t *testing.T) {
	const requiredActionWant = "restore the deployment"

	s := newScheme()
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "iss-contained", Namespace: "default"},
		Spec: platformv1alpha1.IssueSpec{
			Severity: platformv1alpha1.IssueSeverityCritical,
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
		},
		Status: platformv1alpha1.IssueStatus{
			State:                  platformv1alpha1.IssueStateRemediating,
			RemediationAttempts:    1,
			MaxRemediationAttempts: 3,
		},
	}
	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "plan-iss-contained", Namespace: "default", Labels: map[string]string{
			"platform.chatcli.io/issue": issue.Name,
		}},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{
					"replicas":    "0",
					"containment": "true",
				}},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:  platformv1alpha1.RemediationStateCompleted,
			Result: "deployment silenced",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.Issue{}, &platformv1alpha1.PostMortem{}).
		WithObjects(issue, plan).
		Build()

	r := &IssueReconciler{Client: c, Scheme: s}
	if _, err := r.handleRemediating(context.Background(), issue); err != nil {
		t.Fatalf("handleRemediating: %v", err)
	}

	var got platformv1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: issue.Name, Namespace: issue.Namespace}, &got); err != nil {
		t.Fatalf("post-reconcile Get: %v", err)
	}
	if got.Status.State != platformv1alpha1.IssueStateContained {
		t.Fatalf("state: want Contained, got %q", got.Status.State)
	}
	if !got.Status.RequiresHumanAction {
		t.Fatalf("Status.RequiresHumanAction must be true after Contained transition — this is the GAP-07 regression")
	}
	if !strings.Contains(got.Status.RequiredAction, requiredActionWant) {
		t.Fatalf("Status.RequiredAction must describe the follow-up containing %q, got %q",
			requiredActionWant, got.Status.RequiredAction)
	}
	// The conditions still get set (backwards-compat with consumers that
	// observe them) — assert the typed RequiresHumanAction condition is
	// also True so the dual-write contract holds.
	var found bool
	for _, cond := range got.Status.Conditions {
		if cond.Type == "RequiresHumanAction" && cond.Status == metav1.ConditionTrue {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RequiresHumanAction condition must also be True after Contained transition")
	}
}

// =============================================================================
// GAP-07 — handleContained auto-resolve clears the typed status fields
// =============================================================================

// TestHandleContained_ClearsStatusFieldsOnAutoResolve guards that the
// human-action flags do NOT linger after the operator observes the resource
// back to a healthy state. The CR must reflect the current truth: once the
// human acted (and the resource is healthy again) there is no pending action.
func TestHandleContained_ClearsStatusFieldsOnAutoResolve(t *testing.T) {
	s := newScheme()
	// Issue starts in Contained with the typed fields populated, mirroring
	// the state after a real containment transition.
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "iss-recovered", Namespace: "default"},
		Spec: platformv1alpha1.IssueSpec{
			Severity: platformv1alpha1.IssueSeverityHigh,
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
		},
		Status: platformv1alpha1.IssueStatus{
			State:               platformv1alpha1.IssueStateContained,
			RequiresHumanAction: true,
			RequiredAction:      "restore the deployment's replicas to the desired count after fixing the root cause",
		},
	}
	// Deployment that the human just restored — replicas > 0, fully ready.
	desired := int32(3)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas:       3,
			Replicas:            3,
			UnavailableReplicas: 0,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.Issue{}).
		WithObjects(issue, deploy).
		Build()
	r := &IssueReconciler{Client: c, Scheme: s}

	if _, err := r.handleContained(context.Background(), issue); err != nil {
		t.Fatalf("handleContained: %v", err)
	}

	var got platformv1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: issue.Name, Namespace: issue.Namespace}, &got); err != nil {
		t.Fatalf("post-resolve Get: %v", err)
	}
	if got.Status.State != platformv1alpha1.IssueStateResolved {
		t.Fatalf("state: want Resolved after auto-resolve, got %q", got.Status.State)
	}
	if got.Status.RequiresHumanAction {
		t.Fatalf("RequiresHumanAction must be FALSE after auto-resolve — the CR cannot keep claiming an action is pending once we observe the resource is healthy again")
	}
	if got.Status.RequiredAction != "" {
		t.Fatalf("RequiredAction must be cleared after auto-resolve, got %q", got.Status.RequiredAction)
	}
	// Condition should flip to False (not removed — the transition history matters).
	var rhCond *metav1.Condition
	for i := range got.Status.Conditions {
		if got.Status.Conditions[i].Type == "RequiresHumanAction" {
			rhCond = &got.Status.Conditions[i]
			break
		}
	}
	if rhCond == nil {
		t.Fatalf("RequiresHumanAction condition must be present (flipped to False), not removed entirely")
	}
	if rhCond.Status != metav1.ConditionFalse {
		t.Fatalf("RequiresHumanAction condition must be False after auto-resolve, got %q", rhCond.Status)
	}
}

// TestHandleContained_KeepsFieldsWhenWorkloadStillSilenced verifies the
// inverse: when the human has NOT yet restored replicas, the controller must
// NOT auto-resolve and the typed fields must remain populated.
func TestHandleContained_KeepsFieldsWhenWorkloadStillSilenced(t *testing.T) {
	s := newScheme()
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "iss-still-contained", Namespace: "default"},
		Spec: platformv1alpha1.IssueSpec{
			Severity: platformv1alpha1.IssueSeverityCritical,
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
		},
		Status: platformv1alpha1.IssueStatus{
			State:               platformv1alpha1.IssueStateContained,
			RequiresHumanAction: true,
			RequiredAction:      "restore replicas",
		},
	}
	// Deployment still at zero replicas — the human hasn't acted yet.
	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 0, Replicas: 0},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.Issue{}).
		WithObjects(issue, deploy).
		Build()
	r := &IssueReconciler{Client: c, Scheme: s}

	res, err := r.handleContained(context.Background(), issue)
	if err != nil {
		t.Fatalf("handleContained: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatalf("handleContained must requeue when the resource has not been restored yet")
	}

	var got platformv1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: issue.Name, Namespace: issue.Namespace}, &got); err != nil {
		t.Fatalf("post-noop Get: %v", err)
	}
	if got.Status.State != platformv1alpha1.IssueStateContained {
		t.Fatalf("state must stay Contained while resource is still silenced, got %q", got.Status.State)
	}
	if !got.Status.RequiresHumanAction || got.Status.RequiredAction == "" {
		t.Fatalf("status fields must NOT be cleared while the action is still pending, got requires=%v required=%q",
			got.Status.RequiresHumanAction, got.Status.RequiredAction)
	}
}

// =============================================================================
// GAP-07 — generatePostMortem propagates containment outcome to Status
// =============================================================================

// TestGeneratePostMortem_WritesContainmentToStatus covers the second half of
// the GAP-07 fix: the PostMortem CR's typed status fields must be populated
// when the parent Issue is in Contained state, so consumers can query
// `kubectl get postmortem -o jsonpath='{.status.requiresHumanAction}'`.
func TestGeneratePostMortem_WritesContainmentToStatus(t *testing.T) {
	now := metav1.Now()
	s := newScheme()

	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "iss-pm-contained", Namespace: "default"},
		Spec: platformv1alpha1.IssueSpec{
			SignalType: "crashloop",
			Severity:   platformv1alpha1.IssueSeverityCritical,
			Resource:   platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"},
		},
		Status: platformv1alpha1.IssueStatus{
			State:               platformv1alpha1.IssueStateContained,
			DetectedAt:          &now,
			RequiresHumanAction: true,
			RequiredAction:      "restore the deployment's replicas to the desired count after fixing the root cause",
		},
	}
	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "plan-iss-pm-contained", Namespace: "default"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Actions: []platformv1alpha1.RemediationAction{
				{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{
					"replicas": "0", "containment": "true",
				}},
			},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:  platformv1alpha1.RemediationStateCompleted,
			Result: "deployment silenced",
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.PostMortem{}, &platformv1alpha1.Issue{}).
		WithObjects(issue, plan).
		Build()
	r := &IssueReconciler{Client: c, Scheme: s}

	if err := r.generatePostMortem(context.Background(), issue, plan); err != nil {
		t.Fatalf("generatePostMortem: %v", err)
	}

	var pm platformv1alpha1.PostMortem
	if err := c.Get(context.Background(), types.NamespacedName{Name: "pm-iss-pm-contained", Namespace: "default"}, &pm); err != nil {
		t.Fatalf("PostMortem must exist after generatePostMortem, got: %v", err)
	}
	if !pm.Status.RequiresHumanAction {
		t.Fatalf("PostMortem.Status.RequiresHumanAction must be true when parent Issue is Contained — GAP-07 regression")
	}
	if !strings.Contains(pm.Status.RequiredAction, "restore the deployment") {
		t.Fatalf("PostMortem.Status.RequiredAction must describe the follow-up, got %q", pm.Status.RequiredAction)
	}
}

// TestGeneratePostMortem_OmitsStatusFieldsForResolvedIssue covers the
// negative case: a normal Resolved issue produces a PostMortem without the
// human-action flags set, so dashboards do not paint false positives.
func TestGeneratePostMortem_OmitsStatusFieldsForResolvedIssue(t *testing.T) {
	now := metav1.Now()
	s := newScheme()
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "iss-pm-resolved", Namespace: "default"},
		Spec:       platformv1alpha1.IssueSpec{Severity: platformv1alpha1.IssueSeverityMedium, Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}},
		Status:     platformv1alpha1.IssueStatus{State: platformv1alpha1.IssueStateResolved, DetectedAt: &now},
	}
	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{Name: "plan-iss-pm-resolved", Namespace: "default"},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Actions:  []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionRestartDeployment}},
		},
		Status: platformv1alpha1.RemediationPlanStatus{State: platformv1alpha1.RemediationStateCompleted, Result: "restart applied"},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.PostMortem{}, &platformv1alpha1.Issue{}).
		WithObjects(issue, plan).
		Build()
	r := &IssueReconciler{Client: c, Scheme: s}

	if err := r.generatePostMortem(context.Background(), issue, plan); err != nil {
		t.Fatalf("generatePostMortem: %v", err)
	}

	var pm platformv1alpha1.PostMortem
	if err := c.Get(context.Background(), types.NamespacedName{Name: "pm-iss-pm-resolved", Namespace: "default"}, &pm); err != nil {
		t.Fatalf("PostMortem Get: %v", err)
	}
	if pm.Status.RequiresHumanAction {
		t.Fatalf("Resolved Issue must NOT produce a PostMortem with RequiresHumanAction=true")
	}
	if pm.Status.RequiredAction != "" {
		t.Fatalf("Resolved Issue must NOT produce a PostMortem with RequiredAction set, got %q", pm.Status.RequiredAction)
	}
}

// Silences unused-import warnings when the time package is only used by a
// subset of helpers above (kept for clarity in case the test file is
// trimmed later).
var _ = time.Second
