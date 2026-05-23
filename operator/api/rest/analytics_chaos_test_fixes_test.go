/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package rest

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// TestAccumulateIssueCounters guards the GAP-03 + GAP-04 dashboard counters:
//
//   - Contained Issues are tracked in their own bucket AND counted as Open
//     (customer impact persists until a human restores the workload).
//   - chaos-induced Issues are tracked separately so production dashboards
//     can subtract them from "real" incident counts.
func TestAccumulateIssueCounters(t *testing.T) {
	cases := []struct {
		name     string
		issue    *v1alpha1.Issue
		mutator  func(summary *AnalyticsSummary)
		expected func(summary *AnalyticsSummary) bool
		describe string
	}{
		{
			name: "Resolved issue increments ResolvedIssues only",
			issue: &v1alpha1.Issue{
				Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityHigh},
				Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved},
			},
			expected: func(s *AnalyticsSummary) bool {
				return s.ResolvedIssues == 1 && s.OpenIssues == 0 && s.ContainedIssues == 0
			},
			describe: "ResolvedIssues=1, OpenIssues=0, ContainedIssues=0",
		},
		{
			name: "Contained issue increments BOTH ContainedIssues and OpenIssues (GAP-03)",
			issue: &v1alpha1.Issue{
				Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityCritical},
				Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateContained},
			},
			expected: func(s *AnalyticsSummary) bool {
				return s.ContainedIssues == 1 && s.OpenIssues == 1 && s.ResolvedIssues == 0 && s.CriticalIssues == 1
			},
			describe: "ContainedIssues=1, OpenIssues=1 (customer impact persists)",
		},
		{
			name: "Detected/Analyzing/Remediating/Escalated/Failed all count as Open",
			issue: &v1alpha1.Issue{
				Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityMedium},
				Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateRemediating},
			},
			expected: func(s *AnalyticsSummary) bool { return s.OpenIssues == 1 && s.ContainedIssues == 0 },
		},
		{
			name: "Chaos-induced issue increments ChaosInducedIssues (GAP-04)",
			issue: &v1alpha1.Issue{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"platform.chatcli.io/source": "chaos-experiment"},
				},
				Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityLow},
				Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved},
			},
			expected: func(s *AnalyticsSummary) bool {
				return s.ResolvedIssues == 1 && s.ChaosInducedIssues == 1
			},
			describe: "ChaosInducedIssues=1 (tracked separately so MTTR widgets can subtract)",
		},
		{
			name: "Critical Open chaos-induced increments three counters at once",
			issue: &v1alpha1.Issue{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"platform.chatcli.io/source": "chaos-experiment"},
				},
				Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityCritical},
				Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateDetected},
			},
			expected: func(s *AnalyticsSummary) bool {
				return s.OpenIssues == 1 && s.CriticalIssues == 1 && s.ChaosInducedIssues == 1
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &AnalyticsSummary{SeverityBreakdown: map[string]int{}}
			accumulateIssueCounters(s, tc.issue)
			if !tc.expected(s) {
				t.Fatalf("counters wrong (expected %s): %+v", tc.describe, s)
			}
		})
	}
}

// TestFoldIssueSummary verifies the time-range filtering plus the aggregate
// math (total, severity breakdown, average risk).
func TestFoldIssueSummary(t *testing.T) {
	now := metav1.Now()

	issues := []v1alpha1.Issue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "i1", CreationTimestamp: now},
			Spec:       v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityCritical, RiskScore: 90},
			Status:     v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "i2", CreationTimestamp: now},
			Spec:       v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityHigh, RiskScore: 60},
			Status:     v1alpha1.IssueStatus{State: v1alpha1.IssueStateContained},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "i3", CreationTimestamp: now,
				Labels: map[string]string{"platform.chatcli.io/source": "chaos-experiment"},
			},
			Spec:   v1alpha1.IssueSpec{Severity: v1alpha1.IssueSeverityLow, RiskScore: 30},
			Status: v1alpha1.IssueStatus{State: v1alpha1.IssueStateResolved},
		},
	}

	s := &AnalyticsSummary{SeverityBreakdown: map[string]int{}}
	foldIssueSummary(s, issues, timeRangeParams{}) // empty tr → include all

	if s.TotalIssues != 3 {
		t.Fatalf("TotalIssues: want 3, got %d", s.TotalIssues)
	}
	if s.ResolvedIssues != 2 {
		t.Fatalf("ResolvedIssues: want 2 (i1 + i3), got %d", s.ResolvedIssues)
	}
	if s.ContainedIssues != 1 || s.OpenIssues != 1 {
		t.Fatalf("Contained=%d Open=%d: Contained must be 1 AND counted as Open", s.ContainedIssues, s.OpenIssues)
	}
	if s.CriticalIssues != 1 {
		t.Fatalf("CriticalIssues: want 1, got %d", s.CriticalIssues)
	}
	if s.ChaosInducedIssues != 1 {
		t.Fatalf("ChaosInducedIssues: want 1 (only i3 has the label), got %d", s.ChaosInducedIssues)
	}
	wantAvg := float64(90+60+30) / 3.0
	if s.AvgRiskScore != wantAvg {
		t.Fatalf("AvgRiskScore: want %f, got %f", wantAvg, s.AvgRiskScore)
	}
	if s.SeverityBreakdown["critical"] != 1 || s.SeverityBreakdown["high"] != 1 || s.SeverityBreakdown["low"] != 1 {
		t.Fatalf("SeverityBreakdown does not match: %+v", s.SeverityBreakdown)
	}
}

// TestRemediationOutcomesByIssue covers the inner helper of foldRemediationSummary:
// it must skip plans whose parent Issue was deleted (orphans), and mark an
// Issue as "resolved by remediation" only when ALL parent state lookups
// match Resolved.
func TestRemediationOutcomesByIssue(t *testing.T) {
	plans := []v1alpha1.RemediationPlan{
		{Spec: v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-resolved"}}},
		{Spec: v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-resolved"}}}, // second attempt for the same Issue
		{Spec: v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-failed"}}},
		{Spec: v1alpha1.RemediationPlanSpec{IssueRef: v1alpha1.IssueRef{Name: "iss-orphan"}}}, // parent deleted
	}
	stateByIssue := map[string]v1alpha1.IssueState{
		"iss-resolved": v1alpha1.IssueStateResolved,
		"iss-failed":   v1alpha1.IssueStateEscalated,
		// iss-orphan deliberately omitted
	}

	got := remediationOutcomesByIssue(plans, stateByIssue)

	if len(got) != 2 {
		t.Fatalf("orphan must be skipped, want 2 entries, got %d: %+v", len(got), got)
	}
	if !got["iss-resolved"] {
		t.Fatalf("iss-resolved must map to true (parent reached Resolved)")
	}
	if got["iss-failed"] {
		t.Fatalf("iss-failed must map to false (parent reached Escalated)")
	}
}

// TestIsUnstructuredStateOneOf covers the small helper that reads
// item.status.state from an unstructured K8s object. The analytics endpoint
// uses unstructured access because the SLO CRD may not be registered with the
// operator's scheme when /api/v1/analytics is called.
func TestIsUnstructuredStateOneOf(t *testing.T) {
	cases := []struct {
		name string
		item map[string]interface{}
		want bool
	}{
		{name: "missing status → false", item: map[string]interface{}{}, want: false},
		{name: "missing state → false", item: map[string]interface{}{"status": map[string]interface{}{}}, want: false},
		{name: "state matches first → true", item: map[string]interface{}{"status": map[string]interface{}{"state": "AtRisk"}}, want: true},
		{name: "state matches second → true", item: map[string]interface{}{"status": map[string]interface{}{"state": "Breached"}}, want: true},
		{name: "state doesn't match → false", item: map[string]interface{}{"status": map[string]interface{}{"state": "Healthy"}}, want: false},
		{name: "non-string state → false", item: map[string]interface{}{"status": map[string]interface{}{"state": 123}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnstructuredStateOneOf(tc.item, "AtRisk", "Breached"); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}
