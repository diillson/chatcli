package controllers

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

type AuditRecorder struct {
	client client.Client
	scheme *runtime.Scheme
}

type AuditEventParams struct {
	EventType         string
	ActorType         string
	ActorName         string
	ActorController   string
	ResourceKind      string
	ResourceName      string
	ResourceNamespace string
	ResourceUID       string
	Details           map[string]string
	Severity          string
	CorrelationID     string
}

func NewAuditRecorder(c client.Client, scheme *runtime.Scheme) *AuditRecorder {
	return &AuditRecorder{client: c, scheme: scheme}
}

func (ar *AuditRecorder) Record(ctx context.Context, params AuditEventParams) error {
	name := fmt.Sprintf("audit-%d-%s", time.Now().UnixNano(), randomSuffix(6))
	if len(name) > 63 {
		name = name[:63]
	}

	severity := params.Severity
	if severity == "" {
		severity = "info"
	}

	event := &platformv1alpha1.AuditEvent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: params.ResourceNamespace,
			Labels: map[string]string{
				"platform.chatcli.io/event-type": params.EventType,
				"platform.chatcli.io/severity":   severity,
			},
			Annotations: map[string]string{
				"platform.chatcli.io/immutable": "true",
			},
		},
		Spec: platformv1alpha1.AuditEventSpec{
			EventType: params.EventType,
			Actor: platformv1alpha1.AuditActor{
				Type: params.ActorType, Name: params.ActorName, Controller: params.ActorController,
			},
			Resource: platformv1alpha1.AuditResource{
				Kind: params.ResourceKind, Name: params.ResourceName,
				Namespace: params.ResourceNamespace, UID: params.ResourceUID,
			},
			Details:       params.Details,
			Severity:      severity,
			CorrelationID: params.CorrelationID,
			Timestamp:     metav1.Now(),
		},
	}
	return ar.client.Create(ctx, event)
}

func (ar *AuditRecorder) RecordIssueCreated(ctx context.Context, issue *platformv1alpha1.Issue) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "issue_created", ActorType: "controller", ActorName: "IssueReconciler", ActorController: "issue-controller",
		ResourceKind: "Issue", ResourceName: issue.Name, ResourceNamespace: issue.Namespace, ResourceUID: string(issue.UID),
		Details:  map[string]string{"severity": string(issue.Spec.Severity), "resource": fmt.Sprintf("%s/%s", issue.Spec.Resource.Kind, issue.Spec.Resource.Name), "description": truncate(issue.Spec.Description, 200)},
		Severity: "info", CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordIssueResolved(ctx context.Context, issue *platformv1alpha1.Issue) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "issue_resolved", ActorType: "controller", ActorName: "IssueReconciler", ActorController: "issue-controller",
		ResourceKind: "Issue", ResourceName: issue.Name, ResourceNamespace: issue.Namespace, ResourceUID: string(issue.UID),
		Details:  map[string]string{"resolution": truncate(issue.Status.Resolution, 200), "attempts": fmt.Sprintf("%d", issue.Status.RemediationAttempts)},
		Severity: "info", CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordIssueEscalated(ctx context.Context, issue *platformv1alpha1.Issue) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "issue_escalated", ActorType: "controller", ActorName: "IssueReconciler", ActorController: "issue-controller",
		ResourceKind: "Issue", ResourceName: issue.Name, ResourceNamespace: issue.Namespace, ResourceUID: string(issue.UID),
		Details:  map[string]string{"severity": string(issue.Spec.Severity), "attempts": fmt.Sprintf("%d", issue.Status.RemediationAttempts)},
		Severity: "warning", CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordRemediationStarted(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "remediation_started", ActorType: "controller", ActorName: "RemediationReconciler", ActorController: "remediation-controller",
		ResourceKind: "RemediationPlan", ResourceName: plan.Name, ResourceNamespace: plan.Namespace, ResourceUID: string(plan.UID),
		Details:  map[string]string{"issue": issue.Name, "attempt": fmt.Sprintf("%d", plan.Spec.Attempt), "strategy": truncate(plan.Spec.Strategy, 200), "agentic": fmt.Sprintf("%t", plan.Spec.AgenticMode)},
		Severity: "info", CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordRemediationCompleted(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "remediation_completed", ActorType: "controller", ActorName: "RemediationReconciler", ActorController: "remediation-controller",
		ResourceKind: "RemediationPlan", ResourceName: plan.Name, ResourceNamespace: plan.Namespace, ResourceUID: string(plan.UID),
		Details:  map[string]string{"issue": issue.Name, "result": truncate(plan.Status.Result, 200)},
		Severity: "info", CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordRemediationFailed(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "remediation_failed", ActorType: "controller", ActorName: "RemediationReconciler", ActorController: "remediation-controller",
		ResourceKind: "RemediationPlan", ResourceName: plan.Name, ResourceNamespace: plan.Namespace, ResourceUID: string(plan.UID),
		Details:  map[string]string{"issue": issue.Name, "result": truncate(plan.Status.Result, 200), "attempt": fmt.Sprintf("%d", plan.Spec.Attempt)},
		Severity: "warning", CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordApprovalRequested(ctx context.Context, req *platformv1alpha1.ApprovalRequest) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "approval_requested", ActorType: "controller", ActorName: "ApprovalReconciler", ActorController: "approval-controller",
		ResourceKind: "ApprovalRequest", ResourceName: req.Name, ResourceNamespace: req.Namespace, ResourceUID: string(req.UID),
		Details:  map[string]string{"issue": req.Spec.IssueRef.Name, "plan": req.Spec.RemediationPlanRef, "rule": req.Spec.RuleName},
		Severity: "info", CorrelationID: req.Spec.IssueRef.Name,
	})
}

func (ar *AuditRecorder) RecordApprovalDecision(ctx context.Context, req *platformv1alpha1.ApprovalRequest, decision string) error {
	sev := "info"
	if decision == "rejected" || decision == "expired" {
		sev = "warning"
	}
	return ar.Record(ctx, AuditEventParams{
		EventType: "approval_" + decision, ActorType: "controller", ActorName: "ApprovalReconciler", ActorController: "approval-controller",
		ResourceKind: "ApprovalRequest", ResourceName: req.Name, ResourceNamespace: req.Namespace, ResourceUID: string(req.UID),
		Details:  map[string]string{"issue": req.Spec.IssueRef.Name, "decision": decision, "auto": fmt.Sprintf("%t", req.Status.AutoApproved)},
		Severity: sev, CorrelationID: req.Spec.IssueRef.Name,
	})
}

func (ar *AuditRecorder) RecordNotificationSent(ctx context.Context, channelName string, issue *platformv1alpha1.Issue, success bool, errMsg string) error {
	sev := "info"
	details := map[string]string{"channel": channelName, "success": fmt.Sprintf("%t", success)}
	if !success {
		sev = "warning"
		details["error"] = truncate(errMsg, 200)
	}
	return ar.Record(ctx, AuditEventParams{
		EventType: "notification_sent", ActorType: "controller", ActorName: "NotificationReconciler", ActorController: "notification-controller",
		ResourceKind: "Issue", ResourceName: issue.Name, ResourceNamespace: issue.Namespace, ResourceUID: string(issue.UID),
		Details: details, Severity: sev, CorrelationID: issue.Name,
	})
}

func (ar *AuditRecorder) RecordSLOViolation(ctx context.Context, sloName, namespace, window string, burnRate float64) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "slo_violation", ActorType: "controller", ActorName: "SLOReconciler", ActorController: "slo-controller",
		ResourceKind: "ServiceLevelObjective", ResourceName: sloName, ResourceNamespace: namespace,
		Details:  map[string]string{"window": window, "burn_rate": fmt.Sprintf("%.2f", burnRate)},
		Severity: "warning", CorrelationID: sloName,
	})
}

func (ar *AuditRecorder) RecordSLABreach(ctx context.Context, issue *platformv1alpha1.Issue, slaType, elapsed, threshold string) error {
	return ar.Record(ctx, AuditEventParams{
		EventType: "sla_breach", ActorType: "controller", ActorName: "SLAReconciler", ActorController: "sla-controller",
		ResourceKind: "Issue", ResourceName: issue.Name, ResourceNamespace: issue.Namespace, ResourceUID: string(issue.UID),
		Details:  map[string]string{"type": slaType, "elapsed": elapsed, "threshold": threshold},
		Severity: "critical", CorrelationID: issue.Name,
	})
}

func randomSuffix(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
	}
	for i := range b {
		b[i] = letters[b[i]%byte(len(letters))]
	}
	return string(b)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
