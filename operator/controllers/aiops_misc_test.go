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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// Attaching an anomaly to an open Issue recomputes the risk from every
// anomaly the Issue holds, the new one included.
func TestAnomaly_RiskRecalculationIncludesTheNewAnomaly(t *testing.T) {
	res := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	issue := newIssue("web-issue", "default")
	issue.Spec.Resource = res
	issue.Spec.RiskScore = 10
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	first := newAnomaly("first", "default", platformv1alpha1.SignalErrorRate, res)
	first.Status.Correlated = true
	first.Status.IssueRef = &platformv1alpha1.IssueRef{Name: "web-issue"}
	second := newAnomaly("second", "default", platformv1alpha1.SignalOOMKill, res)
	r, c := setupFakeAnomalyReconciler(issue, first, second)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "second", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.Issue
	if err := c.Get(context.Background(), types.NamespacedName{Name: "web-issue", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	want := int32(signalWeight(platformv1alpha1.SignalErrorRate) + signalWeight(platformv1alpha1.SignalOOMKill))
	if got.Spec.RiskScore != want {
		t.Fatalf("risk = %d, want %d (error_rate + the new oom_kill)", got.Spec.RiskScore, want)
	}
}

// resolutionCooldownMinutes 0 turns the cooldown off: a fresh anomaly on a
// just-resolved resource opens a new Issue.
func TestAnomaly_ZeroCooldownDisablesSuppression(t *testing.T) {
	if got := (&platformv1alpha1.AIOpsSpec{ResolutionCooldownMinutes: 0}).GetResolutionCooldown(); got != 0 {
		t.Fatalf("cooldown for 0 = %v, want off", got)
	}
	if got := (*platformv1alpha1.AIOpsSpec)(nil).GetResolutionCooldown(); got != 10*time.Minute {
		t.Fatalf("cooldown without settings = %v, want the 10m default", got)
	}
	res := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	inst := newInstance("main", "default")
	inst.Spec.AIOps = &platformv1alpha1.AIOpsSpec{ResolutionCooldownMinutes: 0}
	resolved := newIssue("web-old", "default")
	resolved.Spec.Resource = res
	resolved.Status.State = platformv1alpha1.IssueStateResolved
	now := metav1.Now()
	resolved.Status.ResolvedAt = &now
	anomaly := newAnomaly("fresh", "default", platformv1alpha1.SignalErrorRate, res)
	r, c := setupFakeAnomalyReconciler(inst, resolved, anomaly)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "fresh", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var issues platformv1alpha1.IssueList
	if err := c.List(context.Background(), &issues); err != nil {
		t.Fatal(err)
	}
	if len(issues.Items) != 2 {
		t.Fatalf("issues = %d, want a new one next to the resolved Issue", len(issues.Items))
	}
}

// The AIOps settings come from the Instance an object names, else the
// Instance the WatcherBridge connects to (first Ready), not Items[0].
func TestAIOpsInstanceSelection(t *testing.T) {
	notReady := newInstance("aaa-not-ready", "default")
	ready := newInstance("zzz-ready", "default")
	ready.Status.Ready = true
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(notReady, ready).Build()
	ctx := context.Background()
	if got := aiopsInstance(ctx, c, nil); got == nil || got.Name != "zzz-ready" {
		t.Fatalf("default selection = %v, want the Ready Instance", got)
	}
	labels := map[string]string{labelSourceInstance: "aaa-not-ready", labelSourceInstanceNamespace: "default"}
	if got := aiopsInstance(ctx, c, labels); got == nil || got.Name != "aaa-not-ready" {
		t.Fatalf("labeled selection = %v, want the named Instance", got)
	}
	if got := aiopsInstance(ctx, fake.NewClientBuilder().WithScheme(newScheme()).Build(), nil); got != nil {
		t.Fatalf("no Instance must select nothing, got %v", got)
	}
}

// A runbook in the Issue's namespace is a candidate once, not twice.
func TestIssue_RunbooksAreNotListedTwice(t *testing.T) {
	issue := newIssue("i", "default")
	issue.Spec.Resource.Kind = "Deployment"
	rb := newRunbook("scale", "default")
	rb.Spec.Trigger.Severity = issue.Spec.Severity
	rb.Spec.Trigger.ResourceKind = "Deployment"
	other := newRunbook("scale-elsewhere", "ops")
	other.Spec.Trigger = rb.Spec.Trigger
	r, _ := setupFakeIssueReconciler(issue, rb, other)
	got := r.findAllMatchingRunbooks(context.Background(), issue)
	if len(got) != 2 || got[0].Namespace != "default" {
		t.Fatalf("candidates = %d (first %s), want the same-namespace runbook then the other, once each", len(got), got[0].Namespace)
	}
}

// A failed plan action is timelined as action_failed.
func TestPlanActionTimelineTypedByOutcome(t *testing.T) {
	plan := &platformv1alpha1.RemediationPlan{Status: platformv1alpha1.RemediationPlanStatus{
		ActionCheckpoints: []platformv1alpha1.ActionCheckpoint{{ActionIndex: 1, Success: false}},
	}}
	actions := []platformv1alpha1.RemediationAction{{Type: platformv1alpha1.ActionScaleDeployment}, {Type: platformv1alpha1.ActionRestartDeployment}}
	now := metav1.Now()
	if ev := planActionToTimelineEvent(actions[0], 0, plan, now); ev.Type != "action_executed" {
		t.Fatalf("successful action typed %s", ev.Type)
	}
	if ev := planActionToTimelineEvent(actions[1], 1, plan, now); ev.Type != "action_failed" {
		t.Fatalf("failed action typed %s, want action_failed", ev.Type)
	}
}

// Auto-resolve and verification understand DaemonSets, Jobs and Nodes.
func TestResourceHealth_DaemonSetJobNode(t *testing.T) {
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default", Generation: 2},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: 3, NumberReady: 3, UpdatedNumberScheduled: 3}}
	sickDS := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "sick", Namespace: "default"},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, NumberReady: 2, UpdatedNumberScheduled: 3, NumberUnavailable: 1}}
	done := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "migrate", Namespace: "default"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}
	running := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "default"}, Status: batchv1.JobStatus{Active: 1}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	downNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}}
	r, c := setupFakeIssueReconciler(ds, sickDS, done, running, node, downNode)
	rem := &RemediationReconciler{Client: c}
	cases := []struct {
		ref  platformv1alpha1.ResourceRef
		want bool
	}{
		{platformv1alpha1.ResourceRef{Kind: "DaemonSet", Name: "agent", Namespace: "default"}, true},
		{platformv1alpha1.ResourceRef{Kind: "DaemonSet", Name: "sick", Namespace: "default"}, false},
		{platformv1alpha1.ResourceRef{Kind: "Job", Name: "migrate", Namespace: "default"}, true},
		{platformv1alpha1.ResourceRef{Kind: "Job", Name: "running", Namespace: "default"}, false},
		{platformv1alpha1.ResourceRef{Kind: "Node", Name: "node-a", Namespace: "default"}, true},
		{platformv1alpha1.ResourceRef{Kind: "Node", Name: "node-b"}, false},
	}
	for _, tc := range cases {
		got, err := r.isResourceHealthy(context.Background(), tc.ref)
		if err != nil || got != tc.want {
			t.Errorf("isResourceHealthy(%s/%s) = %v, %v; want %v", tc.ref.Kind, tc.ref.Name, got, err, tc.want)
		}
	}
	for _, tc := range cases[4:] {
		got, err := rem.verifyResourceHealth(context.Background(), tc.ref)
		if err != nil || got != tc.want {
			t.Errorf("verifyResourceHealth(Node %s) = %v, %v; want %v", tc.ref.Name, got, err, tc.want)
		}
	}
}

func chaosFixture(exp *platformv1alpha1.ChaosExperiment, objs ...client.Object) (*ChaosReconciler, client.Client) {
	s := newScheme()
	c := fake.NewClientBuilder().WithScheme(s).WithStatusSubresource(&platformv1alpha1.ChaosExperiment{}).
		WithObjects(append(objs, exp)...).Build()
	return &ChaosReconciler{Client: c, Scheme: s}, c
}

func miscChaosExperiment(name string, typ platformv1alpha1.ChaosExperimentType) *platformv1alpha1.ChaosExperiment {
	return &platformv1alpha1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: platformv1alpha1.ChaosExperimentSpec{ExperimentType: typ, Enabled: true, Duration: "1m",
			Target: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}},
	}
}

// Network faults and schedules fail at once with a status that says why,
// instead of "running" without injecting anything.
func TestChaos_UnsupportedExperimentsFailClearly(t *testing.T) {
	scheduled := miscChaosExperiment("weekly", platformv1alpha1.ChaosTypePodKill)
	scheduled.Spec.Schedule = "0 3 * * 1"
	for _, exp := range []*platformv1alpha1.ChaosExperiment{
		miscChaosExperiment("latency", platformv1alpha1.ChaosTypeNetworkDelay),
		miscChaosExperiment("loss", platformv1alpha1.ChaosTypeNetworkLoss),
		scheduled,
	} {
		r, c := chaosFixture(exp)
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: exp.Name, Namespace: "default"}}); err != nil {
			t.Fatal(err)
		}
		var got platformv1alpha1.ChaosExperiment
		if err := c.Get(context.Background(), types.NamespacedName{Name: exp.Name, Namespace: "default"}, &got); err != nil {
			t.Fatal(err)
		}
		if got.Status.State != platformv1alpha1.ChaosStateFailed || !strings.Contains(got.Status.Result, "not supported") {
			t.Fatalf("%s: state %s result %q, want Failed saying it is not supported", exp.Name, got.Status.State, got.Status.Result)
		}
	}
}

// Recovery time runs from the end of the injection, not across one check.
func TestChaos_RecoveryTimeFromInjectionEnd(t *testing.T) {
	replicas := int32(2)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 2}}
	exp := miscChaosExperiment("kill", platformv1alpha1.ChaosTypePodKill)
	exp.Spec.PostExperiment.VerifyRecovery = true
	started := metav1.NewTime(time.Now().Add(-90 * time.Second)) // ended 30s ago
	exp.Status.State = platformv1alpha1.ChaosStateRunning
	exp.Status.StartedAt = &started
	exp.Status.PodsAffected = 1
	r, c := chaosFixture(exp, dep)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "kill", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var got platformv1alpha1.ChaosExperiment
	if err := c.Get(context.Background(), types.NamespacedName{Name: "kill", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	d, err := time.ParseDuration(got.Status.RecoveryTime)
	if err != nil || d < 29*time.Second || d > 40*time.Second {
		t.Fatalf("recoveryTime = %q, want about 30s after the injection ended", got.Status.RecoveryTime)
	}
}

// Without usage history the planner says so instead of reporting a flat
// trend, and the dashboard fields it serves are populated.
func TestCapacity_InsufficientHistoryIsExplicit(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("900m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
		}}}}}}
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(dep).Build()
	f, err := NewCapacityPlanner(c).AnalyzeResourceTrends(context.Background(), platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if f.HistoryAvailable || f.Trend.Direction != TrendInsufficientHistory || f.Forecast.CPUExhaustionDate != nil {
		t.Fatalf("forecast = %+v, want no fabricated trend", f)
	}
	if f.UsageSource != "requests" || f.Urgency != "plan" || !strings.Contains(f.Forecast.Recommendation, "80%") {
		t.Fatalf("forecast = %+v, want requests at 90%% of limits flagged for planning", f)
	}
}
