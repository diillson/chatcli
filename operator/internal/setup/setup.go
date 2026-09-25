/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package setup wires the operator's controllers into a controller-runtime
// manager. main.go and the integration suite share it, so a test runs the
// same reconcilers, in the same configuration, that a deployed operator
// runs. Anything that only makes sense in a real pod (REST API keys,
// health probes, signal handling) stays in main.
package setup

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/diillson/chatcli/operator/controllers"
)

// Options carries the dependencies the controllers need beyond the manager.
type Options struct {
	// Clientset reaches pod logs and other typed APIs the diagnostics use.
	Clientset kubernetes.Interface
	// ServerClient is the shared gRPC client to the ChatCLI server.
	ServerClient *controllers.ServerClient
	// Logger is the structured logger shared by the bridge and clients.
	Logger *zap.Logger
	// AlertTransport selects how the WatcherBridge receives alerts.
	AlertTransport controllers.AlertTransport
	// PrometheusURL enables the metrics collector when set.
	PrometheusURL string
	// DecisionEngine turns on the confidence and circuit-breaker gate that
	// parks plans for a human; off, ApprovalPolicies are the only gate.
	DecisionEngine bool
	// ClusterName is the local cluster's ClusterRegistration name. When set,
	// the registration's tier decides which severities wait for a human.
	ClusterName string
}

// Components are the long-lived pieces main may need after wiring.
type Components struct {
	WatcherBridge *controllers.WatcherBridge
}

// OptionsFromEnv reads the process environment the way main does.
func OptionsFromEnv(clientset kubernetes.Interface, sc *controllers.ServerClient, logger *zap.Logger) (Options, error) {
	transport, err := controllers.ParseAlertTransport(os.Getenv("CHATCLI_OPERATOR_ALERT_TRANSPORT"))
	if err != nil {
		return Options{}, fmt.Errorf("invalid CHATCLI_OPERATOR_ALERT_TRANSPORT: %w", err)
	}
	return Options{
		Clientset:      clientset,
		ServerClient:   sc,
		Logger:         logger,
		AlertTransport: transport,
		PrometheusURL:  os.Getenv("PROMETHEUS_URL"),
		DecisionEngine: strings.EqualFold(os.Getenv("CHATCLI_OPERATOR_DECISION_ENGINE"), "true"),
		ClusterName:    strings.TrimSpace(os.Getenv("CHATCLI_OPERATOR_CLUSTER_NAME")),
	}, nil
}

// Controllers registers every reconciler and the WatcherBridge with mgr.
func Controllers(mgr ctrl.Manager, o Options) (*Components, error) {
	if o.AlertTransport == "" {
		o.AlertTransport = controllers.AlertTransportStream
	}
	c := mgr.GetClient()
	s := mgr.GetScheme()

	watcherBridge := controllers.NewWatcherBridge(c, s, o.ServerClient, o.Logger)
	watcherBridge.SetAlertTransport(o.AlertTransport)
	if err := mgr.Add(watcherBridge); err != nil {
		return nil, fmt.Errorf("adding WatcherBridge: %w", err)
	}

	auditRecorder := controllers.NewAuditRecorder(c, s)

	// One federation reconciler serves both its own controller and the
	// Issue and Remediation reconcilers: it owns the remote client cache.
	federation := &controllers.FederationReconciler{Client: c, Scheme: s}
	var engine *controllers.DecisionEngine
	if o.DecisionEngine {
		engine = &controllers.DecisionEngine{}
	}

	// Shared components for the AIOps pipeline.
	patternStore := controllers.NewPatternStore(c)
	costTracker := controllers.NewCostTracker(c)
	noiseReducer := controllers.NewNoiseReducer(c)

	// Shared components for enriched AI analysis.
	contextBuilder := controllers.NewKubernetesContextBuilder(c, o.Clientset)
	logAnalyzer := controllers.NewLogAnalyzer(c, o.Clientset)
	gitOpsDetector := controllers.NewGitOpsDetector(c)
	sourceCodeAnalyzer := controllers.NewSourceCodeAnalyzer(c)
	cascadeAnalyzer := controllers.NewCascadeAnalyzer(c)
	blastRadiusPredictor := controllers.NewBlastRadiusPredictor(c)

	// MetricsCollector is optional: it needs a Prometheus endpoint.
	var metricsCollector *controllers.MetricsCollector
	if o.PrometheusURL != "" {
		metricsCollector = controllers.NewMetricsCollector(o.PrometheusURL)
	}

	type named struct {
		name  string
		setup func(ctrl.Manager) error
	}
	reconcilers := []named{
		{"Instance", (&controllers.InstanceReconciler{
			Client: c, Scheme: s, Prober: controllers.NewGRPCProber(c, o.Logger),
		}).SetupWithManager},
		{"Issue", (&controllers.IssueReconciler{
			Client: c, Scheme: s, DedupInvalidator: watcherBridge, AuditRecorder: auditRecorder, Federation: federation,
		}).SetupWithManager},
		{"Remediation", (&controllers.RemediationReconciler{
			Client: c, Scheme: s, ServerClient: o.ServerClient, ContextBuilder: contextBuilder,
			AuditRecorder: auditRecorder, PatternStore: patternStore, CostTracker: costTracker,
			DecisionEngine: engine, ClusterTier: o.ClusterName, Federation: federation,
		}).SetupWithManager},
		{"Anomaly", (&controllers.AnomalyReconciler{
			Client: c, Scheme: s, NoiseReducer: noiseReducer,
		}).SetupWithManager},
		{"AIInsight", (&controllers.AIInsightReconciler{
			Client: c, Scheme: s, ServerClient: o.ServerClient, ContextBuilder: contextBuilder,
			LogAnalyzer: logAnalyzer, MetricsCollector: metricsCollector, GitOpsDetector: gitOpsDetector,
			SourceCodeAnalyzer: sourceCodeAnalyzer, CascadeAnalyzer: cascadeAnalyzer,
			BlastRadiusPredictor: blastRadiusPredictor, CostTracker: costTracker,
		}).SetupWithManager},
		{"PostMortem", (&controllers.PostMortemReconciler{Client: c, Scheme: s}).SetupWithManager},
		{"Notification", (&controllers.NotificationReconciler{Client: c, Scheme: s, AuditRecorder: auditRecorder}).SetupWithManager},
		{"SLO", (&controllers.SLOReconciler{Client: c, Scheme: s, AuditRecorder: auditRecorder}).SetupWithManager},
		{"SLA", (&controllers.SLAReconciler{Client: c, Scheme: s, AuditRecorder: auditRecorder}).SetupWithManager},
		{"Approval", (&controllers.ApprovalReconciler{
			Client: c, Scheme: s, BlastRadiusPredictor: blastRadiusPredictor,
		}).SetupWithManager},
		{"Federation", federation.SetupWithManager},
		{"SourceRepository", (&controllers.SourceRepositoryReconciler{Client: c, Scheme: s}).SetupWithManager},
		{"Chaos", (&controllers.ChaosReconciler{Client: c, Scheme: s}).SetupWithManager},
	}
	for _, r := range reconcilers {
		if err := r.setup(mgr); err != nil {
			return nil, fmt.Errorf("creating controller %s: %w", r.name, err)
		}
	}

	return &Components{WatcherBridge: watcherBridge}, nil
}
