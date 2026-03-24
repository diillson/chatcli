package controllers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

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

	// Check if the approval annotation was removed (approved or rejected by ApprovalReconciler)
	if !IsApprovalPending(plan) {
		// Approval was processed — check if the approval request was approved or rejected
		arName := fmt.Sprintf("approval-%s", plan.Name)
		var ar platformv1alpha1.ApprovalRequest
		if err := r.Get(ctx, types.NamespacedName{Name: arName, Namespace: plan.Namespace}, &ar); err != nil {
			// ApprovalRequest not found — may have been cleaned up, proceed with execution
			log.Info("Approval request not found, proceeding with execution", "plan", plan.Name)
		} else {
			switch ar.Status.State {
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
			case platformv1alpha1.ApprovalStateApproved:
				approvedBy, _ := lastDecision(ar.Status.Decisions)
				log.Info("Approval granted, proceeding with execution", "plan", plan.Name, "approvedBy", approvedBy)
			case platformv1alpha1.ApprovalStatePending:
				// Still waiting
				log.Info("Still waiting for approval decision", "plan", plan.Name)
				return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
			}
		}

		// Transition to Executing
		now := metav1.Now()
		plan.Status.State = platformv1alpha1.RemediationStateExecuting
		plan.Status.StartedAt = &now
		plan.Status.Result = ""
		return ctrl.Result{Requeue: true}, r.Status().Update(ctx, plan)
	}

	log.Info("Still waiting for approval", "plan", plan.Name)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
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
			// Brief stabilization wait before health check
			time.Sleep(5 * time.Second)

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
		if action.Type == platformv1alpha1.ActionCordonNode || action.Type == platformv1alpha1.ActionDrainNode {
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
func (r *RemediationReconciler) executeAction(ctx context.Context, resource platformv1alpha1.ResourceRef, action *platformv1alpha1.RemediationAction) error {
	switch action.Type {
	case platformv1alpha1.ActionScaleDeployment:
		return r.executeScaleDeployment(ctx, resource, action.Params)
	case platformv1alpha1.ActionRollbackDeployment:
		return r.executeRollbackDeployment(ctx, resource, action.Params)
	case platformv1alpha1.ActionRestartDeployment:
		return r.executeRestartDeployment(ctx, resource)
	case platformv1alpha1.ActionPatchConfig:
		return r.executePatchConfig(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustResources:
		return r.executeAdjustResources(ctx, resource, action.Params)
	case platformv1alpha1.ActionDeletePod:
		return r.executeDeletePod(ctx, resource, action.Params)
	case platformv1alpha1.ActionHelmRollback:
		return r.executeHelmRollback(ctx, resource, action.Params)
	case platformv1alpha1.ActionArgoSyncApp:
		return r.executeArgoSyncApp(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustHPA:
		return r.executeAdjustHPA(ctx, resource, action.Params)
	case platformv1alpha1.ActionRestartStatefulSetPod:
		return r.executeRestartStatefulSetPod(ctx, resource, action.Params)
	case platformv1alpha1.ActionCordonNode:
		return r.executeCordonNode(ctx, resource, action.Params)
	case platformv1alpha1.ActionDrainNode:
		return r.executeDrainNode(ctx, resource, action.Params)
	case platformv1alpha1.ActionResizePVC:
		return r.executeResizePVC(ctx, resource, action.Params)
	case platformv1alpha1.ActionRotateSecret:
		return r.executeRotateSecret(ctx, resource, action.Params)
	case platformv1alpha1.ActionExecDiagnostic:
		return r.executeExecDiagnostic(ctx, resource, action.Params)
	case platformv1alpha1.ActionUpdateIngress:
		return r.executeUpdateIngress(ctx, resource, action.Params)
	case platformv1alpha1.ActionPatchNetworkPolicy:
		return r.executePatchNetworkPolicy(ctx, resource, action.Params)
	case platformv1alpha1.ActionApplyManifest:
		return r.executeApplyManifest(ctx, resource, action.Params)

	// --- StatefulSet Actions ---
	case platformv1alpha1.ActionScaleStatefulSet:
		return r.executeScaleStatefulSet(ctx, resource, action.Params)
	case platformv1alpha1.ActionRestartStatefulSet:
		return r.executeRestartStatefulSet(ctx, resource)
	case platformv1alpha1.ActionRollbackStatefulSet:
		return r.executeRollbackStatefulSet(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustStatefulSetResources:
		return r.executeAdjustStatefulSetResources(ctx, resource, action.Params)
	case platformv1alpha1.ActionDeleteStatefulSetPod:
		return r.executeDeleteStatefulSetPod(ctx, resource, action.Params)
	case platformv1alpha1.ActionForceDeleteStatefulSetPod:
		return r.executeForceDeleteStatefulSetPod(ctx, resource, action.Params)
	case platformv1alpha1.ActionUpdateStatefulSetStrategy:
		return r.executeUpdateStatefulSetStrategy(ctx, resource, action.Params)
	case platformv1alpha1.ActionRecreateStatefulSetPVC:
		return r.executeRecreateStatefulSetPVC(ctx, resource, action.Params)
	case platformv1alpha1.ActionPartitionStatefulSetUpdate:
		return r.executePartitionStatefulSetUpdate(ctx, resource, action.Params)

	// --- DaemonSet Actions ---
	case platformv1alpha1.ActionRestartDaemonSet:
		return r.executeRestartDaemonSet(ctx, resource)
	case platformv1alpha1.ActionRollbackDaemonSet:
		return r.executeRollbackDaemonSet(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustDaemonSetResources:
		return r.executeAdjustDaemonSetResources(ctx, resource, action.Params)
	case platformv1alpha1.ActionDeleteDaemonSetPod:
		return r.executeDeleteDaemonSetPod(ctx, resource, action.Params)
	case platformv1alpha1.ActionUpdateDaemonSetStrategy:
		return r.executeUpdateDaemonSetStrategy(ctx, resource, action.Params)
	case platformv1alpha1.ActionPauseDaemonSetRollout:
		return r.executePauseDaemonSetRollout(ctx, resource)
	case platformv1alpha1.ActionCordonAndDeleteDaemonSetPod:
		return r.executeCordonAndDeleteDaemonSetPod(ctx, resource, action.Params)

	// --- Job Actions ---
	case platformv1alpha1.ActionRetryJob:
		return r.executeRetryJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustJobResources:
		return r.executeAdjustJobResources(ctx, resource, action.Params)
	case platformv1alpha1.ActionDeleteFailedJob:
		return r.executeDeleteFailedJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionSuspendJob:
		return r.executeSuspendJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionResumeJob:
		return r.executeResumeJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustJobParallelism:
		return r.executeAdjustJobParallelism(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustJobDeadline:
		return r.executeAdjustJobDeadline(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustJobBackoffLimit:
		return r.executeAdjustJobBackoffLimit(ctx, resource, action.Params)
	case platformv1alpha1.ActionForceDeleteJobPods:
		return r.executeForceDeleteJobPods(ctx, resource, action.Params)

	// --- CronJob Actions ---
	case platformv1alpha1.ActionSuspendCronJob:
		return r.executeSuspendCronJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionResumeCronJob:
		return r.executeResumeCronJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionTriggerCronJob:
		return r.executeTriggerCronJob(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustCronJobResources:
		return r.executeAdjustCronJobResources(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustCronJobSchedule:
		return r.executeAdjustCronJobSchedule(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustCronJobDeadline:
		return r.executeAdjustCronJobDeadline(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustCronJobHistory:
		return r.executeAdjustCronJobHistory(ctx, resource, action.Params)
	case platformv1alpha1.ActionAdjustCronJobConcurrency:
		return r.executeAdjustCronJobConcurrency(ctx, resource, action.Params)
	case platformv1alpha1.ActionDeleteCronJobActiveJobs:
		return r.executeDeleteCronJobActiveJobs(ctx, resource, action.Params)
	case platformv1alpha1.ActionReplaceCronJobTemplate:
		return r.executeReplaceCronJobTemplate(ctx, resource, action.Params)

	default:
		return fmt.Errorf("unsupported action type: %s", action.Type)
	}
}

// handleAgenticExecuting runs one step of the AI-driven agentic remediation loop.
// Each reconcile: ask AI → execute action → record observation → requeue for next step.
func (r *RemediationReconciler) handleAgenticExecuting(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Safety: check max steps
	maxSteps := plan.Spec.AgenticMaxSteps
	if maxSteps == 0 {
		maxSteps = 10
	}
	currentStep := int32(len(plan.Spec.AgenticHistory)) + 1
	if currentStep > maxSteps {
		now := metav1.Now()
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.Result = fmt.Sprintf("Agentic loop exceeded max steps (%d)", maxSteps)
		plan.Status.CompletedAt = &now
		return ctrl.Result{}, r.Status().Update(ctx, plan)
	}

	// Safety: check timeout
	if plan.Status.AgenticStartedAt != nil {
		if time.Since(plan.Status.AgenticStartedAt.Time) > agenticTimeout {
			now := metav1.Now()
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.Result = "Agentic loop timed out (10 minutes)"
			plan.Status.CompletedAt = &now
			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}
	}

	// Get parent issue for resource context
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

	// Refresh K8s context
	kubeCtx := ""
	if r.ContextBuilder != nil {
		var err error
		kubeCtx, err = r.ContextBuilder.BuildContext(ctx, resource)
		if err != nil {
			log.Info("Failed to build K8s context for agentic step", "error", err)
		}
	}

	// Check server connectivity
	if r.ServerClient == nil || !r.ServerClient.IsConnected() {
		log.Info("Server not connected, requeuing agentic step")
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Build history for RPC
	var history []*pb.AgenticHistoryEntry
	for _, step := range plan.Spec.AgenticHistory {
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

	// Resolve provider/model from the connected Instance CR
	provider, model := resolveInstanceProvider(ctx, r.Client)

	// Call AgenticStep RPC
	resp, err := r.ServerClient.AgenticStep(ctx, &pb.AgenticStepRequest{
		IssueName:         issue.Name,
		Namespace:         issue.Namespace,
		ResourceKind:      resource.Kind,
		ResourceName:      resource.Name,
		SignalType:        issue.Spec.SignalType,
		Severity:          string(issue.Spec.Severity),
		Description:       issue.Spec.Description,
		RiskScore:         issue.Spec.RiskScore,
		Provider:          provider,
		Model:             model,
		KubernetesContext: kubeCtx,
		History:           history,
		MaxSteps:          maxSteps,
		CurrentStep:       currentStep,
	})
	if err != nil {
		log.Error(err, "AgenticStep RPC failed, requeuing")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// If resolved — transition to Verifying
	if resp.Resolved {
		log.Info("AI determined issue resolved", "plan", plan.Name, "step", currentStep)

		// Store postmortem data in annotations
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

		// Record final reasoning step (no action)
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

	// If next_action is nil but not resolved — AI is observing or stuck
	if resp.NextAction == nil || resp.NextAction.Action == "" {
		// Record observation step
		plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
			StepNumber:  currentStep,
			AIMessage:   resp.Reasoning,
			Observation: "Observation step — no action taken",
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

		// Set status fields AFTER r.Update() — spec update resets in-memory status
		plan.Status.AgenticStepCount = agenticStepCount
		if needStartedAt {
			now := metav1.Now()
			plan.Status.AgenticStartedAt = &now
		}
		if err := r.Status().Update(ctx, plan); err != nil {
			return ctrl.Result{}, err
		}

		log.Info("Agentic observation step", "plan", plan.Name, "step", currentStep)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Map next_action to RemediationAction
	nextAction := &platformv1alpha1.RemediationAction{
		Type:   mapActionType(resp.NextAction.Action),
		Params: resp.NextAction.Params,
	}

	// Execute the action
	log.Info("Agentic executing action", "plan", plan.Name, "step", currentStep,
		"action", nextAction.Type)

	observation := ""
	execErr := r.executeAction(ctx, resource, nextAction)
	if execErr != nil {
		observation = fmt.Sprintf("FAILED: %v", execErr)
		remediationsTotal.WithLabelValues(string(nextAction.Type), "failed").Inc()
	} else {
		observation = fmt.Sprintf("SUCCESS: %s executed successfully", nextAction.Type)
		remediationsTotal.WithLabelValues(string(nextAction.Type), "success").Inc()
	}

	// Record the step
	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber:  currentStep,
		AIMessage:   resp.Reasoning,
		Action:      nextAction,
		Observation: observation,
		Timestamp:   metav1.Now(),
	})

	agenticStepCount := int32(len(plan.Spec.AgenticHistory))
	needStartedAt := plan.Status.AgenticStartedAt == nil

	// Persist spec (history) then status
	if err := r.Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	// Set status fields AFTER r.Update() — spec update resets in-memory status
	plan.Status.AgenticStepCount = agenticStepCount
	if needStartedAt {
		now := metav1.Now()
		plan.Status.AgenticStartedAt = &now
	}
	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Agentic step completed", "plan", plan.Name, "step", currentStep,
		"action", nextAction.Type, "observation", observation)

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
func (r *RemediationReconciler) executeRestartDeployment(ctx context.Context, resource platformv1alpha1.ResourceRef) error {
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

// validateSafetyConstraints checks that actions don't violate safety rules.
func (r *RemediationReconciler) validateSafetyConstraints(actions []platformv1alpha1.RemediationAction, constraints []string) error {
	for _, action := range actions {
		switch action.Type {
		case platformv1alpha1.ActionScaleDeployment:
			if replicas, ok := action.Params["replicas"]; ok {
				n, err := strconv.Atoi(replicas)
				if err == nil && n == 0 {
					return fmt.Errorf("scaling to 0 replicas is not allowed (destructive)")
				}
			}
		case platformv1alpha1.ActionAdjustResources:
			hasResource := false
			for _, key := range []string{"memory_limit", "memory_request", "cpu_limit", "cpu_request"} {
				if _, ok := action.Params[key]; ok {
					hasResource = true
					break
				}
			}
			if !hasResource {
				return fmt.Errorf("AdjustResources requires at least one of: memory_limit, memory_request, cpu_limit, cpu_request")
			}
		case platformv1alpha1.ActionDeletePod:
			deletePodCount := 0
			for _, a := range actions {
				if a.Type == platformv1alpha1.ActionDeletePod {
					deletePodCount++
				}
			}
			if deletePodCount > 1 {
				return fmt.Errorf("only one DeletePod action is allowed per remediation plan")
			}
		case platformv1alpha1.ActionScaleStatefulSet:
			if replicas, ok := action.Params["replicas"]; ok {
				n, err := strconv.Atoi(replicas)
				if err == nil && n == 0 {
					return fmt.Errorf("scaling StatefulSet to 0 replicas is not allowed (destructive)")
				}
			}
		case platformv1alpha1.ActionAdjustStatefulSetResources,
			platformv1alpha1.ActionAdjustDaemonSetResources,
			platformv1alpha1.ActionAdjustJobResources,
			platformv1alpha1.ActionAdjustCronJobResources:
			hasResource := false
			for _, key := range []string{"memory_limit", "memory_request", "cpu_limit", "cpu_request"} {
				if _, ok := action.Params[key]; ok {
					hasResource = true
					break
				}
			}
			if !hasResource {
				return fmt.Errorf("%s requires at least one of: memory_limit, memory_request, cpu_limit, cpu_request", action.Type)
			}
		case platformv1alpha1.ActionDeleteStatefulSetPod, platformv1alpha1.ActionDeleteDaemonSetPod:
			deleteCount := 0
			for _, a := range actions {
				if a.Type == action.Type {
					deleteCount++
				}
			}
			if deleteCount > 1 {
				return fmt.Errorf("only one %s action is allowed per remediation plan", action.Type)
			}
		case platformv1alpha1.ActionForceDeleteStatefulSetPod:
			if _, ok := action.Params["pod"]; !ok {
				return fmt.Errorf("ForceDeleteStatefulSetPod requires 'pod' param")
			}
			forceCount := 0
			for _, a := range actions {
				if a.Type == platformv1alpha1.ActionForceDeleteStatefulSetPod {
					forceCount++
				}
			}
			if forceCount > 1 {
				return fmt.Errorf("only one ForceDeleteStatefulSetPod is allowed per plan")
			}
		case platformv1alpha1.ActionRecreateStatefulSetPVC:
			if action.Params["confirm"] != "true" {
				return fmt.Errorf("RecreateStatefulSetPVC requires confirm=true (destructive)")
			}
		case platformv1alpha1.ActionCordonAndDeleteDaemonSetPod:
			if _, ok := action.Params["node"]; !ok {
				return fmt.Errorf("CordonAndDeleteDaemonSetPod requires 'node' param")
			}
		case platformv1alpha1.ActionForceDeleteJobPods:
			forceCount := 0
			for _, a := range actions {
				if a.Type == platformv1alpha1.ActionForceDeleteJobPods {
					forceCount++
				}
			}
			if forceCount > 1 {
				return fmt.Errorf("only one ForceDeleteJobPods is allowed per plan")
			}
		case platformv1alpha1.ActionDeleteCronJobActiveJobs:
			deleteCount := 0
			for _, a := range actions {
				if a.Type == platformv1alpha1.ActionDeleteCronJobActiveJobs {
					deleteCount++
				}
			}
			if deleteCount > 1 {
				return fmt.Errorf("only one DeleteCronJobActiveJobs is allowed per plan")
			}
		case platformv1alpha1.ActionReplaceCronJobTemplate:
			if _, ok := action.Params["configmap"]; !ok {
				return fmt.Errorf("ReplaceCronJobTemplate requires 'configmap' param")
			}
		case platformv1alpha1.ActionCustom:
			return fmt.Errorf("custom actions require manual approval")
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
