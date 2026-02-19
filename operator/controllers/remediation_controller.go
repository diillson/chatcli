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

	switch plan.Status.State {
	case "", platformv1alpha1.RemediationStatePending:
		return r.handlePending(ctx, &plan)
	case platformv1alpha1.RemediationStateExecuting:
		if plan.Spec.AgenticMode {
			return r.handleAgenticExecuting(ctx, &plan)
		}
		return r.handleExecuting(ctx, &plan)
	case platformv1alpha1.RemediationStateVerifying:
		return r.handleVerifying(ctx, &plan)
	case platformv1alpha1.RemediationStateCompleted, platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
		// Terminal states
		return ctrl.Result{}, nil
	}

	log.Info("Unknown remediation state", "state", plan.Status.State)
	return ctrl.Result{}, nil
}

func (r *RemediationReconciler) handlePending(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Starting remediation", "plan", plan.Name, "attempt", plan.Spec.Attempt)

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

	// Capture pre-flight snapshot for manual rollback reference
	var evidence []platformv1alpha1.EvidenceItem
	if snapshot, err := r.capturePreflightSnapshot(ctx, resource); err != nil {
		log.Info("Failed to capture preflight snapshot", "error", err)
	} else {
		evidence = append(evidence, snapshot)
	}

	// Execute each action
	for _, action := range plan.Spec.Actions {
		log.Info("Executing action", "type", action.Type, "resource", resource.Name)

		err := r.executeAction(ctx, resource, &action)

		if err != nil {
			log.Error(err, "Action failed", "type", action.Type)
			now := metav1.Now()
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.CompletedAt = &now
			plan.Status.Result = fmt.Sprintf("Action %s failed: %v", action.Type, err)
			plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
				Type:      "error",
				Data:      err.Error(),
				Timestamp: now,
			})

			remediationsTotal.WithLabelValues(string(action.Type), "failed").Inc()
			if plan.Status.StartedAt != nil {
				remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
			}

			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}

		evidence = append(evidence, platformv1alpha1.EvidenceItem{
			Type:      "action_completed",
			Data:      fmt.Sprintf("Action %s executed successfully", action.Type),
			Timestamp: metav1.Now(),
		})
		remediationsTotal.WithLabelValues(string(action.Type), "success").Inc()
	}

	// All actions executed — transition to Verifying to confirm deployment health.
	now := metav1.Now()
	plan.Status.State = platformv1alpha1.RemediationStateVerifying
	plan.Status.ActionsCompletedAt = &now
	plan.Status.Evidence = evidence

	log.Info("Actions executed, verifying deployment health", "plan", plan.Name)

	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
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

	// Check deployment health.
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		if errors.IsNotFound(err) {
			plan.Status.State = platformv1alpha1.RemediationStateFailed
			plan.Status.Result = fmt.Sprintf("Deployment %s/%s not found during verification", resource.Namespace, resource.Name)
			return ctrl.Result{}, r.Status().Update(ctx, plan)
		}
		return ctrl.Result{}, err
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	healthy := deploy.Status.ReadyReplicas >= desired &&
		deploy.Status.UpdatedReplicas >= desired &&
		deploy.Status.UnavailableReplicas == 0

	if healthy {
		now := metav1.Now()
		plan.Status.State = platformv1alpha1.RemediationStateCompleted
		plan.Status.CompletedAt = &now
		plan.Status.Result = fmt.Sprintf("Remediation verified: %d/%d replicas ready, deployment healthy",
			deploy.Status.ReadyReplicas, desired)
		plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
			Type:      "verification_passed",
			Data:      fmt.Sprintf("ReadyReplicas=%d UpdatedReplicas=%d UnavailableReplicas=%d", deploy.Status.ReadyReplicas, deploy.Status.UpdatedReplicas, deploy.Status.UnavailableReplicas),
			Timestamp: now,
		})

		if plan.Status.StartedAt != nil {
			remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
		}

		log.Info("Verification passed, deployment healthy", "plan", plan.Name, "readyReplicas", deploy.Status.ReadyReplicas)
		return ctrl.Result{}, r.Status().Update(ctx, plan)
	}

	// Check if verification timeout exceeded.
	if plan.Status.ActionsCompletedAt != nil && time.Since(plan.Status.ActionsCompletedAt.Time) > verificationTimeout {
		now := metav1.Now()
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.CompletedAt = &now
		plan.Status.Result = fmt.Sprintf("Verification failed: deployment unhealthy after %s (ready=%d/%d, unavailable=%d)",
			verificationTimeout, deploy.Status.ReadyReplicas, desired, deploy.Status.UnavailableReplicas)
		plan.Status.Evidence = append(plan.Status.Evidence, platformv1alpha1.EvidenceItem{
			Type:      "verification_failed",
			Data:      fmt.Sprintf("ReadyReplicas=%d UpdatedReplicas=%d UnavailableReplicas=%d desired=%d", deploy.Status.ReadyReplicas, deploy.Status.UpdatedReplicas, deploy.Status.UnavailableReplicas, desired),
			Timestamp: now,
		})

		if plan.Status.StartedAt != nil {
			remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
		}
		remediationsTotal.WithLabelValues("verification", "failed").Inc()

		log.Info("Verification failed, deployment still unhealthy", "plan", plan.Name,
			"readyReplicas", deploy.Status.ReadyReplicas, "unavailable", deploy.Status.UnavailableReplicas)
		return ctrl.Result{}, r.Status().Update(ctx, plan)
	}

	// Still waiting — requeue.
	log.Info("Verifying deployment health", "plan", plan.Name,
		"readyReplicas", deploy.Status.ReadyReplicas, "desired", desired,
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

	// No specific pod — find the most-unhealthy pod owned by this deployment
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	var matchingPods []corev1.Pod
	for i := range podList.Items {
		if isPodOwnedByDeployment(&podList.Items[i], resource.Name) {
			matchingPods = append(matchingPods, podList.Items[i])
		}
	}

	if len(matchingPods) == 0 {
		return fmt.Errorf("no pods found for deployment %s/%s", resource.Namespace, resource.Name)
	}

	// Safety: never delete all pods
	if len(matchingPods) <= 1 {
		return fmt.Errorf("only %d pod(s) found for deployment %s/%s, refusing to delete (would cause full outage)",
			len(matchingPods), resource.Namespace, resource.Name)
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

// capturePreflightSnapshot records the deployment's current state as evidence before
// executing any remediation actions, enabling manual rollback if needed.
func (r *RemediationReconciler) capturePreflightSnapshot(ctx context.Context, resource platformv1alpha1.ResourceRef) (platformv1alpha1.EvidenceItem, error) {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return platformv1alpha1.EvidenceItem{}, fmt.Errorf("failed to get deployment for snapshot: %w", err)
	}

	desired := int32(1)
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("deployment=%s/%s replicas=%d", resource.Namespace, resource.Name, desired))
	for _, c := range deploy.Spec.Template.Spec.Containers {
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
		case platformv1alpha1.ActionCustom:
			return fmt.Errorf("custom actions require manual approval")
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RemediationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.RemediationPlan{}).
		Complete(r)
}
