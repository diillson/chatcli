package controllers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pb "github.com/diillson/chatcli/proto/chatcli/v1"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// AIInsightReconciler reconciles AIInsight objects by calling the server's
// AnalyzeIssue RPC to fill the analysis, confidence, and recommendations.
type AIInsightReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	ServerClient         *ServerClient
	ContextBuilder       *KubernetesContextBuilder
	LogAnalyzer          *LogAnalyzer
	MetricsCollector     *MetricsCollector
	GitOpsDetector       *GitOpsDetector
	SourceCodeAnalyzer   *SourceCodeAnalyzer
	CascadeAnalyzer      *CascadeAnalyzer
	BlastRadiusPredictor *BlastRadiusPredictor
	CostTracker          *CostTracker // Records LLM costs per AI analysis call
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=aiinsights,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=aiinsights/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch

func (r *AIInsightReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var insight platformv1alpha1.AIInsight
	if err := r.Get(ctx, req.NamespacedName, &insight); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Skip if analysis is already filled
	if insight.Status.Analysis != "" {
		return ctrl.Result{}, nil
	}

	// Check if server is connected
	if r.ServerClient == nil || !r.ServerClient.IsConnected() {
		logger.Info("Server not connected, requeuing", "name", insight.Name)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Get the parent Issue for context
	issueName := insight.Spec.IssueRef.Name
	if issueName == "" {
		logger.Error(nil, "AIInsight has no issueRef", "name", insight.Name)
		return ctrl.Result{}, nil
	}

	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: issueName, Namespace: insight.Namespace}, &issue); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Parent Issue not found, requeuing", "issue", issueName)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// Determine signal type: prefer IssueSpec.SignalType, fallback to label
	signalType := issue.Spec.SignalType
	if signalType == "" {
		if labels := issue.Labels; labels != nil {
			signalType = labels["platform.chatcli.io/signal"]
		}
	}

	// Build Kubernetes context for AI enrichment
	var kubeCtx string
	if r.ContextBuilder != nil {
		var ctxErr error
		kubeCtx, ctxErr = r.ContextBuilder.BuildContext(ctx, issue.Spec.Resource)
		if ctxErr != nil {
			logger.Info("Failed to build K8s context, continuing without it", "error", ctxErr)
		}
	}

	// Determine incident time for contextual enrichment
	incidentTime := issue.CreationTimestamp.Time
	if issue.Status.DetectedAt != nil {
		incidentTime = issue.Status.DetectedAt.Time
	}

	// Build enriched context from all sources
	var enrichedContext strings.Builder
	enrichedContext.WriteString(kubeCtx)

	// Log analysis
	if r.LogAnalyzer != nil {
		if logResult, err := r.LogAnalyzer.AnalyzePodLogs(ctx, issue.Spec.Resource, incidentTime); err == nil && logResult != nil {
			logText := logResult.FormatForAI()
			if logText != "" {
				enrichedContext.WriteString("\n")
				enrichedContext.WriteString(logText)
			}
			insight.Status.LogAnalysis = logResult.Summary
		}
	}

	// Prometheus metrics
	if r.MetricsCollector != nil {
		if metricsResult, err := r.MetricsCollector.CollectIncidentMetrics(ctx, issue.Spec.Resource, incidentTime); err == nil && metricsResult != nil {
			metricsText := metricsResult.FormatForAI()
			if metricsText != "" {
				enrichedContext.WriteString("\n")
				enrichedContext.WriteString(metricsText)
			}
			insight.Status.MetricsContext = metricsResult.Summary
		}
	}

	// GitOps context (Helm/ArgoCD/Flux)
	if r.GitOpsDetector != nil {
		if gitopsCtx, err := r.GitOpsDetector.DetectGitOpsContext(ctx, issue.Spec.Resource); err == nil && gitopsCtx != nil {
			gitopsText := gitopsCtx.FormatForAI()
			if gitopsText != "" {
				enrichedContext.WriteString("\n")
				enrichedContext.WriteString(gitopsText)
			}
			insight.Status.GitOpsContext = gitopsCtx.Summary
		}
	}

	// Source code correlation
	var stackTraces []StackTrace
	if r.LogAnalyzer != nil {
		if logResult, err := r.LogAnalyzer.AnalyzePodLogs(ctx, issue.Spec.Resource, incidentTime); err == nil && logResult != nil {
			stackTraces = logResult.StackTraces
		}
	}
	if r.SourceCodeAnalyzer != nil {
		if srcCtx, err := r.SourceCodeAnalyzer.BuildSourceContext(ctx, issue.Spec.Resource, incidentTime, stackTraces); err == nil && srcCtx != nil {
			srcText := srcCtx.FormatForAI()
			if srcText != "" {
				enrichedContext.WriteString("\n")
				enrichedContext.WriteString(srcText)
			}
			insight.Status.SourceCodeContext = srcCtx.Summary
		}
	}

	// Cascade / cross-service analysis
	if r.CascadeAnalyzer != nil {
		if cascadeResult, err := r.CascadeAnalyzer.AnalyzeCascade(ctx, &issue); err == nil && cascadeResult != nil {
			cascadeText := cascadeResult.FormatForAI()
			if cascadeText != "" {
				enrichedContext.WriteString("\n")
				enrichedContext.WriteString(cascadeText)
			}
			insight.Status.CascadeAnalysis = cascadeResult.Summary
		}
	}

	// RCA enrichment
	rcaEnricher := NewRCAEnricher(r.Client)
	if rcaCtx, err := rcaEnricher.EnrichIssueContext(ctx, &issue); err == nil && rcaCtx != nil {
		rcaText := rcaCtx.FormatForAI()
		if rcaText != "" {
			enrichedContext.WriteString("\n")
			enrichedContext.WriteString(rcaText)
		}
	}

	// Security (M1): Scrub sensitive data from logs before sending to LLM
	scrubber := NewLogScrubber(os.Getenv("CHATCLI_LOG_SCRUB_PATTERNS"))
	combinedContext := scrubber.ScrubText(enrichedContext.String())
	if len(combinedContext) > 30000 {
		combinedContext = combinedContext[:30000] + "\n... (context truncated)"
	}

	// Read failure context from annotation (set by retry re-analysis flow)
	var failureCtx string
	if insight.Annotations != nil {
		failureCtx = insight.Annotations["platform.chatcli.io/failure-context"]
	}

	// Inject candidate runbook context for AI validation (if present)
	if insight.Annotations != nil {
		if rbCtx := insight.Annotations["platform.chatcli.io/runbook-context"]; rbCtx != "" {
			combinedContext = combinedContext + "\n\n--- RUNBOOK VALIDATION ---\n" + rbCtx
			// Re-truncate if needed
			if len(combinedContext) > 32000 {
				combinedContext = combinedContext[:32000] + "\n... (context truncated)"
			}
		}
	}

	// Call AnalyzeIssue RPC
	analyzeReq := &pb.AnalyzeIssueRequest{
		IssueName:              issue.Name,
		Namespace:              issue.Namespace,
		ResourceKind:           issue.Spec.Resource.Kind,
		ResourceName:           issue.Spec.Resource.Name,
		SignalType:             signalType,
		Severity:               string(issue.Spec.Severity),
		Description:            issue.Spec.Description,
		RiskScore:              issue.Spec.RiskScore,
		Provider:               insight.Spec.Provider,
		Model:                  insight.Spec.Model,
		KubernetesContext:      combinedContext,
		PreviousFailureContext: failureCtx,
	}

	resp, err := r.ServerClient.AnalyzeIssue(ctx, analyzeReq)
	if err != nil {
		logger.Error(err, "AnalyzeIssue RPC failed, requeuing", "issue", issueName)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Record LLM cost for this analysis call
	if r.CostTracker != nil {
		// Estimate tokens from context length (~4 chars per token) and response length
		inputTokens := int64(len(combinedContext) / 4)
		outputTokens := int64(len(resp.Analysis) / 4)
		_ = r.CostTracker.RecordLLMCost(ctx, insight.Spec.IssueRef, issue.Namespace,
			insight.Spec.Provider, insight.Spec.Model, inputTokens, outputTokens)
	}

	// Update AIInsight status with the analysis
	now := metav1.Now()
	insight.Status.Analysis = resp.Analysis
	insight.Status.Confidence = float64(resp.Confidence)
	insight.Status.Recommendations = resp.Recommendations
	insight.Status.GeneratedAt = &now

	// Store suggested remediation actions from AI
	for _, a := range resp.SuggestedActions {
		insight.Status.SuggestedActions = append(insight.Status.SuggestedActions, platformv1alpha1.SuggestedAction{
			Name:        a.Name,
			Action:      a.Action,
			Description: a.Description,
			Params:      a.Params,
		})
	}

	if err := r.Status().Update(ctx, &insight); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating AIInsight status: %w", err)
	}

	// Clear failure-context annotation after re-analysis
	if failureCtx != "" {
		delete(insight.Annotations, "platform.chatcli.io/failure-context")
		if err := r.Update(ctx, &insight); err != nil {
			logger.Info("Failed to clear failure-context annotation", "error", err)
		}
	}

	logger.Info("AIInsight analysis complete",
		"name", insight.Name,
		"confidence", resp.Confidence,
		"provider", resp.Provider,
		"model", resp.Model)

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AIInsightReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AIInsight{}).
		Complete(r)
}
