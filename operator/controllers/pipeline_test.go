package controllers

import (
	"context"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
)

// setupPipelineReconcilers creates all three reconcilers sharing the same fake client.
func setupPipelineReconcilers(objs ...client.Object) (*AnomalyReconciler, *IssueReconciler, *RemediationReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.Anomaly{},
		&platformv1alpha1.Issue{},
		&platformv1alpha1.AIInsight{},
		&platformv1alpha1.RemediationPlan{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()

	anomalyR := &AnomalyReconciler{
		Client:            c,
		Scheme:            s,
		CorrelationEngine: NewCorrelationEngine(c),
	}
	issueR := &IssueReconciler{Client: c, Scheme: s}
	remediationR := &RemediationReconciler{Client: c, Scheme: s}

	return anomalyR, issueR, remediationR, c
}

// TestFullPipeline_AnomalyToResolution tests the complete happy path:
// Anomaly → Issue(Detected) → AIInsight → Runbook match → RemediationPlan → Resolved
func TestFullPipeline_AnomalyToResolution(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "payments-api", Namespace: "default"}

	// Pre-create a Runbook for matching (severity=medium matches single error_rate anomaly risk=30)
	runbook := &platformv1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "error-rate-runbook",
			Namespace: "default",
		},
		Spec: platformv1alpha1.RunbookSpec{
			Trigger: platformv1alpha1.RunbookTrigger{
				SignalType:   platformv1alpha1.SignalErrorRate,
				Severity:     platformv1alpha1.IssueSeverityMedium,
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

	// Pre-create the target deployment (for remediation actions)
	deploy := newDeployment("payments-api", "default", 2)

	// Create the anomaly
	anomaly := newAnomaly("error-spike", "default", platformv1alpha1.SignalErrorRate, resource)

	anomalyR, issueR, remediationR, c := setupPipelineReconcilers(anomaly, runbook, deploy)
	ctx := context.Background()

	// Step 1: AnomalyReconciler processes the anomaly → creates Issue
	_, err := anomalyR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "error-spike", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("anomaly reconcile failed: %v", err)
	}

	// Verify Issue was created
	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	if len(issues.Items) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues.Items))
	}
	issueName := issues.Items[0].Name
	t.Logf("Issue created: %s", issueName)

	// Step 2: IssueReconciler handles Detected state → adds finalizer
	_, err = issueR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("issue reconcile (finalizer) failed: %v", err)
	}

	// Step 3: IssueReconciler handles Detected → Analyzing (creates AIInsight)
	_, err = issueR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("issue reconcile (detected→analyzing) failed: %v", err)
	}

	var issue platformv1alpha1.Issue
	if err := c.Get(ctx, types.NamespacedName{Name: issueName, Namespace: "default"}, &issue); err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue.Status.State != platformv1alpha1.IssueStateAnalyzing {
		t.Fatalf("expected Analyzing, got %s", issue.Status.State)
	}

	// Simulate AI providing analysis on the insight
	var insight platformv1alpha1.AIInsight
	insightName := issueName + "-insight"
	if err := c.Get(ctx, types.NamespacedName{Name: insightName, Namespace: "default"}, &insight); err != nil {
		t.Fatalf("AIInsight not found: %v", err)
	}
	insight.Status.Analysis = "High error rate caused by recent deployment"
	insight.Status.Confidence = 0.92
	insight.Status.Recommendations = []string{"Scale up", "Rollback"}
	if err := c.Status().Update(ctx, &insight); err != nil {
		t.Fatalf("failed to update insight status: %v", err)
	}

	// Step 4: IssueReconciler handles Analyzing → Remediating (creates RemediationPlan)
	_, err = issueR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("issue reconcile (analyzing→remediating) failed: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: issueName, Namespace: "default"}, &issue); err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue.Status.State != platformv1alpha1.IssueStateRemediating {
		t.Fatalf("expected Remediating, got %s", issue.Status.State)
	}

	// Get the remediation plan
	planName := issueName + "-plan-1"
	var plan platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: planName, Namespace: "default"}, &plan); err != nil {
		t.Fatalf("RemediationPlan not found: %v", err)
	}

	// Step 5: RemediationReconciler: Pending → Executing
	_, err = remediationR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: planName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("remediation reconcile (pending→executing) failed: %v", err)
	}

	// Step 6: RemediationReconciler: Executing → Completed (executes actions)
	_, err = remediationR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: planName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("remediation reconcile (executing→completed) failed: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: planName, Namespace: "default"}, &plan); err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if plan.Status.State != platformv1alpha1.RemediationStateCompleted {
		t.Fatalf("expected plan Completed, got %s", plan.Status.State)
	}

	// Verify deployment was scaled
	var updatedDeploy appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Name: "payments-api", Namespace: "default"}, &updatedDeploy); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 4 {
		t.Errorf("expected deployment scaled to 4, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Step 7: IssueReconciler: Remediating → Resolved
	_, err = issueR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("issue reconcile (remediating→resolved) failed: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Name: issueName, Namespace: "default"}, &issue); err != nil {
		t.Fatalf("failed to get issue: %v", err)
	}
	if issue.Status.State != platformv1alpha1.IssueStateResolved {
		t.Fatalf("expected Resolved, got %s", issue.Status.State)
	}
	if issue.Status.ResolvedAt == nil {
		t.Error("expected resolvedAt to be set")
	}

	t.Logf("Pipeline completed: Anomaly → Issue(%s) → AIInsight → RemediationPlan → Resolved", issueName)
}

// TestFullPipeline_AnomalyToEscalation tests the failure path:
// All remediation attempts fail → Issue escalated
func TestFullPipeline_AnomalyToEscalation(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "checkout-api", Namespace: "default"}

	runbook := &platformv1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "error-runbook",
			Namespace: "default",
		},
		Spec: platformv1alpha1.RunbookSpec{
			Trigger: platformv1alpha1.RunbookTrigger{
				SignalType:   platformv1alpha1.SignalErrorRate,
				Severity:     platformv1alpha1.IssueSeverityMedium,
				ResourceKind: "Deployment",
			},
			Steps: []platformv1alpha1.RunbookStep{
				{
					Name:   "Scale up",
					Action: "ScaleDeployment",
					Params: map[string]string{"replicas": "4"},
				},
			},
			MaxAttempts: 3,
		},
	}

	anomaly := newAnomaly("checkout-error", "default", platformv1alpha1.SignalErrorRate, resource)

	anomalyR, issueR, _, c := setupPipelineReconcilers(anomaly, runbook)
	ctx := context.Background()

	// Step 1: AnomalyReconciler → creates Issue
	_, err := anomalyR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "checkout-error", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("anomaly reconcile failed: %v", err)
	}

	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	issueName := issues.Items[0].Name

	// Step 2-3: IssueReconciler: finalizer + Detected → Analyzing
	for i := 0; i < 2; i++ {
		_, err = issueR.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
		})
		if err != nil {
			t.Fatalf("issue reconcile step %d failed: %v", i, err)
		}
	}

	// Simulate AI analysis
	insightName := issueName + "-insight"
	var insight platformv1alpha1.AIInsight
	if err := c.Get(ctx, types.NamespacedName{Name: insightName, Namespace: "default"}, &insight); err != nil {
		t.Fatalf("insight not found: %v", err)
	}
	insight.Status.Analysis = "Root cause unknown"
	insight.Status.Confidence = 0.3
	if err := c.Status().Update(ctx, &insight); err != nil {
		t.Fatalf("failed to update insight: %v", err)
	}

	// Step 4: Analyzing → Remediating (creates plan attempt 1)
	_, err = issueR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("issue reconcile (analyzing→remediating) failed: %v", err)
	}

	// Simulate 3 failed remediation attempts
	for attempt := 1; attempt <= 3; attempt++ {
		planName := fmt.Sprintf("%s-plan-%d", issueName, attempt)
		var plan platformv1alpha1.RemediationPlan
		if err := c.Get(ctx, types.NamespacedName{Name: planName, Namespace: "default"}, &plan); err != nil {
			t.Fatalf("plan attempt %d not found: %v", attempt, err)
		}

		// Mark plan as failed
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.Result = "Action did not resolve the issue"
		now := metav1.Now()
		plan.Status.CompletedAt = &now
		if err := c.Status().Update(ctx, &plan); err != nil {
			t.Fatalf("failed to update plan status: %v", err)
		}

		// Reconcile issue
		_, err = issueR.Reconcile(ctx, ctrl.Request{
			NamespacedName: types.NamespacedName{Name: issueName, Namespace: "default"},
		})
		if err != nil {
			t.Fatalf("issue reconcile attempt %d failed: %v", attempt, err)
		}

		var issue platformv1alpha1.Issue
		if err := c.Get(ctx, types.NamespacedName{Name: issueName, Namespace: "default"}, &issue); err != nil {
			t.Fatalf("failed to get issue: %v", err)
		}

		if attempt < 3 {
			if issue.Status.State != platformv1alpha1.IssueStateRemediating {
				t.Fatalf("attempt %d: expected Remediating, got %s", attempt, issue.Status.State)
			}
			if issue.Status.RemediationAttempts != int32(attempt+1) {
				t.Fatalf("attempt %d: expected %d attempts, got %d", attempt, attempt+1, issue.Status.RemediationAttempts)
			}
		} else {
			// On attempt 3 failure, should escalate
			if issue.Status.State != platformv1alpha1.IssueStateEscalated {
				t.Fatalf("attempt 3: expected Escalated, got %s", issue.Status.State)
			}
		}
	}

	t.Log("Pipeline completed: Anomaly → Issue → 3 failed attempts → Escalated")
}

// TestFullPipeline_CorrelatedAnomalies tests that multiple anomalies for the same resource
// are grouped into a single Issue with escalated risk score.
func TestFullPipeline_CorrelatedAnomalies(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web-app", Namespace: "default"}

	a1 := newAnomaly("web-error-rate", "default", platformv1alpha1.SignalErrorRate, resource)
	a2 := newAnomaly("web-latency", "default", platformv1alpha1.SignalLatency, resource)
	a3 := newAnomaly("web-pod-restart", "default", platformv1alpha1.SignalPodRestart, resource)

	anomalyR, _, _, c := setupPipelineReconcilers(a1, a2, a3)
	ctx := context.Background()

	// Process first anomaly — creates new Issue
	_, err := anomalyR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "web-error-rate", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("first anomaly reconcile failed: %v", err)
	}

	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	if len(issues.Items) != 1 {
		t.Fatalf("expected 1 issue after first anomaly, got %d", len(issues.Items))
	}
	issueName := issues.Items[0].Name

	// Process second anomaly — should correlate with existing issue
	_, err = anomalyR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "web-latency", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("second anomaly reconcile failed: %v", err)
	}

	// Process third anomaly — should also correlate
	_, err = anomalyR.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "web-pod-restart", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("third anomaly reconcile failed: %v", err)
	}

	// Verify still only 1 issue exists
	if err := c.List(ctx, &issues, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	if len(issues.Items) != 1 {
		t.Fatalf("expected 1 issue after all anomalies, got %d", len(issues.Items))
	}

	// All anomalies should be correlated
	for _, name := range []string{"web-error-rate", "web-latency", "web-pod-restart"} {
		var a platformv1alpha1.Anomaly
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: "default"}, &a); err != nil {
			t.Fatalf("failed to get anomaly %s: %v", name, err)
		}
		if !a.Status.Correlated {
			t.Errorf("anomaly %s should be correlated", name)
		}
		if a.Status.IssueRef == nil || a.Status.IssueRef.Name != issueName {
			t.Errorf("anomaly %s should reference issue %s", name, issueName)
		}
	}

	t.Logf("Correlation test passed: 3 anomalies → 1 Issue (%s)", issueName)
}
