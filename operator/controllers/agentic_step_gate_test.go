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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// agenticGateFixture is an agentic plan for Deployment web, whose only pod
// runs on node-a (owned through ReplicaSet web-7d9), next to node-b, which
// runs nothing of it, and a "web-admin" pod on node-b whose name merely
// shares the prefix.
func agenticGateFixture(extra ...client.Object) []client.Object {
	isController := true
	issue := newIssue("agent-issue", "default")
	issue.Spec.Resource = platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-7d9", Namespace: "default",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "deploy-uid", Controller: &isController}}}}
	adminRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-admin-5c4", Namespace: "default",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web-admin", UID: "admin-uid", Controller: &isController}}}}
	pod := func(name, rsName, node string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: rsName, UID: "rs-uid", Controller: &isController}}},
			Spec: corev1.PodSpec{NodeName: node},
		}
	}
	objs := []client.Object{
		issue, newAgenticPlan("agent-plan", "default", "agent-issue"), newDeployment("web", "default", 2), rs, adminRS,
		pod("web-7d9-abcde", "web-7d9", "node-a"), pod("web-admin-5c4-fghij", "web-admin-5c4", "node-b"),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"}},
	}
	return append(objs, extra...)
}

func agenticGateReconciler(mode RemediationMode, mock *mockAgenticStepper, objs []client.Object) (*RemediationReconciler, client.Client) {
	var evictions []string
	c := fake.NewClientBuilder().WithScheme(newScheme()).WithStatusSubresource(
		&platformv1alpha1.RemediationPlan{}, &platformv1alpha1.Issue{}, &platformv1alpha1.ApprovalRequest{}, &platformv1alpha1.AIInsight{},
	).WithObjects(objs...).WithInterceptorFuncs(evictionFuncs(&evictions, 0)).Build()
	return &RemediationReconciler{Client: c, Scheme: newScheme(), ServerClient: mock, RemediationMode: mode}, c
}

func stepper(action string, params map[string]string) *mockAgenticStepper {
	return &mockAgenticStepper{response: &pb.AgenticStepResponse{
		Reasoning:  "the node is unhealthy",
		NextAction: &pb.SuggestedAction{Name: action, Action: action, Params: params},
	}}
}

func reconcileAgentic(t *testing.T, r *RemediationReconciler, c client.Client) platformv1alpha1.RemediationPlan {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "agent-plan", Namespace: "default"}}); err != nil {
		t.Fatal(err)
	}
	var p platformv1alpha1.RemediationPlan
	if err := c.Get(ctx, types.NamespacedName{Name: "agent-plan", Namespace: "default"}, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func nodeCordoned(t *testing.T, c client.Client, name string) bool {
	t.Helper()
	var n corev1.Node
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &n); err != nil {
		t.Fatal(err)
	}
	return n.Spec.Unschedulable
}

func decide(t *testing.T, c client.Client, name string, state platformv1alpha1.ApprovalRequestState) {
	t.Helper()
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &ar); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	ar.Status.State = state
	ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{Approver: "sre-oncall", Decision: strings.ToLower(string(state)), Reason: "test", Timestamp: now})
	if err := c.Status().Update(context.Background(), &ar); err != nil {
		t.Fatal(err)
	}
}

// An agentic DrainNode step does not run when the AI proposes it, even
// with a policy auto rule for every action: it parks the plan on an
// ApprovalRequest that names the concrete step. Once approved, exactly
// that action runs, without asking the AI again.
func TestAgentic_DrainNodeStepRequiresApproval(t *testing.T) {
	mock := stepper("DrainNode", map[string]string{"node": "node-a"})
	r, c := agenticGateReconciler(RemediationModeApprove, mock, agenticGateFixture(autoPolicy()))

	p := reconcileAgentic(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("state = %s (%s), want WaitingApproval", p.Status.State, p.Status.Result)
	}
	if nodeCordoned(t, c, "node-a") {
		t.Fatal("the drain ran before any approval")
	}
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: "approval-agent-plan-step-1", Namespace: "default"}, &ar); err != nil {
		t.Fatal(err)
	}
	if len(ar.Spec.RequestedActions) != 1 || ar.Spec.RequestedActions[0].Type != platformv1alpha1.ActionDrainNode || ar.Spec.RequestedActions[0].Params["node"] != "node-a" {
		t.Fatalf("the request must name the step's concrete action, got %+v", ar.Spec.RequestedActions)
	}
	if !strings.Contains(p.Status.Result, "DrainNode node=node-a") {
		t.Errorf("plan result %q must name the step", p.Status.Result)
	}

	decide(t, c, ar.Name, platformv1alpha1.ApprovalStateApproved)
	mock.called = false
	mock.response = &pb.AgenticStepResponse{Reasoning: "something else", NextAction: &pb.SuggestedAction{Action: "DrainNode", Params: map[string]string{"node": "node-b"}}}
	if p = reconcileAgentic(t, r, c); p.Status.State != platformv1alpha1.RemediationStateExecuting {
		t.Fatalf("after approval state = %s, want Executing", p.Status.State)
	}
	p = reconcileAgentic(t, r, c)
	if mock.called {
		t.Fatal("the approved step must run as approved, not be proposed again")
	}
	if !nodeCordoned(t, c, "node-a") || nodeCordoned(t, c, "node-b") {
		t.Fatal("the approved drain of node-a did not run, or another node was touched")
	}
	if len(p.Spec.AgenticHistory) != 1 || p.Spec.AgenticHistory[0].Action == nil || !strings.Contains(p.Spec.AgenticHistory[0].Observation, "approved by sre-oncall") {
		t.Fatalf("history = %+v", p.Spec.AgenticHistory)
	}
	if _, pending := p.Annotations[annotationAgenticPendingStep]; pending {
		t.Fatal("the pending step must be cleared once it ran")
	}
}

// A rejected step fails the plan; the action never runs.
func TestAgentic_RejectedStepFailsPlan(t *testing.T) {
	mock := stepper("CordonNode", map[string]string{"node": "node-a"})
	r, c := agenticGateReconciler(RemediationModeApprove, mock, agenticGateFixture())
	reconcileAgentic(t, r, c)
	decide(t, c, "approval-agent-plan-step-1", platformv1alpha1.ApprovalStateRejected)
	p := reconcileAgentic(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Fatalf("state = %s, want Failed", p.Status.State)
	}
	if nodeCordoned(t, c, "node-a") {
		t.Fatal("a rejected step ran")
	}
}

// An approval that no longer names the recorded action does not run.
func TestAgentic_ApprovalMustMatchPendingAction(t *testing.T) {
	mock := stepper("CordonNode", map[string]string{"node": "node-a"})
	r, c := agenticGateReconciler(RemediationModeApprove, mock, agenticGateFixture())
	reconcileAgentic(t, r, c)
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: "approval-agent-plan-step-1", Namespace: "default"}, &ar); err != nil {
		t.Fatal(err)
	}
	ar.Spec.RequestedActions[0].Params["node"] = "node-b"
	if err := c.Update(context.Background(), &ar); err != nil {
		t.Fatal(err)
	}
	decide(t, c, ar.Name, platformv1alpha1.ApprovalStateApproved)
	reconcileAgentic(t, r, c)
	if p := reconcileAgentic(t, r, c); p.Status.State != platformv1alpha1.RemediationStateFailed {
		t.Fatalf("state = %s, want Failed on a mismatching approval", p.Status.State)
	}
	if nodeCordoned(t, c, "node-a") || nodeCordoned(t, c, "node-b") {
		t.Fatal("a mismatching approval ran an action")
	}
}

// ApprovalPolicy rules that match by action type now match agentic steps:
// the step's action is what the policy is evaluated against, even in auto
// mode.
func TestAgentic_PolicyMatchesStepActionType(t *testing.T) {
	manual := &platformv1alpha1.ApprovalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "scale-needs-human", Namespace: "default"},
		Spec: platformv1alpha1.ApprovalPolicySpec{Enabled: true, Rules: []platformv1alpha1.ApprovalRule{{
			Name: "scale", Mode: platformv1alpha1.ApprovalModeManual, RequiredApprovers: 1,
			Match: platformv1alpha1.ApprovalMatch{ActionTypes: []platformv1alpha1.RemediationActionType{platformv1alpha1.ActionScaleDeployment}},
		}}},
	}
	mock := stepper("ScaleDeployment", map[string]string{"replicas": "5"})
	r, c := agenticGateReconciler(RemediationModeAuto, mock, agenticGateFixture(manual))
	p := reconcileAgentic(t, r, c)
	if p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("state = %s (%s), want WaitingApproval under the matching policy", p.Status.State, p.Status.Result)
	}
	var ar platformv1alpha1.ApprovalRequest
	if err := c.Get(context.Background(), types.NamespacedName{Name: "approval-agent-plan-step-1", Namespace: "default"}, &ar); err != nil || ar.Spec.PolicyRef != "scale-needs-human" {
		t.Fatalf("request = %+v (err=%v)", ar.Spec, err)
	}
	assertReplicas(t, c, "web", 2)
}

// Each step passes the same safety validator a runbook plan does.
func TestAgentic_StepGoesThroughSafetyValidator(t *testing.T) {
	mock := stepper("ScaleDeployment", map[string]string{"replicas": "0"})
	r, c := agenticGateReconciler(RemediationModeAuto, mock, agenticGateFixture())
	p := reconcileAgentic(t, r, c)
	if len(p.Spec.AgenticHistory) != 1 || p.Spec.AgenticHistory[0].Action != nil ||
		!strings.HasPrefix(p.Spec.AgenticHistory[0].Observation, "REJECTED by the safety validator") {
		t.Fatalf("history = %+v, want the step rejected by the validator", p.Spec.AgenticHistory)
	}
	assertReplicas(t, c, "web", 2)
}

// In approve mode a low-risk step still waits for a human unless a policy
// auto rule or the decision engine allows it.
func TestAgentic_ApproveModeParksUngatedStep(t *testing.T) {
	mock := stepper("ScaleDeployment", map[string]string{"replicas": "5"})
	r, c := agenticGateReconciler(RemediationModeApprove, mock, agenticGateFixture())
	if p := reconcileAgentic(t, r, c); p.Status.State != platformv1alpha1.RemediationStateWaitingApproval {
		t.Fatalf("state = %s, want WaitingApproval", p.Status.State)
	}
	r, c = agenticGateReconciler(RemediationModeApprove, mock, agenticGateFixture(autoPolicy(platformv1alpha1.ActionScaleDeployment)))
	if p := reconcileAgentic(t, r, c); p.Status.State != platformv1alpha1.RemediationStateExecuting || len(p.Spec.AgenticHistory) != 1 {
		t.Fatalf("auto rule for the step's type: state = %s history = %d", p.Status.State, len(p.Spec.AgenticHistory))
	}
	assertReplicas(t, c, "web", 5)
}

// A node action must target a node running a pod the Issue's workload
// owns; a node hosting only a pod whose name shares the prefix does not
// count.
func TestAgentic_NodeActionBoundToIssueWorkload(t *testing.T) {
	mock := stepper("CordonNode", map[string]string{"node": "node-b"})
	r, c := agenticGateReconciler(RemediationModeAuto, mock, agenticGateFixture())
	p := reconcileAgentic(t, r, c)
	if nodeCordoned(t, c, "node-b") {
		t.Fatal("cordoned a node that runs nothing of the Issue's workload")
	}
	if len(p.Spec.AgenticHistory) != 1 || !strings.Contains(p.Spec.AgenticHistory[0].Observation, "hosts no pod of Deployment default/web") {
		t.Fatalf("history = %+v", p.Spec.AgenticHistory)
	}

	r, c = agenticGateReconciler(RemediationModeAuto, stepper("CordonNode", map[string]string{"node": "node-a"}), agenticGateFixture())
	reconcileAgentic(t, r, c)
	if !nodeCordoned(t, c, "node-a") {
		t.Fatal("a node running the workload's pod must be actionable")
	}
}

// The binding holds for runbook plans too, and a Node Issue may only act on
// its own node.
func TestNodeBinding_RunbookAndNodeIssues(t *testing.T) {
	r, c := agenticGateReconciler(RemediationModeAuto, nil, agenticGateFixture())
	ctx := context.Background()
	web := platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "web", Namespace: "default"}
	cordon := func(node string) *platformv1alpha1.RemediationAction {
		return &platformv1alpha1.RemediationAction{Type: platformv1alpha1.ActionCordonNode, Params: map[string]string{"node": node}}
	}
	if err := r.executeAction(ctx, web, cordon("node-b")); err == nil {
		t.Fatal("runbook cordon of an unrelated node must be refused")
	}
	if err := r.executeAction(ctx, platformv1alpha1.ResourceRef{Kind: "Node", Name: "node-a"}, cordon("node-b")); err == nil {
		t.Fatal("a Node Issue must only act on its own node")
	}
	if err := r.executeAction(ctx, platformv1alpha1.ResourceRef{Kind: "Node", Name: "node-b"}, cordon("node-b")); err != nil {
		t.Fatalf("a Node Issue acting on its node: %v", err)
	}
	if !nodeCordoned(t, c, "node-b") {
		t.Fatal("node-b not cordoned")
	}
}
