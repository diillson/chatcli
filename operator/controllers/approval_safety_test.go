/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const testNS = "default"

func approvalTestClient(funcs *interceptor.Funcs, objs ...client.Object) client.Client {
	b := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(
		&platformv1alpha1.RemediationPlan{}, &platformv1alpha1.Issue{}, &platformv1alpha1.AIInsight{},
		&platformv1alpha1.ApprovalRequest{}, &platformv1alpha1.ApprovalPolicy{}, &platformv1alpha1.ChaosExperiment{},
	).WithObjects(objs...)
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

func reconcilePlan(t *testing.T, r *RemediationReconciler, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNS}})
}

func getPlan(t *testing.T, c client.Client, name string) platformv1alpha1.RemediationPlan {
	t.Helper()
	var p platformv1alpha1.RemediationPlan
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: testNS}, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func getAR(t *testing.T, c client.Client, name string) platformv1alpha1.ApprovalRequest {
	t.Helper()
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: testNS}, &ar); err != nil {
		t.Fatal(err)
	}
	return ar
}

func manualPolicy(name string, mode platformv1alpha1.ApprovalMode, required int32) *platformv1alpha1.ApprovalPolicy {
	return &platformv1alpha1.ApprovalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: platformv1alpha1.ApprovalPolicySpec{Enabled: true, Rules: []platformv1alpha1.ApprovalRule{{
			Name: "r", Mode: mode, RequiredApprovers: required, TimeoutMinutes: 30,
		}}},
	}
}

func pendingAR(name, policy string, required int32, created time.Time) *platformv1alpha1.ApprovalRequest {
	return &platformv1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS, CreationTimestamp: metav1.NewTime(created)},
		Spec: platformv1alpha1.ApprovalRequestSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: "test-issue"}, RemediationPlanRef: "plan-1",
			PolicyRef: policy, RuleName: "r", TimeoutMinutes: 30, RequiredApprovers: required,
		},
		Status: platformv1alpha1.ApprovalRequestStatus{State: platformv1alpha1.ApprovalStatePending},
	}
}

// approvalsCounter reads chatcli_operator_approvals_total{mode,result}.
func approvalsCounter(t *testing.T, mode, result string) float64 {
	t.Helper()
	mfs, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "chatcli_operator_approvals_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}
			if labels["mode"] == mode && labels["result"] == result {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func reconcileAR(t *testing.T, c client.Client, name string) ctrl.Result {
	t.Helper()
	r := &ApprovalReconciler{Client: c, Scheme: newScheme()}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: testNS}})
	if err != nil {
		t.Fatalf("approval reconcile: %v", err)
	}
	return res
}

// Item 1: every read the gate needs fails closed. Before the fix a policy
// listing error logged "proceeding without approval" and the plan ran.
func TestApprovalGate_FailsClosedOnReadErrors(t *testing.T) {
	boom := stderrors.New("apiserver unavailable")
	cases := map[string]interceptor.Funcs{
		"policy list": {List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*platformv1alpha1.ApprovalPolicyList); ok {
				return boom
			}
			return c.List(ctx, list, opts...)
		}},
		"issue get": {Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*platformv1alpha1.Issue); ok {
				return boom
			}
			return c.Get(ctx, key, obj, opts...)
		}},
		"insight get": {Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*platformv1alpha1.AIInsight); ok {
				return boom
			}
			return c.Get(ctx, key, obj, opts...)
		}},
	}
	for name, funcs := range cases {
		t.Run(name, func(t *testing.T) {
			funcs := funcs
			c := approvalTestClient(&funcs, newRemediationPlan("plan-1", testNS), newIssue("test-issue", testNS),
				manualPolicy("p", platformv1alpha1.ApprovalModeManual, 1), newDeployment("web", testNS, 2))
			rec := events.NewFakeRecorder(4)
			r := &RemediationReconciler{Client: c, Scheme: newScheme(), EventRecorder: rec}
			if _, err := reconcilePlan(t, r, "plan-1"); err == nil || !strings.Contains(err.Error(), "approval gate") {
				t.Fatalf("reconcile error = %v, want the gate error (backoff)", err)
			}
			if p := getPlan(t, c, "plan-1"); p.Status.State != "" && p.Status.State != platformv1alpha1.RemediationStatePending {
				t.Fatalf("plan state = %s, want it to stay Pending", p.Status.State)
			}
			select {
			case e := <-rec.Events:
				if !strings.Contains(e, "ApprovalGateUnavailable") {
					t.Fatalf("event = %q", e)
				}
			default:
				t.Fatal("no ApprovalGateUnavailable event")
			}
		})
	}
}

// Item 1: a decision engine that cannot decide has not allowed the plan.
func TestApprovalGate_DecisionEngineErrorFailsClosed(t *testing.T) {
	funcs := interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if _, ok := list.(*platformv1alpha1.RemediationPlanList); ok {
			return stderrors.New("cache not synced")
		}
		return c.List(ctx, list, opts...)
	}}
	c := approvalTestClient(&funcs, newRemediationPlan("plan-1", testNS), newIssue("test-issue", testNS))
	r := &RemediationReconciler{Client: c, Scheme: newScheme(), DecisionEngine: &DecisionEngine{}}
	if _, err := reconcilePlan(t, r, "plan-1"); err == nil {
		t.Fatal("want an error so the plan is retried")
	}
	if p := getPlan(t, c, "plan-1"); p.Status.State == platformv1alpha1.RemediationStateExecuting {
		t.Fatal("plan executed although the decision engine failed")
	}
}

// Item 1: a plan whose Issue is gone cannot be gated, so it fails rather
// than running.
func TestApprovalGate_MissingIssueFailsPlan(t *testing.T) {
	c := approvalTestClient(nil, newRemediationPlan("plan-1", testNS))
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	if _, err := reconcilePlan(t, r, "plan-1"); err != nil {
		t.Fatal(err)
	}
	p := getPlan(t, c, "plan-1")
	if p.Status.State != platformv1alpha1.RemediationStateFailed || !strings.Contains(p.Status.Result, "Parent issue not found") {
		t.Fatalf("state=%s result=%q", p.Status.State, p.Status.Result)
	}
}

// Item 8: an auto rule with autoApproveConditions parks the plan so the
// conditions are evaluated; one without conditions still lets it through.
func TestApprovalGate_AutoRuleWithConditionsRaisesRequest(t *testing.T) {
	auto := manualPolicy("p", platformv1alpha1.ApprovalModeAuto, 1)
	auto.Spec.Rules[0].AutoApproveConditions = &platformv1alpha1.AutoApproveConditions{MinConfidence: 0.9, MaxSeverity: platformv1alpha1.IssueSeverityLow}
	c := approvalTestClient(nil, newRemediationPlan("plan-1", testNS), newIssue("test-issue", testNS), auto)
	r := &RemediationReconciler{Client: c, Scheme: newScheme()}
	if _, err := reconcilePlan(t, r, "plan-1"); err != nil {
		t.Fatal(err)
	}
	if p := getPlan(t, c, "plan-1"); p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("auto rule with conditions: state=%s, want WaitingApproval", p.Status.State)
	}
	getAR(t, c, "approval-plan-1")

	plain := manualPolicy("p", platformv1alpha1.ApprovalModeAuto, 1)
	c = approvalTestClient(nil, newRemediationPlan("plan-1", testNS), newIssue("test-issue", testNS), plain)
	r = &RemediationReconciler{Client: c, Scheme: newScheme()}
	if _, err := reconcilePlan(t, r, "plan-1"); err != nil {
		t.Fatal(err)
	}
	if p := getPlan(t, c, "plan-1"); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("auto rule without conditions: state=%s, want Executing", p.Status.State)
	}
}

// Items 7 and 8: auto approval happens only when every condition holds,
// maxSeverity included (it used to be ignored: the severity rank was 0).
func TestApproval_AutoConditionsEnforceMaxSeverity(t *testing.T) {
	for _, tc := range []struct {
		sev      platformv1alpha1.IssueSeverity
		approved bool
	}{{platformv1alpha1.IssueSeverityHigh, false}, {platformv1alpha1.IssueSeverityLow, true}} {
		auto := manualPolicy("p", platformv1alpha1.ApprovalModeAuto, 1)
		auto.Spec.Rules[0].AutoApproveConditions = &platformv1alpha1.AutoApproveConditions{
			MinConfidence: 0.8, MaxSeverity: platformv1alpha1.IssueSeverityMedium, HistoricalSuccessRate: 0.5,
		}
		issue := newIssue("test-issue", testNS)
		issue.Spec.Severity = tc.sev
		ar := pendingAR("ar", "p", 1, time.Now())
		ar.Spec.Evidence = &platformv1alpha1.ApprovalEvidence{AIConfidence: 0.99, HistoricalSuccessRate: 1}
		c := approvalTestClient(nil, auto, issue, ar)
		reconcileAR(t, c, "ar")
		got := getAR(t, c, "ar")
		if (got.Status.State == platformv1alpha1.ApprovalStateApproved) != tc.approved {
			t.Fatalf("severity %s: state=%s, want approved=%v", tc.sev, got.Status.State, tc.approved)
		}
		if tc.approved && (!got.Status.AutoApproved || len(got.Status.Decisions) != 1 || got.Status.Decisions[0].Approver != autoPolicyApprover) {
			t.Fatalf("auto approval not recorded: %+v", got.Status)
		}
	}
}

// Item 11: an approve annotation used to be consumed while the decision
// was lost (the object Update overwrote the in-memory status first).
func TestApproval_AnnotationDecisionIsPersisted(t *testing.T) {
	ar := pendingAR("ar", "p", 1, time.Now())
	ar.Annotations = map[string]string{annotationApprove: "alice: looks right"}
	c := approvalTestClient(nil, manualPolicy("p", platformv1alpha1.ApprovalModeManual, 1), ar)
	before := approvalsCounter(t, "manual", "approved")
	reconcileAR(t, c, "ar")
	got := getAR(t, c, "ar")
	if got.Status.State != platformv1alpha1.ApprovalStateApproved || len(got.Status.Decisions) != 1 || got.Status.Decisions[0].Approver != "alice" {
		t.Fatalf("status = %+v", got.Status)
	}
	if _, left := got.Annotations[annotationApprove]; left {
		t.Fatal("approve annotation not removed")
	}
	reconcileAR(t, c, "ar")
	if d := approvalsCounter(t, "manual", "approved") - before; d != 1 {
		t.Fatalf("approvals_total grew by %v, want 1", d)
	}
}

// Item 2: quorum counts distinct approvers; the same API key counts once
// whatever name is typed with it.
func TestApproval_QuorumCountsDistinctApprovers(t *testing.T) {
	ar := pendingAR("ar", "p", 2, time.Now())
	c := approvalTestClient(nil, manualPolicy("p", platformv1alpha1.ApprovalModeQuorum, 2), ar)
	now := metav1.Now()
	if err := AppendApprovalDecision(ar, FormatAPIKeyApprover("key-a", "alice"), ApprovalDecisionApproved, "", now); err != nil {
		t.Fatal(err)
	}
	if err := AppendApprovalDecision(ar, FormatAPIKeyApprover("key-a", "mallory"), ApprovalDecisionApproved, "", now); !stderrors.Is(err, ErrApproverAlreadyDecided) {
		t.Fatalf("same key twice: err=%v", err)
	}
	// A decision written around the helper still counts once.
	ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{Approver: "Alice (api-key: KEY-A)", Decision: ApprovalDecisionApproved, Timestamp: now})
	if err := c.Status().Update(context.Background(), ar); err != nil {
		t.Fatal(err)
	}
	reconcileAR(t, c, "ar")
	if got := getAR(t, c, "ar"); got.Status.State != platformv1alpha1.ApprovalStatePending {
		t.Fatalf("one distinct approver approved a quorum-2 request: %s", got.Status.State)
	}
	got := getAR(t, c, "ar")
	if err := AppendApprovalDecision(&got, FormatAPIKeyApprover("key-b", "bob"), ApprovalDecisionApproved, "", now); err != nil {
		t.Fatal(err)
	}
	if err := c.Status().Update(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
	reconcileAR(t, c, "ar")
	if got := getAR(t, c, "ar"); got.Status.State != platformv1alpha1.ApprovalStateApproved {
		t.Fatalf("two distinct approvers: state=%s", got.Status.State)
	}
	final := getAR(t, c, "ar")
	if err := AppendApprovalDecision(&final, "carol", ApprovalDecisionRejected, "", now); !stderrors.Is(err, ErrApprovalNotPending) {
		t.Fatalf("decision on an approved request: err=%v", err)
	}
}

// Item 9: a request raised outside its change window does not expire
// while the window stays closed, and an approved one waits for the window.
func TestApproval_ChangeWindowPausesTimeout(t *testing.T) {
	closedDay := time.Now().UTC().Add(72 * time.Hour).Weekday().String()
	policy := manualPolicy("p", platformv1alpha1.ApprovalModeManual, 1)
	policy.Spec.Rules[0].ChangeWindow = &platformv1alpha1.ChangeWindowSpec{Timezone: "UTC", AllowedDays: []string{closedDay}, StartHour: 0, EndHour: 23}
	ar := pendingAR("ar", "p", 1, time.Now().Add(-3*time.Hour))
	ar.Annotations = map[string]string{annotationApprove: "alice"}
	c := approvalTestClient(nil, policy, ar)
	reconcileAR(t, c, "ar")
	got := getAR(t, c, "ar")
	if got.Status.State != platformv1alpha1.ApprovalStatePending {
		t.Fatalf("state=%s, want Pending (waiting for the window, not expired)", got.Status.State)
	}
	if len(got.Status.Decisions) != 1 {
		t.Fatalf("decision not recorded while outside the window: %+v", got.Status.Decisions)
	}
	cond := meta.FindStatusCondition(got.Status.Conditions, conditionChangeWindow)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("ChangeWindow condition = %+v", cond)
	}
}

func TestWindowOpenDuration(t *testing.T) {
	cw := &platformv1alpha1.ChangeWindowSpec{Timezone: "UTC", AllowedDays: []string{"Monday"}, StartHour: 9, EndHour: 17}
	mon := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC) // a Monday
	if d := windowOpenDuration(cw, time.UTC, mon, mon.Add(4*time.Hour)); d != 3*time.Hour {
		t.Fatalf("08:00-12:00 in a 9-17 window: %s, want 3h", d)
	}
	if d := windowOpenDuration(cw, time.UTC, mon.Add(-24*time.Hour), mon); d != 0 {
		t.Fatalf("Sunday: %s, want 0", d)
	}
	night := &platformv1alpha1.ChangeWindowSpec{Timezone: "UTC", AllowedDays: []string{"Monday"}, StartHour: 22, EndHour: 6}
	if !windowOpenAt(night, mon.Add(15*time.Hour)) || windowOpenAt(night, mon.Add(4*time.Hour)) {
		t.Fatal("overnight window evaluated wrong")
	}
	if _, err := validateChangeWindow(&platformv1alpha1.ChangeWindowSpec{Timezone: "UTC", AllowedDays: []string{"Monday"}, StartHour: 9, EndHour: 9}); err == nil {
		t.Fatal("a window that never opens must be invalid")
	}
}

// Item 9: an invalid window keeps the request from being approved and lets
// it expire on wall-clock time instead of waiting forever.
func TestApproval_InvalidWindowExpires(t *testing.T) {
	policy := manualPolicy("p", platformv1alpha1.ApprovalModeManual, 1)
	policy.Spec.Rules[0].ChangeWindow = &platformv1alpha1.ChangeWindowSpec{Timezone: "Mars/Olympus", AllowedDays: []string{"Monday"}, StartHour: 9, EndHour: 17}
	c := approvalTestClient(nil, policy, pendingAR("ar", "p", 1, time.Now().Add(-time.Hour)))
	reconcileAR(t, c, "ar")
	if got := getAR(t, c, "ar"); got.Status.State != platformv1alpha1.ApprovalStateExpired {
		t.Fatalf("state=%s, want Expired", got.Status.State)
	}
}

// Item 12: a finished request is counted on its policy once, however many
// times it is reconciled.
func TestApproval_PolicyCountersCountOnce(t *testing.T) {
	ar := pendingAR("ar", "p", 1, time.Now())
	ar.Status.State = platformv1alpha1.ApprovalStateRejected
	c := approvalTestClient(nil, manualPolicy("p", platformv1alpha1.ApprovalModeManual, 1), ar)
	for i := 0; i < 3; i++ {
		reconcileAR(t, c, "ar")
	}
	var p platformv1alpha1.ApprovalPolicy
	if err := c.Get(context.Background(), types.NamespacedName{Name: "p", Namespace: testNS}, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status.TotalRejected != 1 {
		t.Fatalf("TotalRejected = %d after 3 reconciles, want 1", p.Status.TotalRejected)
	}
	if getAR(t, c, "ar").Annotations[annotationPolicyCounted] != ApprovalDecisionRejected {
		t.Fatal("counted mark missing")
	}
}

// A request whose policy is gone is evaluated with its own timeout, so it
// expires instead of waiting forever (the chaos request relies on it).
func TestApproval_MissingPolicyStillExpires(t *testing.T) {
	c := approvalTestClient(nil, pendingAR("ar", "gone", 1, time.Now().Add(-time.Hour)))
	reconcileAR(t, c, "ar")
	if got := getAR(t, c, "ar"); got.Status.State != platformv1alpha1.ApprovalStateExpired {
		t.Fatalf("state=%s, want Expired", got.Status.State)
	}
}

func chaosExperiment() *platformv1alpha1.ChaosExperiment {
	return &platformv1alpha1.ChaosExperiment{
		ObjectMeta: metav1.ObjectMeta{Name: "exp", Namespace: testNS, UID: "exp-uid"},
		Spec: platformv1alpha1.ChaosExperimentSpec{
			Enabled: true, ExperimentType: platformv1alpha1.ChaosTypePodKill, Duration: "1m",
			Target:       platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: testNS},
			SafetyChecks: platformv1alpha1.ChaosSafetyChecks{RequireApproval: true},
		},
	}
}

// Item 6: the chaos request is valid for the CRD (requestedActions), owned
// by the experiment, expires, and a rejected or expired request ends the
// experiment instead of erroring forever.
func TestChaosApproval_RequestAndTerminalOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, state := range []platformv1alpha1.ApprovalRequestState{platformv1alpha1.ApprovalStateRejected, platformv1alpha1.ApprovalStateExpired} {
		c := approvalTestClient(nil, chaosExperiment())
		r := &ChaosReconciler{Client: c, Scheme: newScheme()}
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "exp", Namespace: testNS}}
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
		ar := getAR(t, c, "chaos-exp")
		if len(ar.Spec.RequestedActions) != 1 || ar.Spec.RequestedActions[0].Type != platformv1alpha1.ActionCustom ||
			ar.Spec.PolicyRef != ChaosApprovalPolicyName || ar.Spec.TimeoutMinutes != chaosApprovalTimeout || len(ar.OwnerReferences) != 1 {
			t.Fatalf("chaos request = %+v owners=%v", ar.Spec, ar.OwnerReferences)
		}
		ar.Status.State = state
		ar.Status.Decisions = []platformv1alpha1.ApprovalDecision{{Approver: "sre", Decision: ApprovalDecisionRejected, Reason: "not today", Timestamp: metav1.Now()}}
		if err := c.Status().Update(ctx, &ar); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("%s: reconcile error %v (it used to retry forever)", state, err)
		}
		var exp platformv1alpha1.ChaosExperiment
		if err := c.Get(ctx, req.NamespacedName, &exp); err != nil {
			t.Fatal(err)
		}
		if exp.Status.State != platformv1alpha1.ChaosStateFailed || !strings.Contains(exp.Status.Result, "approval") {
			t.Fatalf("%s: experiment state=%s result=%q", state, exp.Status.State, exp.Status.Result)
		}
	}
	// Without a chaos-safety policy the request still expires.
	c := approvalTestClient(nil, chaosExperiment())
	ar := chaosApprovalRequest("chaos-exp", chaosExperiment())
	ar.CreationTimestamp = metav1.NewTime(time.Now().Add(-time.Hour))
	if err := c.Create(ctx, &ar); err != nil {
		t.Fatal(err)
	}
	reconcileAR(t, c, "chaos-exp")
	if got := getAR(t, c, "chaos-exp"); got.Status.State != platformv1alpha1.ApprovalStateExpired {
		t.Fatalf("chaos request state=%s, want Expired", got.Status.State)
	}
}

// Item 5: stress pods pass the restricted Pod Security Standard.
func TestChaosStressPodIsRestricted(t *testing.T) {
	c := approvalTestClient(nil)
	r := &ChaosReconciler{Client: c, Scheme: newScheme()}
	if err := r.createStressPod(context.Background(), "chaos-cpu-exp", testNS, "node-1", []string{"stress-ng"}, "exp"); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Name: "chaos-cpu-exp", Namespace: testNS}, &pod); err != nil {
		t.Fatal(err)
	}
	psc, sc := pod.Spec.SecurityContext, pod.Spec.Containers[0].SecurityContext
	if psc == nil || psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot || psc.SeccompProfile == nil ||
		psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatalf("pod securityContext = %+v", psc)
	}
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("container securityContext = %+v", sc)
	}
}
