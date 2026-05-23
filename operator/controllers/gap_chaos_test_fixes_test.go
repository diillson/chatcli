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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// =============================================================================
// GAP-03 — planAppliedContainment / isResourceRestored / Contained transitions
// =============================================================================

// TestPlanAppliedContainment guards the GAP-03 detection that switches the
// Issue transition from Resolved to Contained when remediation silenced the
// workload via a stop-the-bleeding action.
func TestPlanAppliedContainment(t *testing.T) {
	cases := []struct {
		name         string
		plan         *platformv1alpha1.RemediationPlan
		wantContains bool
		wantSubstr   string // substring expected in the required-action description (when contained)
	}{
		{
			name: "empty plan → not contained",
			plan: &platformv1alpha1.RemediationPlan{},
		},
		{
			name: "plan with ScaleDeployment containment=true → contained",
			plan: &platformv1alpha1.RemediationPlan{
				Spec: platformv1alpha1.RemediationPlanSpec{
					Actions: []platformv1alpha1.RemediationAction{
						{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{"replicas": "0", "containment": "true"}},
					},
				},
			},
			wantContains: true,
			wantSubstr:   "restore the deployment",
		},
		{
			name: "plan with ScaleStatefulSet containment=true → contained",
			plan: &platformv1alpha1.RemediationPlan{
				Spec: platformv1alpha1.RemediationPlanSpec{
					Actions: []platformv1alpha1.RemediationAction{
						{Type: platformv1alpha1.ActionScaleStatefulSet, Params: map[string]string{"replicas": "0", "containment": "true"}},
					},
				},
			},
			wantContains: true,
			wantSubstr:   "restore the StatefulSet",
		},
		{
			name: "plan with non-containment scale → not contained",
			plan: &platformv1alpha1.RemediationPlan{
				Spec: platformv1alpha1.RemediationPlanSpec{
					Actions: []platformv1alpha1.RemediationAction{
						{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{"replicas": "3"}},
					},
				},
			},
		},
		{
			name: "agentic history with successful containment step → contained",
			plan: &platformv1alpha1.RemediationPlan{
				Spec: platformv1alpha1.RemediationPlanSpec{
					AgenticHistory: []platformv1alpha1.AgenticStep{
						{
							Action:      &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{"replicas": "0", "containment": "true"}},
							Observation: "SUCCESS: ScaleDeployment executed successfully",
						},
					},
				},
			},
			wantContains: true,
		},
		{
			name: "agentic history with FAILED containment step → not contained (workload still serving traffic)",
			plan: &platformv1alpha1.RemediationPlan{
				Spec: platformv1alpha1.RemediationPlanSpec{
					AgenticHistory: []platformv1alpha1.AgenticStep{
						{
							Action:      &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionScaleDeployment, Params: map[string]string{"replicas": "0", "containment": "true"}},
							Observation: "FAILED: rbac forbidden",
						},
					},
				},
			},
		},
		{
			name: "agentic observation-only step (no Action) → not contained",
			plan: &platformv1alpha1.RemediationPlan{
				Spec: platformv1alpha1.RemediationPlanSpec{
					AgenticHistory: []platformv1alpha1.AgenticStep{
						{Action: nil, Observation: "Observation step — no action taken"},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contained, action := planAppliedContainment(tc.plan)
			if contained != tc.wantContains {
				t.Fatalf("planAppliedContainment: want %v, got %v (action=%q)", tc.wantContains, contained, action)
			}
			if tc.wantContains && tc.wantSubstr != "" && !strings.Contains(action, tc.wantSubstr) {
				t.Fatalf("required-action description should contain %q, got %q", tc.wantSubstr, action)
			}
			if !tc.wantContains && action != "" {
				t.Fatalf("non-contained plan must return empty required-action, got %q", action)
			}
		})
	}
}

// =============================================================================
// GAP-04 — IsChaosInduced label-derived helper
// =============================================================================

func TestIsChaosInduced(t *testing.T) {
	cases := []struct {
		name  string
		issue *platformv1alpha1.Issue
		want  bool
	}{
		{name: "nil issue → false", issue: nil},
		{name: "no labels → false", issue: &platformv1alpha1.Issue{}},
		{
			name: "unrelated label → false",
			issue: &platformv1alpha1.Issue{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
			},
		},
		{
			name: "source=production → false",
			issue: &platformv1alpha1.Issue{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelSource: "production"}},
			},
		},
		{
			name: "source=chaos-experiment → true",
			issue: &platformv1alpha1.Issue{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelSource: SourceChaosExperiment}},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsChaosInduced(tc.issue); got != tc.want {
				t.Fatalf("IsChaosInduced: want %v, got %v", tc.want, got)
			}
		})
	}
}

// =============================================================================
// GAP-04 — FindActiveChaosExperiment correlation
// =============================================================================

func TestFindActiveChaosExperiment(t *testing.T) {
	now := metav1.Now()
	old := metav1.NewTime(time.Now().Add(-10 * time.Minute))

	target := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}

	cases := []struct {
		name    string
		exps    []platformv1alpha1.ChaosExperiment
		want    bool
		wantExp string
	}{
		{name: "no experiments → nil"},
		{
			name: "running experiment targeting same resource → match",
			exps: []platformv1alpha1.ChaosExperiment{{
				ObjectMeta: metav1.ObjectMeta{Name: "kill-1", Namespace: "default"},
				Spec:       platformv1alpha1.ChaosExperimentSpec{Target: target, ExperimentType: platformv1alpha1.ChaosTypePodKill, Duration: "1m"},
				Status:     platformv1alpha1.ChaosExperimentStatus{State: platformv1alpha1.ChaosStateRunning, StartedAt: &now},
			}},
			want:    true,
			wantExp: "kill-1",
		},
		{
			name: "completed experiment within 2min recovery window → match",
			exps: []platformv1alpha1.ChaosExperiment{{
				ObjectMeta: metav1.ObjectMeta{Name: "kill-2", Namespace: "default"},
				Spec:       platformv1alpha1.ChaosExperimentSpec{Target: target, Duration: "1m"},
				Status:     platformv1alpha1.ChaosExperimentStatus{State: platformv1alpha1.ChaosStateCompleted, CompletedAt: &now},
			}},
			want:    true,
			wantExp: "kill-2",
		},
		{
			name: "completed experiment 10min ago → no match (outside recovery window)",
			exps: []platformv1alpha1.ChaosExperiment{{
				ObjectMeta: metav1.ObjectMeta{Name: "kill-3", Namespace: "default"},
				Spec:       platformv1alpha1.ChaosExperimentSpec{Target: target, Duration: "1m"},
				Status:     platformv1alpha1.ChaosExperimentStatus{State: platformv1alpha1.ChaosStateCompleted, CompletedAt: &old},
			}},
		},
		{
			name: "running experiment in different namespace → no match",
			exps: []platformv1alpha1.ChaosExperiment{{
				ObjectMeta: metav1.ObjectMeta{Name: "kill-4", Namespace: "other"},
				Spec: platformv1alpha1.ChaosExperimentSpec{
					Target: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "other"},
				},
				Status: platformv1alpha1.ChaosExperimentStatus{State: platformv1alpha1.ChaosStateRunning},
			}},
		},
		{
			name: "running experiment targeting different name → no match",
			exps: []platformv1alpha1.ChaosExperiment{{
				ObjectMeta: metav1.ObjectMeta{Name: "kill-5", Namespace: "default"},
				Spec: platformv1alpha1.ChaosExperimentSpec{
					Target: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "api", Namespace: "default"},
				},
				Status: platformv1alpha1.ChaosExperimentStatus{State: platformv1alpha1.ChaosStateRunning},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheme()
			builder := fake.NewClientBuilder().WithScheme(s)
			for i := range tc.exps {
				builder = builder.WithObjects(&tc.exps[i])
			}
			ce := NewCorrelationEngine(builder.Build())

			got, err := ce.FindActiveChaosExperiment(context.Background(), target)
			if err != nil {
				t.Fatalf("FindActiveChaosExperiment: unexpected error %v", err)
			}
			if (got != nil) != tc.want {
				t.Fatalf("match presence: want %v, got %v", tc.want, got != nil)
			}
			if tc.want && got.Name != tc.wantExp {
				t.Fatalf("matched wrong experiment: want %q, got %q", tc.wantExp, got.Name)
			}
		})
	}
}

// =============================================================================
// GAP-03 — PostMortemReconciler reverts premature Closed transitions
// =============================================================================

func TestPostMortemReconciler_RevertsPrematureClose(t *testing.T) {
	cases := []struct {
		name      string
		pm        *platformv1alpha1.PostMortem
		wantState platformv1alpha1.PostMortemState
	}{
		{
			name: "Closed PostMortem without requiresHumanAction stays Closed",
			pm: &platformv1alpha1.PostMortem{
				ObjectMeta: metav1.ObjectMeta{Name: "pm-1", Namespace: "default"},
				Spec:       platformv1alpha1.PostMortemSpec{IssueRef: platformv1alpha1.IssueRef{Name: "iss"}, Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: platformv1alpha1.IssueSeverityHigh},
				Status:     platformv1alpha1.PostMortemStatus{State: platformv1alpha1.PostMortemStateClosed},
			},
			wantState: platformv1alpha1.PostMortemStateClosed,
		},
		{
			name: "Closed PostMortem with requiresHumanAction and no ack → reverts to Open",
			pm: &platformv1alpha1.PostMortem{
				ObjectMeta: metav1.ObjectMeta{Name: "pm-2", Namespace: "default"},
				Spec: platformv1alpha1.PostMortemSpec{
					IssueRef: platformv1alpha1.IssueRef{Name: "iss"}, Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: platformv1alpha1.IssueSeverityHigh,
					RequiresHumanAction: true,
					RequiredAction:      "restore replicas",
				},
				Status: platformv1alpha1.PostMortemStatus{State: platformv1alpha1.PostMortemStateClosed},
			},
			wantState: platformv1alpha1.PostMortemStateOpen,
		},
		{
			name: "Closed PostMortem with requiresHumanAction and ack annotation stays Closed",
			pm: &platformv1alpha1.PostMortem{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "pm-3",
					Namespace:   "default",
					Annotations: map[string]string{AnnotationHumanActionAcknowledged: "true"},
				},
				Spec: platformv1alpha1.PostMortemSpec{
					IssueRef: platformv1alpha1.IssueRef{Name: "iss"}, Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: platformv1alpha1.IssueSeverityHigh,
					RequiresHumanAction: true,
				},
				Status: platformv1alpha1.PostMortemStatus{State: platformv1alpha1.PostMortemStateClosed},
			},
			wantState: platformv1alpha1.PostMortemStateClosed,
		},
		{
			name: "PostMortem with empty status gets initialized to Open",
			pm: &platformv1alpha1.PostMortem{
				ObjectMeta: metav1.ObjectMeta{Name: "pm-4", Namespace: "default"},
				Spec:       platformv1alpha1.PostMortemSpec{IssueRef: platformv1alpha1.IssueRef{Name: "iss"}, Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, Severity: platformv1alpha1.IssueSeverityHigh},
			},
			wantState: platformv1alpha1.PostMortemStateOpen,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScheme()
			c := fake.NewClientBuilder().
				WithScheme(s).
				WithStatusSubresource(&platformv1alpha1.PostMortem{}).
				WithObjects(tc.pm).
				Build()
			r := &PostMortemReconciler{Client: c, Scheme: s}

			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: tc.pm.Name, Namespace: tc.pm.Namespace}}
			if _, err := r.Reconcile(context.Background(), req); err != nil {
				t.Fatalf("Reconcile: unexpected error %v", err)
			}

			var got platformv1alpha1.PostMortem
			if err := c.Get(context.Background(), types.NamespacedName{Name: tc.pm.Name, Namespace: tc.pm.Namespace}, &got); err != nil {
				t.Fatalf("post-reconcile Get: %v", err)
			}
			if got.Status.State != tc.wantState {
				t.Fatalf("status.state: want %q, got %q", tc.wantState, got.Status.State)
			}
		})
	}
}

// TestHumanActionAcknowledged covers the predicate that gates the PostMortem
// close-revert behaviour. Accepts a small set of truthy values so manual
// `kubectl annotate` users don't get tripped up.
func TestHumanActionAcknowledged(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"false", false},
		{"no", false},
		{"true", true},
		{"True", true},
		{"yes", true},
		{"ack", true},
		{"acknowledged", true},
	}
	for _, tc := range cases {
		t.Run("value="+tc.val, func(t *testing.T) {
			pm := &platformv1alpha1.PostMortem{}
			if tc.val != "" {
				pm.Annotations = map[string]string{AnnotationHumanActionAcknowledged: tc.val}
			}
			if got := humanActionAcknowledged(pm); got != tc.want {
				t.Fatalf("humanActionAcknowledged(%q): want %v, got %v", tc.val, tc.want, got)
			}
		})
	}
}

// =============================================================================
// GAP-05 — Effective ExecDiagnostic allowlist startup summary
// =============================================================================

func TestGetDiagnosticAllowlistSummary(t *testing.T) {
	summary := GetDiagnosticAllowlistSummary()

	if summary.DefaultCount == 0 {
		t.Fatalf("DefaultCount must be > 0 — the operator ships built-in defaults")
	}
	if summary.TotalCount < summary.DefaultCount {
		t.Fatalf("TotalCount (%d) must be >= DefaultCount (%d)", summary.TotalCount, summary.DefaultCount)
	}
	if summary.CustomCount != len(summary.Custom) {
		t.Fatalf("CustomCount (%d) and len(Custom) (%d) must agree", summary.CustomCount, len(summary.Custom))
	}
	if summary.DefaultCount+summary.CustomCount != summary.TotalCount {
		t.Fatalf("DefaultCount + CustomCount should equal TotalCount when no overlap, got %d+%d != %d",
			summary.DefaultCount, summary.CustomCount, summary.TotalCount)
	}
}

// =============================================================================
// GAP-02 — alertHash uniqueness on resource recreation
// =============================================================================

// TestAlertHash_DifferentUIDs is the canonical assertion for the GAP-02 fix:
// two alerts identical in type/deployment/namespace but with different UIDs
// (resource recreated with the same name) MUST produce distinct hashes so the
// dedup cache lets the new Anomaly through.
func TestAlertHash_DifferentUIDs(t *testing.T) {
	alert := &pb.WatcherAlert{Type: "CrashLoopBackOff", Deployment: "web", Namespace: "default"}
	h1 := alertHash(alert, "uid-A")
	h2 := alertHash(alert, "uid-B")
	if h1 == h2 {
		t.Fatalf("alertHash collision across UIDs would re-introduce the GAP-02 bug")
	}
}

// TestAlertHash_StableWithinUID guards the inverse: the same alert with the
// same UID must produce the same hash on every poll cycle, otherwise an
// ongoing CrashLoopBackOff would create one Anomaly per 30s poll.
func TestAlertHash_StableWithinUID(t *testing.T) {
	alert := &pb.WatcherAlert{Type: "CrashLoopBackOff", Deployment: "web", Namespace: "default"}
	if alertHash(alert, "uid-X") != alertHash(alert, "uid-X") {
		t.Fatalf("alertHash must be deterministic for a (type, deployment, namespace, uid) tuple")
	}
}

// =============================================================================
// REST analytics — accumulateIssueCounters (GAP-03 + GAP-04 counters)
// =============================================================================

// Note: REST-package helpers (accumulateIssueCounters, foldIssueSummary) are
// covered by tests in operator/api/rest/ — kept out of this file to respect
// package boundaries (this package is operator/controllers).

// =============================================================================
// REST analytics regression tests live in api/rest because of the package
// boundary. The functions exercised below are package-public though, so we
// touch them here too for the patch-coverage gate.
// =============================================================================

// Sentinel: ensures package-level constants required by other tests exist
// and have the values their callers rely on. Failing this test means a
// downstream consumer (Helm chart, docs) is now stale.
func TestPackageConstants(t *testing.T) {
	if LabelSource != "platform.chatcli.io/source" {
		t.Fatalf("LabelSource constant drifted from its documented value")
	}
	if SourceChaosExperiment != "chaos-experiment" {
		t.Fatalf("SourceChaosExperiment constant drifted from its documented value")
	}
	if AnnotationHumanActionAcknowledged != "aiops.chatcli.io/human-action-acknowledged" {
		t.Fatalf("AnnotationHumanActionAcknowledged drifted — Helm NOTES.txt and docs reference this exact key")
	}
}
