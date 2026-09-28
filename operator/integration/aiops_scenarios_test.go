/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package integration

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	escalationLevelKey = "platform.chatcli.io/escalation-level"
	escalationTimeKey  = "platform.chatcli.io/escalation-time"
)

// parkIssue creates the Issue the fake server refuses to analyze and writes
// the given status, retrying while the Issue reconciler races the write.
func parkIssue(t *testing.T, ns string, labels map[string]string, set func(*platformv1alpha1.IssueStatus)) *platformv1alpha1.Issue {
	t.Helper()
	ctx := context.Background()
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: analysisUnavailableFor, Namespace: ns, Labels: labels},
		Spec: platformv1alpha1.IssueSpec{
			Severity: platformv1alpha1.IssueSeverityHigh, Source: platformv1alpha1.IssueSourceWatcher, SignalType: "pod_not_ready",
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "db", Namespace: ns}, Description: "db pods not ready", RiskScore: 70,
		},
	}
	mustCreate(t, issue)
	eventually(t, wait, "the parked status to be accepted", func() bool {
		if err := k8sClient.Get(ctx, key(ns, analysisUnavailableFor), issue); err != nil {
			return false
		}
		set(&issue.Status)
		return k8sClient.Status().Update(ctx, issue) == nil
	})
	return issue
}

// An Escalated Issue climbs L1 -> L2 -> L3 as each level times out, the
// level is persisted on the Issue each time, and the chain stops at L3.
// The timeouts are simulated by moving the escalation clock back.
func TestEscalationReachesL3(t *testing.T) {
	ns := namespace(t, "it-escalation")
	ctx := context.Background()
	level := func(name string) platformv1alpha1.EscalationLevel {
		return platformv1alpha1.EscalationLevel{Name: name, TimeoutMinutes: 1,
			Targets: []platformv1alpha1.EscalationTarget{{Type: platformv1alpha1.EscalationTargetTeam, Name: "sre"}}}
	}
	mustCreate(t, &platformv1alpha1.EscalationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "db-chain", Namespace: ns},
		Spec: platformv1alpha1.EscalationPolicySpec{Enabled: true, Severities: []platformv1alpha1.IssueSeverity{platformv1alpha1.IssueSeverityHigh},
			Levels: []platformv1alpha1.EscalationLevel{level("L1"), level("L2"), level("L3")}},
	})
	detected := metav1.NewTime(time.Now().Add(-time.Hour))
	parkIssue(t, ns, nil, func(s *platformv1alpha1.IssueStatus) {
		s.State = platformv1alpha1.IssueStateEscalated
		s.DetectedAt = &detected
	})

	var issue platformv1alpha1.Issue
	levelIs := func(want string) func() bool {
		return func() bool {
			return k8sClient.Get(ctx, key(ns, analysisUnavailableFor), &issue) == nil && issue.Annotations[escalationLevelKey] == want
		}
	}
	eventually(t, wait, "escalation to start at L1", levelIs("0"))
	backdate := func() {
		t.Helper()
		eventually(t, wait, "the escalation clock to move back", func() bool {
			if err := k8sClient.Get(ctx, key(ns, analysisUnavailableFor), &issue); err != nil {
				return false
			}
			patch := client.MergeFrom(issue.DeepCopy())
			issue.Annotations[escalationTimeKey] = time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339)
			return k8sClient.Patch(ctx, &issue, patch) == nil
		})
	}
	backdate()
	eventually(t, wait, "escalation to reach L2", levelIs("1"))
	backdate()
	eventually(t, wait, "escalation to reach L3", levelIs("2"))

	// Past L3's timeout the chain stays at L3.
	backdate()
	time.Sleep(3 * time.Second)
	if err := k8sClient.Get(ctx, key(ns, analysisUnavailableFor), &issue); err != nil {
		t.Fatal(err)
	}
	if got := issue.Annotations[escalationLevelKey]; got != "2" {
		t.Fatalf("level after the last timeout = %q, want 2", got)
	}
	var policy platformv1alpha1.EscalationPolicy
	eventually(t, wait, "the policy to record level 2", func() bool {
		return k8sClient.Get(ctx, key(ns, "db-chain"), &policy) == nil &&
			len(policy.Status.ActiveEscalations) == 1 && policy.Status.ActiveEscalations[0].CurrentLevel == 2
	})
	if policy.Status.TotalEscalations != 1 {
		t.Errorf("TotalEscalations = %d, want 1", policy.Status.TotalEscalations)
	}
}

// An exhausted error budget opens exactly one critical Issue however many
// times the SLO reconciles while it stays exhausted.
func TestSLOBudgetExhaustionPagesOnce(t *testing.T) {
	ns := namespace(t, "it-slo")
	ctx := context.Background()
	detected := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	parkIssue(t, ns, map[string]string{"platform.chatcli.io/service": "checkout"}, func(s *platformv1alpha1.IssueStatus) {
		s.State = platformv1alpha1.IssueStateAnalyzing
		s.DetectedAt = &detected
	})
	slo := &platformv1alpha1.ServiceLevelObjective{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout-availability", Namespace: ns},
		Spec: platformv1alpha1.ServiceLevelObjectiveSpec{
			ServiceName: "checkout", Enabled: true,
			Indicator:   platformv1alpha1.SLOIndicator{Type: platformv1alpha1.SLOIndicatorAvailability, MetricSource: platformv1alpha1.SLOSourceIssues},
			Target:      platformv1alpha1.SLOTarget{Percentage: 99.9, Window: "1d"},
			AlertPolicy: platformv1alpha1.SLOAlertPolicy{PageOnBudgetExhausted: true},
		},
	}
	mustCreate(t, slo)

	pages := func() int {
		var list platformv1alpha1.IssueList
		if err := k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
			return -1
		}
		n := 0
		for _, iss := range list.Items {
			if iss.Annotations["platform.chatcli.io/slo-window"] == "budget-exhausted" {
				n++
			}
		}
		return n
	}
	eventually(t, wait, "the budget-exhausted page", func() bool { return pages() >= 1 })

	// Force several more reconciles, a second apart (Issue names carry the
	// second, so a regression would show up as more Issues).
	for i := 0; i < 3; i++ {
		time.Sleep(1100 * time.Millisecond)
		eventually(t, wait, "the SLO to be touched", func() bool {
			var cur platformv1alpha1.ServiceLevelObjective
			if err := k8sClient.Get(ctx, key(ns, slo.Name), &cur); err != nil {
				return false
			}
			patch := client.MergeFrom(cur.DeepCopy())
			if cur.Annotations == nil {
				cur.Annotations = map[string]string{}
			}
			cur.Annotations["it.chatcli.io/touch"] = time.Now().Format(time.RFC3339Nano)
			return k8sClient.Patch(ctx, &cur, patch) == nil
		})
	}
	time.Sleep(2 * time.Second)
	if got := pages(); got != 1 {
		t.Fatalf("budget-exhausted Issues = %d, want 1", got)
	}
	var cur platformv1alpha1.ServiceLevelObjective
	if err := k8sClient.Get(ctx, key(ns, slo.Name), &cur); err != nil {
		t.Fatal(err)
	}
	if cur.Status.ErrorBudgetRemaining != 0 {
		t.Errorf("errorBudgetRemaining = %v, want 0", cur.Status.ErrorBudgetRemaining)
	}
}
