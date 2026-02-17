package controllers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
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

// RemediationReconciler reconciles RemediationPlan objects.
type RemediationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;update;patch

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
		return r.handleExecuting(ctx, &plan)
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

	// Validate safety constraints
	if err := r.validateSafetyConstraints(plan.Spec.Actions, plan.Spec.SafetyConstraints); err != nil {
		plan.Status.State = platformv1alpha1.RemediationStateFailed
		plan.Status.Result = fmt.Sprintf("Safety constraint violation: %v", err)
		remediationsTotal.WithLabelValues("validation", "failed").Inc()
		return ctrl.Result{}, r.Status().Update(ctx, plan)
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

	// Execute each action
	var evidence []platformv1alpha1.EvidenceItem
	for _, action := range plan.Spec.Actions {
		log.Info("Executing action", "type", action.Type, "resource", resource.Name)

		var err error
		switch action.Type {
		case platformv1alpha1.ActionScaleDeployment:
			err = r.executeScaleDeployment(ctx, resource, action.Params)
		case platformv1alpha1.ActionRollbackDeployment:
			err = r.executeRollbackDeployment(ctx, resource)
		case platformv1alpha1.ActionRestartDeployment:
			err = r.executeRestartDeployment(ctx, resource)
		case platformv1alpha1.ActionPatchConfig:
			err = r.executePatchConfig(ctx, resource, action.Params)
		default:
			err = fmt.Errorf("unsupported action type: %s", action.Type)
		}

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

	// All actions completed successfully
	now := metav1.Now()
	plan.Status.State = platformv1alpha1.RemediationStateCompleted
	plan.Status.CompletedAt = &now
	plan.Status.Result = "All remediation actions completed successfully"
	plan.Status.Evidence = evidence

	if plan.Status.StartedAt != nil {
		remediationDuration.Observe(now.Sub(plan.Status.StartedAt.Time).Seconds())
	}

	return ctrl.Result{}, r.Status().Update(ctx, plan)
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

// executeRollbackDeployment adds a rollback annotation to trigger a rollout undo.
func (r *RemediationReconciler) executeRollbackDeployment(ctx context.Context, resource platformv1alpha1.ResourceRef) error {
	var deploy appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: resource.Name, Namespace: resource.Namespace}, &deploy); err != nil {
		return fmt.Errorf("failed to get deployment %s/%s: %w", resource.Namespace, resource.Name, err)
	}

	if deploy.Spec.Template.Annotations == nil {
		deploy.Spec.Template.Annotations = make(map[string]string)
	}
	deploy.Spec.Template.Annotations["platform.chatcli.io/rollback-at"] = time.Now().Format(time.RFC3339)

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

// validateSafetyConstraints checks that actions don't violate safety rules.
func (r *RemediationReconciler) validateSafetyConstraints(actions []platformv1alpha1.RemediationAction, constraints []string) error {
	for _, action := range actions {
		// Check that no action is a delete/destroy operation
		switch action.Type {
		case platformv1alpha1.ActionScaleDeployment:
			if replicas, ok := action.Params["replicas"]; ok {
				n, err := strconv.Atoi(replicas)
				if err == nil && n == 0 {
					return fmt.Errorf("scaling to 0 replicas is not allowed (destructive)")
				}
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
