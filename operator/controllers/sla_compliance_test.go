/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"encoding/json"
	"strings"
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

func TestParseSLADuration(t *testing.T) {
	cases := map[string]time.Duration{
		"15m":   15 * time.Minute,
		"4h":    4 * time.Hour,
		"1h30m": 90 * time.Minute,
		"1d":    24 * time.Hour,
		"2d12h": 60 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseSLADuration(in)
		if err != nil || got != want {
			t.Errorf("ParseSLADuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "xd", "1w", "0s", "1d-"} {
		if _, err := ParseSLADuration(bad); err == nil {
			t.Errorf("ParseSLADuration(%q) accepted an invalid duration", bad)
		}
	}
}

func slaFixture(objs ...client.Object) (*SLAReconciler, client.Client) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&platformv1alpha1.IncidentSLA{}, &platformv1alpha1.Issue{}).
		WithObjects(objs...).Build()
	return &SLAReconciler{Client: c, Scheme: s}, c
}

func reconcileSLA(t *testing.T, r *SLAReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func getSLA(t *testing.T, c client.Client, name string) *platformv1alpha1.IncidentSLA {
	t.Helper()
	var sla platformv1alpha1.IncidentSLA
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &sla); err != nil {
		t.Fatal(err)
	}
	return &sla
}

// A day-based SLA is enforced, and its violation stops being active once
// the Issue resolves.
func TestSLA_DayDurationsEnforcedAndActiveViolationsClose(t *testing.T) {
	sla := &platformv1alpha1.IncidentSLA{
		ObjectMeta: metav1.ObjectMeta{Name: "high-1d", Namespace: "default"},
		Spec:       platformv1alpha1.IncidentSLASpec{Severity: platformv1alpha1.IssueSeverityHigh, ResponseTime: "1d", ResolutionTime: "2d"},
	}
	issue := newIssue("slow", "default")
	detected := metav1.NewTime(time.Now().Add(-26 * time.Hour))
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	issue.Status.DetectedAt = &detected
	r, c := slaFixture(sla, issue)

	reconcileSLA(t, r, "slow")
	got := getSLA(t, c, "high-1d")
	if got.Status.TotalViolations != 1 || got.Status.ActiveViolations != 1 {
		t.Fatalf("after the late response: total %d active %d, want 1/1", got.Status.TotalViolations, got.Status.ActiveViolations)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, "Ready"); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %+v, want True for valid durations", cond)
	}

	var iss platformv1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: "slow", Namespace: "default"}, &iss); err != nil {
		t.Fatal(err)
	}
	resolved := metav1.Now()
	iss.Status.State = platformv1alpha1.IssueStateResolved
	iss.Status.ResolvedAt = &resolved
	if err := c.Status().Update(context.Background(), &iss); err != nil {
		t.Fatal(err)
	}
	reconcileSLA(t, r, "slow")
	reconcileSLA(t, r, "slow")
	got = getSLA(t, c, "high-1d")
	if got.Status.ActiveViolations != 0 {
		t.Fatalf("active violations after resolve = %d, want 0", got.Status.ActiveViolations)
	}
	if got.Status.TotalViolations != 1 {
		t.Fatalf("total violations = %d, want 1 (resolved within 2d)", got.Status.TotalViolations)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, "SLAViolation"); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("SLAViolation = %+v, want False once every violating Issue resolved", cond)
	}
}

// An unparsable duration is reported on the SLA instead of being ignored.
func TestSLA_InvalidDurationIsReported(t *testing.T) {
	sla := &platformv1alpha1.IncidentSLA{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "default"},
		Spec:       platformv1alpha1.IncidentSLASpec{Severity: platformv1alpha1.IssueSeverityHigh, ResponseTime: "1 week", ResolutionTime: "4h"},
	}
	issue := newIssue("any", "default")
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	r, c := slaFixture(sla, issue)
	reconcileSLA(t, r, "any")
	cond := meta.FindStatusCondition(getSLA(t, c, "broken").Status.Conditions, "Ready")
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != "InvalidDuration" {
		t.Fatalf("Ready = %+v, want False/InvalidDuration", cond)
	}
}

// With IncidentSLAs in scope the report counts what their controller
// recorded, response violations included, and lists each SLA.
func TestCompliance_CountsIncidentSLAViolations(t *testing.T) {
	now := time.Now()
	mk := func(name, violated string, state platformv1alpha1.IssueState) *platformv1alpha1.Issue {
		iss := newIssue(name, "default")
		iss.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
		iss.Status.State = state
		if violated != "" {
			iss.Annotations = map[string]string{"platform.chatcli.io/sla-violated": violated}
		}
		return iss
	}
	sla := &platformv1alpha1.IncidentSLA{
		ObjectMeta: metav1.ObjectMeta{Name: "high", Namespace: "default"},
		Spec:       platformv1alpha1.IncidentSLASpec{Severity: platformv1alpha1.IssueSeverityHigh, ResponseTime: "5m", ResolutionTime: "1h"},
		Status:     platformv1alpha1.IncidentSLAStatus{TotalViolations: 3, ActiveViolations: 1, CompliancePercentage: 50},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		sla,
		mk("both", "response,resolution", platformv1alpha1.IssueStateResolved),
		mk("late-answer", "response", platformv1alpha1.IssueStateResolved),
		mk("escalated-in-time", "", platformv1alpha1.IssueStateEscalated),
		mk("fine", "", platformv1alpha1.IssueStateResolved),
	).Build()
	report, err := NewComplianceReporter(c).GenerateReportForPeriod(context.Background(), "default", now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	m := report.SLAMetrics
	if m.ResponseSLAViolations != 2 || m.ResolutionSLAViolations != 1 {
		t.Fatalf("violations = response %d resolution %d, want 2/1", m.ResponseSLAViolations, m.ResolutionSLAViolations)
	}
	if m.CompliancePercentage != 50 {
		t.Fatalf("compliance = %v, want 50 (2 of 4 Issues violated)", m.CompliancePercentage)
	}
	if len(report.IncidentSLAs) != 1 || report.IncidentSLAs[0].TotalViolations != 3 || report.IncidentSLAs[0].ActiveViolations != 1 {
		t.Fatalf("IncidentSLAs = %+v", report.IncidentSLAs)
	}

	// The wire format keeps the PascalCase keys the API always served.
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"Period"`, `"SLAMetrics"`, `"ResponseSLAViolations"`, `"IncidentMetrics"`, `"MTTD"`, `"IncidentSLAs"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("report JSON lacks %s: %s", key, raw)
		}
	}
}

// The report honors an absolute period: Issues after its end are out.
func TestCompliance_PeriodHasAnEnd(t *testing.T) {
	now := time.Now()
	inside := newIssue("inside", "default")
	inside.CreationTimestamp = metav1.NewTime(now.Add(-72 * time.Hour))
	after := newIssue("after", "default")
	after.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(inside, after).Build()
	start, end := now.Add(-96*time.Hour), now.Add(-48*time.Hour)
	report, err := NewComplianceReporter(c).GenerateReportForPeriod(context.Background(), "", start, end)
	if err != nil {
		t.Fatal(err)
	}
	if report.IncidentMetrics.TotalIncidents != 1 || !report.Period.End.Equal(end) {
		t.Fatalf("incidents %d period %+v, want only the Issue inside the period", report.IncidentMetrics.TotalIncidents, report.Period)
	}
}
