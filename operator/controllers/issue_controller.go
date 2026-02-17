package controllers

import (
	"context"
	"fmt"
	"sort"
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

// IssueReconciler reconciles Issue objects.
type IssueReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans/status,verbs=get
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=aiinsights,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=aiinsights/status,verbs=get
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=runbooks,verbs=get;list;watch

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

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, insight, func() error {
		if err := controllerutil.SetControllerReference(issue, insight, r.Scheme); err != nil {
			return err
		}
		insight.Spec = platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Provider: "CLAUDEAI",
			Model:    "claude-sonnet-4-5",
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

	if err := r.Status().Update(ctx, issue); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// handleAnalyzing processes an issue in the Analyzing state.
// Checks if AIInsight is ready, finds matching Runbook, creates RemediationPlan.
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

	// Find matching Runbook
	runbook, err := r.findMatchingRunbook(ctx, issue)
	if err != nil {
		return ctrl.Result{}, err
	}

	if runbook == nil {
		// No Runbook — try AI-suggested actions as fallback
		if len(insight.Status.SuggestedActions) > 0 {
			log.Info("No runbook found, using AI-suggested actions", "issue", issue.Name, "actions", len(insight.Status.SuggestedActions))
			if err := r.createRemediationPlanFromAI(ctx, issue, &insight, 1); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			// No Runbook and no AI actions — escalate
			log.Info("No runbook or AI actions found, escalating", "issue", issue.Name)
			issue.Status.State = platformv1alpha1.IssueStateEscalated
			meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
				Type:               "Escalated",
				Status:             metav1.ConditionTrue,
				Reason:             "NoRunbookOrAIActions",
				Message:            "No matching runbook and no AI-suggested actions for automatic remediation",
				LastTransitionTime: metav1.Now(),
			})
			return ctrl.Result{}, r.Status().Update(ctx, issue)
		}
	} else {
		// Create RemediationPlan from Runbook
		if err := r.createRemediationPlan(ctx, issue, runbook, &insight, 1); err != nil {
			return ctrl.Result{}, err
		}
		if runbook.Spec.MaxAttempts > 0 {
			issue.Status.MaxRemediationAttempts = runbook.Spec.MaxAttempts
		}
	}

	// Transition to Remediating
	issue.Status.State = platformv1alpha1.IssueStateRemediating
	issue.Status.RemediationAttempts = 1
	meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
		Type:               "Remediating",
		Status:             metav1.ConditionTrue,
		Reason:             "RemediationPlanCreated",
		Message:            fmt.Sprintf("Remediation attempt 1/%d started", issue.Status.MaxRemediationAttempts),
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

		return ctrl.Result{}, r.Status().Update(ctx, issue)

	case platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
		// Check if we can retry
		if issue.Status.RemediationAttempts < issue.Status.MaxRemediationAttempts {
			nextAttempt := issue.Status.RemediationAttempts + 1
			log.Info("Remediation failed, retrying", "attempt", nextAttempt, "max", issue.Status.MaxRemediationAttempts)

			// Get existing insight
			var insight platformv1alpha1.AIInsight
			insightName := issue.Name + "-insight"
			if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err != nil {
				return ctrl.Result{}, err
			}

			// Find matching runbook for next attempt
			runbook, err := r.findMatchingRunbook(ctx, issue)
			if err != nil {
				return ctrl.Result{}, err
			}

			if runbook != nil {
				if err := r.createRemediationPlan(ctx, issue, runbook, &insight, nextAttempt); err != nil {
					return ctrl.Result{}, err
				}
			} else if len(insight.Status.SuggestedActions) > 0 {
				// Fallback to AI-suggested actions for retry
				if err := r.createRemediationPlanFromAI(ctx, issue, &insight, nextAttempt); err != nil {
					return ctrl.Result{}, err
				}
			} else {
				issue.Status.State = platformv1alpha1.IssueStateEscalated
				meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
					Type:               "Escalated",
					Status:             metav1.ConditionTrue,
					Reason:             "NoRunbookOrAIActionsForRetry",
					Message:            "No runbook or AI actions found for retry attempt",
					LastTransitionTime: metav1.Now(),
				})
				return ctrl.Result{}, r.Status().Update(ctx, issue)
			}

			issue.Status.RemediationAttempts = nextAttempt
			meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
				Type:               "Remediating",
				Status:             metav1.ConditionTrue,
				Reason:             "RetryingRemediation",
				Message:            fmt.Sprintf("Remediation attempt %d/%d", nextAttempt, issue.Status.MaxRemediationAttempts),
				LastTransitionTime: metav1.Now(),
			})

			if err := r.Status().Update(ctx, issue); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
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

		return ctrl.Result{}, r.Status().Update(ctx, issue)

	default:
		// Still executing, requeue
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

// findMatchingRunbook finds a Runbook matching the issue's signal type, severity, and resource kind.
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

	for i := range candidates {
		rb := &candidates[i]
		if rb.Spec.Trigger.Severity == issue.Spec.Severity &&
			rb.Spec.Trigger.ResourceKind == issue.Spec.Resource.Kind {
			return rb, nil
		}
	}

	return nil, nil
}

// createRemediationPlan creates a RemediationPlan from a Runbook.
func (r *IssueReconciler) createRemediationPlan(ctx context.Context, issue *platformv1alpha1.Issue, runbook *platformv1alpha1.Runbook, insight *platformv1alpha1.AIInsight, attempt int32) error {
	planName := fmt.Sprintf("%s-plan-%d", issue.Name, attempt)

	// Build actions from runbook steps
	var actions []platformv1alpha1.RemediationAction
	for _, step := range runbook.Spec.Steps {
		actionType := platformv1alpha1.ActionCustom
		switch step.Action {
		case "ScaleDeployment":
			actionType = platformv1alpha1.ActionScaleDeployment
		case "RollbackDeployment":
			actionType = platformv1alpha1.ActionRollbackDeployment
		case "RestartDeployment":
			actionType = platformv1alpha1.ActionRestartDeployment
		case "PatchConfig":
			actionType = platformv1alpha1.ActionPatchConfig
		}
		actions = append(actions, platformv1alpha1.RemediationAction{
			Type:   actionType,
			Params: step.Params,
		})
	}

	// Build strategy from AI insight
	strategy := fmt.Sprintf("Attempt %d: %s", attempt, runbook.Spec.Steps[0].Name)
	if insight.Status.Analysis != "" {
		strategy = fmt.Sprintf("Attempt %d based on AI analysis: %s", attempt, insight.Status.Recommendations)
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

// createRemediationPlanFromAI creates a RemediationPlan from AI-suggested actions (when no Runbook exists).
func (r *IssueReconciler) createRemediationPlanFromAI(ctx context.Context, issue *platformv1alpha1.Issue, insight *platformv1alpha1.AIInsight, attempt int32) error {
	planName := fmt.Sprintf("%s-plan-%d", issue.Name, attempt)

	var actions []platformv1alpha1.RemediationAction
	for _, sa := range insight.Status.SuggestedActions {
		actionType := mapActionType(sa.Action)
		actions = append(actions, platformv1alpha1.RemediationAction{
			Type:   actionType,
			Params: sa.Params,
		})
	}

	strategy := fmt.Sprintf("Attempt %d (AI-generated): %s", attempt, insight.Status.Analysis)
	if len(strategy) > 256 {
		strategy = strategy[:253] + "..."
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

// SetupWithManager sets up the controller with the Manager.
func (r *IssueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.Issue{}).
		Owns(&platformv1alpha1.RemediationPlan{}).
		Owns(&platformv1alpha1.AIInsight{}).
		Complete(r)
}
