package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

var (
	remediationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "remediations_total",
		Help:      "Total remediations by action type and result.",
	}, []string{"action_type", "result"})

	remediationDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "remediation_duration_seconds",
		Help:      "Duration of remediation plan execution.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 12),
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		remediationsTotal,
		remediationDuration,
	)
}

const verificationTimeout = 90 * time.Second

const agenticTimeout = 10 * time.Minute

// RemediationReconciler reconciles RemediationPlan objects.
type RemediationReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ServerClient   AgenticStepCaller
	ContextBuilder *KubernetesContextBuilder
	AuditRecorder  *AuditRecorder
	PatternStore   *PatternStore // Records resolution/failure patterns for Decision Engine learning
	CostTracker    *CostTracker  // Tracks LLM and downtime costs per incident
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete

func (r *RemediationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var plan platformv1alpha1.RemediationPlan
	if err := r.Get(ctx, req.NamespacedName, &plan); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	prevState := plan.Status.State

	var result ctrl.Result
	var reconcileErr error

	switch plan.Status.State {
	case "", platformv1alpha1.RemediationStatePending:
		result, reconcileErr = r.handlePending(ctx, &plan)
	case platformv1alpha1.RemediationStateWaitingApproval:
		result, reconcileErr = r.handleWaitingApproval(ctx, &plan)
	case platformv1alpha1.RemediationStateExecuting:
		if plan.Spec.AgenticMode {
			result, reconcileErr = r.handleAgenticExecuting(ctx, &plan)
		} else {
			result, reconcileErr = r.handleExecuting(ctx, &plan)
		}
	case platformv1alpha1.RemediationStateVerifying:
		result, reconcileErr = r.handleVerifying(ctx, &plan)
	case platformv1alpha1.RemediationStateCompleted, platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
		return ctrl.Result{}, nil
	default:
		log.Info("Unknown remediation state", "state", plan.Status.State)
		return ctrl.Result{}, nil
	}

	// Check if handler transitioned to a terminal state (use local plan, not re-fetch)
	// The handler modifies the plan pointer directly via Status().Update(), so plan.Status.State
	// reflects the latest state without needing a re-fetch (avoiding race conditions).
	if plan.Status.State != prevState {
		switch plan.Status.State {
		case platformv1alpha1.RemediationStateCompleted:
			r.recordPatternResolution(ctx, &plan)
		case platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
			r.recordPatternFailure(ctx, &plan)
		}
	}

	return result, reconcileErr
}

func (r *RemediationReconciler) handleWaitingApproval(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Always check the ApprovalRequest CR status directly — not just the annotation.
	// This prevents bypass via manual annotation deletion.
	arName := fmt.Sprintf("approval-%s", plan.Name)
	var ar platformv1alpha1.ApprovalRequest
	if err := r.Get(ctx, types.NamespacedName{Name: arName, Namespace: plan.Namespace}, &ar); err != nil {
		log.Info("Approval request not found, failing plan", "plan", plan.Name)
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.Result = "Approval request was deleted before decision"
		return ctrl.Result{}, r.Status().Update(ctx, plan)
	}

	switch ar.Status.State {
	case platformv1alpha1.ApprovalStateApproved:
		approvedBy, _ := lastDecision(ar.Status.Decisions)
		log.Info("Approval granted, proceeding with execution", "plan", plan.Name, "approvedBy", approvedBy)
		now := metav1.Now()
		plan.Status.State = platformv1alpha1.RemediationStateExecuting
		plan.Status.StartedAt = &now
		plan.Status.Result = ""
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, plan)

	case platformv1alpha1.ApprovalStateRejected:
		rejectedBy, reason := lastDecision(ar.Status.Decisions)
		log.Info("Approval rejected", "plan", plan.Name, "rejectedBy", rejectedBy)
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.Result = fmt.Sprintf("Approval rejected by %s: %s", rejectedBy, reason)
		return ctrl.Result{}, r.Status().Update(ctx, plan)

	case platformv1alpha1.ApprovalStateExpired:
		log.Info("Approval expired", "plan", plan.Name)
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.Result = "Approval request expired without decision"
		return ctrl.Result{}, r.Status().Update(ctx, plan)

	default: // Pending
		log.Info("Still waiting for approval decision", "plan", plan.Name)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

func (r *RemediationReconciler) handlePending(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Starting remediation", "plan", plan.Name, "attempt", plan.Spec.Attempt)

	// Check if an ApprovalPolicy requires approval for this plan
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err == nil {
		var insight platformv1alpha1.AIInsight
		insightName := issue.Name + "-insight"
		_ = r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: plan.Namespace}, &insight)

		required, policy, rule, err := CheckApprovalRequired(ctx, r.Client, plan, &issue, &insight)
		if err != nil {
			log.Error(err, "Failed to check approval policy, proceeding without approval")
		} else if required && rule.Mode != platformv1alpha1.ApprovalModeAuto {
			log.Info("Approval required by policy", "plan", plan.Name, "policy", policy.Name, "rule", rule.Name, "mode", rule.Mode)
			if err := CreateApprovalRequest(ctx, r.Client, r.Scheme, plan, &issue, &insight, policy, rule); err != nil {
				log.Error(err, "Failed to create approval request, proceeding without approval")
			} else {
				plan.Status.State = platformv1alpha1.RemediationStateWaitingApproval
				plan.Status.Result = fmt.Sprintf("Waiting for %s approval (policy: %s, rule: %s)", rule.Mode, policy.Name, rule.Name)
				if err := r.Status().Update(ctx, plan); err != nil {
					return ctrl.Result{}, err
				}
				log.Info("Approval request created, plan set to WaitingApproval", "plan", plan.Name)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}
	}

	// Validate safety constraints (skip for agentic mode — no pre-planned actions)
	if !plan.Spec.AgenticMode {
		if err := r.validateSafetyConstraints(plan.Spec.Actions, plan.Spec.SafetyConstraints); err != nil {
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.Result = fmt.Sprintf("Safety constraint violation: %v", err)
			remediationsTotal.WithLabelValues("validation", "failed").Inc()
			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}
	}

	now := metav1.Now()
	plan.Status.State = platformv1alpha1.RemediationStateExecuting
	plan.Status.StartedAt = &now

	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}

	// Record audit event for remediation start
	if r.AuditRecorder != nil {
		var issue platformv1alpha1.Issue
		if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err == nil {
			_ = r.AuditRecorder.RecordRemediationStarted(ctx, plan, &issue)
		}
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *RemediationReconciler) handleExecuting(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get the issue to find the target resource
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err != nil {
		if errors.IsNotFound(err) {
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.Result = "Parent issue not found"
			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}
		return ctrl.Result{}, err
	}

	resource := issue.Spec.Resource
	rollbackEngine := NewRollbackEngine(r.Client)

	// Capture full restorable pre-flight snapshot (structured, not just text)
	var evidence []platformv1alpha1.EvidenceItem
	preflightSnapshot, err := rollbackEngine.CaptureSnapshot(ctx, resource)
	if err != nil {
		log.Info("Failed to capture structured snapshot, falling back to text", "error", err)
	} else {
		plan.Status.PreflightSnapshot = preflightSnapshot
		evidence = append(evidence, platformv1alpha1.EvidenceItem{
			Type:      "preflight_snapshot",
			Data:      fmt.Sprintf("Structured snapshot captured: kind=%s replicas=%v containers=%d", resource.Kind, preflightSnapshot.Replicas, len(preflightSnapshot.ContainerImages)),
			Timestamp: metav1.Now(),
		})
	}

	// Also capture legacy text snapshot for backward compatibility
	if textSnapshot, err := r.capturePreflightSnapshot(ctx, resource); err == nil {
		evidence = append(evidence, textSnapshot)
	}

	// Execute each action with ReAct loop: Act → Observe → decide if resource is healthy (early exit)
	var checkpoints []platformv1alpha1.ActionCheckpoint
	for i, action := range plan.Spec.Actions {
		// ReAct: OBSERVE — check if resource is already healthy before running next action
		if i > 0 {
			if rollbackEngine.VerifyPostFailureHealth(ctx, resource) {
				log.Info("Resource healthy after action — early exit (ReAct)",
					"plan", plan.Name, "actionsExecuted", i, "totalActions", len(plan.Spec.Actions),
					"skippedActions", len(plan.Spec.Actions)-i)

				evidence = append(evidence, platformv1alpha1.EvidenceItem{
					Type:      "react_early_exit",
					Data:      fmt.Sprintf("Resource healthy after %d/%d actions — skipped remaining %d actions", i, len(plan.Spec.Actions), len(plan.Spec.Actions)-i),
					Timestamp: metav1.Now(),
				})
				break
			}
		}

		// ReAct: ACT — execute the action
		log.Info("Executing action", "type", action.Type, "resource", resource.Name, "index", i, "total", len(plan.Spec.Actions))

		// Capture checkpoint before this action (for partial rollback)
		checkpoint := platformv1alpha1.ActionCheckpoint{
			ActionIndex: int32(i),
			ActionType:  action.Type,
			Timestamp:   metav1.Now(),
		}

		// For node actions, capture node-specific snapshot
		if action.Type == platformv1alpha1.ActionCordonNode || action.Type == platformv1alpha1.ActionUncordonNode || action.Type == platformv1alpha1.ActionDrainNode {
			if nodeName, ok := action.Params["node"]; ok {
				if nodeSnap, err := rollbackEngine.CaptureNodeSnapshot(ctx, nodeName); err == nil {
					checkpoint.SnapshotBefore = nodeSnap
				}
			}
		} else {
			// Re-capture resource snapshot before each action (state may have changed from previous action)
			if actionSnap, err := rollbackEngine.CaptureSnapshot(ctx, resource); err == nil {
				checkpoint.SnapshotBefore = actionSnap
			}
		}

		execErr := r.executeAction(ctx, resource, &action)

		if execErr != nil {
			log.Error(execErr, "Action failed", "type", action.Type, "index", i)
			checkpoint.Success = false
			checkpoints = append(checkpoints, checkpoint)

			now := metav1.Now()
			plan.Status.ActionCheckpoints = checkpoints

			// Attempt automatic rollback to pre-flight state
			rollbackResult := ""
			if preflightSnapshot != nil {
				log.Info("Attempting automatic rollback to pre-flight state", "plan", plan.Name)
				if rbResult, rbErr := rollbackEngine.Rollback(ctx, preflightSnapshot); rbErr != nil {
					rollbackResult = fmt.Sprintf("Rollback FAILED: %v", rbErr)
					log.Error(rbErr, "Automatic rollback failed", "plan", plan.Name)
					plan.Status.RollbackPerformed = true
					plan.Status.RollbackResult = rollbackResult
				} else {
					rollbackResult = rbResult
					log.Info("Automatic rollback succeeded", "plan", plan.Name, "result", rbResult)
					plan.Status.RollbackPerformed = true
					plan.Status.RollbackResult = rbResult
				}
			}

			// Verify post-failure health
			healthy := rollbackEngine.VerifyPostFailureHealth(ctx, resource)
			plan.Status.PostFailureHealthy = &healthy

			// Set final state
			if plan.Status.RollbackPerformed && healthy {
				plan.Status.State = platformv1alpha1.RemediationStateRolledBack
			} else {
				plan.Status.State = platformv1alpha1.RemediationStateFailed
			}

			plan.Status.CompletedAt = &now
			plan.Status.Result = fmt.Sprintf("Action %s (index %d) failed: %v", action.Type, i, execErr)
			if rollbackResult != "" {
				plan.Status.Result += fmt.Sprintf(" | Rollback: %s", rollbackResult)
			}
			if healthy {
				plan.Status.Result += " | Post-rollback: resource healthy"
			} else {
				plan.Status.Result += " | Post-failure: resource may be unhealthy"
			}

			plan.Status.Evidence = append(evidence, platformv1alpha1.EvidenceItem{
				Type:      "action_failed",
				Data:      fmt.Sprintf("Action %s failed: %v", action.Type, execErr),
				Timestamp: now,
			})
			if rollbackResult != "" {
				plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
					Type:      "rollback",
					Data:      rollbackResult,
					Timestamp: now,
				})
			}

			remediationsTotal.WithLabelValues(string(action.Type), "failed").Inc()
			if plan.Status.StartedAt != nil {
				remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
			}

			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}

		// Action succeeded — record checkpoint
		checkpoint.Success = true
		checkpoints = append(checkpoints, checkpoint)

		evidence = append(evidence, platformv1alpha1.EvidenceItem{
			Type:      "action_completed",
			Data:      fmt.Sprintf("Action %s executed successfully (%d/%d)", action.Type, i+1, len(plan.Spec.Actions)),
			Timestamp: metav1.Now(),
		})
		remediationsTotal.WithLabelValues(string(action.Type), "success").Inc()
	}

	// All actions executed (or early exit) — transition to Verifying
	now := metav1.Now()
	plan.Status.State = platformv1alpha1.RemediationStateVerifying
	plan.Status.ActionsCompletedAt = &now
	plan.Status.Evidence = evidence
	plan.Status.ActionCheckpoints = checkpoints

	log.Info("Actions executed, verifying resource health", "plan", plan.Name, "actionsRun", len(checkpoints), "totalPlanned", len(plan.Spec.Actions))

	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// lastDecision returns the approver and reason from the last decision in the list.
func lastDecision(decisions []platformv1alpha1.ApprovalDecision) (string, string) {
	if len(decisions) == 0 {
		return "unknown", ""
	}
	d := decisions[len(decisions)-1]
	return d.Approver, d.Reason
}

func (r *RemediationReconciler) handleVerifying(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Get the parent Issue to find target resource.
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err != nil {
		if errors.IsNotFound(err) {
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.Result = "Parent issue not found during verification"
			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}
		return ctrl.Result{}, err
	}

	resource := issue.Spec.Resource

	// Check resource health based on kind.
	healthy, verifyErr := r.verifyResourceHealth(ctx, resource)
	if verifyErr != nil {
		if errors.IsNotFound(verifyErr) {
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.Result = fmt.Sprintf("%s %s/%s not found during verification", resource.Kind, resource.Namespace, resource.Name)
			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}
		return ctrl.Result{}, verifyErr
	}

	if healthy {
		now := metav1.Now()
		plan.Status.State = platformv1alpha1.RemediationStateCompleted
		plan.Status.CompletedAt = &now
		plan.Status.Result = fmt.Sprintf("Remediation verified: %s %s/%s is healthy",
			resource.Kind, resource.Namespace, resource.Name)
		plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
			Type:      "verification_passed",
			Data:      fmt.Sprintf("%s %s/%s verified healthy", resource.Kind, resource.Namespace, resource.Name),
			Timestamp: now,
		})

		if plan.Status.StartedAt != nil {
			remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
		}

		// Record audit event for remediation completion
		if r.AuditRecorder != nil {
			var iss platformv1alpha1.Issue
			if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &iss); err == nil {
				_ = r.AuditRecorder.RecordRemediationCompleted(ctx, plan, &iss)
			}
		}

		log.Info("Verification passed, resource healthy", "plan", plan.Name, "kind", resource.Kind)
		return ctrl.Result{}, r.Status().Update(ctx, plan)
	}

	// Check if verification timeout exceeded.
	if plan.Status.ActionsCompletedAt != nil && time.Since(plan.Status.ActionsCompletedAt.Time) > verificationTimeout {
		now := metav1.Now()

		// Resource is still unhealthy after actions — attempt rollback to pre-flight state
		if plan.Status.PreflightSnapshot != nil && !plan.Status.RollbackPerformed {
			rollbackEngine := NewRollbackEngine(r.Client)
			log.Info("Verification timeout — attempting rollback to pre-flight state", "plan", plan.Name)

			if rbResult, rbErr := rollbackEngine.Rollback(ctx, plan.Status.PreflightSnapshot); rbErr != nil {
				log.Error(rbErr, "Rollback after verification timeout failed", "plan", plan.Name)
				plan.Status.RollbackPerformed = true
				plan.Status.RollbackResult = fmt.Sprintf("Rollback FAILED: %v", rbErr)
			} else {
				log.Info("Rollback after verification timeout succeeded", "plan", plan.Name, "result", rbResult)
				plan.Status.RollbackPerformed = true
				plan.Status.RollbackResult = rbResult
			}

			postRollbackHealthy := rollbackEngine.VerifyPostFailureHealth(ctx, resource)
			plan.Status.PostFailureHealthy = &postRollbackHealthy

			plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
				Type:      "verification_timeout_rollback",
				Data:      fmt.Sprintf("Verification timeout after %s. Rollback: %s", verificationTimeout, plan.Status.RollbackResult),
				Timestamp: now,
			})
		}

		if plan.Status.RollbackPerformed {
			plan.Status.State = platformv1alpha1.RemediationStateRolledBack
		} else {
			plan.Status.State = platformv1alpha1.RemediationStateFailed
		}

		plan.Status.CompletedAt = &now
		plan.Status.Result = fmt.Sprintf("Verification failed: %s %s/%s still unhealthy after %s",
			resource.Kind, resource.Namespace, resource.Name, verificationTimeout)
		if plan.Status.RollbackResult != "" {
			plan.Status.Result += fmt.Sprintf(" | Rollback: %s", plan.Status.RollbackResult)
		}

		plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
			Type:      "verification_failed",
			Data:      fmt.Sprintf("%s %s/%s unhealthy after verification timeout", resource.Kind, resource.Namespace, resource.Name),
			Timestamp: now,
		})

		if plan.Status.StartedAt != nil {
			remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
		}
		remediationsTotal.WithLabelValues("verification", "failed").Inc()

		log.Info("Verification failed, resource still unhealthy", "plan", plan.Name, "kind", resource.Kind,
			"rollbackPerformed", plan.Status.RollbackPerformed)
		return ctrl.Result{}, r.Status().Update(ctx, plan)
	}

	// Still waiting — requeue.
	log.Info("Verifying resource health", "plan", plan.Name, "kind", resource.Kind,
		"elapsed", time.Since(plan.Status.ActionsCompletedAt.Time).Round(time.Second))
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// executeAction dispatches a single remediation action to the appropriate handler.
// actionExecutor is the canonical signature every per-action executor satisfies.
// The dispatcher table maps RemediationActionType to one of these method values,
// which keeps executeAction at constant cyclomatic complexity regardless of how
// many action kinds the platform supports. Executors that do not consume params
// declare the parameter as `_ map[string]string` to make that intent explicit.
type actionExecutor func(*RemediationReconciler, context.Context, platformv1alpha1.ResourceRef, map[string]string) error

// actionExecutors is the dispatch table: RemediationActionType → executor.
// Adding a new action requires (1) defining the enum constant, (2) implementing
// the executor method on RemediationReconciler with the actionExecutor signature,
// (3) adding the entry here, and (4) registering the name → enum mapping in
// actionTypeByName (issue_controller.go).
var actionExecutors = map[platformv1alpha1.RemediationActionType]actionExecutor{
	// Core workload actions
	platformv1alpha1.ActionScaleDeployment:       (*RemediationReconciler).executeScaleDeployment,
	platformv1alpha1.ActionRollbackDeployment:    (*RemediationReconciler).executeRollbackDeployment,
	platformv1alpha1.ActionRestartDeployment:     (*RemediationReconciler).executeRestartDeployment,
	platformv1alpha1.ActionPatchConfig:           (*RemediationReconciler).executePatchConfig,
	platformv1alpha1.ActionAdjustResources:       (*RemediationReconciler).executeAdjustResources,
	platformv1alpha1.ActionDeletePod:             (*RemediationReconciler).executeDeletePod,
	platformv1alpha1.ActionHelmRollback:          (*RemediationReconciler).executeHelmRollback,
	platformv1alpha1.ActionArgoSyncApp:           (*RemediationReconciler).executeArgoSyncApp,
	platformv1alpha1.ActionAdjustHPA:             (*RemediationReconciler).executeAdjustHPA,
	platformv1alpha1.ActionRestartStatefulSetPod: (*RemediationReconciler).executeRestartStatefulSetPod,
	platformv1alpha1.ActionCordonNode:            (*RemediationReconciler).executeCordonNode,
	platformv1alpha1.ActionUncordonNode:          (*RemediationReconciler).executeUncordonNode,
	platformv1alpha1.ActionDrainNode:             (*RemediationReconciler).executeDrainNode,
	platformv1alpha1.ActionResizePVC:             (*RemediationReconciler).executeResizePVC,
	platformv1alpha1.ActionRotateSecret:          (*RemediationReconciler).executeRotateSecret,
	platformv1alpha1.ActionExecDiagnostic:        (*RemediationReconciler).executeExecDiagnostic,
	platformv1alpha1.ActionUpdateIngress:         (*RemediationReconciler).executeUpdateIngress,
	platformv1alpha1.ActionPatchNetworkPolicy:    (*RemediationReconciler).executePatchNetworkPolicy,
	platformv1alpha1.ActionApplyManifest:         (*RemediationReconciler).executeApplyManifest,

	// StatefulSet
	platformv1alpha1.ActionScaleStatefulSet:           (*RemediationReconciler).executeScaleStatefulSet,
	platformv1alpha1.ActionRestartStatefulSet:         (*RemediationReconciler).executeRestartStatefulSet,
	platformv1alpha1.ActionRollbackStatefulSet:        (*RemediationReconciler).executeRollbackStatefulSet,
	platformv1alpha1.ActionAdjustStatefulSetResources: (*RemediationReconciler).executeAdjustStatefulSetResources,
	platformv1alpha1.ActionDeleteStatefulSetPod:       (*RemediationReconciler).executeDeleteStatefulSetPod,
	platformv1alpha1.ActionForceDeleteStatefulSetPod:  (*RemediationReconciler).executeForceDeleteStatefulSetPod,
	platformv1alpha1.ActionUpdateStatefulSetStrategy:  (*RemediationReconciler).executeUpdateStatefulSetStrategy,
	platformv1alpha1.ActionRecreateStatefulSetPVC:     (*RemediationReconciler).executeRecreateStatefulSetPVC,
	platformv1alpha1.ActionPartitionStatefulSetUpdate: (*RemediationReconciler).executePartitionStatefulSetUpdate,

	// DaemonSet
	platformv1alpha1.ActionRestartDaemonSet:            (*RemediationReconciler).executeRestartDaemonSet,
	platformv1alpha1.ActionRollbackDaemonSet:           (*RemediationReconciler).executeRollbackDaemonSet,
	platformv1alpha1.ActionAdjustDaemonSetResources:    (*RemediationReconciler).executeAdjustDaemonSetResources,
	platformv1alpha1.ActionDeleteDaemonSetPod:          (*RemediationReconciler).executeDeleteDaemonSetPod,
	platformv1alpha1.ActionUpdateDaemonSetStrategy:     (*RemediationReconciler).executeUpdateDaemonSetStrategy,
	platformv1alpha1.ActionPauseDaemonSetRollout:       (*RemediationReconciler).executePauseDaemonSetRollout,
	platformv1alpha1.ActionCordonAndDeleteDaemonSetPod: (*RemediationReconciler).executeCordonAndDeleteDaemonSetPod,

	// Job
	platformv1alpha1.ActionRetryJob:              (*RemediationReconciler).executeRetryJob,
	platformv1alpha1.ActionAdjustJobResources:    (*RemediationReconciler).executeAdjustJobResources,
	platformv1alpha1.ActionDeleteFailedJob:       (*RemediationReconciler).executeDeleteFailedJob,
	platformv1alpha1.ActionSuspendJob:            (*RemediationReconciler).executeSuspendJob,
	platformv1alpha1.ActionResumeJob:             (*RemediationReconciler).executeResumeJob,
	platformv1alpha1.ActionAdjustJobParallelism:  (*RemediationReconciler).executeAdjustJobParallelism,
	platformv1alpha1.ActionAdjustJobDeadline:     (*RemediationReconciler).executeAdjustJobDeadline,
	platformv1alpha1.ActionAdjustJobBackoffLimit: (*RemediationReconciler).executeAdjustJobBackoffLimit,
	platformv1alpha1.ActionForceDeleteJobPods:    (*RemediationReconciler).executeForceDeleteJobPods,

	// CronJob
	platformv1alpha1.ActionSuspendCronJob:           (*RemediationReconciler).executeSuspendCronJob,
	platformv1alpha1.ActionResumeCronJob:            (*RemediationReconciler).executeResumeCronJob,
	platformv1alpha1.ActionTriggerCronJob:           (*RemediationReconciler).executeTriggerCronJob,
	platformv1alpha1.ActionAdjustCronJobResources:   (*RemediationReconciler).executeAdjustCronJobResources,
	platformv1alpha1.ActionAdjustCronJobSchedule:    (*RemediationReconciler).executeAdjustCronJobSchedule,
	platformv1alpha1.ActionAdjustCronJobDeadline:    (*RemediationReconciler).executeAdjustCronJobDeadline,
	platformv1alpha1.ActionAdjustCronJobHistory:     (*RemediationReconciler).executeAdjustCronJobHistory,
	platformv1alpha1.ActionAdjustCronJobConcurrency: (*RemediationReconciler).executeAdjustCronJobConcurrency,
	platformv1alpha1.ActionDeleteCronJobActiveJobs:  (*RemediationReconciler).executeDeleteCronJobActiveJobs,
	platformv1alpha1.ActionReplaceCronJobTemplate:   (*RemediationReconciler).executeReplaceCronJobTemplate,
}

// executeAction dispatches a remediation action to its registered executor.
// Unsupported types return a structured error so callers can record the failure
// and skip (the agentic loop turns this into an observation and requeues).
func (r *RemediationReconciler) executeAction(ctx context.Context, resource platformv1alpha1.ResourceRef, action *platformv1alpha1.RemediationAction) error {
	exec, ok := actionExecutors[action.Type]
	if !ok {
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
	return exec(r, ctx, resource, action.Params)
}

// handleAgenticExecuting runs one step of the AI-driven agentic remediation loop.
// Each reconcile: ask AI → execute action → record observation → requeue for next step.
// defaultAgenticMaxSteps caps the loop when the plan doesn't override it.
const defaultAgenticMaxSteps int32 = 10

// handleAgenticExecuting runs one iteration of the AI-driven agentic remediation
// loop. The body is intentionally a thin orchestrator over focused helpers:
// each branch of the loop (safety, RPC, resolved / observation / action / reject)
// lives in its own method, which makes the control flow easy to follow and
// keeps cyclomatic complexity bounded.
func (r *RemediationReconciler) handleAgenticExecuting(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	maxSteps := plan.Spec.AgenticMaxSteps
	if maxSteps == 0 {
		maxSteps = defaultAgenticMaxSteps
	}
	currentStep := int32(len(plan.Spec.AgenticHistory)) + 1

	if res, terminal, err := r.applyAgenticSafetyGuards(ctx, plan, maxSteps, currentStep); terminal {
		return res, err
	}

	issue, res, err := r.loadIssueForAgenticStep(ctx, plan)
	if err != nil || issue == nil {
		return res, err
	}

	if r.ServerClient == nil || !r.ServerClient.IsConnected() {
		log.Info("Server not connected, requeuing agentic step")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	resp, requeue := r.callAgenticStepRPC(ctx, plan, issue, maxSteps, currentStep, log)
	if resp == nil {
		return requeue, nil
	}

	switch {
	case resp.Resolved:
		return r.handleAgenticResolved(ctx, plan, resp, currentStep, log)
	case resp.NextAction == nil || resp.NextAction.Action == "":
		return r.handleAgenticObservation(ctx, plan, resp, currentStep, log)
	case resp.DivergesFromInsight && strings.TrimSpace(resp.DivergenceReason) == "":
		// GAP-01 fix: refuse divergent actions without a written justification.
		return r.rejectDivergentAgenticAction(ctx, plan, resp, currentStep, log)
	default:
		return r.executeAgenticAction(ctx, plan, issue, resp, currentStep, log)
	}
}

// applyAgenticSafetyGuards enforces the loop's hard limits: max steps and the
// wall-clock timeout. When a guard fires it transitions the plan to Failed and
// signals the caller via terminal=true. The Status update error (if any) is
// propagated so the controller manager re-queues.
func (r *RemediationReconciler) applyAgenticSafetyGuards(ctx context.Context, plan *platformv1alpha1.RemediationPlan, maxSteps, currentStep int32) (ctrl.Result, bool, error) {
	if currentStep > maxSteps {
		return r.failAgenticPlan(ctx, plan, fmt.Sprintf("Agentic loop exceeded max steps (%d)", maxSteps))
	}
	if plan.Status.AgenticStartedAt != nil && time.Since(plan.Status.AgenticStartedAt.Time) > agenticTimeout {
		return r.failAgenticPlan(ctx, plan, "Agentic loop timed out (10 minutes)")
	}
	return ctrl.Result{}, false, nil
}

// failAgenticPlan transitions the plan to Failed with the given result message
// and persists the status. Used by safety-guard and missing-issue paths.
func (r *RemediationReconciler) failAgenticPlan(ctx context.Context, plan *platformv1alpha1.RemediationPlan, reason string) (ctrl.Result, bool, error) {
	now := metav1.Now()
	plan.Status.State = platformv1alpha1.RemediationStateFailed
	plan.Status.Result = reason
	plan.Status.CompletedAt = &now
	return ctrl.Result{}, true, r.Status().Update(ctx, plan)
}

// loadIssueForAgenticStep fetches the parent Issue and refreshes the K8s
// context attached to it for the next agentic step. When the Issue has been
// deleted out from under the plan we fail the plan rather than spinning.
// Returns (nil, result, err) when the caller should return immediately.
func (r *RemediationReconciler) loadIssueForAgenticStep(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (*platformv1alpha1.Issue, ctrl.Result, error) {
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err != nil {
		if errors.IsNotFound(err) {
			res, _, statusErr := r.failAgenticPlan(ctx, plan, "Parent issue not found")
			return nil, res, statusErr
		}
		return nil, ctrl.Result{}, err
	}
	return &issue, ctrl.Result{}, nil
}

// callAgenticStepRPC composes the AgenticStep request — including the GAP-01
// AIInsight context — and invokes the server. RPC errors are non-terminal
// (the loop simply retries on the next reconcile), signaled by returning a
// nil response and a non-zero RequeueAfter.
func (r *RemediationReconciler) callAgenticStepRPC(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue, maxSteps, currentStep int32, log logr.Logger) (*pb.AgenticStepResponse, ctrl.Result) {
	resource := issue.Spec.Resource
	kubeCtx := r.buildAgenticKubeContext(ctx, resource, log)
	history := buildAgenticHistoryEntries(plan.Spec.AgenticHistory)
	provider, model := resolveInstanceProvider(ctx, r.Client)
	insightAnalysis, insightConfidence, insightRecs, insightActions := r.loadAIInsightForAgenticContext(ctx, issue, log)

	resp, err := r.ServerClient.AgenticStep(ctx, &pb.AgenticStepRequest{
		IssueName:               issue.Name,
		Namespace:               issue.Namespace,
		ResourceKind:            resource.Kind,
		ResourceName:            resource.Name,
		SignalType:              issue.Spec.SignalType,
		Severity:                string(issue.Spec.Severity),
		Description:             issue.Spec.Description,
		RiskScore:               issue.Spec.RiskScore,
		Provider:                provider,
		Model:                   model,
		KubernetesContext:       kubeCtx,
		History:                 history,
		MaxSteps:                maxSteps,
		CurrentStep:             currentStep,
		InsightAnalysis:         insightAnalysis,
		InsightConfidence:       insightConfidence,
		InsightRecommendations:  insightRecs,
		InsightSuggestedActions: insightActions,
	})
	if err != nil {
		log.Error(err, "AgenticStep RPC failed, requeuing")
		return nil, ctrl.Result{RequeueAfter: 30 * time.Second}
	}
	return resp, ctrl.Result{}
}

// buildAgenticKubeContext returns the freshly-built K8s context for the agentic
// prompt, or an empty string when the context builder is unset or fails.
func (r *RemediationReconciler) buildAgenticKubeContext(ctx context.Context, resource platformv1alpha1.ResourceRef, log logr.Logger) string {
	if r.ContextBuilder == nil {
		return ""
	}
	kubeCtx, err := r.ContextBuilder.BuildContext(ctx, resource)
	if err != nil {
		log.Info("Failed to build K8s context for agentic step", "error", err)
		return ""
	}
	return kubeCtx
}

// buildAgenticHistoryEntries converts internal AgenticStep records into the
// proto wire form for the AgenticStep RPC. Observation-only steps (no Action)
// are still included so the server can see the loop's full chain of thought.
func buildAgenticHistoryEntries(steps []platformv1alpha1.AgenticStep) []*pb.AgenticHistoryEntry {
	history := make([]*pb.AgenticHistoryEntry, 0, len(steps))
	for _, step := range steps {
		entry := &pb.AgenticHistoryEntry{
			StepNumber:  step.StepNumber,
			AiMessage:   step.AIMessage,
			Observation: step.Observation,
		}
		if step.Action != nil {
			entry.Action = string(step.Action.Type)
			entry.Params = step.Action.Params
		}
		history = append(history, entry)
	}
	return history
}

// handleAgenticResolved transitions the plan to Verifying after the loop's AI
// declared victory, persisting its postmortem narrative as annotations for
// downstream generatePostMortem.
func (r *RemediationReconciler) handleAgenticResolved(ctx context.Context, plan *platformv1alpha1.RemediationPlan, resp *pb.AgenticStepResponse, currentStep int32, log logr.Logger) (ctrl.Result, error) {
	log.Info("AI determined issue resolved", "plan", plan.Name, "step", currentStep)

	if plan.Annotations == nil {
		plan.Annotations = make(map[string]string)
	}
	plan.Annotations["platform.chatcli.io/postmortem-summary"] = resp.PostmortemSummary
	plan.Annotations["platform.chatcli.io/root-cause"] = resp.RootCause
	plan.Annotations["platform.chatcli.io/impact"] = resp.Impact
	if len(resp.LessonsLearned) > 0 {
		plan.Annotations["platform.chatcli.io/lessons-learned"] = strings.Join(resp.LessonsLearned, "\n---\n")
	}
	if len(resp.PreventionActions) > 0 {
		plan.Annotations["platform.chatcli.io/prevention-actions"] = strings.Join(resp.PreventionActions, "\n---\n")
	}

	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber: currentStep,
		AIMessage:  resp.Reasoning,
		Timestamp:  metav1.Now(),
	})

	if err := r.Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	now := metav1.Now()
	plan.Status.State = platformv1alpha1.RemediationStateVerifying
	plan.Status.ActionsCompletedAt = &now
	plan.Status.AgenticStepCount = int32(len(plan.Spec.AgenticHistory))
	return ctrl.Result{RequeueAfter: 10 * time.Second}, r.Status().Update(ctx, plan)
}

// handleAgenticObservation records a "no action this turn" step. The AI uses
// this when it wants to wait for the effect of a previous action to settle
// (e.g. give a deployment rollout time to converge) before deciding again.
func (r *RemediationReconciler) handleAgenticObservation(ctx context.Context, plan *platformv1alpha1.RemediationPlan, resp *pb.AgenticStepResponse, currentStep int32, log logr.Logger) (ctrl.Result, error) {
	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber:  currentStep,
		AIMessage:   resp.Reasoning,
		Observation: "Observation step — no action taken",
		Timestamp:   metav1.Now(),
	})
	if res, err := r.persistAgenticStep(ctx, plan); err != nil || res.RequeueAfter > 0 {
		return res, err
	}
	log.Info("Agentic observation step", "plan", plan.Name, "step", currentStep)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// executeAgenticAction dispatches the proposed remediation, records its
// outcome onto AgenticHistory, and emits the Prometheus counter.
func (r *RemediationReconciler) executeAgenticAction(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue, resp *pb.AgenticStepResponse, currentStep int32, log logr.Logger) (ctrl.Result, error) {
	nextAction := &platformv1alpha1.RemediationAction{
		Type:   mapActionType(resp.NextAction.Action),
		Params: resp.NextAction.Params,
	}

	log.Info("Agentic executing action", "plan", plan.Name, "step", currentStep, "action", nextAction.Type)
	if resp.DivergesFromInsight {
		log.Info("Agentic action diverges from AIInsight (justified)",
			"plan", plan.Name, "step", currentStep, "reason", resp.DivergenceReason)
	}

	observation := r.runAgenticAction(ctx, issue.Spec.Resource, nextAction)

	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber:  currentStep,
		AIMessage:   resp.Reasoning,
		Action:      nextAction,
		Observation: observation,
		Timestamp:   metav1.Now(),
	})

	if res, err := r.persistAgenticStep(ctx, plan); err != nil || res.RequeueAfter > 0 {
		return res, err
	}
	log.Info("Agentic step completed", "plan", plan.Name, "step", currentStep,
		"action", nextAction.Type, "observation", observation)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// runAgenticAction executes the action and returns the observation string
// recorded into AgenticHistory. The Prometheus counter is incremented as a
// side-effect so the success/failure breakdown surfaces in /metrics.
func (r *RemediationReconciler) runAgenticAction(ctx context.Context, resource platformv1alpha1.ResourceRef, action *platformv1alpha1.RemediationAction) string {
	if err := r.executeAction(ctx, resource, action); err != nil {
		remediationsTotal.WithLabelValues(string(action.Type), "failed").Inc()
		return fmt.Sprintf("FAILED: %v", err)
	}
	remediationsTotal.WithLabelValues(string(action.Type), "success").Inc()
	return fmt.Sprintf("SUCCESS: %s executed successfully", action.Type)
}

// persistAgenticStep saves the appended AgenticHistory entry and then the
// updated status fields, handling the two-stage spec/status split correctly:
// r.Update() resets the in-memory status, so AgenticStepCount and
// AgenticStartedAt are set AFTER the spec write. Returns a non-zero Requeue
// on conflict so the controller manager retries.
func (r *RemediationReconciler) persistAgenticStep(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	agenticStepCount := int32(len(plan.Spec.AgenticHistory))
	needStartedAt := plan.Status.AgenticStartedAt == nil

	if err := r.Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			// Silent fast retry on optimistic-locking conflict. RequeueAfter
			// (controller-runtime's non-deprecated equivalent of Requeue=true)
			// pairs with nil err so the controller doesn't log the conflict
			// at Error severity — concurrent reconciles are normal under load.
			return ctrl.Result{RequeueAfter: conflictRetryDelay}, nil
		}
		return ctrl.Result{}, err
	}

	plan.Status.AgenticStepCount = agenticStepCount
	if needStartedAt {
		now := metav1.Now()
		plan.Status.AgenticStartedAt = &now
	}
	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// conflictRetryDelay is the small wait we apply when an optimistic-locking
// conflict bounces us out of a spec Update. The default controller-runtime
// rate limiter would back off much harder; this short jitter lets us pick
// up the next reconcile attempt quickly under normal load.
const conflictRetryDelay = 250 * time.Millisecond

// loadAIInsightForAgenticContext fetches the AIInsight CR associated with the
// issue (best-effort) and converts its conclusion into the fields that get
// passed as PRIMARY GUIDANCE to the agentic prompt. Returns zero values when
// the insight is missing, which is fine — the prompt simply skips the guidance
// section. GAP-01 fix (extracted from handleAgenticExecuting to keep its
// cyclomatic complexity below the Floor 8 threshold).
func (r *RemediationReconciler) loadAIInsightForAgenticContext(ctx context.Context, issue *platformv1alpha1.Issue, logger logr.Logger) (string, float32, []string, []*pb.SuggestedAction) {
	var insight platformv1alpha1.AIInsight
	insightName := issue.Name + "-insight"
	if err := r.Get(ctx, types.NamespacedName{Name: insightName, Namespace: issue.Namespace}, &insight); err != nil {
		if !errors.IsNotFound(err) {
			logger.Info("Failed to load AIInsight for agentic context (continuing without)", "error", err)
		}
		return "", 0, nil, nil
	}

	actions := make([]*pb.SuggestedAction, 0, len(insight.Status.SuggestedActions))
	for _, sa := range insight.Status.SuggestedActions {
		actions = append(actions, &pb.SuggestedAction{
			Name:        sa.Name,
			Action:      sa.Action,
			Description: sa.Description,
			Params:      sa.Params,
		})
	}
	return insight.Status.Analysis, float32(insight.Status.Confidence), insight.Status.Recommendations, actions
}

// rejectDivergentAgenticAction handles the GAP-01 fix path where the server
// flagged the proposed next_action as diverging from the AIInsight's primary
// suggested action AND the LLM did not populate divergence_reason. We record
// the step as REJECTED so the divergence shows up in PostMortem / audit, then
// requeue to ask for a different proposal. Without this guard the agentic loop
// can exhaust maxRemediationAttempts proposing the same blocked action
// repeatedly (chaos test Cycle 1, 2026-05-23).
func (r *RemediationReconciler) rejectDivergentAgenticAction(ctx context.Context, plan *platformv1alpha1.RemediationPlan, resp *pb.AgenticStepResponse, currentStep int32, logger logr.Logger) (ctrl.Result, error) {
	logger.Info("Rejecting agentic action: diverges from AIInsight without justification",
		"plan", plan.Name, "step", currentStep,
		"proposed_action", resp.NextAction.Action)
	remediationsTotal.WithLabelValues(resp.NextAction.Action, "rejected_divergence").Inc()

	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber:  currentStep,
		AIMessage:   resp.Reasoning,
		Observation: fmt.Sprintf("REJECTED: proposed action %q diverges from AIInsight primary action without divergence_reason", resp.NextAction.Action),
		Timestamp:   metav1.Now(),
	})

	agenticStepCount := int32(len(plan.Spec.AgenticHistory))
	needStartedAt := plan.Status.AgenticStartedAt == nil
	if err := r.Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	plan.Status.AgenticStepCount = agenticStepCount
	if needStartedAt {
		now := metav1.Now()
		plan.Status.AgenticStartedAt = &now
	}
	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// executeScaleDeployment patches the target deployment's replicas.
func (r *RemediationReconciler) executeScaleDeployment(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	replicasStr, ok := params["replicas"]
	if !ok {
		return fmt.Errorf("missing 'replicas' param")
	}
	replicas, err := strconv.Atoi(replicasStr)
	if err != nil {
		return fmt.Errorf("invalid replicas value %q: %w", replicasStr, err)
	}

	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return fmt.Errorf("failed to get deployment %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	r32 := int32(replicas)
	deploy.Spec.Replicas = &r32
	return r.Update(ctx, &deploy)
}

// executeRollbackDeployment performs a real Kubernetes rollback by copying the pod template
// from a target ReplicaSet revision to the Deployment spec. This is equivalent to kubectl rollout undo.
// Supports params["toRevision"]: "" or "previous" (default), "healthy" (auto-find), or a specific number.
func (r *RemediationReconciler) executeRollbackDeployment(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	log := log.FromContext(ctx)

	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return fmt.Errorf("failed to get deployment %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	// List ReplicaSets owned by this deployment
	var rsList appsv1.ReplicaSetList
	if err := r.List(ctx, &rsList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("failed to list replicasets: %w", err)
	}

	type rsInfo struct {
		revision int
		rs       appsv1.ReplicaSet
	}
	var owned []rsInfo
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		for _, ref := range rs.OwnerReferences {
			if ref.Kind == "Deployment" && ref.Name == resource.Name {
				rev := 0
				if revStr, ok := rs.Annotations["deployment.kubernetes.io/revision"]; ok {
					fmt.Sscanf(revStr, "%d", &rev)
				}
				owned = append(owned, rsInfo{revision: rev, rs: *rs})
				break
			}
		}
	}

	if len(owned) < 2 {
		return fmt.Errorf("deployment %s/%s has fewer than 2 revisions, cannot rollback",
			resource.Namespace, resource.Name)
	}

	// Sort by revision descending (current first)
	sort.Slice(owned, func(i, j int) bool {
		return owned[i].revision > owned[j].revision
	})

	currentRevision := owned[0].revision

	// Determine target revision
	var targetRS *appsv1.ReplicaSet
	toRevision := params["toRevision"]

	switch toRevision {
	case "", "previous":
		targetRS = &owned[1].rs
		log.Info("Rolling back to previous revision",
			"current", currentRevision, "target", owned[1].revision)

	case "healthy":
		// Find latest revision (excluding current) with ready replicas
		for i := 1; i < len(owned); i++ {
			if owned[i].rs.Status.ReadyReplicas > 0 {
				targetRS = &owned[i].rs
				log.Info("Rolling back to last healthy revision",
					"current", currentRevision, "target", owned[i].revision,
					"readyReplicas", owned[i].rs.Status.ReadyReplicas)
				break
			}
		}
		if targetRS == nil {
			// No revision has ready replicas — fall back to previous
			targetRS = &owned[1].rs
			log.Info("No healthy revision found, falling back to previous",
				"current", currentRevision, "target", owned[1].revision)
		}

	default:
		targetRev, err := strconv.Atoi(toRevision)
		if err != nil {
			return fmt.Errorf("invalid toRevision value %q: must be a number, 'previous', or 'healthy'", toRevision)
		}
		for i := range owned {
			if owned[i].revision == targetRev {
				targetRS = &owned[i].rs
				break
			}
		}
		if targetRS == nil {
			return fmt.Errorf("revision %d not found for deployment %s/%s",
				targetRev, resource.Namespace, resource.Name)
		}
		log.Info("Rolling back to specific revision",
			"current", currentRevision, "target", targetRev)
	}

	// Copy the target RS pod template to the deployment — this is what kubectl rollout undo does
	deploy.Spec.Template.Spec = targetRS.Spec.Template.Spec
	deploy.Spec.Template.Labels = targetRS.Spec.Template.Labels
	if targetRS.Spec.Template.Annotations != nil {
		if deploy.Spec.Template.Annotations == nil {
			deploy.Spec.Template.Annotations = make(map[string]string)
		}
		for k, v := range targetRS.Spec.Template.Annotations {
			deploy.Spec.Template.Annotations[k] = v
		}
	}

	return r.Update(ctx, &deploy)
}

// executeRestartDeployment adds a restart annotation to trigger rolling restart.
// Conforms to the uniform actionExecutor signature; params is unused — the
// rolling restart always uses the standard kubectl.kubernetes.io/restartedAt
// timestamp, which the deployment controller picks up automatically.
func (r *RemediationReconciler) executeRestartDeployment(ctx context.Context, resource platformv1alpha1.ResourceRef, _ map[string]string) error {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return fmt.Errorf("failed to get deployment %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)

	return r.Update(ctx, &deploy)
}

// executePatchConfig patches a ConfigMap with the given params.
func (r *RemediationReconciler) executePatchConfig(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	cmName, ok := params["configmap"]
	if !ok {
		return fmt.Errorf("missing 'configmap' param")
	}

	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: resource.Namespace}, &cm); err != nil {
		return fmt.Errorf("failed to get configmap %s/%s: %w", resource.Namespace, cmName, err)
	}

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	for k, v := range params {
		if k != "configmap" {
			cm.Data[k] = v
		}
	}

	return r.Update(ctx, &cm)
}

// executeAdjustResources patches container resource requests/limits on a Deployment.
func (r *RemediationReconciler) executeAdjustResources(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return fmt.Errorf("failed to get deployment %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	// Determine target container
	containerName := params["container"]
	var targetContainer *corev1.Container
	for i := range deploy.Spec.Template.Spec.Containers {
		c := &deploy.Spec.Template.Spec.Containers[i]
		if containerName == "" || c.Name == containerName {
			targetContainer = c
			break
		}
	}
	if targetContainer == nil {
		if containerName != "" {
			return fmt.Errorf("container %q not found in deployment %s/%s", containerName, resource.Namespace, resource.Name)
		}
		return fmt.Errorf("no containers in deployment %s/%s", resource.Namespace, resource.Name)
	}

	if targetContainer.Resources.Limits == nil {
		targetContainer.Resources.Limits = corev1.ResourceList{}
	}
	if targetContainer.Resources.Requests == nil {
		targetContainer.Resources.Requests = corev1.ResourceList{}
	}

	if v, ok := params["memory_limit"]; ok {
		qty, err := apiresource.ParseQuantity(v)
		if err != nil {
			return fmt.Errorf("invalid memory_limit %q: %w", v, err)
		}
		targetContainer.Resources.Limits[corev1.ResourceMemory] = qty
	}
	if v, ok := params["memory_request"]; ok {
		qty, err := apiresource.ParseQuantity(v)
		if err != nil {
			return fmt.Errorf("invalid memory_request %q: %w", v, err)
		}
		targetContainer.Resources.Requests[corev1.ResourceMemory] = qty
	}
	if v, ok := params["cpu_limit"]; ok {
		qty, err := apiresource.ParseQuantity(v)
		if err != nil {
			return fmt.Errorf("invalid cpu_limit %q: %w", v, err)
		}
		targetContainer.Resources.Limits[corev1.ResourceCPU] = qty
	}
	if v, ok := params["cpu_request"]; ok {
		qty, err := apiresource.ParseQuantity(v)
		if err != nil {
			return fmt.Errorf("invalid cpu_request %q: %w", v, err)
		}
		targetContainer.Resources.Requests[corev1.ResourceCPU] = qty
	}

	// Safety: limits must not be lower than requests
	if memLimit, ok := targetContainer.Resources.Limits[corev1.ResourceMemory]; ok {
		if memReq, reqOk := targetContainer.Resources.Requests[corev1.ResourceMemory]; reqOk {
			if memLimit.Cmp(memReq) < 0 {
				return fmt.Errorf("memory limit (%s) cannot be less than request (%s)", memLimit.String(), memReq.String())
			}
		}
	}
	if cpuLimit, ok := targetContainer.Resources.Limits[corev1.ResourceCPU]; ok {
		if cpuReq, reqOk := targetContainer.Resources.Requests[corev1.ResourceCPU]; reqOk {
			if cpuLimit.Cmp(cpuReq) < 0 {
				return fmt.Errorf("cpu limit (%s) cannot be less than request (%s)", cpuLimit.String(), cpuReq.String())
			}
		}
	}

	return r.Update(ctx, &deploy)
}

// executeDeletePod deletes a specific pod or the most-unhealthy pod owned by the deployment.
func (r *RemediationReconciler) executeDeletePod(ctx context.Context, resource platformv1alpha1.ResourceRef, params map[string]string) error {
	log := log.FromContext(ctx)

	// If a specific pod name is given, delete it directly
	if podName, ok := params["pod"]; ok && podName != "" {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      podName,
				Namespace: resource.Namespace,
			},
		}
		if err := r.Delete(ctx, pod); err != nil {
			return fmt.Errorf("failed to delete pod %s/%s: %w", resource.Namespace, podName, err)
		}
		log.Info("Deleted specific pod", "pod", podName, "namespace", resource.Namespace)
		return nil
	}

	// No specific pod — find the most-unhealthy pod owned by this resource
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	var matchingPods []corev1.Pod
	for i := range podList.Items {
		if isResourcePod(&podList.Items[i], resource) {
			matchingPods = append(matchingPods, podList.Items[i])
		}
	}

	if len(matchingPods) == 0 {
		return fmt.Errorf("no pods found for %s/%s", resource.Kind, resource.Name)
	}

	// Safety: never delete all pods
	if len(matchingPods) <= 1 {
		return fmt.Errorf("only %d pod(s) found for %s/%s, refusing to delete (would cause full outage)",
			len(matchingPods), resource.Kind, resource.Name)
	}

	// Sort by unhealthiness: CrashLoopBackOff first, then by restart count
	sort.Slice(matchingPods, func(i, j int) bool {
		iCrash := isPodCrashLooping(&matchingPods[i])
		jCrash := isPodCrashLooping(&matchingPods[j])
		if iCrash != jCrash {
			return iCrash
		}
		return podRestartCount(&matchingPods[i]) > podRestartCount(&matchingPods[j])
	})

	target := &matchingPods[0]
	if err := r.Delete(ctx, target); err != nil {
		return fmt.Errorf("failed to delete pod %s/%s: %w", resource.Namespace, target.Name, err)
	}

	log.Info("Deleted most-unhealthy pod",
		"pod", target.Name,
		"namespace", resource.Namespace,
		"restarts", podRestartCount(target),
		"crashLooping", isPodCrashLooping(target))
	return nil
}

// isPodCrashLooping checks if any container in the pod is in CrashLoopBackOff.
func isPodCrashLooping(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
			return true
		}
	}
	return false
}

// capturePreflightSnapshot records the resource's current state as evidence before
// executing any remediation actions, enabling manual rollback if needed.
func (r *RemediationReconciler) capturePreflightSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef) (platformv1alpha1.EvidenceItem, error) {
	var sb strings.Builder
	nn := types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}

	writeContainers := func(containers []corev1.Container) {
		for _, c := range containers {
			sb.WriteString(fmt.Sprintf(" container=%s image=%s", c.Name, c.Image))
			if c.Resources.Limits != nil {
				sb.WriteString(fmt.Sprintf(" limits=[cpu=%s mem=%s]",
					c.Resources.Limits.Cpu().String(),
					c.Resources.Limits.Memory().String()))
			}
			if c.Resources.Requests != nil {
				sb.WriteString(fmt.Sprintf(" requests=[cpu=%s mem=%s]",
					c.Resources.Requests.Cpu().String(),
					c.Resources.Requests.Memory().String()))
			}
		}
	}

	switch resource.Kind {
	case "Deployment":
		var deploy appsv1.Deployment
		if err := r.Get(ctx, nn, &deploy); err != nil {
			return platformv1alpha1.EvidenceItem{}, fmt.Errorf("failed to get Deployment for snapshot: %w", err)
		}
		desired := int32(1)
		if deploy.Spec.Replicas != nil {
			desired = *deploy.Spec.Replicas
		}
		sb.WriteString(fmt.Sprintf("Deployment=%s/%s replicas=%d", resource.Namespace, resource.Name, desired))
		writeContainers(deploy.Spec.Template.Spec.Containers)

	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, nn, &sts); err != nil {
			return platformv1alpha1.EvidenceItem{}, fmt.Errorf("failed to get StatefulSet for snapshot: %w", err)
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		sb.WriteString(fmt.Sprintf("StatefulSet=%s/%s replicas=%d strategy=%s", resource.Namespace, resource.Name, desired, sts.Spec.UpdateStrategy.Type))
		writeContainers(sts.Spec.Template.Spec.Containers)

	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := r.Get(ctx, nn, &ds); err != nil {
			return platformv1alpha1.EvidenceItem{}, fmt.Errorf("failed to get DaemonSet for snapshot: %w", err)
		}
		sb.WriteString(fmt.Sprintf("DaemonSet=%s/%s desired=%d ready=%d strategy=%s",
			resource.Namespace, resource.Name, ds.Status.DesiredNumberScheduled, ds.Status.NumberReady, ds.Spec.UpdateStrategy.Type))
		writeContainers(ds.Spec.Template.Spec.Containers)

	case "Job":
		var job batchv1.Job
		if err := r.Get(ctx, nn, &job); err != nil {
			return platformv1alpha1.EvidenceItem{}, fmt.Errorf("failed to get Job for snapshot: %w", err)
		}
		sb.WriteString(fmt.Sprintf("Job=%s/%s active=%d succeeded=%d failed=%d",
			resource.Namespace, resource.Name, job.Status.Active, job.Status.Succeeded, job.Status.Failed))
		if job.Spec.Parallelism != nil {
			sb.WriteString(fmt.Sprintf(" parallelism=%d", *job.Spec.Parallelism))
		}
		writeContainers(job.Spec.Template.Spec.Containers)

	case "CronJob":
		var cj batchv1.CronJob
		if err := r.Get(ctx, nn, &cj); err != nil {
			return platformv1alpha1.EvidenceItem{}, fmt.Errorf("failed to get CronJob for snapshot: %w", err)
		}
		suspended := false
		if cj.Spec.Suspend != nil {
			suspended = *cj.Spec.Suspend
		}
		sb.WriteString(fmt.Sprintf("CronJob=%s/%s schedule=%s suspended=%v active=%d",
			resource.Namespace, resource.Name, cj.Spec.Schedule, suspended, len(cj.Status.Active)))
		writeContainers(cj.Spec.JobTemplate.Spec.Template.Spec.Containers)

	default:
		sb.WriteString(fmt.Sprintf("%s=%s/%s", resource.Kind, resource.Namespace, resource.Name))
	}

	return platformv1alpha1.EvidenceItem{
		Type:      "preflight_snapshot",
		Data:      sb.String(),
		Timestamp: metav1.Now(),
	}, nil
}

// actionValidator runs a per-action-type safety check. `all` is the full plan
// action list, needed by validators that enforce "at most N of this type per
// plan" semantics. Returns nil when the action is acceptable.
type actionValidator func(action platformv1alpha1.RemediationAction, all []platformv1alpha1.RemediationAction) error

// resourceParamKeys are the keys an Adjust*Resources action must populate at
// least one of. Shared by every workload kind that supports resource tuning.
var resourceParamKeys = []string{"memory_limit", "memory_request", "cpu_limit", "cpu_request"}

// requireResourceParams enforces that an Adjust*Resources action specifies at
// least one of the standard resource keys. The action.Type is interpolated in
// the error message so the AI's failure-context loop receives a precise hint.
func requireResourceParams(action platformv1alpha1.RemediationAction, _ []platformv1alpha1.RemediationAction) error {
	for _, key := range resourceParamKeys {
		if _, ok := action.Params[key]; ok {
			return nil
		}
	}
	return fmt.Errorf("%s requires at least one of: %s", action.Type, strings.Join(resourceParamKeys, ", "))
}

// atMostOneValidator returns a validator that fails when more than one action
// of the same type appears in the plan. Used for destructive actions where
// repeating the operation amplifies blast radius (DeletePod, ForceDelete*).
func atMostOneValidator(label string) actionValidator {
	return func(action platformv1alpha1.RemediationAction, all []platformv1alpha1.RemediationAction) error {
		count := 0
		for _, a := range all {
			if a.Type == action.Type {
				count++
			}
		}
		if count > 1 {
			return fmt.Errorf("only one %s action is allowed per remediation plan", label)
		}
		return nil
	}
}

// requireParam returns a validator that fails when the named param is missing.
func requireParam(name string) actionValidator {
	return func(action platformv1alpha1.RemediationAction, _ []platformv1alpha1.RemediationAction) error {
		if _, ok := action.Params[name]; !ok {
			return fmt.Errorf("%s requires '%s' param", action.Type, name)
		}
		return nil
	}
}

// chainValidators executes validators in order and returns the first error.
// Lets a single action type compose multiple independent checks (e.g.
// ForceDeleteStatefulSetPod needs both a pod param AND at-most-one in the plan).
func chainValidators(vs ...actionValidator) actionValidator {
	return func(action platformv1alpha1.RemediationAction, all []platformv1alpha1.RemediationAction) error {
		for _, v := range vs {
			if err := v(action, all); err != nil {
				return err
			}
		}
		return nil
	}
}

// scaleToZeroRequiresContainment is the safety guard that blocks accidental
// destructive scale-downs. The "containment=true" opt-in is the deliberate
// stop-the-bleeding signal — see AnalyzeIssue prompt rules in handler_analysis.go.
//
// The error message preserves the pre-existing user-visible wording verbatim
// (Deployment: no label; StatefulSet: labeled). Tests, runbooks and operator
// log parsers grep for those exact substrings, so this asymmetry is held
// stable on purpose — uniformising the wording is its own future change.
func scaleToZeroRequiresContainment(errorMsg string) actionValidator {
	return func(action platformv1alpha1.RemediationAction, _ []platformv1alpha1.RemediationAction) error {
		replicas, ok := action.Params["replicas"]
		if !ok {
			return nil
		}
		n, err := strconv.Atoi(replicas)
		if err != nil || n != 0 {
			return nil
		}
		if action.Params["containment"] != "true" {
			return fmt.Errorf("%s", errorMsg)
		}
		return nil
	}
}

const (
	errScaleDeploymentToZero  = `scaling to 0 replicas is not allowed (destructive) — set params.containment="true" to opt in when this is a deliberate stop-the-bleeding action for an unrecoverable app-level bug`
	errScaleStatefulSetToZero = `scaling StatefulSet to 0 replicas is not allowed (destructive) — set params.containment="true" to opt in when this is a deliberate stop-the-bleeding action for an unrecoverable app-level bug`
)

// requireConfirm enforces an explicit "confirm=true" param on highly destructive
// actions (PVC recreation). Modeled after kubectl --force semantics.
func requireConfirm(action platformv1alpha1.RemediationAction, _ []platformv1alpha1.RemediationAction) error {
	if action.Params["confirm"] != "true" {
		return fmt.Errorf("%s requires confirm=true (destructive)", action.Type)
	}
	return nil
}

// rejectCustom blocks Custom actions from being auto-approved — they always
// require manual review via the approval workflow.
func rejectCustom(_ platformv1alpha1.RemediationAction, _ []platformv1alpha1.RemediationAction) error {
	return fmt.Errorf("custom actions require manual approval")
}

// safetyValidators maps an action type to its safety check. Actions absent
// from the table need no per-plan validation (e.g. RestartDeployment is
// inherently safe). Adding a new validator here keeps the dispatcher in
// validateSafetyConstraints at O(1) cyclomatic complexity.
var safetyValidators = map[platformv1alpha1.RemediationActionType]actionValidator{
	platformv1alpha1.ActionScaleDeployment:             scaleToZeroRequiresContainment(errScaleDeploymentToZero),
	platformv1alpha1.ActionScaleStatefulSet:            scaleToZeroRequiresContainment(errScaleStatefulSetToZero),
	platformv1alpha1.ActionAdjustResources:             requireResourceParams,
	platformv1alpha1.ActionAdjustStatefulSetResources:  requireResourceParams,
	platformv1alpha1.ActionAdjustDaemonSetResources:    requireResourceParams,
	platformv1alpha1.ActionAdjustJobResources:          requireResourceParams,
	platformv1alpha1.ActionAdjustCronJobResources:      requireResourceParams,
	platformv1alpha1.ActionDeletePod:                   atMostOneValidator("DeletePod"),
	platformv1alpha1.ActionDeleteStatefulSetPod:        atMostOneValidator("DeleteStatefulSetPod"),
	platformv1alpha1.ActionDeleteDaemonSetPod:          atMostOneValidator("DeleteDaemonSetPod"),
	platformv1alpha1.ActionForceDeleteJobPods:          atMostOneValidator("ForceDeleteJobPods"),
	platformv1alpha1.ActionDeleteCronJobActiveJobs:     atMostOneValidator("DeleteCronJobActiveJobs"),
	platformv1alpha1.ActionForceDeleteStatefulSetPod:   chainValidators(requireParam("pod"), atMostOneValidator("ForceDeleteStatefulSetPod")),
	platformv1alpha1.ActionRecreateStatefulSetPVC:      requireConfirm,
	platformv1alpha1.ActionCordonAndDeleteDaemonSetPod: requireParam("node"),
	platformv1alpha1.ActionReplaceCronJobTemplate:      requireParam("configmap"),
	platformv1alpha1.ActionCustom:                      rejectCustom,
}

// validateSafetyConstraints runs each action through its registered safety
// validator (if any) and returns the first violation. The validator registry
// pattern keeps this dispatcher trivial regardless of how many action kinds
// the platform supports.
func (r *RemediationReconciler) validateSafetyConstraints(actions []platformv1alpha1.RemediationAction, constraints []string) error {
	for _, action := range actions {
		v, ok := safetyValidators[action.Type]
		if !ok {
			continue
		}
		if err := v(action, actions); err != nil {
			return err
		}
	}
	return nil
}

// verifyResourceHealth checks health for any supported resource kind.
func (r *RemediationReconciler) verifyResourceHealth(ctx context.Context, resource platformv1alpha1.ResourceRef) (bool, error) {
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
		healthy := deploy.Status.ReadyReplicas >= desired &&
			deploy.Status.UpdatedReplicas >= desired &&
			deploy.Status.UnavailableReplicas == 0
		return healthy, nil

	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &sts); err != nil {
			return false, err
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		healthy := sts.Status.ReadyReplicas >= desired && sts.Status.CurrentReplicas >= desired
		return healthy, nil

	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &ds); err != nil {
			return false, err
		}
		healthy := ds.Status.NumberReady >= ds.Status.DesiredNumberScheduled &&
			ds.Status.NumberUnavailable == 0
		return healthy, nil

	case "Job":
		var job batchv1.Job
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &job); err != nil {
			return false, err
		}
		// Job is healthy if it completed successfully
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == "True" {
				return true, nil
			}
		}
		// Also consider it healthy if active pods > 0 (still running)
		return job.Status.Active > 0, nil

	case "CronJob":
		var cj batchv1.CronJob
		if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &cj); err != nil {
			return false, err
		}
		// CronJob is healthy if suspended intentionally, has recent success, or has active jobs
		if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
			return true, nil
		}
		if cj.Status.LastSuccessfulTime != nil || len(cj.Status.Active) > 0 {
			return true, nil
		}
		return false, nil

	default:
		// For unknown resource kinds, check if pods are running
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
			return false, err
		}
		readyPods := 0
		totalPods := 0
		for i := range podList.Items {
			if isResourcePod(&podList.Items[i], resource) {
				totalPods++
				if isPodReady(&podList.Items[i]) {
					readyPods++
				}
			}
		}
		if totalPods == 0 {
			return false, nil
		}
		return readyPods > 0, nil
	}
}

// recordPatternResolution records a successful resolution in the PatternStore for Decision Engine learning.
func (r *RemediationReconciler) recordPatternResolution(ctx context.Context, plan *platformv1alpha1.RemediationPlan) {
	if r.PatternStore == nil {
		return
	}
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err != nil {
		return
	}
	if err := r.PatternStore.RecordResolution(ctx, &issue, plan); err != nil {
		log.FromContext(ctx).Error(err, "Failed to record resolution pattern", "plan", plan.Name)
	}
}

// recordPatternFailure records a failed remediation in the PatternStore.
func (r *RemediationReconciler) recordPatternFailure(ctx context.Context, plan *platformv1alpha1.RemediationPlan) {
	if r.PatternStore == nil {
		return
	}
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: plan.Namespace}, &issue); err != nil {
		return
	}
	if err := r.PatternStore.RecordFailure(ctx, &issue, plan); err != nil {
		log.FromContext(ctx).Error(err, "Failed to record failure pattern", "plan", plan.Name)
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *RemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.RemediationPlan{}).
		Complete(r)
}
