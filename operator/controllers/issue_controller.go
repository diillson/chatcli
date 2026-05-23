package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
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

// DedupInvalidator allows invalidating dedup entries when issues reach terminal states.
type DedupInvalidator interface {
	InvalidateDedupForResource(deployment, namespace string)
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
	case platformv1alpha1.IssueStateContained:
		return r.handleContained(ctx, &issue)
	case platformv1alpha1.IssueStateEscalated:
		return r.handleEscalated(ctx, &issue)
	case platformv1alpha1.IssueStateResolved, platformv1alpha1.IssueStateFailed:
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
		// Read from Instance AIOps config, fallback to default (5)
		issue.Status.MaxRemediationAttempts = r.getMaxRemediationAttempts(ctx)
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

	// Early runbook matching — inject ALL candidates into AIInsight so the AI can choose
	candidateRunbooks := r.findAllMatchingRunbooks(ctx, issue)
	var runbookContext string
	var candidateNames []string
	if len(candidateRunbooks) > 0 {
		var sections []string
		for _, rb := range candidateRunbooks {
			candidateNames = append(candidateNames, rb.Name)
			var stepsDesc []string
			for i, s := range rb.Spec.Steps {
				stepsDesc = append(stepsDesc, fmt.Sprintf("  Step %d: %s (%s) — %s", i+1, s.Name, s.Action, s.Description))
			}
			// Truncate description to avoid bloating the context
			desc := rb.Spec.Description
			if len(desc) > 200 {
				desc = desc[:200] + "..."
			}
			sections = append(sections, fmt.Sprintf(
				"RUNBOOK: %s\nDescription: %s\nTrigger: %s + %s + %s\nSteps:\n%s",
				rb.Name, desc,
				rb.Spec.Trigger.SignalType, rb.Spec.Trigger.Severity, rb.Spec.Trigger.ResourceKind,
				strings.Join(stepsDesc, "\n"),
			))
		}
		runbookContext = fmt.Sprintf(
			"CANDIDATE RUNBOOKS (%d found):\n\n%s\n\n"+
				"Evaluate each runbook against the current incident's root cause. "+
				"If one of the runbooks matches, include 'RUNBOOK_APPROVED: <runbook-name>' in your analysis. "+
				"If NONE of the runbooks match the current root cause, include 'RUNBOOK_REJECTED' and explain why. "+
				"Only approve a runbook if its steps directly address the root cause you identified.",
			len(candidateRunbooks),
			strings.Join(sections, "\n\n---\n\n"),
		)
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, insight, func() error {
		if err := controllerutil.SetControllerReference(issue, insight, r.Scheme); err != nil {
			return err
		}
		insight.Spec = platformv1alpha1.AIInsightSpec{
			IssueRef: platformv1alpha1.IssueRef{Name: issue.Name},
			Provider: provider,
			Model:    model,
		}
		// Inject runbook candidates for AI validation
		if runbookContext != "" {
			if insight.Annotations == nil {
				insight.Annotations = make(map[string]string)
			}
			insight.Annotations["platform.chatcli.io/candidate-runbooks"] = strings.Join(candidateNames, ",")
			insight.Annotations["platform.chatcli.io/runbook-context"] = runbookContext
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

	// 1. Find matching runbooks and check if AI validated one
	var runbook *platformv1alpha1.Runbook
	candidates := r.findAllMatchingRunbooks(ctx, issue)

	if len(candidates) > 0 {
		aiRejected := strings.Contains(insight.Status.Analysis, "RUNBOOK_REJECTED")

		if aiRejected {
			log.Info("AI rejected all candidate runbooks, using alternative strategy",
				"issue", issue.Name, "candidates", len(candidates))
			// runbook stays nil — fallthrough to AI suggestions or agentic
		} else {
			// Check if AI approved a specific runbook by name: "RUNBOOK_APPROVED: <name>"
			var approvedName string
			for _, line := range strings.Split(insight.Status.Analysis, "\n") {
				if idx := strings.Index(line, "RUNBOOK_APPROVED:"); idx >= 0 {
					approvedName = strings.TrimSpace(line[idx+len("RUNBOOK_APPROVED:"):])
					break
				}
				if strings.Contains(line, "RUNBOOK_APPROVED") {
					// Simple approval without name — use first candidate
					approvedName = candidates[0].Name
					break
				}
			}

			if approvedName != "" {
				// Find the approved runbook by name
				for _, rb := range candidates {
					if rb.Name == approvedName {
						runbook = rb
						break
					}
				}
				if runbook == nil {
					// Name didn't match exactly — use first candidate
					runbook = candidates[0]
				}
				log.Info("AI approved runbook", "issue", issue.Name, "runbook", runbook.Name)
			} else {
				// AI didn't explicitly approve or reject — use first candidate (backward compat)
				runbook = candidates[0]
				log.Info("AI did not explicitly validate runbook, using first candidate",
					"issue", issue.Name, "runbook", runbook.Name)
			}

			if runbook != nil && runbook.Spec.MaxAttempts > 0 {
				issue.Status.MaxRemediationAttempts = runbook.Spec.MaxAttempts
			}
		}
	}

	if runbook == nil && len(insight.Status.SuggestedActions) > 0 {
		// 2. No valid runbook — generate one from AI suggestions
		log.Info("No valid runbook, generating from AI", "issue", issue.Name, "actions", len(insight.Status.SuggestedActions))
		var err error
		runbook, err = r.generateRunbookFromAI(ctx, issue, &insight)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("generating runbook from AI: %w", err)
		}
		log.Info("Auto-generated runbook", "runbook", runbook.Name)
	} else if runbook == nil {
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
		// GAP-03 fix: distinguish containment from true resolution. When the plan
		// executed a containment action (e.g., ScaleDeployment replicas=0 with
		// containment=true), the workload is silenced but NOT fixed — a human must
		// roll back the bad image, restore replicas, or apply a config fix to
		// restore service. Marking it as Resolved would mask real downtime.
		contained, requiredAction := planAppliedContainment(plan)

		now := metav1.Now()
		if contained {
			issue.Status.State = platformv1alpha1.IssueStateContained
			issue.Status.Resolution = fmt.Sprintf("CONTAINED — service stopped to prevent further impact. Human action required: %s", requiredAction)
			meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
				Type:               "Contained",
				Status:             metav1.ConditionTrue,
				Reason:             "ContainmentApplied",
				Message:            fmt.Sprintf("Workload silenced via containment on attempt %d. Human action required to restore service: %s", issue.Status.RemediationAttempts, requiredAction),
				LastTransitionTime: metav1.Now(),
			})
			meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
				Type:               "RequiresHumanAction",
				Status:             metav1.ConditionTrue,
				Reason:             "ServiceSilencedNotFixed",
				Message:            requiredAction,
				LastTransitionTime: metav1.Now(),
			})
			issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateContained)).Inc()
			log.Info("Issue transitioned to Contained (requires human action)",
				"issue", issue.Name, "required_action", requiredAction)
			if r.AuditRecorder != nil {
				if err := r.AuditRecorder.RecordIssueContained(ctx, issue, requiredAction); err != nil {
					log.Error(err, "Failed to record audit event for contained issue")
				}
			}
		} else {
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
			// GAP-04 fix: exclude chaos-induced Issues from production MTTD/MTTR
			// metrics. They still get observability via issuesTotal (labeled by
			// state), but we don't let drill data pollute the latency histogram
			// that backs SLO dashboards.
			if issue.Status.DetectedAt != nil && !IsChaosInduced(issue) {
				issueResolutionDuration.Observe(now.Sub(issue.Status.DetectedAt.Time).Seconds())
			}
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
		r.invalidateDedup(issue)

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
		r.invalidateDedup(issue)

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
	candidates := r.findAllMatchingRunbooks(ctx, issue)
	if len(candidates) > 0 {
		return candidates[0], nil
	}
	return nil, nil
}

// findAllMatchingRunbooks returns ALL runbooks matching the issue's trigger criteria,
// sorted by tier (Tier 1 first, then Tier 2). Multiple runbooks for the same signal
// can exist when different root causes have been learned.
func (r *IssueReconciler) findAllMatchingRunbooks(ctx context.Context, issue *platformv1alpha1.Issue) []*platformv1alpha1.Runbook {
	var runbooks platformv1alpha1.RunbookList
	if err := r.List(ctx, &runbooks, client.InNamespace(issue.Namespace)); err != nil {
		return nil
	}

	var allRunbooks platformv1alpha1.RunbookList
	if err := r.List(ctx, &allRunbooks); err != nil {
		return nil
	}

	all := append(runbooks.Items, allRunbooks.Items...)

	signalType := issue.Spec.SignalType
	if signalType == "" && issue.Labels != nil {
		signalType = issue.Labels["platform.chatcli.io/signal"]
	}

	var tier1, tier2 []*platformv1alpha1.Runbook
	for i := range all {
		rb := &all[i]
		if rb.Spec.Trigger.Severity != issue.Spec.Severity ||
			rb.Spec.Trigger.ResourceKind != issue.Spec.Resource.Kind {
			continue
		}
		if signalType != "" && string(rb.Spec.Trigger.SignalType) == signalType {
			tier1 = append(tier1, rb)
		} else {
			tier2 = append(tier2, rb)
		}
	}

	return append(tier1, tier2...)
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

	// Build name with hash of root cause analysis — different causes produce different runbooks
	// e.g.: auto-oom-kill-critical-deployment-a3f2b1
	analysisHash := fmt.Sprintf("%x", sha256.Sum256([]byte(insight.Status.Analysis)))[:6]
	rbName := sanitizeRunbookName(fmt.Sprintf("auto-%s-%s-%s-%s",
		signalType, issue.Spec.Severity, strings.ToLower(issue.Spec.Resource.Kind), analysisHash))

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
			MaxAttempts: r.getMaxRemediationAttempts(ctx),
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
// actionTypeByName maps the string name the AI uses for an action (sent over
// the gRPC SuggestedAction.action field) to the corresponding RemediationActionType
// enum value. Lookup-by-map keeps mapActionType at constant cyclomatic complexity
// regardless of how many action kinds the platform supports.
var actionTypeByName = map[string]platformv1alpha1.RemediationActionType{
	// Core workload actions
	"ScaleDeployment":       platformv1alpha1.ActionScaleDeployment,
	"RollbackDeployment":    platformv1alpha1.ActionRollbackDeployment,
	"RestartDeployment":     platformv1alpha1.ActionRestartDeployment,
	"PatchConfig":           platformv1alpha1.ActionPatchConfig,
	"AdjustResources":       platformv1alpha1.ActionAdjustResources,
	"DeletePod":             platformv1alpha1.ActionDeletePod,
	"HelmRollback":          platformv1alpha1.ActionHelmRollback,
	"ArgoSyncApp":           platformv1alpha1.ActionArgoSyncApp,
	"AdjustHPA":             platformv1alpha1.ActionAdjustHPA,
	"RestartStatefulSetPod": platformv1alpha1.ActionRestartStatefulSetPod,
	"CordonNode":            platformv1alpha1.ActionCordonNode,
	"UncordonNode":          platformv1alpha1.ActionUncordonNode,
	"DrainNode":             platformv1alpha1.ActionDrainNode,
	"ResizePVC":             platformv1alpha1.ActionResizePVC,
	"RotateSecret":          platformv1alpha1.ActionRotateSecret,
	"ExecDiagnostic":        platformv1alpha1.ActionExecDiagnostic,
	"UpdateIngress":         platformv1alpha1.ActionUpdateIngress,
	"PatchNetworkPolicy":    platformv1alpha1.ActionPatchNetworkPolicy,
	"ApplyManifest":         platformv1alpha1.ActionApplyManifest,

	// StatefulSet
	"ScaleStatefulSet":           platformv1alpha1.ActionScaleStatefulSet,
	"RestartStatefulSet":         platformv1alpha1.ActionRestartStatefulSet,
	"RollbackStatefulSet":        platformv1alpha1.ActionRollbackStatefulSet,
	"AdjustStatefulSetResources": platformv1alpha1.ActionAdjustStatefulSetResources,
	"DeleteStatefulSetPod":       platformv1alpha1.ActionDeleteStatefulSetPod,
	"ForceDeleteStatefulSetPod":  platformv1alpha1.ActionForceDeleteStatefulSetPod,
	"UpdateStatefulSetStrategy":  platformv1alpha1.ActionUpdateStatefulSetStrategy,
	"RecreateStatefulSetPVC":     platformv1alpha1.ActionRecreateStatefulSetPVC,
	"PartitionStatefulSetUpdate": platformv1alpha1.ActionPartitionStatefulSetUpdate,

	// DaemonSet
	"RestartDaemonSet":            platformv1alpha1.ActionRestartDaemonSet,
	"RollbackDaemonSet":           platformv1alpha1.ActionRollbackDaemonSet,
	"AdjustDaemonSetResources":    platformv1alpha1.ActionAdjustDaemonSetResources,
	"DeleteDaemonSetPod":          platformv1alpha1.ActionDeleteDaemonSetPod,
	"UpdateDaemonSetStrategy":     platformv1alpha1.ActionUpdateDaemonSetStrategy,
	"PauseDaemonSetRollout":       platformv1alpha1.ActionPauseDaemonSetRollout,
	"CordonAndDeleteDaemonSetPod": platformv1alpha1.ActionCordonAndDeleteDaemonSetPod,

	// Job
	"RetryJob":              platformv1alpha1.ActionRetryJob,
	"AdjustJobResources":    platformv1alpha1.ActionAdjustJobResources,
	"DeleteFailedJob":       platformv1alpha1.ActionDeleteFailedJob,
	"SuspendJob":            platformv1alpha1.ActionSuspendJob,
	"ResumeJob":             platformv1alpha1.ActionResumeJob,
	"AdjustJobParallelism":  platformv1alpha1.ActionAdjustJobParallelism,
	"AdjustJobDeadline":     platformv1alpha1.ActionAdjustJobDeadline,
	"AdjustJobBackoffLimit": platformv1alpha1.ActionAdjustJobBackoffLimit,
	"ForceDeleteJobPods":    platformv1alpha1.ActionForceDeleteJobPods,

	// CronJob
	"SuspendCronJob":           platformv1alpha1.ActionSuspendCronJob,
	"ResumeCronJob":            platformv1alpha1.ActionResumeCronJob,
	"TriggerCronJob":           platformv1alpha1.ActionTriggerCronJob,
	"AdjustCronJobResources":   platformv1alpha1.ActionAdjustCronJobResources,
	"AdjustCronJobSchedule":    platformv1alpha1.ActionAdjustCronJobSchedule,
	"AdjustCronJobDeadline":    platformv1alpha1.ActionAdjustCronJobDeadline,
	"AdjustCronJobHistory":     platformv1alpha1.ActionAdjustCronJobHistory,
	"AdjustCronJobConcurrency": platformv1alpha1.ActionAdjustCronJobConcurrency,
	"DeleteCronJobActiveJobs":  platformv1alpha1.ActionDeleteCronJobActiveJobs,
	"ReplaceCronJobTemplate":   platformv1alpha1.ActionReplaceCronJobTemplate,
}

// mapActionType resolves the AI-produced action name to the typed enum. Unknown
// names map to ActionCustom, which downstream executors treat as "no-op /
// requires manual intervention".
func mapActionType(action string) platformv1alpha1.RemediationActionType {
	if t, ok := actionTypeByName[action]; ok {
		return t
	}
	return platformv1alpha1.ActionCustom
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

// handleContained reconciles Issues that the operator silenced via a containment
// action (typically ScaleDeployment replicas=0 with containment=true). The state
// auto-resolves IF and ONLY IF a human has restored the workload to a healthy
// state (replicas > 0 AND all replicas ready). Mere "0 desired = 0 ready" does
// NOT count as healthy here — that would let the platform declare victory
// because no one fixed anything.
//
// GAP-03 fix (chaos test report 2026-05-23).
func (r *IssueReconciler) handleContained(ctx context.Context, issue *platformv1alpha1.Issue) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	aiops := r.getAIOpsConfig(ctx)
	if !aiops.IsAutoResolveEnabled() {
		log.Info("Auto-resolve disabled, issue remains Contained", "name", issue.Name)
		return ctrl.Result{}, nil
	}

	resource := issue.Spec.Resource
	restored, err := r.isResourceRestored(ctx, resource)
	if err != nil {
		log.Error(err, "Failed to check resource restoration for Contained issue", "name", issue.Name)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}
	if !restored {
		// Still silenced — keep checking. Slow cadence: a human needs to act
		// here and we don't want to hammer the API server while waiting.
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	log.Info("Human action observed (replicas restored healthy) — transitioning Contained → Resolved",
		"name", issue.Name, "resource", resource.Name)
	now := metav1.Now()
	issue.Status.State = platformv1alpha1.IssueStateResolved
	issue.Status.Resolution = "Auto-resolved: human restored the workload to a healthy state"
	issue.Status.ResolvedAt = &now
	meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
		Type:               string(platformv1alpha1.IssueStateResolved),
		Status:             metav1.ConditionTrue,
		Reason:             "HumanActionRestoredService",
		Message:            "Resource scaled back up and reports all replicas ready.",
		LastTransitionTime: now,
	})
	if err := r.Status().Update(ctx, issue); err != nil {
		return ctrl.Result{}, err
	}
	if issue.Annotations == nil {
		issue.Annotations = make(map[string]string)
	}
	issue.Annotations["aiops.chatcli.io/resolved-by"] = "human-action-after-containment"
	if err := r.Update(ctx, issue); err != nil {
		log.Error(err, "Failed to update resolved-by annotation", "name", issue.Name)
	}
	r.invalidateDedup(issue)
	if r.AuditRecorder != nil {
		if recErr := r.AuditRecorder.RecordIssueResolved(ctx, issue); recErr != nil {
			log.Error(recErr, "Failed to record audit event for resolved issue after containment")
		}
	}
	issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateResolved)).Inc()
	return ctrl.Result{}, nil
}

// isResourceRestored is a stricter form of isResourceHealthy: it only returns
// true when desired replicas > 0 AND all replicas are ready AND none are
// unavailable. The "desired > 0" gate is essential for the Contained state
// transition — a workload with replicas=0 is trivially "healthy" (0 ready of 0
// desired) but is still silenced and unresolved.
func (r *IssueReconciler) isResourceRestored(ctx context.Context, resource platformv1alpha1.ResourceRef) (bool, error) {
	switch resource.Kind {
	case "Deployment":
		var deploy appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
			return false, err
		}
		desired := int32(0)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		if desired == 0 {
			return false, nil
		}
		return deploy.Status.ReadyReplicas >= desired && deploy.Status.UnavailableReplicas == 0, nil
	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
			return false, err
		}
		desired := int32(0)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if desired == 0 {
			return false, nil
		}
		return sts.Status.ReadyReplicas >= desired, nil
	default:
		// Fall back to the looser check for resource kinds that don't have a
		// meaningful "desired replicas" semantic (DaemonSet, Job, etc.).
		return r.isResourceHealthy(ctx, resource)
	}
}

// applyPostMortemContextLabels marks the PostMortem with chaos-correlation
// labels (GAP-04) and the containment human-action fields (GAP-03) based on
// the parent Issue and the executed remediation plan. Extracted from
// generatePostMortem to keep that function's cyclomatic complexity bounded.
func applyPostMortemContextLabels(pm *platformv1alpha1.PostMortem, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) {
	// GAP-04 fix: label PostMortems for chaos-induced Issues so dashboards and
	// exports can filter them out of "real production incidents". The PostMortem
	// still carries the timeline and actions — it's just clearly tagged so it
	// doesn't pollute trend analysis.
	if IsChaosInduced(issue) {
		pm.Labels[LabelSource] = SourceChaosExperiment
		if expName := issue.Labels["platform.chatcli.io/chaos-experiment"]; expName != "" {
			pm.Labels["platform.chatcli.io/chaos-experiment"] = expName
		}
	}
	// GAP-03 fix: when the parent issue is in Contained state, the workload is
	// silenced but the underlying bug is unresolved. Marking the PostMortem
	// accordingly prevents it from being prematurely closed and surfaces the
	// concrete follow-up in tooling and notifications.
	if issue.Status.State == platformv1alpha1.IssueStateContained {
		pm.Spec.RequiresHumanAction = true
		if _, action := planAppliedContainment(plan); action != "" {
			pm.Spec.RequiredAction = action
		}
	}
}

// planAppliedContainment reports whether the remediation plan executed an action
// with params["containment"]="true". Used to distinguish a "stop the bleeding"
// outcome from a true fix. Inspects both runbook-style Actions and AgenticHistory.
// Returns a short human-readable description of the required follow-up action.
func planAppliedContainment(plan *platformv1alpha1.RemediationPlan) (bool, string) {
	describe := func(a platformv1alpha1.RemediationAction) string {
		switch a.Type {
		case platformv1alpha1.ActionScaleDeployment:
			return "restore the deployment's replicas to the desired count after fixing the root cause (image rollback, config correction, etc.)"
		case platformv1alpha1.ActionScaleStatefulSet:
			return "restore the StatefulSet's replicas after fixing the root cause"
		default:
			return fmt.Sprintf("review the action %q and restore service manually", string(a.Type))
		}
	}

	for _, a := range plan.Spec.Actions {
		if a.Params["containment"] == "true" {
			return true, describe(a)
		}
	}
	for _, step := range plan.Spec.AgenticHistory {
		if step.Action == nil {
			continue
		}
		// Only count steps that actually succeeded — failed containment is not
		// containment, the workload is still serving traffic.
		if !strings.HasPrefix(step.Observation, "SUCCESS") {
			continue
		}
		if step.Action.Params["containment"] == "true" {
			return true, describe(*step.Action)
		}
	}
	return false, ""
}

// handleEscalated checks if an escalated issue's resource has recovered and auto-resolves it.
func (r *IssueReconciler) handleEscalated(ctx context.Context, issue *platformv1alpha1.Issue) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	aiops := r.getAIOpsConfig(ctx)
	if !aiops.IsAutoResolveEnabled() {
		log.Info("Auto-resolve disabled, issue remains Escalated", "name", issue.Name)
		return ctrl.Result{}, nil
	}

	resource := issue.Spec.Resource
	healthy, err := r.isResourceHealthy(ctx, resource)
	if err != nil {
		log.Error(err, "Failed to check resource health for auto-resolve", "name", issue.Name)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if !healthy {
		// Resource still unhealthy — recheck in 30 seconds
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Resource is healthy — auto-resolve the escalated issue
	log.Info("Auto-resolving Escalated issue: resource recovered", "name", issue.Name, "resource", resource.Name)
	now := metav1.Now()
	issue.Status.State = platformv1alpha1.IssueStateResolved
	issue.Status.Resolution = "Auto-resolved: resource recovered while awaiting human intervention"
	issue.Status.ResolvedAt = &now
	meta.SetStatusCondition(&issue.Status.Conditions, metav1.Condition{
		Type:               string(platformv1alpha1.IssueStateResolved),
		Status:             metav1.ConditionTrue,
		Reason:             "AutoResolved",
		Message:            "Resource recovered — all replicas healthy. Issue auto-resolved.",
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, issue); err != nil {
		return ctrl.Result{}, fmt.Errorf("auto-resolving escalated issue: %w", err)
	}

	// Add annotations to distinguish auto-resolve from manual resolve in audit trail
	if issue.Annotations == nil {
		issue.Annotations = make(map[string]string)
	}
	issue.Annotations["aiops.chatcli.io/resolved-by"] = "auto-resolve"
	issue.Annotations["aiops.chatcli.io/resolved-at"] = now.Format("2006-01-02T15:04:05Z")
	issue.Annotations["aiops.chatcli.io/auto-resolution"] = "true"
	if err := r.Update(ctx, issue); err != nil {
		log.Error(err, "Failed to update auto-resolve annotations", "name", issue.Name)
	}

	r.invalidateDedup(issue)

	if r.AuditRecorder != nil {
		r.AuditRecorder.RecordIssueResolved(ctx, issue)
	}

	issuesTotal.WithLabelValues(string(issue.Spec.Severity), string(platformv1alpha1.IssueStateResolved)).Inc()
	return ctrl.Result{}, nil
}

// isResourceHealthy checks if the target resource has all replicas ready.
func (r *IssueReconciler) isResourceHealthy(ctx context.Context, resource platformv1alpha1.ResourceRef) (bool, error) {
	switch resource.Kind {
	case "Deployment":
		var deploy appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
			return false, err
		}
		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		return deploy.Status.ReadyReplicas >= desired && deploy.Status.UnavailableReplicas == 0, nil

	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
			return false, err
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		return sts.Status.ReadyReplicas >= desired, nil

	default:
		// For other kinds, assume not healthy to avoid false auto-resolve
		return false, nil
	}
}

// invalidateDedup removes dedup entries for the issue's resource, allowing new anomalies to be detected.
func (r *IssueReconciler) invalidateDedup(issue *platformv1alpha1.Issue) {
	if r.DedupInvalidator != nil {
		r.DedupInvalidator.InvalidateDedupForResource(
			issue.Spec.Resource.Name,
			issue.Spec.Resource.Namespace,
		)
	}
}

// getMaxRemediationAttempts reads the max attempts from the first Instance's AIOps config.
func (r *IssueReconciler) getMaxRemediationAttempts(ctx context.Context) int32 {
	var instances platformv1alpha1.InstanceList
	if err := r.List(ctx, &instances); err == nil && len(instances.Items) > 0 {
		return instances.Items[0].Spec.AIOps.GetMaxRemediationAttempts()
	}
	return 5 // default
}

// getAgenticMaxSteps reads the agentic max steps from the first Instance's AIOps config.
func (r *IssueReconciler) getAgenticMaxSteps(ctx context.Context) int32 {
	cfg := r.getAIOpsConfig(ctx)
	if cfg != nil {
		return cfg.GetAgenticMaxSteps()
	}
	return 10 // default
}

// getAIOpsConfig reads the AIOps config from the first Instance.
func (r *IssueReconciler) getAIOpsConfig(ctx context.Context) *platformv1alpha1.AIOpsSpec {
	var instances platformv1alpha1.InstanceList
	if err := r.List(ctx, &instances); err == nil && len(instances.Items) > 0 {
		return instances.Items[0].Spec.AIOps
	}
	return nil
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
			AgenticMaxSteps: r.getAgenticMaxSteps(ctx),
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
// generatePostMortem creates (or updates) the PostMortem CR for a successfully
// resolved or contained incident. The function orchestrates a pipeline of
// focused stages — each stage is a pure helper or a single-responsibility
// method — so adding or changing one section (timeline, narrative, enrichment)
// stays local to that stage.
func (r *IssueReconciler) generatePostMortem(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) error {
	now := metav1.Now()
	pmName := postMortemName(issue)
	timeline := buildPostMortemTimeline(issue, plan, now)
	actions := buildPostMortemActions(plan, now)
	narrative := r.readPostMortemNarrative(ctx, issue, plan)
	duration := postMortemDuration(issue, now)

	pm, err := r.createOrUpdatePostMortem(ctx, issue, plan, pmName)
	if err != nil {
		return err
	}

	apply := func(pm *platformv1alpha1.PostMortem) {
		applyPostMortemStatusFields(pm, narrative, timeline, actions, duration, &now)
		pm.Status.Trending = r.buildTrendingInfo(ctx, issue)
	}
	apply(pm)
	r.enrichPostMortemContext(ctx, pm, issue)

	return r.persistPostMortemStatus(ctx, pm, apply)
}

// postMortemName builds the CR name, truncated to the RFC 1123 subdomain limit.
func postMortemName(issue *platformv1alpha1.Issue) string {
	name := "pm-" + issue.Name
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

// postMortemDuration returns the human-readable duration between detection and
// resolution. Empty when DetectedAt was never set (defensive: should always be
// populated by handleDetected, but the PostMortem path is reachable from edge
// cases like manual resolution).
func postMortemDuration(issue *platformv1alpha1.Issue, now metav1.Time) string {
	if issue.Status.DetectedAt == nil {
		return ""
	}
	return now.Sub(issue.Status.DetectedAt.Time).Round(time.Second).String()
}

// buildPostMortemTimeline assembles the chronological event list. Picks
// agentic-history events when the plan ran in agentic mode, plan.Spec.Actions
// otherwise. A "detected" event opens the timeline and a "resolved" event
// closes it so the reader can read the full incident in one pass.
func buildPostMortemTimeline(issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan, now metav1.Time) []platformv1alpha1.TimelineEvent {
	var timeline []platformv1alpha1.TimelineEvent
	if issue.Status.DetectedAt != nil {
		timeline = append(timeline, platformv1alpha1.TimelineEvent{
			Timestamp: *issue.Status.DetectedAt,
			Type:      "detected",
			Detail:    issue.Spec.Description,
		})
	}

	if len(plan.Spec.AgenticHistory) > 0 {
		for _, step := range plan.Spec.AgenticHistory {
			timeline = append(timeline, agenticHistoryToTimelineEvent(step))
		}
	} else {
		for i, action := range plan.Spec.Actions {
			timeline = append(timeline, planActionToTimelineEvent(action, i, plan, now))
		}
	}

	timeline = append(timeline, platformv1alpha1.TimelineEvent{
		Timestamp: now,
		Type:      "resolved",
		Detail:    plan.Status.Result,
	})
	return timeline
}

// agenticHistoryToTimelineEvent renders a single agentic step into the
// timeline. Embeds the AI reasoning when present so the rendered PostMortem
// reads like a transcript of the loop's decisions.
func agenticHistoryToTimelineEvent(step platformv1alpha1.AgenticStep) platformv1alpha1.TimelineEvent {
	evType := "action_executed"
	if strings.HasPrefix(step.Observation, "FAILED:") {
		evType = "action_failed"
	}
	actionStr := "(observation)"
	if step.Action != nil {
		actionStr = string(step.Action.Type)
	}
	detail := fmt.Sprintf("Step %d: %s — %s", step.StepNumber, actionStr, step.Observation)
	if step.AIMessage != "" {
		detail = fmt.Sprintf("Step %d [AI reasoning: %s] Action: %s — %s", step.StepNumber, step.AIMessage, actionStr, step.Observation)
	}
	return platformv1alpha1.TimelineEvent{
		Timestamp: step.Timestamp,
		Type:      evType,
		Detail:    detail,
	}
}

// planActionToTimelineEvent renders a runbook-style action into the timeline,
// consulting plan.Status.ActionCheckpoints to determine success/failure.
func planActionToTimelineEvent(action platformv1alpha1.RemediationAction, idx int, plan *platformv1alpha1.RemediationPlan, now metav1.Time) platformv1alpha1.TimelineEvent {
	_, detail := planActionOutcome(action, idx, plan)
	ts := planActionTimestamp(plan, now)
	return platformv1alpha1.TimelineEvent{
		Timestamp: ts,
		Type:      "action_executed",
		Detail:    fmt.Sprintf("%s: %s", action.Type, detail),
	}
}

// planActionTimestamp returns the timestamp to attribute to a plan action.
// Falls back to "now" when StartedAt was never set.
func planActionTimestamp(plan *platformv1alpha1.RemediationPlan, now metav1.Time) metav1.Time {
	if plan.Status.StartedAt != nil {
		return *plan.Status.StartedAt
	}
	return now
}

// planActionOutcome returns (result, detail) for a plan action by index.
func planActionOutcome(action platformv1alpha1.RemediationAction, idx int, plan *platformv1alpha1.RemediationPlan) (string, string) {
	for _, cp := range plan.Status.ActionCheckpoints {
		if cp.ActionIndex == int32(idx) && !cp.Success {
			return "failed", fmt.Sprintf("Action %s failed", action.Type)
		}
	}
	return "success", fmt.Sprintf("Action %s executed", action.Type)
}

// buildPostMortemActions returns the ActionRecord list for the PostMortem.
// Mirrors the timeline source: agentic history or runbook actions, never both.
func buildPostMortemActions(plan *platformv1alpha1.RemediationPlan, now metav1.Time) []platformv1alpha1.ActionRecord {
	if len(plan.Spec.AgenticHistory) > 0 {
		return agenticHistoryToActionRecords(plan.Spec.AgenticHistory)
	}
	return planActionsToActionRecords(plan, now)
}

// agenticHistoryToActionRecords extracts action records from agentic history,
// skipping observation-only steps (Action == nil).
func agenticHistoryToActionRecords(history []platformv1alpha1.AgenticStep) []platformv1alpha1.ActionRecord {
	records := make([]platformv1alpha1.ActionRecord, 0, len(history))
	for _, step := range history {
		if step.Action == nil {
			continue
		}
		result := "success"
		if strings.HasPrefix(step.Observation, "FAILED:") {
			result = "failed"
		}
		detail := step.Observation
		if step.AIMessage != "" {
			detail = fmt.Sprintf("[AI: %s] %s", step.AIMessage, step.Observation)
		}
		records = append(records, platformv1alpha1.ActionRecord{
			Action:    string(step.Action.Type),
			Params:    step.Action.Params,
			Result:    result,
			Detail:    detail,
			Timestamp: step.Timestamp,
		})
	}
	return records
}

// planActionsToActionRecords extracts action records from a runbook-style plan.
func planActionsToActionRecords(plan *platformv1alpha1.RemediationPlan, now metav1.Time) []platformv1alpha1.ActionRecord {
	records := make([]platformv1alpha1.ActionRecord, 0, len(plan.Spec.Actions))
	ts := planActionTimestamp(plan, now)
	for i, action := range plan.Spec.Actions {
		result, detail := planActionOutcome(action, i, plan)
		records = append(records, platformv1alpha1.ActionRecord{
			Action:    string(action.Type),
			Params:    action.Params,
			Result:    result,
			Detail:    detail,
			Timestamp: ts,
		})
	}
	return records
}

// postMortemNarrative carries the AI-generated prose fields. Pulled out as a
// struct so the PostMortem builder can pass it through enrichment stages
// without growing a long parameter list.
type postMortemNarrative struct {
	Summary           string
	RootCause         string
	Impact            string
	LessonsLearned    []string
	PreventionActions []string
}

// readPostMortemNarrative composes the AI-generated prose, preferring agentic
// plan annotations (richer — they carry the loop's own postmortem_summary,
// root_cause, impact and lessons) and falling back to the AIInsight analysis
// for runbook-style plans.
func (r *IssueReconciler) readPostMortemNarrative(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) postMortemNarrative {
	n := postMortemNarrative{
		Summary:           plan.Annotations["platform.chatcli.io/postmortem-summary"],
		RootCause:         plan.Annotations["platform.chatcli.io/root-cause"],
		Impact:            plan.Annotations["platform.chatcli.io/impact"],
		LessonsLearned:    splitAnnotation(plan.Annotations["platform.chatcli.io/lessons-learned"]),
		PreventionActions: splitAnnotation(plan.Annotations["platform.chatcli.io/prevention-actions"]),
	}
	if n.Summary != "" && n.RootCause != "" {
		return n
	}
	r.fillNarrativeFromInsight(ctx, &n, issue)
	return n
}

// splitAnnotation splits a multi-value annotation on the standard separator.
func splitAnnotation(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n---\n")
}

// fillNarrativeFromInsight populates empty narrative fields from the AIInsight.
// Best-effort: when the insight CR is missing we simply leave the gaps.
func (r *IssueReconciler) fillNarrativeFromInsight(ctx context.Context, n *postMortemNarrative, issue *platformv1alpha1.Issue) {
	var insight platformv1alpha1.AIInsight
	insightName := issue.Name + "-insight"
	if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err != nil {
		return
	}
	if n.Summary == "" && insight.Status.Analysis != "" {
		n.Summary = insight.Status.Analysis
	}
	if n.RootCause == "" && insight.Status.Analysis != "" {
		n.RootCause = insight.Status.Analysis
	}
	if len(n.LessonsLearned) == 0 && len(insight.Status.Recommendations) > 0 {
		n.LessonsLearned = insight.Status.Recommendations
	}
	if n.Impact == "" {
		n.Impact = fmt.Sprintf("Issue %s on %s/%s (severity: %s, risk: %d)",
			issue.Spec.SignalType, issue.Spec.Resource.Kind, issue.Spec.Resource.Name,
			issue.Spec.Severity, issue.Spec.RiskScore)
	}
}

// createOrUpdatePostMortem performs the upsert of the PostMortem CR and seeds
// its labels and spec. Returns the live CR (with the OwnerReference set) ready
// for status enrichment.
func (r *IssueReconciler) createOrUpdatePostMortem(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan, pmName string) (*platformv1alpha1.PostMortem, error) {
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
		applyPostMortemContextLabels(pm, issue, plan)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pm, nil
}

// applyPostMortemStatusFields writes the core narrative + chronology fields
// onto the PostMortem status. Pure function: idempotent and called both on
// initial apply and on conflict-retry, which is why it lives outside the
// orchestration method.
func applyPostMortemStatusFields(pm *platformv1alpha1.PostMortem, n postMortemNarrative, timeline []platformv1alpha1.TimelineEvent, actions []platformv1alpha1.ActionRecord, duration string, generatedAt *metav1.Time) {
	pm.Status.State = platformv1alpha1.PostMortemStateOpen
	pm.Status.Summary = n.Summary
	pm.Status.RootCause = n.RootCause
	pm.Status.Impact = n.Impact
	pm.Status.LessonsLearned = n.LessonsLearned
	pm.Status.PreventionActions = n.PreventionActions
	pm.Status.Timeline = timeline
	pm.Status.ActionsExecuted = actions
	pm.Status.Duration = duration
	pm.Status.GeneratedAt = generatedAt
}

// enrichPostMortemContext attaches the optional cross-system enrichments
// (cascade chain, GitOps context, Git correlation). Each stage is best-effort
// — a failure in one enrichment never blocks the PostMortem from being saved.
func (r *IssueReconciler) enrichPostMortemContext(ctx context.Context, pm *platformv1alpha1.PostMortem, issue *platformv1alpha1.Issue) {
	r.attachCascadeChain(ctx, pm, issue)
	r.attachGitOpsContext(ctx, pm, issue)
	r.attachGitCorrelation(ctx, pm, issue)
}

// attachCascadeChain runs the cascade analyzer and records the service chain
// when the failure spans two or more services.
func (r *IssueReconciler) attachCascadeChain(ctx context.Context, pm *platformv1alpha1.PostMortem, issue *platformv1alpha1.Issue) {
	cascade, err := NewCascadeAnalyzer(r.Client).AnalyzeCascade(ctx, issue)
	if err != nil || len(cascade.Chain) < 2 {
		return
	}
	chain := make([]string, 0, len(cascade.Chain))
	for _, n := range cascade.Chain {
		chain = append(chain, fmt.Sprintf("%s/%s(%s)", n.Namespace, n.ServiceName, n.Role))
	}
	pm.Status.CascadeChain = chain
}

// attachGitOpsContext records whether the resource is managed by Helm/Argo/Flux.
func (r *IssueReconciler) attachGitOpsContext(ctx context.Context, pm *platformv1alpha1.PostMortem, issue *platformv1alpha1.Issue) {
	gitopsCtx, err := NewGitOpsDetector(r.Client).DetectGitOpsContext(ctx, issue.Spec.Resource)
	if err != nil {
		return
	}
	pm.Status.GitOpsContext = gitopsCtx.Summary
}

// attachGitCorrelation surfaces the suspected commit (image bump, config
// change) that likely introduced the regression, based on the linked
// SourceRepository CR.
func (r *IssueReconciler) attachGitCorrelation(ctx context.Context, pm *platformv1alpha1.PostMortem, issue *platformv1alpha1.Issue) {
	detectedAt := issue.CreationTimestamp.Time
	if issue.Status.DetectedAt != nil {
		detectedAt = issue.Status.DetectedAt.Time
	}
	srcCtx, err := NewSourceCodeAnalyzer(r.Client).BuildSourceContext(ctx, issue.Spec.Resource, detectedAt, nil)
	if err != nil || srcCtx == nil || srcCtx.SuspectedCommit == nil {
		return
	}
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

// persistPostMortemStatus writes the PostMortem status, with one conflict-retry
// because the PostMortemReconciler may have already started its own Update on
// the freshly-created CR. The retry re-fetches and re-applies via the same
// pure-function path, which avoids re-running the enrichment stages twice.
func (r *IssueReconciler) persistPostMortemStatus(ctx context.Context, pm *platformv1alpha1.PostMortem, apply func(*platformv1alpha1.PostMortem)) error {
	if err := r.Status().Update(ctx, pm); err == nil {
		return nil
	} else if !errors.IsConflict(err) && !errors.IsNotFound(err) {
		return err
	}

	time.Sleep(500 * time.Millisecond)
	if err := r.Get(ctx, types.NamespacedName{Name: pm.Name, Namespace: pm.Namespace}, pm); err != nil {
		return fmt.Errorf("re-fetching PostMortem after conflict: %w", err)
	}
	apply(pm)
	return r.Status().Update(ctx, pm)
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
			MaxAttempts: r.getMaxRemediationAttempts(ctx),
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
