/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

func newRestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	return s
}

// TestFoldRemediationSummary covers the GAP-03 + Floor 3 counters around
// remediation outcomes: total plans, success/failure split, and the
// "remediated → resolved" mapping that powers the dashboard's Success Rate.
func TestFoldRemediationSummary(t *testing.T) {
	now := metav1.Now()

	plans := []v1alpha1.RemediationPlan{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p1", CreationTimestamp: now},
			Spec:       v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-resolved"}},
			Status:     v1alpha1.RemediationPlanStatus{State: v1alpha1.RemediationStateCompleted},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p2", CreationTimestamp: now},
			Spec:       v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-failed"}},
			Status:     v1alpha1.RemediationPlanStatus{State: v1alpha1.RemediationStateFailed},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p3", CreationTimestamp: now},
			Spec:       v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-rolled-back"}},
			Status:     v1alpha1.RemediationPlanStatus{State: v1alpha1.RemediationStateRolledBack},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "p4-orphan", CreationTimestamp: now},
			Spec:       v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-deleted"}},
			Status:     v1alpha1.RemediationPlanStatus{State: v1alpha1.RemediationStateCompleted},
		},
	}
	issues := []v1alpha1.Issue{
		{ObjectMeta: metav1.ObjectMeta{Name: "iss-resolved"}, Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved}},
		{ObjectMeta: metav1.ObjectMeta{Name: "iss-failed"}, Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateEscalated}},
		{ObjectMeta: metav1.ObjectMeta{Name: "iss-rolled-back"}, Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateRemediating}},
		// iss-deleted intentionally absent → orphan path
	}

	s := &AnalyticsSummary{SeverityBreakdown: map[string]int{}}
	foldRemediationSummary(s, plans, issues, timeRangeParams{})

	if s.TotalRemediations != 4 {
		t.Fatalf("TotalRemediations: want 4, got %d", s.TotalRemediations)
	}
	if s.SuccessfulRemediations != 2 {
		t.Fatalf("SuccessfulRemediations: want 2 (p1 + orphan p4), got %d", s.SuccessfulRemediations)
	}
	if s.FailedRemediations != 2 {
		t.Fatalf("FailedRemediations: want 2 (p2 Failed + p3 RolledBack), got %d", s.FailedRemediations)
	}
	// 3 non-orphan plans → 3 RemediatedIssues; only iss-resolved actually reached Resolved.
	if s.RemediatedIssues != 3 {
		t.Fatalf("RemediatedIssues: want 3, got %d", s.RemediatedIssues)
	}
	if s.ResolvedByRemediation != 1 {
		t.Fatalf("ResolvedByRemediation: want 1, got %d", s.ResolvedByRemediation)
	}
}

// TestFilterPlansByRange covers the small helper that filters plans by
// creation time. The empty timeRangeParams means "include all".
func TestFilterPlansByRange(t *testing.T) {
	now := metav1.Now()
	plans := []v1alpha1.RemediationPlan{
		{ObjectMeta: metav1.ObjectMeta{Name: "in", CreationTimestamp: now}},
	}
	got := filterPlansByRange(plans, timeRangeParams{})
	if len(got) != 1 {
		t.Fatalf("want 1 plan included, got %d", len(got))
	}
}

// TestComputeSummary_EndToEnd exercises the full pipeline against a fake
// K8s client. Verifies that the GAP-03 + GAP-04 counters (containedIssues,
// chaosInducedIssues, postMortemsRequiringHumanAction) make it into the
// final summary returned by the REST endpoint.
func TestComputeSummary_EndToEnd(t *testing.T) {
	now := metav1.Now()

	objs := []client.Object{
		&v1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: "i1", Namespace: "default", CreationTimestamp: now},
			Spec:       v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityCritical},
			Status:     v1alpha1.IssueStatus{State: v1alpha1.IssueStateContained},
		},
		&v1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: "i2", Namespace: "default", CreationTimestamp: now,
				Labels: map[string]string{"platform.chatcli.io/source": "chaos-experiment"}},
			Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityLow},
			Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved},
		},
		&v1alpha1.PostMortem{
			ObjectMeta: metav1.ObjectMeta{Name: "pm-1", Namespace: "default", CreationTimestamp: now},
			Spec: v1alpha1.PostMortemSpec{
				IssueRef: v1alpha1.IssueRef{Name: "i1"}, Resource: v1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: v1alpha1.IssueSeverityCritical,
				RequiresHumanAction: true,
			},
			Status: v1alpha1.PostMortemStatus{State: v1alpha1.PostMortemStateOpen},
		},
		&v1alpha1.Runbook{ObjectMeta: metav1.ObjectMeta{Name: "rb-1", Namespace: "default"}},
	}

	s := newRestScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()

	api := NewAPIServer(c, ":0")
	summary, err := api.computeSummary(context.Background(), timeRangeParams{})
	if err != nil {
		t.Fatalf("computeSummary: %v", err)
	}

	if summary.TotalIssues != 2 {
		t.Fatalf("TotalIssues: want 2, got %d", summary.TotalIssues)
	}
	if summary.ContainedIssues != 1 {
		t.Fatalf("ContainedIssues (GAP-03): want 1, got %d", summary.ContainedIssues)
	}
	if summary.OpenIssues != 1 {
		t.Fatalf("OpenIssues: want 1 (Contained is open until human acts), got %d", summary.OpenIssues)
	}
	if summary.ChaosInducedIssues != 1 {
		t.Fatalf("ChaosInducedIssues (GAP-04): want 1, got %d", summary.ChaosInducedIssues)
	}
	if summary.PostMortemsRequiringHumanAction != 1 {
		t.Fatalf("PostMortemsRequiringHumanAction (GAP-03): want 1, got %d", summary.PostMortemsRequiringHumanAction)
	}
	if summary.TotalRunbooks != 1 {
		t.Fatalf("TotalRunbooks: want 1, got %d", summary.TotalRunbooks)
	}
}

// TestNewAPIServer is a sanity check that the constructor wires up the client.
func TestNewAPIServer(t *testing.T) {
	s := newRestScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	api := NewAPIServer(c, ":0")
	if api == nil {
		t.Fatal("NewAPIServer returned nil")
	}
}

// TestAckHumanActionEndpoint_HappyPath covers the GAP-03 acknowledgement
// REST endpoint end-to-end: the body sets the operator + note annotations
// and the PostMortem becomes Closeable.
func TestAckHumanActionEndpoint_HappyPath(t *testing.T) {
	s := newRestScheme()
	pm := &v1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{Name: "pm-needs-human", Namespace: "default"},
		Spec: v1alpha1.PostMortemSpec{
			IssueRef: v1alpha1.IssueRef{Name: "i1"}, Resource: v1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: v1alpha1.IssueSeverityHigh,
			RequiresHumanAction: true,
		},
		Status: v1alpha1.PostMortemStatus{State: v1alpha1.PostMortemStateOpen},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&v1alpha1.PostMortem{}).
		WithObjects(pm).
		Build()
	api := NewAPIServer(c, ":0")

	body := strings.NewReader(`{"acknowledgedBy":"sre","note":"rolled back to v1.2.3"}`)
	req := httptest.NewRequest("POST", "/api/v1/postmortems/pm-needs-human/ack-human-action?namespace=default", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.handlePostMortemAckHumanAction(w, req, "pm-needs-human")

	if w.Code != 200 {
		t.Fatalf("status: want 200, got %d (body=%s)", w.Code, w.Body.String())
	}

	var got v1alpha1.PostMortem
	if err := c.Get(context.Background(), client.ObjectKey{Name: "pm-needs-human", Namespace: "default"}, &got); err != nil {
		t.Fatalf("post-ack Get: %v", err)
	}
	if got.Annotations["aiops.chatcli.io/human-action-acknowledged"] != "true" {
		t.Fatalf("acknowledgement annotation must be set, got %v", got.Annotations)
	}
	if got.Annotations["aiops.chatcli.io/human-action-acknowledged-by"] != "sre" {
		t.Fatalf("acknowledgedBy annotation must reflect the request body")
	}
	if got.Annotations["aiops.chatcli.io/human-action-note"] != "rolled back to v1.2.3" {
		t.Fatalf("note annotation must reflect the request body")
	}
}

// TestAckHumanActionEndpoint_RejectsNonContained covers the API guard that
// blocks acknowledging PostMortems that don't actually require human action.
func TestAckHumanActionEndpoint_RejectsNonContained(t *testing.T) {
	s := newRestScheme()
	pm := &v1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{Name: "pm-normal", Namespace: "default"},
		Spec: v1alpha1.PostMortemSpec{
			IssueRef: v1alpha1.IssueRef{Name: "i1"}, Resource: v1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: v1alpha1.IssueSeverityMedium,
			RequiresHumanAction: false,
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pm).Build()
	api := NewAPIServer(c, ":0")

	req := httptest.NewRequest("POST", "/api/v1/postmortems/pm-normal/ack-human-action?namespace=default", strings.NewReader(""))
	w := httptest.NewRecorder()
	api.handlePostMortemAckHumanAction(w, req, "pm-normal")

	if w.Code != 400 {
		t.Fatalf("must reject with 400 when PostMortem does not require human action, got %d", w.Code)
	}
}
