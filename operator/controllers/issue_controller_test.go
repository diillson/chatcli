package controllers

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func setupFakeIssueReconciler(objs ...client.Object) (*IssueReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.Issue{},
		&platformv1alpha1.AIInsight{},
		&platformv1alpha1.RemediationPlan{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	return &IssueReconciler{Client: c, Scheme: s}, c
}

func newIssue(name, ns string) *platformv1alpha1.Issue {
	return &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  ns,
			UID:        "issue-uid",
			Generation: 1,
		},
		Spec: platformv1alpha1.IssueSpec{
			Severity:    platformv1alpha1.IssueSeverityHigh,
			Source:      platformv1alpha1.IssueSourcePrometheus,
			Description: "Error rate above SLO threshold",
			Resource: platformv1alpha1.ResourceRef{
				Kind:      "Deployment",
				Name:      "payments-api",
				Namespace: "prod",
			},
			RiskScore: 85,
		},
		Status: platformv1alpha1.IssueStatus{
			State: platformv1alpha1.IssueStateDetected,
		},
	}
}

func newRunbook(name, ns string) *platformv1alpha1.Runbook {
	return &platformv1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: platformv1alpha1.RunbookSpec{
			Trigger: platformv1alpha1.RunbookTrigger{
				SignalType:   platformv1alpha1.SignalErrorRate,
				Severity:     platformv1alpha1.IssueSeverityHigh,
				ResourceKind: "Deployment",
			},
			Steps: []platformv1alpha1.RunbookStep{
				{
					Name:   "Scale up deployment",
					Action: "ScaleDeployment",
					Params: map[string]string{"replicas": "4"},
				},
			},
			MaxAttempts: 3,
		},
	}
}

func TestIssueReconcile_NotFound(t *testing.T) {
	r, _ := setupFakeIssueReconciler()
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

func TestIssueReconcile_DetectedToAnalyzing(t *testing.T) {
	issue := newIssue("test-issue", "default")
	r, c := setupFakeIssueReconciler(issue)
	ctx := context.Background()

	// First reconcile: add finalizer
	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}

	// Second reconcile: Detected -> Analyzing (creates AIInsight)
	_, err = r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}

	// Verify AIInsight was created
	var insight platformv1alpha1.AIInsight
	if err := c.Get(ctx, types.NamespacedName{Name: "test-issue-insight", Namespace: "default"}, &insight); err != nil {
		t.Fatalf("expected AIInsight to be created: %v", err)
	}
	if insight.Spec.IssueRef.Name != "test-issue" {
		t.Errorf("expected issueRef name 'test-issue', got %q", insight.Spec.IssueRef.Name)
	}

	// Verify Issue transitioned to Analyzing
	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "test-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.State != platformv1alpha1.IssueStateAnalyzing {
		t.Errorf("expected state Analyzing, got %q", updated.Status.State)
	}
	if updated.Status.DetectedAt == nil {
		t.Error("expected detectedAt to be set")
	}
	if updated.Status.MaxRemediationAttempts != 3 {
		t.Errorf("expected maxRemediationAttempts 3, got %d", updated.Status.MaxRemediationAttempts)
	}
}

func TestIssueReconcile_AnalyzingToRemediating(t *testing.T) {
	issue := newIssue("test-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	issue.Status.MaxRemediationAttempts = 3
	now := metav1.Now()
	issue.Status.DetectedAt = &now

	// Create AIInsight with analysis populated
	insight := &platformv1alpha1.AIInsight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-issue-insight",
			Namespace: "default",
			UID:       "insight-uid",
		},
		Spec: platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "test-issue"},
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
		},
		Status: platformv1alpha1.AIInsightStatus{
			Analysis:        "High error rate caused by memory leak",
			Confidence:      0.87,
			Recommendations: []string{"Scale up", "Rollback"},
		},
	}

	// Create matching Runbook
	runbook := newRunbook("high-error-rate-runbook", "default")

	r, c := setupFakeIssueReconciler(issue, insight, runbook)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify RemediationPlan was created
	var plan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "test-issue-plan-1", Namespace: "default"}, &plan); err != nil {
		t.Fatalf("expected RemediationPlan to be created: %v", err)
	}
	if plan.Spec.IssueRef.Name != "test-issue" {
		t.Errorf("expected issueRef 'test-issue', got %q", plan.Spec.IssueRef.Name)
	}
	if plan.Spec.Attempt != 1 {
		t.Errorf("expected attempt 1, got %d", plan.Spec.Attempt)
	}
	if len(plan.Spec.Actions) != 1 || plan.Spec.Actions[0].Type != platformv1alpha1.ActionScaleDeployment {
		t.Errorf("expected ScaleDeployment action, got %v", plan.Spec.Actions)
	}

	// Verify Issue transitioned to Remediating
	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "test-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.State != platformv1alpha1.IssueStateRemediating {
		t.Errorf("expected state Remediating, got %q", updated.Status.State)
	}
	if updated.Status.RemediationAttempts != 1 {
		t.Errorf("expected remediationAttempts 1, got %d", updated.Status.RemediationAttempts)
	}
}

func TestIssueReconcile_AnalyzingToEscalated(t *testing.T) {
	issue := newIssue("no-runbook-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	issue.Status.MaxRemediationAttempts = 3
	now := metav1.Now()
	issue.Status.DetectedAt = &now

	// AIInsight with analysis but no matching runbook
	insight := &platformv1alpha1.AIInsight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-runbook-issue-insight",
			Namespace: "default",
			UID:       "insight-uid",
		},
		Spec: platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "no-runbook-issue"},
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
		},
		Status: platformv1alpha1.AIInsightStatus{
			Analysis:   "Unknown issue type",
			Confidence: 0.4,
		},
	}

	r, c := setupFakeIssueReconciler(issue, insight) // No runbook
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "no-runbook-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "no-runbook-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.State != platformv1alpha1.IssueStateEscalated {
		t.Errorf("expected state Escalated, got %q", updated.Status.State)
	}
}

func TestIssueReconcile_RemediatingToResolved(t *testing.T) {
	now := metav1.Now()
	issue := newIssue("resolved-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateRemediating
	issue.Status.RemediationAttempts = 1
	issue.Status.MaxRemediationAttempts = 3
	issue.Status.DetectedAt = &now

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resolved-issue-plan-1",
			Namespace: "default",
			UID:       "plan-uid",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "resolved-issue"},
			Attempt:  1,
			Strategy: "Scale up",
			Actions:  []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionScaleDeployment}},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:  platformv1alpha1.RemediationStateCompleted,
			Result: "Scaling resolved high error rate",
		},
	}

	r, c := setupFakeIssueReconciler(issue, plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "resolved-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "resolved-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.State != platformv1alpha1.IssueStateResolved {
		t.Errorf("expected state Resolved, got %q", updated.Status.State)
	}
	if updated.Status.Resolution != "Scaling resolved high error rate" {
		t.Errorf("expected resolution message, got %q", updated.Status.Resolution)
	}
	if updated.Status.ResolvedAt == nil {
		t.Error("expected resolvedAt to be set")
	}
}

func TestIssueReconcile_RemediatingRetry(t *testing.T) {
	now := metav1.Now()
	issue := newIssue("retry-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateRemediating
	issue.Status.RemediationAttempts = 1
	issue.Status.MaxRemediationAttempts = 3
	issue.Status.DetectedAt = &now

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retry-issue-plan-1",
			Namespace: "default",
			UID:       "plan-uid",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "retry-issue"},
			Attempt:  1,
			Strategy: "Scale up",
			Actions:  []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionScaleDeployment}},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:  platformv1alpha1.RemediationStateFailed,
			Result: "Scale did not help",
		},
	}

	// Create insight and runbook for retry
	insight := &platformv1alpha1.AIInsight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "retry-issue-insight",
			Namespace: "default",
			UID:       "insight-uid",
		},
		Spec: platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "retry-issue"},
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
		},
		Status: platformv1alpha1.AIInsightStatus{
			Analysis: "Try rollback",
		},
	}

	runbook := newRunbook("retry-runbook", "default")

	r, c := setupFakeIssueReconciler(issue, plan, insight, runbook)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "retry-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify new plan was created (attempt 2)
	var plan2 platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "retry-issue-plan-2", Namespace: "default"}, &plan2); err != nil {
		t.Fatalf("expected second RemediationPlan: %v", err)
	}
	if plan2.Spec.Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", plan2.Spec.Attempt)
	}

	// Verify issue state updated
	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "retry-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.RemediationAttempts != 2 {
		t.Errorf("expected remediationAttempts 2, got %d", updated.Status.RemediationAttempts)
	}
	if updated.Status.State != platformv1alpha1.IssueStateRemediating {
		t.Errorf("expected state Remediating, got %q", updated.Status.State)
	}
}

func TestIssueReconcile_AnalyzingToRemediatingFromAI(t *testing.T) {
	issue := newIssue("ai-actions-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	issue.Status.MaxRemediationAttempts = 3
	now := metav1.Now()
	issue.Status.DetectedAt = &now

	// AIInsight with analysis AND suggested actions, but NO runbook
	insight := &platformv1alpha1.AIInsight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ai-actions-issue-insight",
			Namespace: "default",
			UID:       "insight-uid",
		},
		Spec: platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "ai-actions-issue"},
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
		},
		Status: platformv1alpha1.AIInsightStatus{
			Analysis:   "High memory usage causing OOMKills; restart pods to reclaim memory",
			Confidence: 0.82,
			SuggestedActions: []platformv1alpha1.SuggestedAction{
				{
					Name:        "Restart deployment",
					Action:      "RestartDeployment",
					Description: "Restart pods to reclaim leaked memory",
				},
				{
					Name:        "Scale up replicas",
					Action:      "ScaleDeployment",
					Description: "Add more replicas to handle load",
					Params:      map[string]string{"replicas": "5"},
				},
			},
		},
	}

	r, c := setupFakeIssueReconciler(issue, insight) // No runbook!
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ai-actions-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify RemediationPlan was created from AI actions
	var plan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "ai-actions-issue-plan-1", Namespace: "default"}, &plan); err != nil {
		t.Fatalf("expected RemediationPlan from AI actions: %v", err)
	}
	if plan.Spec.IssueRef.Name != "ai-actions-issue" {
		t.Errorf("expected issueRef 'ai-actions-issue', got %q", plan.Spec.IssueRef.Name)
	}
	if plan.Spec.Attempt != 1 {
		t.Errorf("expected attempt 1, got %d", plan.Spec.Attempt)
	}
	if len(plan.Spec.Actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(plan.Spec.Actions))
	}
	if plan.Spec.Actions[0].Type != platformv1alpha1.ActionRestartDeployment {
		t.Errorf("expected first action RestartDeployment, got %q", plan.Spec.Actions[0].Type)
	}
	if plan.Spec.Actions[1].Type != platformv1alpha1.ActionScaleDeployment {
		t.Errorf("expected second action ScaleDeployment, got %q", plan.Spec.Actions[1].Type)
	}
	if plan.Spec.Actions[1].Params["replicas"] != "5" {
		t.Errorf("expected replicas param '5', got %q", plan.Spec.Actions[1].Params["replicas"])
	}

	// Verify strategy mentions AI-generated
	if len(plan.Spec.Strategy) == 0 {
		t.Error("expected non-empty strategy")
	}

	// Verify Issue transitioned to Remediating
	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "ai-actions-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.State != platformv1alpha1.IssueStateRemediating {
		t.Errorf("expected state Remediating, got %q", updated.Status.State)
	}
	if updated.Status.RemediationAttempts != 1 {
		t.Errorf("expected remediationAttempts 1, got %d", updated.Status.RemediationAttempts)
	}
}

func TestIssueReconcile_RemediatingRetryFromAI(t *testing.T) {
	now := metav1.Now()
	issue := newIssue("ai-retry-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateRemediating
	issue.Status.RemediationAttempts = 1
	issue.Status.MaxRemediationAttempts = 3
	issue.Status.DetectedAt = &now

	// Failed plan from first attempt
	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ai-retry-issue-plan-1",
			Namespace: "default",
			UID:       "plan-uid",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "ai-retry-issue"},
			Attempt:  1,
			Strategy: "First attempt",
			Actions:  []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionRestartDeployment}},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:  platformv1alpha1.RemediationStateFailed,
			Result: "Restart did not help",
		},
	}

	// AIInsight with suggested actions but NO runbook
	insight := &platformv1alpha1.AIInsight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ai-retry-issue-insight",
			Namespace: "default",
			UID:       "insight-uid",
		},
		Spec: platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "ai-retry-issue"},
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
		},
		Status: platformv1alpha1.AIInsightStatus{
			Analysis: "Memory leak requires rollback",
			SuggestedActions: []platformv1alpha1.SuggestedAction{
				{
					Name:   "Rollback deployment",
					Action: "RollbackDeployment",
				},
			},
		},
	}

	r, c := setupFakeIssueReconciler(issue, plan, insight) // No runbook
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "ai-retry-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify new plan was created (attempt 2) from AI actions
	var plan2 platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "ai-retry-issue-plan-2", Namespace: "default"}, &plan2); err != nil {
		t.Fatalf("expected second RemediationPlan from AI: %v", err)
	}
	if plan2.Spec.Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", plan2.Spec.Attempt)
	}
	if len(plan2.Spec.Actions) != 1 || plan2.Spec.Actions[0].Type != platformv1alpha1.ActionRollbackDeployment {
		t.Errorf("expected RollbackDeployment action, got %v", plan2.Spec.Actions)
	}

	// Verify issue state
	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "ai-retry-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.RemediationAttempts != 2 {
		t.Errorf("expected remediationAttempts 2, got %d", updated.Status.RemediationAttempts)
	}
	if updated.Status.State != platformv1alpha1.IssueStateRemediating {
		t.Errorf("expected state Remediating, got %q", updated.Status.State)
	}
}

func TestMapActionType(t *testing.T) {
	tests := []struct {
		input    string
		expected platformv1alpha1.RemediationActionType
	}{
		{"ScaleDeployment", platformv1alpha1.ActionScaleDeployment},
		{"RollbackDeployment", platformv1alpha1.ActionRollbackDeployment},
		{"RestartDeployment", platformv1alpha1.ActionRestartDeployment},
		{"PatchConfig", platformv1alpha1.ActionPatchConfig},
		{"Unknown", platformv1alpha1.ActionCustom},
		{"", platformv1alpha1.ActionCustom},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := mapActionType(tc.input)
			if got != tc.expected {
				t.Errorf("mapActionType(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestIssueReconcile_RemediatingToEscalated(t *testing.T) {
	now := metav1.Now()
	issue := newIssue("max-attempts-issue", "default")
	issue.Finalizers = []string{issueFinalizerName}
	issue.Status.State = platformv1alpha1.IssueStateRemediating
	issue.Status.RemediationAttempts = 3
	issue.Status.MaxRemediationAttempts = 3
	issue.Status.DetectedAt = &now

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "max-attempts-issue-plan-3",
			Namespace: "default",
			UID:       "plan-uid",
		},
		Spec: platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "max-attempts-issue"},
			Attempt:  3,
			Strategy: "Last attempt",
			Actions:  []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionRestartDeployment}},
		},
		Status: platformv1alpha1.RemediationPlanStatus{
			State:  platformv1alpha1.RemediationStateFailed,
			Result: "Restart did not help",
		},
	}

	r, c := setupFakeIssueReconciler(issue, plan)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "max-attempts-issue", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	var updated platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: "max-attempts-issue", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get updated issue: %v", err)
	}
	if updated.Status.State != platformv1alpha1.IssueStateEscalated {
		t.Errorf("expected state Escalated, got %q", updated.Status.State)
	}
}
