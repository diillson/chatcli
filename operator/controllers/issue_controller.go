package controllers

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const issueFinalizerName = "platform.chatcli.io/issue-finalizer"

// resolveInstanceProvider looks up the first ready Instance CR and returns its provider and model.
// Falls back to empty strings if no ready Instance is found (the server will use its own defaults).
func resolveInstanceProvider(ctx context.Context, c client.Client) (provider, model string) {
	var instances platformv1alpha1.InstanceList
	if err := c.List(ctx, &instances); err != nil {
		return "", ""
	}
	for _, inst := range instances.Items {
		if inst.Status.Ready {
			return inst.Spec.Provider, inst.Spec.Model
		}
	}
	return "", ""
}

var (
	issuesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "issues_total",
		Help:      "Total issues by severity and state.",
	}, []string{"severity", "state"})

	issueResolutionDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "issue_resolution_duration_seconds",
		Help:      "Duration from detection to resolution.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 15),
	})

	activeIssues = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "active_issues",
		Help:      "Number of issues not yet resolved.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		issuesTotal,
		issueResolutionDuration,
		activeIssues,
	)
}

// DedupInvalidator allows refreshing dedup entries when issues reach terminal states.
type DedupInvalidator interface {
	RefreshDedupForResource(deployment, namespace string)
}

// IssueReconciler reconciles Issue objects.
type IssueReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	DedupInvalidator DedupInvalidator // optional: watcher bridge for dedup invalidation
	AuditRecorder    *AuditRecorder   // optional: records audit trail events
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans/status,verbs=get
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=aiinsights,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=aiinsights/status,verbs=get
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=runbooks,verbs=get;list;watch;create;update;patch

func (r *IssueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// 1. Fetch the Issue
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, req.NamespacedName, &issue); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. Handle deletion
	if issue.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(&issue, issueFinalizerName) {
			controllerutil.RemoveFinalizer(&issue, issueFinalizerName)
			if err := r.Update(ctx, &issue); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// 3. Add finalizer
	if !controllerutil.ContainsFinalizer(&issue, issueFinalizerName) {
		controllerutil.AddFinalizer(&issue, issueFinalizerName)
		if err := r.Update(ctx, &issue); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 4. State machine
	switch issue.Status.State {
	case "", platformv1alpha1.IssueStateDetected:
		return r.handleDetected(ctx, &issue)
	case platformv1alpha1.IssueStateAnalyzing:
		return r.handleAnalyzing(ctx, &issue)
	case platformv1alpha1.IssueStateRemediating:
		return r.handleRemediating(ctx, &issue)
	case platformv1alpha1.IssueStateResolved, platformv1alpha1.IssueStateEscalated, platformv1alpha1.IssueStateFailed:
		// Terminal states
		log.Info("Issue in terminal state", "name", issue.Name, "state", issue.Status.State)
		return ctrl.Result{}, nil
	default:
		log.Info("Unknown issue state", "state", issue.Status.State)
		return ctrl.Result{}, nil
	}
}

// handleDetected processes an issue in the Detected state.
// Sets detectedAt, creates AIInsight, transitions to Analyzing.
func (r *IssueReconciler) handleDetected(ctx context.Context, issue *platformv1alpha1.Issue) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Handling Detected issue", "name", issue.Name)

	// Set detectedAt if not set
	now := metav1.Now()
	if issue.Status.DetectedAt == nil {
		issue.Status.DetectedAt = &now
	}
	if issue.Status.MaxRemediationAttempts == 0 {
		issue.Status.MaxRemediationAttempts = 3
	}

	// Create AIInsight for analysis
	insightName := issue.Name + "-insight"
	insight := &platformv1alpha1.AIInsight{
		ObjectMeta: metav1.ObjectMeta{
			Name:      insightName,
			Namespace: issue.Namespace,
		},
	}

	provider, model := resolveInstanceProvider(ctx, r.Client)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, insight, func() error {
		if err := controllerutil.SetControllerReference(issue, insight, r.Scheme); err != nil {
			return err
		}
		insight.Spec = platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Provider: provider,
			Model:    model,
		}
		return nil
	})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create AIInsight: %w", err)
	}

	// Transition to Analyzing
	issue.Status.State = platformv1alpha1.IssueStateAnalyzing
	meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
		Type:               "Analyzing",
		Status:             metav1.ConditionTrue,
		Reason:             "AIInsightCreated",
		Message:            fmt.Sprintf("AIInsight %s created for analysis", insightName),
		LastTransitionTime: metav1.Now(),
	})

	issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateAnalyzing)).Inc()

	// Record audit event
	if r.AuditRecorder != nil {
		if err := r.AuditRecorder.RecordIssueCreated(ctx, issue); err != nil {
			log.Error(err, "Failed to record audit event for created issue")
		}
	}

	if err := r.Status().Update(ctx, issue); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleAnalyzing processes an issue in the Analyzing state.
// Runbook-first flow: manual runbook has precedence, otherwise generates runbook from AI.
// All remediation plans are created from a Runbook (manual or auto-generated).
func (r *IssueReconciler) handleAnalyzing(ctx context.Context, issue *platformv1alpha1.Issue) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Handling Analyzing issue", "name", issue.Name)

	// Check if AIInsight has analysis
	insightName := issue.Name + "-insight"
	var insight platformv1alpha1.AIInsight
	if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	// Wait for analysis to be populated
	if insight.Status.Analysis == "" {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// 1. Find manual runbook (has precedence)
	runbook, err := r.findMatchingRunbook(ctx, issue)
	if err != nil {
		return ctrl.Result{}, err
	}

	if runbook != nil {
		// Manual runbook found — use it
		log.Info("Using manual runbook", "issue", issue.Name, "runbook", runbook.Name)
		if runbook.Spec.MaxAttempts > 0 {
			issue.Status.MaxRemediationAttempts = runbook.Spec.MaxAttempts
		}
	} else if len(insight.Status.SuggestedActions) > 0 {
		// 2. No manual runbook — generate one from AI
		log.Info("No manual runbook found, generating from AI", "issue", issue.Name, "actions", len(insight.Status.SuggestedActions))
		runbook, err = r.generateRunbookFromAI(ctx, issue, &insight)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("generating runbook from AI: %w", err)
		}
		log.Info("Auto-generated runbook", "runbook", runbook.Name)
	} else {
		// 3. No runbook and no AI actions — use agentic mode
		log.Info("No runbook or AI actions found, using agentic remediation", "issue", issue.Name)

		attempt := issue.Status.RemediationAttempts
		if attempt == 0 {
			attempt = 1
		}

		if err := r.createAgenticRemediationPlan(ctx, issue, attempt); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating agentic remediation plan: %w", err)
		}

		issue.Status.State = platformv1alpha1.IssueStateRemediating
		issue.Status.RemediationAttempts = attempt
		meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
			Type:               "Remediating",
			Status:             metav1.ConditionTrue,
			Reason:             "AgenticRemediationStarted",
			Message:            "AI-driven agentic remediation in progress",
			LastTransitionTime: metav1.Now(),
		})
		return ctrl.Result{RequeueAfter: 15 * time.Second}, r.Status().Update(ctx, issue)
	}

	// Determine attempt number: use existing if this is a re-analysis, otherwise start at 1
	attempt := issue.Status.RemediationAttempts
	if attempt == 0 {
		attempt = 1
	}

	// Create RemediationPlan from Runbook (manual or auto-generated)
	if err := r.createRemediationPlan(ctx, issue, runbook, &insight, attempt); err != nil {
		return ctrl.Result{}, err
	}

	// Transition to Remediating
	issue.Status.State = platformv1alpha1.IssueStateRemediating
	issue.Status.RemediationAttempts = attempt
	meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
		Type:               "Remediating",
		Status:             metav1.ConditionTrue,
		Reason:             "RemediationPlanCreated",
		Message:            fmt.Sprintf("Remediation attempt %d/%d started (runbook: %s)", attempt, issue.Status.MaxRemediationAttempts, runbook.Name),
		LastTransitionTime: metav1.Now(),
	})

	issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateRemediating)).Inc()

	if err := r.Status().Update(ctx, issue); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// handleRemediating processes an issue in the Remediating state.
// Checks latest RemediationPlan status.
func (r *IssueReconciler) handleRemediating(ctx context.Context, issue *platformv1alpha1.Issue) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Handling Remediating issue", "name", issue.Name, "attempt", issue.Status.RemediationAttempts)

	// Find the latest RemediationPlan for this issue
	plan, err := r.findLatestRemediationPlan(ctx, issue)
	if err != nil {
		return ctrl.Result{}, err
	}
	if plan == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	switch plan.Status.State {
	case platformv1alpha1.RemediationStateCompleted:
		// Resolved!
		now := metav1.Now()
		issue.Status.State = platformv1alpha1.IssueStateResolved
		issue.Status.Resolution = plan.Status.Result
		issue.Status.ResolvedAt = &now

		meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
			Type:               "Resolved",
			Status:             metav1.ConditionTrue,
			Reason:             "RemediationSucceeded",
			Message:            fmt.Sprintf("Issue resolved on attempt %d: %s", issue.Status.RemediationAttempts, plan.Status.Result),
			LastTransitionTime: metav1.Now(),
		})

		issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateResolved)).Inc()
		if issue.Status.DetectedAt != nil {
			issueResolutionDuration.Observe(now.Sub(issue.Status.DetectedAt.Time).Seconds())
		}

		// Generate PostMortem for ALL remediation modes (not just agentic)
		if err := r.generatePostMortem(ctx, issue, plan); err != nil {
			log.Error(err, "Failed to generate PostMortem, continuing")
		}

		// For agentic plans, also generate a reusable Runbook
		if plan.Spec.AgenticMode && len(plan.Spec.AgenticHistory) > 0 {
			if err := r.generateAgenticRunbook(ctx, issue, plan); err != nil {
				log.Error(err, "Failed to generate agentic Runbook, continuing")
			}
		}

		// Record audit event
		if r.AuditRecorder != nil {
			if err := r.AuditRecorder.RecordIssueResolved(ctx, issue); err != nil {
				log.Error(err, "Failed to record audit event for resolved issue")
			}
		}

		// Refresh dedup cooldown so stale alerts don't immediately re-trigger
		r.refreshDedup(issue)

		return ctrl.Result{}, r.Status().Update(ctx, issue)

	case platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
		// Check if we can retry
		if issue.Status.RemediationAttempts < issue.Status.MaxRemediationAttempts {
			nextAttempt := issue.Status.RemediationAttempts + 1
			log.Info("Remediation failed, requesting re-analysis with failure context",
				"attempt", nextAttempt, "max", issue.Status.MaxRemediationAttempts)

			// Collect failure evidence from all failed plans
			failureCtx := r.collectFailureEvidence(ctx, issue)

			// Request re-analysis: clears insight so AIInsightReconciler re-runs
			if err := r.requestReanalysis(ctx, issue, failureCtx); err != nil {
				log.Error(err, "Failed to request re-analysis, falling back to existing runbook")
				// Fallback: use existing runbook without re-analysis
				return r.retryWithExistingRunbook(ctx, issue, nextAttempt)
			}

			// Transition back to Analyzing for re-analysis
			issue.Status.State = platformv1alpha1.IssueStateAnalyzing
			issue.Status.RemediationAttempts = nextAttempt
			meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
				Type:               "Analyzing",
				Status:             metav1.ConditionTrue,
				Reason:             "ReanalysisRequested",
				Message:            fmt.Sprintf("Re-analyzing with failure context from attempt %d", nextAttempt-1),
				LastTransitionTime: metav1.Now(),
			})

			if err := r.Status().Update(ctx, issue); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Max attempts reached - escalate
		log.Info("Max remediation attempts reached, escalating", "issue", issue.Name)
		issue.Status.State = platformv1alpha1.IssueStateEscalated
		meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
			Type:               "Escalated",
			Status:             metav1.ConditionTrue,
			Reason:             "MaxAttemptsReached",
			Message:            fmt.Sprintf("All %d remediation attempts failed", issue.Status.MaxRemediationAttempts),
			LastTransitionTime: metav1.Now(),
		})

		issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateEscalated)).Inc()

		// Record audit event
		if r.AuditRecorder != nil {
			if err := r.AuditRecorder.RecordIssueEscalated(ctx, issue); err != nil {
				log.Error(err, "Failed to record audit event for escalated issue")
			}
		}

		// Refresh dedup cooldown so stale alerts don't immediately re-trigger
		r.refreshDedup(issue)

		return ctrl.Result{}, r.Status().Update(ctx, issue)

	default:
		// Still executing, requeue
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

// findMatchingRunbook finds a Runbook matching the issue using tiered matching:
// Tier 1 (preferred): SignalType + Severity + ResourceKind (exact match)
// Tier 2 (fallback):  Severity + ResourceKind (without signal match)
func (r *IssueReconciler) findMatchingRunbook(ctx context.Context, issue *platformv1alpha1.Issue) (*platformv1alpha1.Runbook, error) {
	var runbooks platformv1alpha1.RunbookList
	if err := r.List(ctx, &runbooks, client.InNamespace(issue.Namespace)); err != nil {
		return nil, err
	}

	// Also list cluster-scoped runbooks (in all namespaces)
	var allRunbooks platformv1alpha1.RunbookList
	if err := r.List(ctx, &allRunbooks); err != nil {
		return nil, err
	}

	candidates := append(runbooks.Items, allRunbooks.Items...)

	// Resolve signal type: prefer Spec.SignalType, fallback to label
	signalType := issue.Spec.SignalType
	if signalType == "" && issue.Labels != nil {
		signalType = issue.Labels["platform.chatcli.io/signal"]
	}

	// Tier 1: SignalType + Severity + ResourceKind
	var tier2Match *platformv1alpha1.Runbook
	for i := range candidates {
		rb := &candidates[i]
		if rb.Spec.Trigger.Severity != issue.Spec.Severity ||
			rb.Spec.Trigger.ResourceKind != issue.Spec.Resource.Kind {
			continue
		}
		// Severity + ResourceKind match
		if signalType != "" && string(rb.Spec.Trigger.SignalType) == signalType {
			// Tier 1 exact match — return immediately
			return rb, nil
		}
		// Tier 2 match — save as fallback
		if tier2Match == nil {
			tier2Match = rb
		}
	}

	return tier2Match, nil
}

// createRemediationPlan creates a RemediationPlan from a Runbook.
func (r *IssueReconciler) createRemediationPlan(ctx context.Context, issue *platformv1alpha1.Issue, runbook *platformv1alpha1.Runbook, insight *platformv1alpha1.AIInsight, attempt int32) error {
	planName := fmt.Sprintf("%s-plan-%d", issue.Name, attempt)

	// Build actions from runbook steps — use mapActionType for consistent mapping
	var actions []platformv1alpha1.RemediationAction
	for _, step := range runbook.Spec.Steps {
		actionType := mapActionType(step.Action)
		actions = append(actions, platformv1alpha1.RemediationAction{
			Type:   actionType,
			Params: step.Params,
		})
	}

	// Build strategy with full context (no truncation)
	strategy := fmt.Sprintf("Attempt %d via runbook '%s': %s", attempt, runbook.Name, runbook.Spec.Description)
	if insight.Status.Analysis != "" {
		strategy = fmt.Sprintf("Attempt %d via runbook '%s'. AI analysis: %s", attempt, runbook.Name, insight.Status.Analysis)
	}

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      planName,
			Namespace: issue.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, plan, func() error {
		if err := controllerutil.SetControllerReference(issue, plan, r.Scheme); err != nil {
			return err
		}
		plan.Spec = platformv1alpha1.RemediationPlanSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Attempt:  attempt,
			Strategy: strategy,
			Actions:  actions,
			SafetyConstraints: []string{
				"No delete operations",
				"No destructive changes",
				"Rollback on failure",
			},
		}
		return nil
	})

	return err
}

// generateRunbookFromAI materializes AI-suggested actions as a reusable Runbook.
// The runbook can be matched by findMatchingRunbook on future occurrences of the same issue type.
func (r *IssueReconciler) generateRunbookFromAI(ctx context.Context, issue *platformv1alpha1.Issue, insight *platformv1alpha1.AIInsight) (*platformv1alpha1.Runbook, error) {
	// Resolve signal type
	signalType := issue.Spec.SignalType
	if signalType == "" && issue.Labels != nil {
		signalType = issue.Labels["platform.chatcli.io/signal"]
	}

	// Build a deterministic name: auto-{signal}-{severity}-{kind}
	rbName := sanitizeRunbookName(fmt.Sprintf("auto-%s-%s-%s",
		signalType, issue.Spec.Severity, strings.ToLower(issue.Spec.Resource.Kind)))

	// Convert AI suggested actions to runbook steps
	var steps []platformv1alpha1.RunbookStep
	for _, sa := range insight.Status.SuggestedActions {
		steps = append(steps, platformv1alpha1.RunbookStep{
			Name:        sa.Name,
			Action:      sa.Action,
			Description: sa.Description,
			Params:      sa.Params,
		})
	}

	// Build full description from AI analysis (no truncation)
	description := insight.Status.Analysis
	if len(insight.Status.Recommendations) > 0 {
		description += "\n\nRecommendations:\n- " + strings.Join(insight.Status.Recommendations, "\n- ")
	}

	runbook := &platformv1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbName,
			Namespace: issue.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, runbook, func() error {
		// Set labels for identification
		if runbook.Labels == nil {
			runbook.Labels = make(map[string]string)
		}
		runbook.Labels["platform.chatcli.io/auto-generated"] = "true"
		runbook.Labels["platform.chatcli.io/source-issue"] = issue.Name

		runbook.Spec = platformv1alpha1.RunbookSpec{
			Description: description,
			Trigger: platformv1alpha1.RunbookTrigger{
				SignalType:   platformv1alpha1.AnomalySignalType(signalType),
				Severity:     issue.Spec.Severity,
				ResourceKind: issue.Spec.Resource.Kind,
			},
			Steps:       steps,
			MaxAttempts: 3,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return runbook, nil
}

// sanitizeRunbookName produces a Kubernetes-compliant name (lowercase, max 63 chars).
var k8sNameRegex = regexp.MustCompile(`[^a-z0-9-]`)

func sanitizeRunbookName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "-")
	name = k8sNameRegex.ReplaceAllString(name, "")
	// Remove consecutive dashes
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, "-")
	if len(name) > 63 {
		name = name[:63]
	}
	if name == "" {
		name = "auto-runbook"
	}
	return name
}

// mapActionType converts a string action name to RemediationActionType.
func mapActionType(action string) platformv1alpha1.RemediationActionType {
	switch action {
	case "ScaleDeployment":
		return platformv1alpha1.ActionScaleDeployment
	case "RollbackDeployment":
		return platformv1alpha1.ActionRollbackDeployment
	case "RestartDeployment":
		return platformv1alpha1.ActionRestartDeployment
	case "PatchConfig":
		return platformv1alpha1.ActionPatchConfig
	case "AdjustResources":
		return platformv1alpha1.ActionAdjustResources
	case "DeletePod":
		return platformv1alpha1.ActionDeletePod
	case "HelmRollback":
		return platformv1alpha1.ActionHelmRollback
	case "ArgoSyncApp":
		return platformv1alpha1.ActionArgoSyncApp
	case "AdjustHPA":
		return platformv1alpha1.ActionAdjustHPA
	case "RestartStatefulSetPod":
		return platformv1alpha1.ActionRestartStatefulSetPod
	case "CordonNode":
		return platformv1alpha1.ActionCordonNode
	case "DrainNode":
		return platformv1alpha1.ActionDrainNode
	case "ResizePVC":
		return platformv1alpha1.ActionResizePVC
	case "RotateSecret":
		return platformv1alpha1.ActionRotateSecret
	case "ExecDiagnostic":
		return platformv1alpha1.ActionExecDiagnostic
	case "UpdateIngress":
		return platformv1alpha1.ActionUpdateIngress
	case "PatchNetworkPolicy":
		return platformv1alpha1.ActionPatchNetworkPolicy
	case "ApplyManifest":
		return platformv1alpha1.ActionApplyManifest
	// StatefulSet
	case "ScaleStatefulSet":
		return platformv1alpha1.ActionScaleStatefulSet
	case "RestartStatefulSet":
		return platformv1alpha1.ActionRestartStatefulSet
	case "RollbackStatefulSet":
		return platformv1alpha1.ActionRollbackStatefulSet
	case "AdjustStatefulSetResources":
		return platformv1alpha1.ActionAdjustStatefulSetResources
	case "DeleteStatefulSetPod":
		return platformv1alpha1.ActionDeleteStatefulSetPod
	case "ForceDeleteStatefulSetPod":
		return platformv1alpha1.ActionForceDeleteStatefulSetPod
	case "UpdateStatefulSetStrategy":
		return platformv1alpha1.ActionUpdateStatefulSetStrategy
	case "RecreateStatefulSetPVC":
		return platformv1alpha1.ActionRecreateStatefulSetPVC
	case "PartitionStatefulSetUpdate":
		return platformv1alpha1.ActionPartitionStatefulSetUpdate
	// DaemonSet
	case "RestartDaemonSet":
		return platformv1alpha1.ActionRestartDaemonSet
	case "RollbackDaemonSet":
		return platformv1alpha1.ActionRollbackDaemonSet
	case "AdjustDaemonSetResources":
		return platformv1alpha1.ActionAdjustDaemonSetResources
	case "DeleteDaemonSetPod":
		return platformv1alpha1.ActionDeleteDaemonSetPod
	case "UpdateDaemonSetStrategy":
		return platformv1alpha1.ActionUpdateDaemonSetStrategy
	case "PauseDaemonSetRollout":
		return platformv1alpha1.ActionPauseDaemonSetRollout
	case "CordonAndDeleteDaemonSetPod":
		return platformv1alpha1.ActionCordonAndDeleteDaemonSetPod
	// Job
	case "RetryJob":
		return platformv1alpha1.ActionRetryJob
	case "AdjustJobResources":
		return platformv1alpha1.ActionAdjustJobResources
	case "DeleteFailedJob":
		return platformv1alpha1.ActionDeleteFailedJob
	case "SuspendJob":
		return platformv1alpha1.ActionSuspendJob
	case "ResumeJob":
		return platformv1alpha1.ActionResumeJob
	case "AdjustJobParallelism":
		return platformv1alpha1.ActionAdjustJobParallelism
	case "AdjustJobDeadline":
		return platformv1alpha1.ActionAdjustJobDeadline
	case "AdjustJobBackoffLimit":
		return platformv1alpha1.ActionAdjustJobBackoffLimit
	case "ForceDeleteJobPods":
		return platformv1alpha1.ActionForceDeleteJobPods
	// CronJob
	case "SuspendCronJob":
		return platformv1alpha1.ActionSuspendCronJob
	case "ResumeCronJob":
		return platformv1alpha1.ActionResumeCronJob
	case "TriggerCronJob":
		return platformv1alpha1.ActionTriggerCronJob
	case "AdjustCronJobResources":
		return platformv1alpha1.ActionAdjustCronJobResources
	case "AdjustCronJobSchedule":
		return platformv1alpha1.ActionAdjustCronJobSchedule
	case "AdjustCronJobDeadline":
		return platformv1alpha1.ActionAdjustCronJobDeadline
	case "AdjustCronJobHistory":
		return platformv1alpha1.ActionAdjustCronJobHistory
	case "AdjustCronJobConcurrency":
		return platformv1alpha1.ActionAdjustCronJobConcurrency
	case "DeleteCronJobActiveJobs":
		return platformv1alpha1.ActionDeleteCronJobActiveJobs
	case "ReplaceCronJobTemplate":
		return platformv1alpha1.ActionReplaceCronJobTemplate
	default:
		return platformv1alpha1.ActionCustom
	}
}

// findLatestRemediationPlan finds the most recent RemediationPlan for an issue.
func (r *IssueReconciler) findLatestRemediationPlan(ctx context.Context, issue *platformv1alpha1.Issue) (*platformv1alpha1.RemediationPlan, error) {
	var plans platformv1alpha1.RemediationPlanList
	if err := r.List(ctx, &plans, client.InNamespace(issue.Namespace)); err != nil {
		return nil, err
	}

	var matching []platformv1alpha1.RemediationPlan
	for _, p := range plans.Items {
		if p.Spec.IssueRef.Name == issue.Name {
			matching = append(matching, p)
		}
	}

	if len(matching) == 0 {
		return nil, nil
	}

	// Sort by attempt number descending
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].Spec.Attempt > matching[j].Spec.Attempt
	})

	return &matching[0], nil
}

// collectFailureEvidence builds a text summary of all failed remediation plans for an issue.
func (r *IssueReconciler) collectFailureEvidence(ctx context.Context, issue *platformv1alpha1.Issue) string {
	var plans platformv1alpha1.RemediationPlanList
	if err := r.List(ctx, &plans, client.InNamespace(issue.Namespace)); err != nil {
		return fmt.Sprintf("Error listing plans: %v", err)
	}

	var sb strings.Builder
	for _, p := range plans.Items {
		if p.Spec.IssueRef.Name != issue.Name {
			continue
		}
		if p.Status.State != platformv1alpha1.RemediationStateFailed &&
			p.Status.State != platformv1alpha1.RemediationStateRolledBack {
			continue
		}

		sb.WriteString(fmt.Sprintf("Attempt %d (state=%s):\n", p.Spec.Attempt, p.Status.State))
		sb.WriteString(fmt.Sprintf("  Strategy: %s\n", p.Spec.Strategy))
		sb.WriteString(fmt.Sprintf("  Result: %s\n", p.Status.Result))
		for _, a := range p.Spec.Actions {
			sb.WriteString(fmt.Sprintf("  Action: %s params=%v\n", a.Type, a.Params))
		}
		for _, ev := range p.Status.Evidence {
			sb.WriteString(fmt.Sprintf("  Evidence: [%s] %s\n", ev.Type, ev.Data))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// requestReanalysis clears the AIInsight analysis and sets failure context annotation,
// triggering the AIInsightReconciler to re-analyze with failure context.
func (r *IssueReconciler) requestReanalysis(ctx context.Context, issue *platformv1alpha1.Issue, failureContext string) error {
	insightName := issue.Name + "-insight"
	var insight platformv1alpha1.AIInsight
	if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err != nil {
		return fmt.Errorf("getting AIInsight for re-analysis: %w", err)
	}

	// Set failure context annotation
	if insight.Annotations == nil {
		insight.Annotations = make(map[string]string)
	}
	insight.Annotations["platform.chatcli.io/failure-context"] = failureContext

	if err := r.Update(ctx, &insight); err != nil {
		return fmt.Errorf("updating AIInsight annotations: %w", err)
	}

	// Clear analysis to trigger re-analysis
	insight.Status.Analysis = ""
	insight.Status.SuggestedActions = nil
	insight.Status.Recommendations = nil
	insight.Status.Confidence = 0

	return r.Status().Update(ctx, &insight)
}

// retryWithExistingRunbook is a fallback that retries with the current runbook
// when re-analysis fails.
func (r *IssueReconciler) retryWithExistingRunbook(ctx context.Context, issue *platformv1alpha1.Issue, nextAttempt int32) (ctrl.Result, error) {
	var insight platformv1alpha1.AIInsight
	insightName := issue.Name + "-insight"
	if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err != nil {
		return ctrl.Result{}, err
	}

	runbook, err := r.findMatchingRunbook(ctx, issue)
	if err != nil {
		return ctrl.Result{}, err
	}

	if runbook == nil {
		issue.Status.State = platformv1alpha1.IssueStateEscalated
		meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
			Type:               "Escalated",
			Status:             metav1.ConditionTrue,
			Reason:             "NoRunbookForRetry",
			Message:            "No runbook found for retry attempt",
			LastTransitionTime: metav1.Now(),
		})
		return ctrl.Result{}, r.Status().Update(ctx, issue)
	}

	if err := r.createRemediationPlan(ctx, issue, runbook, &insight, nextAttempt); err != nil {
		return ctrl.Result{}, err
	}

	issue.Status.RemediationAttempts = nextAttempt
	meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
		Type:               "Remediating",
		Status:             metav1.ConditionTrue,
		Reason:             "RetryingRemediation",
		Message:            fmt.Sprintf("Remediation attempt %d/%d (runbook: %s, fallback)", nextAttempt, issue.Status.MaxRemediationAttempts, runbook.Name),
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, issue); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// refreshDedup resets dedup cooldown for the issue's resource, extending suppression.
func (r *IssueReconciler) refreshDedup(issue *platformv1alpha1.Issue) {
	if r.DedupInvalidator != nil {
		r.DedupInvalidator.RefreshDedupForResource(
			issue.Spec.Resource.Name,
			issue.Spec.Resource.Namespace,
		)
	}
}

// createAgenticRemediationPlan creates a RemediationPlan in agentic mode (AI-driven step-by-step).
func (r *IssueReconciler) createAgenticRemediationPlan(ctx context.Context, issue *platformv1alpha1.Issue, attempt int32) error {
	planName := fmt.Sprintf("%s-plan-%d", issue.Name, attempt)

	plan := &platformv1alpha1.RemediationPlan{
		ObjectMeta: metav1.ObjectMeta{
			Name:      planName,
			Namespace: issue.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, plan, func() error {
		if err := controllerutil.SetControllerReference(issue, plan, r.Scheme); err != nil {
			return err
		}
		plan.Spec = platformv1alpha1.RemediationPlanSpec{
			IssueRef:        platformv1alpha1.IssueRef{Name: issue.Name},
			Attempt:         attempt,
			Strategy:        "Agentic AI-driven remediation",
			AgenticMode:     true,
			AgenticMaxSteps: 10,
			SafetyConstraints: []string{
				"No scaling to 0 replicas",
				"No delete operations without pod count check",
			},
		}
		return nil
	})
	return err
}

// generatePostMortem creates a PostMortem CR from an agentic remediation plan.
func (r *IssueReconciler) generatePostMortem(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) error {
	pmName := "pm-" + issue.Name
	if len(pmName) > 63 {
		pmName = pmName[:63]
	}
	now := metav1.Now()

	// Build timeline
	var timeline []platformv1alpha1.TimelineEvent
	if issue.Status.DetectedAt != nil {
		timeline = append(timeline, platformv1alpha1.TimelineEvent{
			Timestamp: *issue.Status.DetectedAt,
			Type:      "detected",
			Detail:    issue.Spec.Description,
		})
	}

	// Build action records — from agentic history or standard plan actions
	var actions []platformv1alpha1.ActionRecord

	if len(plan.Spec.AgenticHistory) > 0 {
		// Agentic mode: build from step history
		for _, step := range plan.Spec.AgenticHistory {
			evType := "action_executed"
			if strings.HasPrefix(step.Observation, "FAILED:") {
				evType = "action_failed"
			}
			actionStr := "(observation)"
			if step.Action != nil {
				actionStr = string(step.Action.Type)
			}
			timeline = append(timeline, platformv1alpha1.TimelineEvent{
				Timestamp: step.Timestamp,
				Type:      evType,
				Detail:    fmt.Sprintf("Step %d: %s — %s", step.StepNumber, actionStr, step.Observation),
			})
			if step.Action != nil {
				result := "success"
				if strings.HasPrefix(step.Observation, "FAILED:") {
					result = "failed"
				}
				actions = append(actions, platformv1alpha1.ActionRecord{
					Action:    string(step.Action.Type),
					Params:    step.Action.Params,
					Result:    result,
					Detail:    step.Observation,
					Timestamp: step.Timestamp,
				})
			}
		}
	} else {
		// Standard mode: build from plan actions + checkpoints/evidence
		for i, action := range plan.Spec.Actions {
			result := "success"
			detail := fmt.Sprintf("Action %s executed", action.Type)

			// Check checkpoint for this action
			for _, cp := range plan.Status.ActionCheckpoints {
				if cp.ActionIndex == int32(i) {
					if !cp.Success {
						result = "failed"
						detail = fmt.Sprintf("Action %s failed", action.Type)
					}
					break
				}
			}

			ts := now
			if plan.Status.StartedAt != nil {
				ts = *plan.Status.StartedAt
			}

			actions = append(actions, platformv1alpha1.ActionRecord{
				Action:    string(action.Type),
				Params:    action.Params,
				Result:    result,
				Detail:    detail,
				Timestamp: ts,
			})

			timeline = append(timeline, platformv1alpha1.TimelineEvent{
				Timestamp: ts,
				Type:      "action_executed",
				Detail:    fmt.Sprintf("%s: %s", action.Type, detail),
			})
		}
	}

	timeline = append(timeline, platformv1alpha1.TimelineEvent{
		Timestamp: now,
		Type:      "resolved",
		Detail:    plan.Status.Result,
	})

	// Duration
	duration := ""
	if issue.Status.DetectedAt != nil {
		duration = now.Sub(issue.Status.DetectedAt.Time).Round(time.Second).String()
	}

	// Read postmortem data — from plan annotations (agentic) or AIInsight (standard)
	summary := plan.Annotations["platform.chatcli.io/postmortem-summary"]
	rootCause := plan.Annotations["platform.chatcli.io/root-cause"]
	impact := plan.Annotations["platform.chatcli.io/impact"]

	var lessonsLearned []string
	if raw := plan.Annotations["platform.chatcli.io/lessons-learned"]; raw != "" {
		lessonsLearned = strings.Split(raw, "\n---\n")
	}

	var preventionActions []string
	if raw := plan.Annotations["platform.chatcli.io/prevention-actions"]; raw != "" {
		preventionActions = strings.Split(raw, "\n---\n")
	}

	// For standard mode: if annotations are empty, populate from AIInsight
	if summary == "" || rootCause == "" {
		insightName := issue.Name + "-insight"
		var insight platformv1alpha1.AIInsight
		if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err == nil {
			if summary == "" && insight.Status.Analysis != "" {
				summary = insight.Status.Analysis
			}
			if rootCause == "" && insight.Status.Analysis != "" {
				// Use first 500 chars of analysis as root cause
				rootCause = insight.Status.Analysis
				if len(rootCause) > 500 {
					rootCause = rootCause[:500] + "..."
				}
			}
			if len(lessonsLearned) == 0 && len(insight.Status.Recommendations) > 0 {
				lessonsLearned = insight.Status.Recommendations
			}
			if impact == "" {
				impact = fmt.Sprintf("Issue %s on %s/%s (severity: %s, risk: %d)",
					issue.Spec.SignalType, issue.Spec.Resource.Kind, issue.Spec.Resource.Name,
					issue.Spec.Severity, issue.Spec.RiskScore)
			}
		}
	}

	pm := &platformv1alpha1.PostMortem{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pmName,
			Namespace: issue.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, pm, func() error {
		if err := controllerutil.SetControllerReference(issue, pm, r.Scheme); err != nil {
			return err
		}
		if pm.Labels == nil {
			pm.Labels = make(map[string]string)
		}
		pm.Labels["platform.chatcli.io/issue"] = issue.Name
		pm.Labels["platform.chatcli.io/severity"] = string(issue.Spec.Severity)
		pm.Spec = platformv1alpha1.PostMortemSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Resource: issue.Spec.Resource,
			Severity: issue.Spec.Severity,
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Apply all status fields
	applyPostMortemStatus := func(pm *platformv1alpha1.PostMortem) {
		pm.Status.State = platformv1alpha1.PostMortemStateOpen
		pm.Status.Summary = summary
		pm.Status.RootCause = rootCause
		pm.Status.Impact = impact
		pm.Status.Timeline = timeline
		pm.Status.ActionsExecuted = actions
		pm.Status.LessonsLearned = lessonsLearned
		pm.Status.PreventionActions = preventionActions
		pm.Status.Duration = duration
		pm.Status.GeneratedAt = &now
		pm.Status.Trending = r.buildTrendingInfo(ctx, issue)
	}

	applyPostMortemStatus(pm)

	// Enrich with cascade chain
	cascadeAnalyzer := NewCascadeAnalyzer(r.Client)
	if cascadeResult, err := cascadeAnalyzer.AnalyzeCascade(ctx, issue); err == nil && len(cascadeResult.Chain) >= 2 {
		var chain []string
		for _, n := range cascadeResult.Chain {
			chain = append(chain, fmt.Sprintf("%s/%s(%s)", n.Namespace, n.ServiceName, n.Role))
		}
		pm.Status.CascadeChain = chain
	}

	// Enrich with GitOps context
	gitopsDetector := NewGitOpsDetector(r.Client)
	if gitopsCtx, err := gitopsDetector.DetectGitOpsContext(ctx, issue.Spec.Resource); err == nil {
		pm.Status.GitOpsContext = gitopsCtx.Summary
	}

	// Enrich with git correlation from SourceRepository
	srcAnalyzer := NewSourceCodeAnalyzer(r.Client)
	detectedAt := issue.CreationTimestamp.Time
	if issue.Status.DetectedAt != nil {
		detectedAt = issue.Status.DetectedAt.Time
	}
	if srcCtx, err := srcAnalyzer.BuildSourceContext(ctx, issue.Spec.Resource, detectedAt, nil); err == nil && srcCtx != nil && srcCtx.SuspectedCommit != nil {
		c := srcCtx.SuspectedCommit
		pm.Status.GitCorrelation = &platformv1alpha1.GitCorrelation{
			CommitSHA:     c.SHA,
			CommitMessage: c.Message,
			Author:        c.Author,
			Timestamp:     c.Timestamp,
			Confidence:    0.7,
			FilesChanged:  c.FilesChanged,
		}
	}

	// Try Status().Update() with retry on conflict (PostMortemReconciler may race).
	if err := r.Status().Update(ctx, pm); err != nil {
		// Conflict: re-fetch and re-apply
		if errors.IsConflict(err) || errors.IsNotFound(err) {
			time.Sleep(500 * time.Millisecond)
			if fetchErr := r.Get(ctx, types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}, pm); fetchErr != nil {
				return fmt.Errorf("re-fetching PostMortem after conflict: %w", fetchErr)
			}
			applyPostMortemStatus(pm)
			return r.Status().Update(ctx, pm)
		}
		return err
	}
	return nil
}

// generateAgenticRunbook creates a Runbook from the successful actions of an agentic session.
func (r *IssueReconciler) generateAgenticRunbook(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) error {
	signalType := issue.Spec.SignalType
	rbName := sanitizeRunbookName(fmt.Sprintf("agentic-%s-%s-%s",
		signalType, issue.Spec.Severity, strings.ToLower(issue.Spec.Resource.Kind)))

	var steps []platformv1alpha1.RunbookStep
	for _, step := range plan.Spec.AgenticHistory {
		if step.Action == nil || strings.HasPrefix(step.Observation, "FAILED:") {
			continue
		}
		steps = append(steps, platformv1alpha1.RunbookStep{
			Name:        fmt.Sprintf("Step %d: %s", step.StepNumber, step.Action.Type),
			Action:      string(step.Action.Type),
			Description: step.AIMessage,
			Params:      step.Action.Params,
		})
	}

	if len(steps) == 0 {
		return nil
	}

	runbook := &platformv1alpha1.Runbook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      rbName,
			Namespace: issue.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, runbook, func() error {
		if runbook.Labels == nil {
			runbook.Labels = make(map[string]string)
		}
		runbook.Labels["platform.chatcli.io/auto-generated"] = "true"
		runbook.Labels["platform.chatcli.io/source"] = "agentic"
		runbook.Labels["platform.chatcli.io/source-issue"] = issue.Name

		runbook.Spec = platformv1alpha1.RunbookSpec{
			Description: fmt.Sprintf("Auto-generated from agentic remediation of issue %s", issue.Name),
			Trigger: platformv1alpha1.RunbookTrigger{
				SignalType:   platformv1alpha1.AnomalySignalType(signalType),
				Severity:     issue.Spec.Severity,
				ResourceKind: issue.Spec.Resource.Kind,
			},
			Steps:       steps,
			MaxAttempts: 3,
		}
		return nil
	})
	return err
}

// buildTrendingInfo detects recurring incident patterns.
func (r *IssueReconciler) buildTrendingInfo(ctx context.Context, issue *platformv1alpha1.Issue) *platformv1alpha1.TrendingInfo {
	// Find PostMortems for same resource/signal in last 30 days
	var pms platformv1alpha1.PostMortemList
	if err := r.List(ctx, &pms, client.InNamespace(issue.Namespace)); err != nil {
		return nil
	}

	windowDays := int32(30)
	cutoff := time.Now().AddDate(0, 0, -int(windowDays))

	var related []string
	for _, pm := range pms.Items {
		if pm.Spec.Resource.Name != issue.Spec.Resource.Name {
			continue
		}
		if pm.Status.GeneratedAt == nil || pm.Status.GeneratedAt.Time.Before(cutoff) {
			continue
		}
		if pm.Name == "pm-"+issue.Name {
			continue
		}
		related = append(related, pm.Name)
	}

	if len(related) == 0 {
		return nil
	}

	return &platformv1alpha1.TrendingInfo{
		OccurrenceCount:    int32(len(related)) + 1, // +1 for current
		WindowDays:         windowDays,
		RelatedPostMortems: related,
		Pattern:            fmt.Sprintf("Recurring %s on %s/%s (%d occurrences in %d days)", issue.Spec.SignalType, issue.Spec.Resource.Kind, issue.Spec.Resource.Name, len(related)+1, windowDays),
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *IssueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Issue{}).
		Owns(&platformv1alpha1.RemediationPlan{}).
		Owns(&platformv1alpha1.AIInsight{}).
		Owns(&platformv1alpha1.PostMortem{}).
		Complete(r)
}
