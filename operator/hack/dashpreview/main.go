/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Command dashpreview serves the operator web dashboard on localhost over a
// fake Kubernetes client seeded with synthetic incidents, so the page can be
// reviewed in a browser without a cluster.
//
// Run it with `make dash-preview` from the operator directory, then open
// http://127.0.0.1:8085/ and log in with the API key "preview". The address
// is overridable through DASHPREVIEW_ADDR. Development tooling only: nothing
// here is shipped in the operator image.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/diillson/chatcli/operator/api/rest"
	v1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const (
	defaultAddr = "127.0.0.1:8085"
	// previewAPIKey opens the dashboard with the admin role so every action
	// in the page (approve, review, feedback) can be exercised.
	previewAPIKey = "preview"
	seedNamespace = "production"
)

func t(minutesAgo int) *metav1.Time {
	m := metav1.NewTime(time.Now().Add(-time.Duration(minutesAgo) * time.Minute))
	return &m
}

// seed returns the synthetic objects the preview serves: issues in every
// state, an insight, remediation plans, a pending approval, a post-mortem,
// SLOs, cluster registrations, audit events and runbooks with steps. Names
// and namespaces are fictional.
func seed() []client.Object {
	ns := seedNamespace
	ref := func(kind, name string) v1.ResourceRef { return v1.ResourceRef{Kind: kind, Name: name, Namespace: ns} }

	issues := []client.Object{
		&v1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "api-gateway-oom-7f3a", Namespace: ns, CreationTimestamp: *t(42)},
			Spec:   v1.IssueSpec{Severity: v1.IssueSeverityCritical, Source: v1.IssueSourceWatcher, SignalType: "oom_kill", Resource: ref("Deployment", "api-gateway"), Description: "api-gateway pods OOMKilled 6 times in 10 minutes after the 14:02 rollout", RiskScore: 87},
			Status: v1.IssueStatus{State: v1.IssueStateContained, DetectedAt: t(42), RemediationAttempts: 2, MaxRemediationAttempts: 5, RequiresHumanAction: true, RequiredAction: "Scaled to 0 to stop the bleeding; approve the rollback to revision 41"}},
		&v1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "checkout-error-rate-2b91", Namespace: ns, CreationTimestamp: *t(18)},
			Spec:   v1.IssueSpec{Severity: v1.IssueSeverityHigh, Source: v1.IssueSourcePrometheus, SignalType: "error_rate", Resource: ref("Deployment", "checkout"), Description: "5xx ratio at 4.8% over 5m (threshold 1%)", RiskScore: 64},
			Status: v1.IssueStatus{State: v1.IssueStateRemediating, DetectedAt: t(18), RemediationAttempts: 1, MaxRemediationAttempts: 5}},
		&v1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "worker-restart-loop-11c0", Namespace: "batch", CreationTimestamp: *t(240)},
			Spec:   v1.IssueSpec{Severity: v1.IssueSeverityMedium, Source: v1.IssueSourceEvents, SignalType: "pod_restart", Resource: v1.ResourceRef{Kind: "StatefulSet", Name: "worker", Namespace: "batch"}, Description: "worker-2 restarted 9 times: liveness probe timeout", RiskScore: 38},
			Status: v1.IssueStatus{State: v1.IssueStateResolved, DetectedAt: t(240), ResolvedAt: t(205), Resolution: "Liveness timeout raised from 1s to 5s; restarts stopped", RemediationAttempts: 1, MaxRemediationAttempts: 5}},
		&v1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "ingress-latency-9d4e", Namespace: ns, CreationTimestamp: *t(3)},
			Spec:   v1.IssueSpec{Severity: v1.IssueSeverityLow, Source: v1.IssueSourcePrometheus, SignalType: "latency", Resource: ref("Deployment", "ingress-nginx"), Description: "p99 latency 820ms over 10m (threshold 500ms)", RiskScore: 22},
			Status: v1.IssueStatus{State: v1.IssueStateDetected, DetectedAt: t(3), MaxRemediationAttempts: 5}},
		&v1.Issue{ObjectMeta: metav1.ObjectMeta{Name: "payments-db-conn-55aa", Namespace: ns, CreationTimestamp: *t(600)},
			Spec:   v1.IssueSpec{Severity: v1.IssueSeverityHigh, Source: v1.IssueSourceLogs, SignalType: "connection_pool", Resource: ref("Deployment", "payments"), Description: "pgbouncer pool exhausted, 312 waiting clients", RiskScore: 71},
			Status: v1.IssueStatus{State: v1.IssueStateResolved, DetectedAt: t(600), ResolvedAt: t(571), Resolution: "Pool size raised to 200 and connection leak patched (PR #4812)", RemediationAttempts: 1, MaxRemediationAttempts: 5}},
	}
	objs := append([]client.Object{}, issues...)
	objs = append(objs,
		&v1.AIInsight{ObjectMeta: metav1.ObjectMeta{Name: "api-gateway-oom-7f3a-insight", Namespace: ns, CreationTimestamp: *t(40)},
			Spec: v1.AIInsightSpec{IssueRef: v1.IssueRef{Name: "api-gateway-oom-7f3a"}, Provider: "CLAUDEAI", Model: "claude-sonnet-5"},
			Status: v1.AIInsightStatus{Analysis: "Revision 42 raised the in-memory response cache from 64MB to 512MB while the container limit stayed at 768MB. Under the 14:00 traffic peak the heap crosses the limit within ~90s and the kernel OOM-kills the process. Containment: scale to 0 is correct; the durable fix is a rollback to revision 41 or a limit of 1.5Gi.", Confidence: 0.91,
				Recommendations:  []string{"Roll back api-gateway to revision 41", "Raise the memory limit to 1.5Gi before re-enabling the cache change", "Add an alert on container_memory_working_set_bytes > 85% of limit"},
				SuggestedActions: []v1.SuggestedAction{{Name: "rollback", Action: "RollbackDeployment", Description: "Roll back to revision 41", Params: map[string]string{"revision": "41"}}}, GeneratedAt: t(40),
				LogAnalysis: "Last 200 lines show 'cache: evicting 0 entries' followed by SIGKILL; no application error precedes the kill.", MetricsContext: "working_set 748Mi/768Mi at 14:03:12, RSS growth 4.1MB/s"}},
		&v1.RemediationPlan{ObjectMeta: metav1.ObjectMeta{Name: "checkout-error-rate-2b91-plan-1", Namespace: ns, CreationTimestamp: *t(15)},
			Spec:   v1.RemediationPlanSpec{IssueRef: v1.IssueRef{Name: "checkout-error-rate-2b91"}, Attempt: 1, Strategy: "agentic", AgenticMode: true, AgenticMaxSteps: 8, Actions: []v1.RemediationAction{{Type: v1.RemediationActionType("RestartDeployment")}}},
			Status: v1.RemediationPlanStatus{State: v1.RemediationStateExecuting, StartedAt: t(14), AgenticStepCount: 3}},
		&v1.RemediationPlan{ObjectMeta: metav1.ObjectMeta{Name: "worker-restart-loop-11c0-plan-1", Namespace: "batch", CreationTimestamp: *t(230)},
			Spec:   v1.RemediationPlanSpec{IssueRef: v1.IssueRef{Name: "worker-restart-loop-11c0"}, Attempt: 1, Strategy: "runbook", Actions: []v1.RemediationAction{{Type: v1.RemediationActionType("PatchProbe"), Params: map[string]string{"timeoutSeconds": "5"}}}},
			Status: v1.RemediationPlanStatus{State: v1.RemediationStateCompleted, StartedAt: t(229), CompletedAt: t(206), Result: "Probe patched; 30 minutes without restarts"}},
		&v1.RemediationPlan{ObjectMeta: metav1.ObjectMeta{Name: "api-gateway-oom-7f3a-plan-2", Namespace: ns, CreationTimestamp: *t(36)},
			Spec:   v1.RemediationPlanSpec{IssueRef: v1.IssueRef{Name: "api-gateway-oom-7f3a"}, Attempt: 2, Strategy: "agentic", AgenticMode: true, AgenticMaxSteps: 8},
			Status: v1.RemediationPlanStatus{State: v1.RemediationStateWaitingApproval, StartedAt: t(35), AgenticStepCount: 2}},
		&v1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: "api-gateway-oom-7f3a-rollback", Namespace: ns, CreationTimestamp: *t(34)},
			Spec:   v1.ApprovalRequestSpec{IssueRef: v1.IssueRef{Name: "api-gateway-oom-7f3a"}, RemediationPlanRef: "api-gateway-oom-7f3a-plan-2", PolicyRef: "prod-rollbacks", RuleName: "rollback-requires-human", Requester: "chatcli-operator", TimeoutMinutes: 60, RequiredApprovers: 1, RequestedActions: []v1.RemediationAction{{Type: v1.RemediationActionType("RollbackDeployment"), Params: map[string]string{"revision": "41"}}}},
			Status: v1.ApprovalRequestStatus{State: v1.ApprovalStatePending}},
		&v1.ApprovalPolicy{ObjectMeta: metav1.ObjectMeta{Name: "prod-rollbacks", Namespace: ns, CreationTimestamp: *t(20000)},
			Spec:   v1.ApprovalPolicySpec{Enabled: true, Rules: []v1.ApprovalRule{{Name: "rollback-requires-human", Match: v1.ApprovalMatch{ActionTypes: []v1.RemediationActionType{"RollbackDeployment"}}, Mode: v1.ApprovalModeManual, TimeoutMinutes: 30, RequiredApprovers: 1}}},
			Status: v1.ApprovalPolicyStatus{TotalApproved: 7, TotalRejected: 1}},
		&v1.NotificationPolicy{ObjectMeta: metav1.ObjectMeta{Name: "sre-oncall", Namespace: ns, CreationTimestamp: *t(30000)},
			Spec: v1.NotificationPolicySpec{Channels: []v1.NotificationChannel{{Name: "slack-oncall", Type: "slack"}, {Name: "pagerduty", Type: "pagerduty"}}, Rules: []v1.NotificationRule{{Name: "critical-page", Severities: []v1.IssueSeverity{v1.IssueSeverityCritical}}}}},
		&v1.EscalationPolicy{ObjectMeta: metav1.ObjectMeta{Name: "critical-escalation", Namespace: ns, CreationTimestamp: *t(30000)},
			Spec: v1.EscalationPolicySpec{Enabled: true, DefaultPolicy: true, Severities: []v1.IssueSeverity{v1.IssueSeverityCritical, v1.IssueSeverityHigh}, Levels: []v1.EscalationLevel{{Name: "primary", TimeoutMinutes: 15}, {Name: "manager", TimeoutMinutes: 45}}}},
		&v1.IncidentSLA{ObjectMeta: metav1.ObjectMeta{Name: "critical-1h", Namespace: ns, CreationTimestamp: *t(40000)},
			Spec:   v1.IncidentSLASpec{Severity: v1.IssueSeverityCritical, ResponseTime: "15m", ResolutionTime: "1h"},
			Status: v1.IncidentSLAStatus{TotalIssuesTracked: 14, TotalViolations: 2, ActiveViolations: 1, CompliancePercentage: 85.7}},
		&v1.Runbook{ObjectMeta: metav1.ObjectMeta{Name: "oom-rollout-guard", Namespace: ns, CreationTimestamp: *t(4320)},
			Spec: v1.RunbookSpec{Description: "Contain an OOM-killed Deployment: confirm whether a rollout introduced the regression, roll it back when it did, otherwise raise the memory limit within the namespace quota.",
				Trigger: v1.RunbookTrigger{SignalType: v1.SignalOOMKill, Severity: v1.IssueSeverityHigh, ResourceKind: "Deployment"}, MaxAttempts: 3,
				Steps: []v1.RunbookStep{
					{Name: "check-recent-rollout", Action: "CheckRecentRollout", Description: "Look for a rollout in the last 30 minutes; a fresh revision is the most likely cause.", Params: map[string]string{"window": "30m"}},
					{Name: "rollback-if-regressed", Action: "RollbackDeployment", Description: "Roll back to the previous revision when the rollout matches the OOM window.", Params: map[string]string{"onlyIf": "recentRollout"}},
					{Name: "raise-memory-limit", Action: "PatchResources", Description: "Raise the container memory limit by 25% when no rollout explains the kill.", Params: map[string]string{"container": "main", "memoryLimitDelta": "25%", "maxMemoryLimit": "2Gi"}},
					{Name: "verify-stability", Action: "WaitForStable", Description: "Watch restarts for 10 minutes before closing the issue.", Params: map[string]string{"timeout": "10m", "maxRestarts": "0"}},
				}}},
		&v1.Runbook{ObjectMeta: metav1.ObjectMeta{Name: "crashloop-probe-fix", Namespace: "batch", CreationTimestamp: *t(9800)},
			Spec: v1.RunbookSpec{Description: "Stop a CrashLoopBackOff caused by a readiness probe that fires before the worker warms up.",
				Trigger: v1.RunbookTrigger{SignalType: v1.SignalCrashLoopBackOff, Severity: v1.IssueSeverityMedium, ResourceKind: "Deployment"}, MaxAttempts: 2,
				Steps: []v1.RunbookStep{
					{Name: "collect-logs", Action: "CollectLogs", Description: "Capture the last 200 lines of the crashing container.", Params: map[string]string{"tail": "200"}},
					{Name: "patch-probe", Action: "PatchProbe", Description: "Give the readiness probe a 30 second initial delay.", Params: map[string]string{"probe": "readiness", "initialDelaySeconds": "30"}},
				}}},
		&v1.PostMortem{ObjectMeta: metav1.ObjectMeta{Name: "payments-db-conn-55aa-pm", Namespace: ns, CreationTimestamp: *t(560)},
			Spec: v1.PostMortemSpec{IssueRef: v1.IssueRef{Name: "payments-db-conn-55aa"}, Resource: ref("Deployment", "payments"), Severity: v1.IssueSeverityHigh},
			Status: v1.PostMortemStatus{State: v1.PostMortemStateOpen, Summary: "pgbouncer pool exhaustion caused 29 minutes of degraded checkout.", RootCause: "A retry loop in the refund worker opened a connection per attempt and never returned it.", Impact: "3.2% of checkout requests failed for 29 minutes; no data loss.", Duration: "29m",
				Timeline:       []v1.TimelineEvent{{Timestamp: *t(600), Type: "detected", Detail: "pool_waiting > 100 for 2m"}, {Timestamp: *t(590), Type: "analysis", Detail: "AIInsight: connection leak in refund worker (confidence 0.84)"}, {Timestamp: *t(575), Type: "remediation", Detail: "pool size raised to 200"}, {Timestamp: *t(571), Type: "resolved", Detail: "waiting clients back to 0"}},
				LessonsLearned: []string{"Pool saturation alerts fired 8 minutes after the first symptom"}, PreventionActions: []string{"Cap retries in the refund worker", "Alert on pgbouncer cl_waiting > 20"}, GeneratedAt: t(565)}},
		&v1.ServiceLevelObjective{ObjectMeta: metav1.ObjectMeta{Name: "checkout-availability", Namespace: ns},
			Spec:   v1.ServiceLevelObjectiveSpec{ServiceName: "checkout", Description: "Successful checkout requests", Indicator: v1.SLOIndicator{Type: v1.SLOIndicatorType("availability"), MetricSource: v1.SLOMetricSource("prometheus"), PrometheusQuery: `sum(rate(http_requests_total{job="checkout",code!~"5.."}[5m])) / sum(rate(http_requests_total{job="checkout"}[5m]))`}, Target: v1.SLOTarget{Percentage: 99.9, Window: "30d"}, Enabled: true},
			Status: v1.ServiceLevelObjectiveStatus{CurrentValue: 99.62, TargetMet: false, ErrorBudgetTotal: 43.2, ErrorBudgetRemaining: 9.7, ErrorBudgetConsumedPercentage: 77.5, LastCalculatedAt: t(1)}},
		&v1.ServiceLevelObjective{ObjectMeta: metav1.ObjectMeta{Name: "api-gateway-latency", Namespace: ns},
			Spec:   v1.ServiceLevelObjectiveSpec{ServiceName: "api-gateway", Description: "p99 under 400ms", Indicator: v1.SLOIndicator{Type: v1.SLOIndicatorType("latency"), MetricSource: v1.SLOMetricSource("prometheus")}, Target: v1.SLOTarget{Percentage: 99.5, Window: "30d"}, Enabled: true},
			Status: v1.ServiceLevelObjectiveStatus{CurrentValue: 99.83, TargetMet: true, ErrorBudgetTotal: 216, ErrorBudgetRemaining: 142.6, ErrorBudgetConsumedPercentage: 34, LastCalculatedAt: t(1)}},
		&v1.ClusterRegistration{ObjectMeta: metav1.ObjectMeta{Name: "prod-us-east-1", Namespace: "chatcli-system"},
			Spec:   v1.ClusterRegistrationSpec{DisplayName: "prod us-east-1", KubeconfigSecretRef: v1.SecretRefSpec{Name: "kubeconfig-prod-use1"}, Region: "us-east-1", Environment: "production", Tier: "critical"},
			Status: v1.ClusterRegistrationStatus{Connected: true, KubernetesVersion: "v1.31.4", NodeCount: 42, NamespaceCount: 61, ActiveIssues: 3, ActiveRemediations: 1, LastHealthCheck: t(1)}},
		&v1.ClusterRegistration{ObjectMeta: metav1.ObjectMeta{Name: "staging-eu-west-1", Namespace: "chatcli-system"},
			Spec:   v1.ClusterRegistrationSpec{DisplayName: "staging eu-west-1", KubeconfigSecretRef: v1.SecretRefSpec{Name: "kubeconfig-stg-euw1"}, Region: "eu-west-1", Environment: "staging", Tier: "standard"},
			Status: v1.ClusterRegistrationStatus{Connected: false, KubernetesVersion: "v1.30.9", NodeCount: 6, NamespaceCount: 14, LastHealthCheck: t(47)}},
		&v1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: "audit-001", Namespace: ns}, Spec: v1.AuditEventSpec{EventType: "remediation.started", Actor: v1.AuditActor{Type: "controller", Name: "chatcli-operator", Controller: "RemediationReconciler"}, Resource: v1.AuditResource{Kind: "Deployment", Name: "checkout", Namespace: ns}, Severity: "info", Timestamp: *t(14), Details: map[string]string{"plan": "checkout-error-rate-2b91-plan-1", "strategy": "agentic"}}},
		&v1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: "audit-002", Namespace: ns}, Spec: v1.AuditEventSpec{EventType: "issue.contained", Actor: v1.AuditActor{Type: "controller", Name: "chatcli-operator", Controller: "IssueReconciler"}, Resource: v1.AuditResource{Kind: "Deployment", Name: "api-gateway", Namespace: ns}, Severity: "warning", Timestamp: *t(36), Details: map[string]string{"action": "ScaleDeployment", "replicas": "0"}}},
		&v1.AuditEvent{ObjectMeta: metav1.ObjectMeta{Name: "audit-003", Namespace: ns}, Spec: v1.AuditEventSpec{EventType: "approval.requested", Actor: v1.AuditActor{Type: "controller", Name: "chatcli-operator", Controller: "ApprovalReconciler"}, Resource: v1.AuditResource{Kind: "ApprovalRequest", Name: "api-gateway-oom-7f3a-rollback", Namespace: ns}, Severity: "info", Timestamp: *t(34)}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
	)

	return objs
}

// newServer wires the REST API server over a fake client holding the seed.
func newServer(addr string) *rest.APIServer {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(seed()...).Build()
	srv := rest.NewAPIServer(c, addr)
	srv.SetAPIKeys(map[string]string{previewAPIKey: "admin"})
	return srv
}

// listenAddr is the configured address: DASHPREVIEW_ADDR or the default.
func listenAddr() string {
	if v := os.Getenv("DASHPREVIEW_ADDR"); v != "" {
		return v
	}
	return defaultAddr
}

// run serves the preview until ctx is done.
func run(ctx context.Context, addr string) error {
	log.Printf("dashboard preview on http://%s (API key: %s)", addr, previewAPIKey)
	return newServer(addr).Start(ctx)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, listenAddr()); err != nil {
		log.Fatal(err)
	}
}
