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

func setupFakeAnomalyReconciler(objs ...client.Object) (*AnomalyReconciler, client.Client) {
	s := newScheme()
	cb := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.Anomaly{},
		&platformv1alpha1.Issue{},
	)
	if len(objs) > 0 {
		cb = cb.WithObjects(objs...)
	}
	c := cb.Build()
	ce := NewCorrelationEngine(c)
	return &AnomalyReconciler{Client: c, Scheme: s, CorrelationEngine: ce}, c
}

func newAnomaly(name, ns string, signal platformv1alpha1.AnomalySignalType, resource platformv1alpha1.ResourceRef) *platformv1alpha1.Anomaly {
	return &platformv1alpha1.Anomaly{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			UID:               types.UID("anomaly-" + name),
			CreationTimestamp: metav1.Now(),
		},
		Spec: platformv1alpha1.AnomalySpec{
			Source:      platformv1alpha1.AnomalySourcePrometheus,
			SignalType:  signal,
			Resource:    resource,
			Value:       "15.4%",
			Threshold:   "5%",
			Description: "Test anomaly",
		},
	}
}

func TestAnomalyReconcile_NotFound(t *testing.T) {
	r, _ := setupFakeAnomalyReconciler()
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.RequeueAfter > 0 {
		t.Error("expected no requeue")
	}
}

func TestAnomalyReconcile_AlreadyCorrelated(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	anomaly := newAnomaly("correlated-anom", "default", platformv1alpha1.SignalErrorRate, resource)
	anomaly.Status.Correlated = true
	anomaly.Status.IssueRef = &platformv1alpha1.IssueRef{Name: "existing-issue"}

	r, _ := setupFakeAnomalyReconciler(anomaly)
	ctx := context.Background()

	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "correlated-anom", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for already correlated anomaly")
	}
}

func TestAnomalyReconcile_CreatesNewIssue(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "payments-api", Namespace: "default"}
	anomaly := newAnomaly("new-anom", "default", platformv1alpha1.SignalErrorRate, resource)

	r, c := setupFakeAnomalyReconciler(anomaly)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "new-anom", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Verify an Issue was created
	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	if len(issues.Items) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues.Items))
	}

	issue := issues.Items[0]
	if issue.Spec.Resource.Name != "payments-api" {
		t.Errorf("expected resource payments-api, got %q", issue.Spec.Resource.Name)
	}
	if issue.Spec.Source != platformv1alpha1.IssueSourcePrometheus {
		t.Errorf("expected source prometheus, got %q", issue.Spec.Source)
	}
	if issue.Labels["platform.chatcli.io/inc-id"] == "" {
		t.Error("expected inc-id label to be set")
	}
	if !strings.HasPrefix(issue.Labels["platform.chatcli.io/inc-id"], "INC-") {
		t.Errorf("expected inc-id to start with INC-, got %q", issue.Labels["platform.chatcli.io/inc-id"])
	}

	// Verify anomaly was marked as correlated
	var updated platformv1alpha1.Anomaly
	if err := c.Get(ctx, types.NamespacedName{Name: "new-anom", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get anomaly: %v", err)
	}
	if !updated.Status.Correlated {
		t.Error("expected anomaly to be marked as correlated")
	}
}

func TestAnomalyReconcile_CorrelatesWithExistingIssue(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "payments-api", Namespace: "default"}

	// Existing active issue for same resource
	existingIssue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-issue",
			Namespace: "default",
			UID:       "existing-issue-uid",
		},
		Spec: platformv1alpha1.IssueSpec{
			Severity:    platformv1alpha1.IssueSeverityHigh,
			Source:      platformv1alpha1.IssueSourcePrometheus,
			Resource:    resource,
			Description: "Existing issue",
			RiskScore:   30,
		},
		Status: platformv1alpha1.IssueStatus{
			State: platformv1alpha1.IssueStateAnalyzing,
		},
	}

	anomaly := newAnomaly("second-anom", "default", platformv1alpha1.SignalLatency, resource)

	r, c := setupFakeAnomalyReconciler(existingIssue, anomaly)
	ctx := context.Background()

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "second-anom", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Should NOT create a new issue
	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace("default")); err != nil {
		t.Fatalf("failed to list issues: %v", err)
	}
	if len(issues.Items) != 1 {
		t.Fatalf("expected 1 issue (existing), got %d", len(issues.Items))
	}

	// Anomaly should be correlated with existing issue
	var updated platformv1alpha1.Anomaly
	if err := c.Get(ctx, types.NamespacedName{Name: "second-anom", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("failed to get anomaly: %v", err)
	}
	if !updated.Status.Correlated {
		t.Error("expected anomaly to be marked as correlated")
	}
	if updated.Status.IssueRef == nil || updated.Status.IssueRef.Name != "existing-issue" {
		t.Error("expected anomaly to reference existing-issue")
	}
}

func TestCorrelationEngine_CalculateRiskScore(t *testing.T) {
	ce := &CorrelationEngine{}

	tests := []struct {
		name     string
		signals  []platformv1alpha1.AnomalySignalType
		expected int32
	}{
		{"empty", nil, 0},
		{"single error_rate", []platformv1alpha1.AnomalySignalType{platformv1alpha1.SignalErrorRate}, 30},
		{"oom_kill", []platformv1alpha1.AnomalySignalType{platformv1alpha1.SignalOOMKill}, 40},
		{"multiple signals", []platformv1alpha1.AnomalySignalType{
			platformv1alpha1.SignalErrorRate,
			platformv1alpha1.SignalPodRestart,
			platformv1alpha1.SignalLatency,
		}, 75}, // 30+25+20
		{"capped at 100", []platformv1alpha1.AnomalySignalType{
			platformv1alpha1.SignalOOMKill,
			platformv1alpha1.SignalErrorRate,
			platformv1alpha1.SignalPodRestart,
			platformv1alpha1.SignalLatency,
		}, 100}, // 40+30+25+20 = 115, capped at 100
	}

	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "test", Namespace: "default"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var anomalies []platformv1alpha1.Anomaly
			for _, sig := range tt.signals {
				anomalies = append(anomalies, platformv1alpha1.Anomaly{
					Spec: platformv1alpha1.AnomalySpec{
						SignalType: sig,
						Resource:   resource,
					},
				})
			}
			score := ce.CalculateRiskScore(anomalies)
			if score != tt.expected {
				t.Errorf("expected score %d, got %d", tt.expected, score)
			}
		})
	}
}

func TestCorrelationEngine_DetermineSeverity(t *testing.T) {
	ce := &CorrelationEngine{}

	tests := []struct {
		signal   platformv1alpha1.AnomalySignalType
		risk     int32
		expected platformv1alpha1.IssueSeverity
	}{
		{platformv1alpha1.SignalOOMKill, 40, platformv1alpha1.IssueSeverityCritical},
		{platformv1alpha1.SignalErrorRate, 90, platformv1alpha1.IssueSeverityCritical},
		{platformv1alpha1.SignalErrorRate, 70, platformv1alpha1.IssueSeverityHigh},
		{platformv1alpha1.SignalLatency, 50, platformv1alpha1.IssueSeverityMedium},
		{platformv1alpha1.SignalCPUHigh, 20, platformv1alpha1.IssueSeverityLow},
	}

	for _, tt := range tests {
		severity := ce.DetermineSeverity(tt.signal, tt.risk)
		if severity != tt.expected {
			t.Errorf("DetermineSeverity(%s, %d): expected %s, got %s", tt.signal, tt.risk, tt.expected, severity)
		}
	}
}

func TestCorrelationEngine_GenerateIncidentID(t *testing.T) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).Build()
	ce := NewCorrelationEngine(c)
	ctx := context.Background()

	id, err := ce.GenerateIncidentID(ctx, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	today := time.Now().Format("20060102")
	expected := "INC-" + today + "-001"
	if id != expected {
		t.Errorf("expected %q, got %q", expected, id)
	}
}

func TestCorrelationEngine_FindRelatedAnomalies(t *testing.T) {
	resource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	otherResource := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "api", Namespace: "default"}

	a1 := newAnomaly("a1", "default", platformv1alpha1.SignalErrorRate, resource)
	a2 := newAnomaly("a2", "default", platformv1alpha1.SignalLatency, resource)
	a3 := newAnomaly("a3", "default", platformv1alpha1.SignalCPUHigh, otherResource) // different resource
	a4 := newAnomaly("a4", "default", platformv1alpha1.SignalPodRestart, resource)
	a4.Status.Correlated = true // already correlated

	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(
		&platformv1alpha1.Anomaly{},
	).WithObjects(a1, a2, a3, a4).Build()
	ce := NewCorrelationEngine(c)
	ctx := context.Background()

	related, err := ce.FindRelatedAnomalies(ctx, resource, 15*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should include a1, a2 (same resource, uncorrelated, within window)
	// Should exclude a3 (different resource) and a4 (already correlated)
	if len(related) != 2 {
		t.Fatalf("expected 2 related anomalies, got %d", len(related))
	}
}
