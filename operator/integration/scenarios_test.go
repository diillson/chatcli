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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const wait = 90 * time.Second

func deployment(name, ns string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: name, Image: "example.invalid/app:1"}}},
			},
		},
	}
}

func scaleRunbook(name, ns string) *platformv1alpha1.Runbook {
	return &platformv1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: platformv1alpha1.RunbookSpec{
			Description: "Scale the deployment when the error rate spikes",
			Trigger: platformv1alpha1.RunbookTrigger{
				SignalType: platformv1alpha1.SignalErrorRate, Severity: platformv1alpha1.IssueSeverityMedium, ResourceKind: "Deployment",
			},
			Steps:       []platformv1alpha1.RunbookStep{{Name: "Scale up", Action: "ScaleDeployment", Params: map[string]string{"replicas": "4"}}},
			MaxAttempts: 3,
		},
	}
}

func errorRateAnomaly(name, ns, target string) *platformv1alpha1.Anomaly {
	return &platformv1alpha1.Anomaly{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: platformv1alpha1.AnomalySpec{
			Source:      platformv1alpha1.AnomalySourcePrometheus,
			SignalType:  platformv1alpha1.SignalErrorRate,
			Resource:    platformv1alpha1.ResourceRef{Kind: "Deployment", Name: target, Namespace: ns},
			Value:       "15.4%",
			Threshold:   "5%",
			Description: "5xx ratio above threshold",
		},
	}
}

// markDeploymentHealthy writes the status a real kube-controller-manager
// would: envtest runs no workload controllers, so the remediation's
// verification step is fed by the test.
func markDeploymentHealthy(t *testing.T, ns, name string, replicas int32) {
	t.Helper()
	ctx := context.Background()
	var d appsv1.Deployment
	if err := k8sClient.Get(ctx, key(ns, name), &d); err != nil {
		t.Fatal(err)
	}
	d.Status.Replicas = replicas
	d.Status.ReadyReplicas = replicas
	d.Status.UpdatedReplicas = replicas
	d.Status.AvailableReplicas = replicas
	d.Status.UnavailableReplicas = 0
	if err := k8sClient.Status().Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
}

// firstIssueFor returns the Issue the anomaly was correlated into.
func firstIssueFor(t *testing.T, ns, target string) *platformv1alpha1.Issue {
	t.Helper()
	var list platformv1alpha1.IssueList
	if err := k8sClient.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		if list.Items[i].Spec.Resource.Name == target {
			return &list.Items[i]
		}
	}
	return nil
}

// The Instance reconciler creates the workload the CR describes, owned by
// the CR so the API server garbage-collects it, and guards the CR with a
// finalizer. An Instance without a server credential is deliberately not
// provisioned (it binds every interface), so the scenario supplies the
// token Secret. Server readiness is not asserted: envtest runs no scheduler.
func TestInstanceCreatesOwnedWorkload(t *testing.T) {
	ns := namespace(t, "it-instance")
	ctx := context.Background()
	mustCreate(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "chatcli-server-token", Namespace: ns},
		StringData: map[string]string{"token": "integration-token"},
	})
	inst := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "chatcli", Namespace: ns},
		Spec: platformv1alpha1.InstanceSpec{
			Provider: "CLAUDEAI", Model: "claude-sonnet-5",
			Server: platformv1alpha1.ServerSpec{Token: &platformv1alpha1.SecretKeyRefSpec{Name: "chatcli-server-token", Key: "token"}},
		},
	}
	mustCreate(t, inst)

	var deploy appsv1.Deployment
	eventually(t, wait, "the owned Deployment", func() bool {
		return k8sClient.Get(ctx, key(ns, "chatcli"), &deploy) == nil
	})
	var got platformv1alpha1.Instance
	if err := k8sClient.Get(ctx, key(ns, "chatcli"), &got); err != nil {
		t.Fatal(err)
	}
	if !ownedBy(&deploy, &got) {
		t.Errorf("Deployment is not owned by the Instance: %+v", deploy.OwnerReferences)
	}
	for _, obj := range []client.Object{&corev1.Service{}, &corev1.ConfigMap{}, &corev1.ServiceAccount{}} {
		eventually(t, wait, "an owned child", func() bool {
			return k8sClient.Get(ctx, key(ns, "chatcli"), obj) == nil && ownedBy(obj, &got)
		})
	}
	eventually(t, wait, "the finalizer", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "chatcli"), &got); err != nil {
			return false
		}
		for _, f := range got.Finalizers {
			if f == "platform.chatcli.io/finalizer" {
				return true
			}
		}
		return false
	})
	if cm := (&corev1.ConfigMap{}); k8sClient.Get(ctx, key(ns, "chatcli"), cm) == nil && cm.Data["LLM_PROVIDER"] != "CLAUDEAI" {
		t.Errorf("ConfigMap LLM_PROVIDER = %q", cm.Data["LLM_PROVIDER"])
	}

	// Without a credential the Instance must stay unprovisioned and say so
	// in its conditions instead of exposing an open server.
	bare := &platformv1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "open", Namespace: ns},
		Spec:       platformv1alpha1.InstanceSpec{Provider: "CLAUDEAI", Model: "claude-sonnet-5"},
	}
	mustCreate(t, bare)
	eventually(t, wait, "the missing-credential condition", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "open"), bare); err != nil {
			return false
		}
		for _, c := range bare.Status.Conditions {
			if c.Type == "AuthenticationConfigured" && c.Status == metav1.ConditionFalse && c.Reason == "CredentialMissing" {
				return true
			}
		}
		return false
	})
	if err := k8sClient.Get(ctx, key(ns, "open"), &appsv1.Deployment{}); err == nil {
		t.Error("an Instance without a credential must not get a Deployment")
	}
}

// One anomaly travels the whole pipeline on a live API server: Anomaly →
// Issue → AIInsight (answered by the fake ChatCLI server) → RemediationPlan
// from the matching Runbook → the Deployment is scaled → the plan completes
// once the workload is healthy → the Issue resolves → a PostMortem exists.
// Every hop is a different controller reacting to the previous one's write.
func TestPipelineAnomalyToResolution(t *testing.T) {
	ns := namespace(t, "it-pipeline")
	ctx := context.Background()
	mustCreate(t, scaleRunbook("error-rate", ns))
	mustCreate(t, deployment("payments-api", ns, 2))
	mustCreate(t, errorRateAnomaly("error-spike", ns, "payments-api"))

	var issue *platformv1alpha1.Issue
	eventually(t, wait, "the Issue", func() bool {
		issue = firstIssueFor(t, ns, "payments-api")
		return issue != nil
	})

	var insight platformv1alpha1.AIInsight
	eventually(t, wait, "the AIInsight analyzed by the server", func() bool {
		return k8sClient.Get(ctx, key(ns, issue.Name+"-insight"), &insight) == nil && insight.Status.Analysis != ""
	})
	if !ownedBy(&insight, issue) {
		t.Errorf("AIInsight is not owned by its Issue")
	}
	if insight.Status.Confidence < 0.9 || len(insight.Status.SuggestedActions) == 0 {
		t.Errorf("insight status not filled from the server response: %+v", insight.Status)
	}

	var plan platformv1alpha1.RemediationPlan
	eventually(t, wait, "the RemediationPlan", func() bool {
		return k8sClient.Get(ctx, key(ns, issue.Name+"-plan-1"), &plan) == nil
	})
	if !ownedBy(&plan, issue) {
		t.Errorf("RemediationPlan is not owned by its Issue")
	}

	var deploy appsv1.Deployment
	eventually(t, wait, "the Deployment scaled by the runbook", func() bool {
		return k8sClient.Get(ctx, key(ns, "payments-api"), &deploy) == nil && deploy.Spec.Replicas != nil && *deploy.Spec.Replicas == 4
	})
	eventually(t, wait, "the plan to verify", func() bool {
		return k8sClient.Get(ctx, key(ns, plan.Name), &plan) == nil && plan.Status.State == platformv1alpha1.RemediationStateVerifying
	})
	markDeploymentHealthy(t, ns, "payments-api", 4)

	eventually(t, wait, "the plan to complete", func() bool {
		return k8sClient.Get(ctx, key(ns, plan.Name), &plan) == nil && plan.Status.State == platformv1alpha1.RemediationStateCompleted
	})
	eventually(t, wait, "the Issue to resolve", func() bool {
		return k8sClient.Get(ctx, key(ns, issue.Name), issue) == nil && issue.Status.State == platformv1alpha1.IssueStateResolved
	})
	if issue.Status.ResolvedAt == nil {
		t.Error("resolved Issue has no resolvedAt")
	}
	var pm platformv1alpha1.PostMortem
	eventually(t, wait, "the PostMortem", func() bool {
		return k8sClient.Get(ctx, key(ns, "pm-"+issue.Name), &pm) == nil
	})
	if pm.Spec.IssueRef.Name != issue.Name {
		t.Errorf("PostMortem references %q, want %q", pm.Spec.IssueRef.Name, issue.Name)
	}
	var anomaly platformv1alpha1.Anomaly
	if err := k8sClient.Get(ctx, key(ns, "error-spike"), &anomaly); err != nil || !anomaly.Status.Correlated {
		t.Errorf("anomaly not marked correlated (err=%v)", err)
	}
}

// An ApprovalPolicy that matches the plan's action parks the plan in
// WaitingApproval with an ApprovalRequest; approving the request lets the
// plan execute. The policy is scoped to this namespace so the other
// scenarios are not gated.
func TestApprovalGatesRemediation(t *testing.T) {
	ns := namespace(t, "it-approval")
	ctx := context.Background()
	mustCreate(t, &platformv1alpha1.ApprovalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "scale-needs-human", Namespace: ns},
		Spec: platformv1alpha1.ApprovalPolicySpec{
			Enabled: true,
			Rules: []platformv1alpha1.ApprovalRule{{
				Name: "scale",
				Match: platformv1alpha1.ApprovalMatch{
					ActionTypes: []platformv1alpha1.RemediationActionType{platformv1alpha1.RemediationActionType("ScaleDeployment")},
					Namespaces:  []string{ns},
				},
				Mode: platformv1alpha1.ApprovalModeManual,
			}},
		},
	})
	mustCreate(t, scaleRunbook("error-rate", ns))
	mustCreate(t, deployment("checkout", ns, 2))
	mustCreate(t, errorRateAnomaly("error-spike", ns, "checkout"))

	var issue *platformv1alpha1.Issue
	eventually(t, wait, "the Issue", func() bool {
		issue = firstIssueFor(t, ns, "checkout")
		return issue != nil
	})
	var plan platformv1alpha1.RemediationPlan
	eventually(t, wait, "the plan to wait for approval", func() bool {
		return k8sClient.Get(ctx, key(ns, issue.Name+"-plan-1"), &plan) == nil && plan.Status.State == platformv1alpha1.RemediationStateWaitingApproval
	})
	var ar platformv1alpha1.ApprovalRequest
	eventually(t, wait, "the ApprovalRequest", func() bool {
		return k8sClient.Get(ctx, key(ns, "approval-"+plan.Name), &ar) == nil
	})
	if ar.Spec.RemediationPlanRef != plan.Name || ar.Spec.PolicyRef != "scale-needs-human" {
		t.Errorf("ApprovalRequest spec = %+v", ar.Spec)
	}
	var deploy appsv1.Deployment
	if err := k8sClient.Get(ctx, key(ns, "checkout"), &deploy); err != nil || *deploy.Spec.Replicas != 2 {
		t.Fatalf("the Deployment must not change before approval (replicas=%v err=%v)", deploy.Spec.Replicas, err)
	}

	now := metav1.Now()
	ar.Status.State = platformv1alpha1.ApprovalStateApproved
	ar.Status.ApprovedAt = &now
	ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{Approver: "sre-oncall", Decision: "approved", Reason: "integration", Timestamp: now})
	if err := k8sClient.Status().Update(ctx, &ar); err != nil {
		t.Fatal(err)
	}
	eventually(t, wait, "the approved plan to scale the Deployment", func() bool {
		return k8sClient.Get(ctx, key(ns, "checkout"), &deploy) == nil && *deploy.Spec.Replicas == 4
	})
}

// An IncidentSLA records a resolution violation when an Issue of its
// severity took longer than the target. The Issue is written already
// resolved, so only the SLA reconciler acts on it.
func TestSLAResolutionViolationIsRecorded(t *testing.T) {
	ns := namespace(t, "it-sla")
	ctx := context.Background()
	mustCreate(t, &platformv1alpha1.IncidentSLA{
		ObjectMeta: metav1.ObjectMeta{Name: "critical-1h", Namespace: ns},
		Spec:       platformv1alpha1.IncidentSLASpec{Severity: platformv1alpha1.IssueSeverityCritical, ResponseTime: "15m", ResolutionTime: "1h"},
	})
	issue := &platformv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "db-outage", Namespace: ns},
		Spec: platformv1alpha1.IssueSpec{
			Severity: platformv1alpha1.IssueSeverityCritical, Source: platformv1alpha1.IssueSourceWatcher, SignalType: "pod_not_ready",
			Resource: platformv1alpha1.ResourceRef{Kind: "Deployment", Name: "db", Namespace: ns}, Description: "db pods not ready", RiskScore: 90,
		},
	}
	mustCreate(t, issue)
	detected := metav1.NewTime(time.Now().Add(-3 * time.Hour))
	resolved := metav1.Now()
	eventually(t, wait, "the resolved status to be accepted", func() bool {
		if err := k8sClient.Get(ctx, key(ns, "db-outage"), issue); err != nil {
			return false
		}
		issue.Status.State = platformv1alpha1.IssueStateResolved
		issue.Status.DetectedAt = &detected
		issue.Status.ResolvedAt = &resolved
		return k8sClient.Status().Update(ctx, issue) == nil
	})

	var sla platformv1alpha1.IncidentSLA
	eventually(t, wait, "the SLA violation", func() bool {
		return k8sClient.Get(ctx, key(ns, "critical-1h"), &sla) == nil && sla.Status.TotalViolations >= 1
	})
	if len(sla.Status.RecentViolations) == 0 || sla.Status.RecentViolations[0].IssueName != "db-outage" {
		t.Errorf("violation record missing or for another issue: %+v", sla.Status.RecentViolations)
	}
	if sla.Status.TotalIssuesTracked < 1 {
		t.Errorf("TotalIssuesTracked = %d", sla.Status.TotalIssuesTracked)
	}
}
