package controllers

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
	chaosExperimentsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "chaos_experiments_total",
		Help:      "Total chaos experiments by type and result.",
	}, []string{"type", "result"})

	chaosRecoveryTimeSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "chaos_recovery_time_seconds",
		Help:      "Time for target to recover after chaos experiment.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 12),
	}, []string{"type"})

	chaosPodsAffectedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "chaos_pods_affected_total",
		Help:      "Total pods affected by chaos experiments by type.",
	}, []string{"type"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		chaosExperimentsTotal,
		chaosRecoveryTimeSeconds,
		chaosPodsAffectedTotal,
	)
}

const (
	chaosLabelExperiment = "platform.chatcli.io/chaos-experiment"
	chaosLabelRole       = "platform.chatcli.io/chaos-role"
	chaosAnnotationDelay = "platform.chatcli.io/chaos-network-delay"
	chaosAnnotationLoss  = "platform.chatcli.io/chaos-network-loss"
	chaosStressImage     = "alexeiled/stress-ng:latest"
)

// ChaosReconciler reconciles ChaosExperiment objects.
type ChaosReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=chaosexperiments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=chaosexperiments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=approvalrequests,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete

func (r *ChaosReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var experiment platformv1alpha1.ChaosExperiment
	if err := r.Get(ctx, req.NamespacedName, &experiment); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !experiment.Spec.Enabled {
		logger.Info("chaos experiment is disabled, skipping", "name", experiment.Name)
		return ctrl.Result{}, nil
	}

	switch experiment.Status.State {
	case platformv1alpha1.ChaosStatePending, "":
		return r.reconcilePending(ctx, &experiment)
	case platformv1alpha1.ChaosStateRunning:
		return r.reconcileRunning(ctx, &experiment)
	case platformv1alpha1.ChaosStateAborted:
		return r.reconcileAborted(ctx, &experiment)
	case platformv1alpha1.ChaosStateCompleted, platformv1alpha1.ChaosStateFailed:
		// Terminal states — nothing to do.
		return ctrl.Result{}, nil
	default:
		logger.Info("unknown chaos experiment state", "state", experiment.Status.State)
		return ctrl.Result{}, nil
	}
}

// reconcilePending validates safety checks, captures pre-experiment snapshot, and transitions to Running.
func (r *ChaosReconciler) reconcilePending(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check namespace restrictions.
	if !isNamespaceAllowed(exp.Spec.Target.Namespace, exp.Spec.SafetyChecks.AllowedNamespaces, exp.Spec.SafetyChecks.BlockedNamespaces) {
		return r.transitionToFailed(ctx, exp, fmt.Sprintf("namespace %q is not allowed by safety checks", exp.Spec.Target.Namespace))
	}

	// Check MaxConcurrentExperiments.
	maxConcurrent := exp.Spec.SafetyChecks.MaxConcurrentExperiments
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	var allExperiments platformv1alpha1.ChaosExperimentList
	if err := r.List(ctx, &allExperiments); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing chaos experiments: %w", err)
	}
	runningCount := int32(0)
	for i := range allExperiments.Items {
		if allExperiments.Items[i].Status.State == platformv1alpha1.ChaosStateRunning {
			runningCount++
		}
	}
	if runningCount >= maxConcurrent {
		logger.Info("max concurrent experiments reached, requeueing", "running", runningCount, "max", maxConcurrent)
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Check MinHealthyPods.
	if exp.Spec.SafetyChecks.MinHealthyPods > 0 {
		healthyPods, totalPods, err := r.countHealthyPods(ctx, exp.Spec.Target)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("counting healthy pods: %w", err)
		}
		killCount := r.getParamInt(exp.Spec.Parameters, "count", 1)
		remainingHealthy := healthyPods - int32(killCount)
		if remainingHealthy < exp.Spec.SafetyChecks.MinHealthyPods {
			return r.transitionToFailed(ctx, exp, fmt.Sprintf(
				"safety check failed: killing %d pods would leave %d healthy (min required: %d, total: %d)",
				killCount, remainingHealthy, exp.Spec.SafetyChecks.MinHealthyPods, totalPods,
			))
		}
	}

	// Check RequireApproval.
	if exp.Spec.SafetyChecks.RequireApproval {
		approved, err := r.checkApproval(ctx, exp)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("checking approval: %w", err)
		}
		if !approved {
			logger.Info("awaiting approval for chaos experiment", "name", exp.Name)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
	}

	// Handle DryRun.
	if exp.Spec.DryRun {
		logger.Info("dry-run mode: simulating chaos experiment",
			"type", exp.Spec.ExperimentType,
			"target", fmt.Sprintf("%s/%s/%s", exp.Spec.Target.Kind, exp.Spec.Target.Namespace, exp.Spec.Target.Name),
			"duration", exp.Spec.Duration,
			"parameters", exp.Spec.Parameters,
		)
		now := metav1.Now()
		exp.Status.State = platformv1alpha1.ChaosStateCompleted
		exp.Status.StartedAt = &now
		exp.Status.CompletedAt = &now
		exp.Status.Result = fmt.Sprintf("dry-run: would execute %s on %s/%s for %s",
			exp.Spec.ExperimentType, exp.Spec.Target.Namespace, exp.Spec.Target.Name, exp.Spec.Duration)
		exp.Status.PreExperimentSnapshot = r.captureSnapshot(ctx, exp.Spec.Target)
		exp.Status.PostExperimentSnapshot = exp.Status.PreExperimentSnapshot
		chaosExperimentsTotal.WithLabelValues(string(exp.Spec.ExperimentType), "dry_run").Inc()
		return ctrl.Result{}, r.Status().Update(ctx, exp)
	}

	// Capture pre-experiment snapshot.
	exp.Status.PreExperimentSnapshot = r.captureSnapshot(ctx, exp.Spec.Target)

	// Transition to Running.
	now := metav1.Now()
	exp.Status.State = platformv1alpha1.ChaosStateRunning
	exp.Status.StartedAt = &now
	logger.Info("starting chaos experiment",
		"type", exp.Spec.ExperimentType,
		"target", fmt.Sprintf("%s/%s/%s", exp.Spec.Target.Kind, exp.Spec.Target.Namespace, exp.Spec.Target.Name),
	)

	if err := r.Status().Update(ctx, exp); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status to Running: %w", err)
	}

	// Requeue immediately to execute the experiment.
	return ctrl.Result{Requeue: true}, nil
}

// reconcileRunning executes the chaos experiment and manages its lifecycle.
func (r *ChaosReconciler) reconcileRunning(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Parse duration.
	duration, err := time.ParseDuration(exp.Spec.Duration)
	if err != nil {
		return r.transitionToFailed(ctx, exp, fmt.Sprintf("invalid duration %q: %v", exp.Spec.Duration, err))
	}

	// Check if experiment duration has elapsed.
	if exp.Status.StartedAt != nil {
		elapsed := time.Since(exp.Status.StartedAt.Time)
		if elapsed >= duration {
			return r.completeExperiment(ctx, exp)
		}
	}

	// Check AbortOnIssueDetected.
	if exp.Spec.SafetyChecks.AbortOnIssueDetected {
		newIssue, err := r.detectNewIssue(ctx, exp)
		if err != nil {
			logger.Error(err, "failed to check for new issues during chaos")
		} else if newIssue != "" {
			logger.Info("aborting chaos experiment due to detected issue", "issue", newIssue)
			exp.Status.State = platformv1alpha1.ChaosStateAborted
			exp.Status.Result = fmt.Sprintf("aborted: new issue detected: %s", newIssue)
			now := metav1.Now()
			exp.Status.CompletedAt = &now
			chaosExperimentsTotal.WithLabelValues(string(exp.Spec.ExperimentType), "aborted").Inc()
			if cleanupErr := r.cleanupStressPods(ctx, exp.Namespace, exp.Name); cleanupErr != nil {
				logger.Error(cleanupErr, "failed to cleanup stress pods during abort")
			}
			return ctrl.Result{}, r.Status().Update(ctx, exp)
		}
	}

	// Execute the experiment if we haven't started yet (first reconcile after Running state).
	// We detect "first execution" by checking if PodsAffected is still 0 and no stress pods exist.
	if exp.Status.PodsAffected == 0 && !r.hasStressPods(ctx, exp) {
		if err := r.executeExperiment(ctx, exp); err != nil {
			return r.transitionToFailed(ctx, exp, fmt.Sprintf("execution failed: %v", err))
		}
		if err := r.Status().Update(ctx, exp); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status after execution: %w", err)
		}
	}

	// Requeue to check duration/abort conditions.
	remaining := duration - time.Since(exp.Status.StartedAt.Time)
	if remaining <= 0 {
		return ctrl.Result{Requeue: true}, nil
	}
	requeueAfter := remaining
	if requeueAfter > 10*time.Second {
		requeueAfter = 10 * time.Second
	}
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// executeExperiment dispatches to the appropriate experiment handler.
func (r *ChaosReconciler) executeExperiment(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	switch exp.Spec.ExperimentType {
	case platformv1alpha1.ChaosTypePodKill:
		return r.executePodKill(ctx, exp)
	case platformv1alpha1.ChaosTypePodFailure:
		return r.executePodFailure(ctx, exp)
	case platformv1alpha1.ChaosTypeCPUStress:
		return r.executeCPUStress(ctx, exp)
	case platformv1alpha1.ChaosTypeMemoryStress:
		return r.executeMemoryStress(ctx, exp)
	case platformv1alpha1.ChaosTypeNetworkDelay:
		return r.executeNetworkDelay(ctx, exp)
	case platformv1alpha1.ChaosTypeNetworkLoss:
		return r.executeNetworkLoss(ctx, exp)
	case platformv1alpha1.ChaosTypeDiskStress:
		return r.executeDiskStress(ctx, exp)
	default:
		return fmt.Errorf("unsupported experiment type: %s", exp.Spec.ExperimentType)
	}
}

// executePodKill deletes N random pods owned by the target deployment.
func (r *ChaosReconciler) executePodKill(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	logger := log.FromContext(ctx)
	count := r.getParamInt(exp.Spec.Parameters, "count", 1)

	pods, err := r.getTargetPods(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("listing target pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("no pods found for target %s/%s", exp.Spec.Target.Namespace, exp.Spec.Target.Name)
	}

	selected := selectRandomPods(pods, count)
	gracePeriod := int64(0)
	deleteOpts := &client.DeleteOptions{
		GracePeriodSeconds: &gracePeriod,
	}

	for i := range selected {
		logger.Info("killing pod", "pod", selected[i].Name, "namespace", selected[i].Namespace)
		if err := r.Delete(ctx, &selected[i], deleteOpts); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting pod %s: %w", selected[i].Name, err)
		}
	}

	exp.Status.PodsAffected = int32(len(selected))
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypePodKill)).Add(float64(len(selected)))
	return nil
}

// executePodFailure deletes pods with a configurable grace period.
func (r *ChaosReconciler) executePodFailure(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	logger := log.FromContext(ctx)
	count := r.getParamInt(exp.Spec.Parameters, "count", 1)
	gracePeriodSec := int64(r.getParamInt(exp.Spec.Parameters, "gracePeriodSeconds", 30))

	pods, err := r.getTargetPods(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("listing target pods: %w", err)
	}
	if len(pods) == 0 {
		return fmt.Errorf("no pods found for target %s/%s", exp.Spec.Target.Namespace, exp.Spec.Target.Name)
	}

	selected := selectRandomPods(pods, count)
	deleteOpts := &client.DeleteOptions{
		GracePeriodSeconds: &gracePeriodSec,
	}

	for i := range selected {
		logger.Info("failing pod", "pod", selected[i].Name, "namespace", selected[i].Namespace, "gracePeriod", gracePeriodSec)
		if err := r.Delete(ctx, &selected[i], deleteOpts); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("deleting pod %s: %w", selected[i].Name, err)
		}
	}

	exp.Status.PodsAffected = int32(len(selected))
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypePodFailure)).Add(float64(len(selected)))
	return nil
}

// executeCPUStress creates a stress-ng pod on the same node as the target to consume CPU.
func (r *ChaosReconciler) executeCPUStress(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	cores := exp.Spec.Parameters["cores"]
	if cores == "" {
		cores = "1"
	}
	loadPercent := exp.Spec.Parameters["loadPercent"]
	if loadPercent == "" {
		loadPercent = "80"
	}

	nodeName, err := r.getTargetNodeName(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("getting target node: %w", err)
	}

	cmd := []string{"stress-ng", "--cpu", cores, "--cpu-load", loadPercent, "--timeout", exp.Spec.Duration}
	podName := fmt.Sprintf("chaos-cpu-%s", exp.Name)
	if err := r.createStressPod(ctx, podName, exp.Spec.Target.Namespace, nodeName, cmd, exp.Name); err != nil {
		return fmt.Errorf("creating CPU stress pod: %w", err)
	}

	exp.Status.PodsAffected = 1
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypeCPUStress)).Inc()
	return nil
}

// executeMemoryStress creates a stress-ng pod consuming memory on the same node as the target.
func (r *ChaosReconciler) executeMemoryStress(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	memBytes := exp.Spec.Parameters["bytes"]
	if memBytes == "" {
		memBytes = "256M"
	}
	workers := exp.Spec.Parameters["workers"]
	if workers == "" {
		workers = "1"
	}

	nodeName, err := r.getTargetNodeName(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("getting target node: %w", err)
	}

	cmd := []string{"stress-ng", "--vm", workers, "--vm-bytes", memBytes, "--timeout", exp.Spec.Duration}
	podName := fmt.Sprintf("chaos-mem-%s", exp.Name)
	if err := r.createStressPod(ctx, podName, exp.Spec.Target.Namespace, nodeName, cmd, exp.Name); err != nil {
		return fmt.Errorf("creating memory stress pod: %w", err)
	}

	exp.Status.PodsAffected = 1
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypeMemoryStress)).Inc()
	return nil
}

// executeNetworkDelay annotates target pods to signal network delay simulation.
func (r *ChaosReconciler) executeNetworkDelay(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	logger := log.FromContext(ctx)
	latencyMs := exp.Spec.Parameters["latencyMs"]
	if latencyMs == "" {
		latencyMs = "100"
	}
	jitterMs := exp.Spec.Parameters["jitterMs"]
	if jitterMs == "" {
		jitterMs = "10"
	}

	pods, err := r.getTargetPods(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("listing target pods: %w", err)
	}

	annotationValue := fmt.Sprintf("%sms jitter=%sms experiment=%s", latencyMs, jitterMs, exp.Name)
	for i := range pods {
		pod := &pods[i]
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[chaosAnnotationDelay] = annotationValue
		if err := r.Update(ctx, pod); err != nil {
			logger.Error(err, "failed to annotate pod for network delay", "pod", pod.Name)
			continue
		}
		logger.Info("annotated pod for network delay", "pod", pod.Name, "latency", latencyMs+"ms")
	}

	exp.Status.PodsAffected = int32(len(pods))
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypeNetworkDelay)).Add(float64(len(pods)))
	return nil
}

// executeNetworkLoss annotates target pods to signal network packet loss simulation.
func (r *ChaosReconciler) executeNetworkLoss(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	logger := log.FromContext(ctx)
	percent := exp.Spec.Parameters["percent"]
	if percent == "" {
		percent = "10"
	}
	correlation := exp.Spec.Parameters["correlation"]
	if correlation == "" {
		correlation = "25"
	}

	pods, err := r.getTargetPods(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("listing target pods: %w", err)
	}

	annotationValue := fmt.Sprintf("%s%% correlation=%s%% experiment=%s", percent, correlation, exp.Name)
	for i := range pods {
		pod := &pods[i]
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[chaosAnnotationLoss] = annotationValue
		if err := r.Update(ctx, pod); err != nil {
			logger.Error(err, "failed to annotate pod for network loss", "pod", pod.Name)
			continue
		}
		logger.Info("annotated pod for network loss", "pod", pod.Name, "percent", percent+"%")
	}

	exp.Status.PodsAffected = int32(len(pods))
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypeNetworkLoss)).Add(float64(len(pods)))
	return nil
}

// executeDiskStress creates a stress-ng pod performing disk I/O on the same node as the target.
func (r *ChaosReconciler) executeDiskStress(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) error {
	workers := exp.Spec.Parameters["workers"]
	if workers == "" {
		workers = "1"
	}
	size := exp.Spec.Parameters["size"]
	if size == "" {
		size = "1G"
	}

	nodeName, err := r.getTargetNodeName(ctx, exp.Spec.Target)
	if err != nil {
		return fmt.Errorf("getting target node: %w", err)
	}

	cmd := []string{"stress-ng", "--hdd", workers, "--hdd-bytes", size, "--timeout", exp.Spec.Duration}
	podName := fmt.Sprintf("chaos-disk-%s", exp.Name)
	if err := r.createStressPod(ctx, podName, exp.Spec.Target.Namespace, nodeName, cmd, exp.Name); err != nil {
		return fmt.Errorf("creating disk stress pod: %w", err)
	}

	exp.Status.PodsAffected = 1
	chaosPodsAffectedTotal.WithLabelValues(string(platformv1alpha1.ChaosTypeDiskStress)).Inc()
	return nil
}

// completeExperiment handles post-experiment verification and transitions to Completed.
func (r *ChaosReconciler) completeExperiment(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Clean up stress pods.
	if err := r.cleanupStressPods(ctx, exp.Namespace, exp.Name); err != nil {
		logger.Error(err, "failed to cleanup stress pods")
	}

	// Clean up network annotations.
	if exp.Spec.ExperimentType == platformv1alpha1.ChaosTypeNetworkDelay || exp.Spec.ExperimentType == platformv1alpha1.ChaosTypeNetworkLoss {
		r.cleanupNetworkAnnotations(ctx, exp)
	}

	// Capture post-experiment snapshot.
	exp.Status.PostExperimentSnapshot = r.captureSnapshot(ctx, exp.Spec.Target)

	// Verify recovery if requested.
	if exp.Spec.PostExperiment.VerifyRecovery {
		recoveryTimeout := 5 * time.Minute
		if exp.Spec.PostExperiment.RecoveryTimeout != "" {
			if parsed, err := time.ParseDuration(exp.Spec.PostExperiment.RecoveryTimeout); err == nil {
				recoveryTimeout = parsed
			}
		}

		recoveryStart := time.Now()
		healthy, err := r.checkDeploymentHealth(ctx, exp.Spec.Target)
		if err != nil {
			logger.Error(err, "failed to check deployment health for recovery verification")
		}

		if healthy {
			recoveryDuration := time.Since(recoveryStart)
			exp.Status.RecoveryVerified = true
			exp.Status.RecoveryTime = recoveryDuration.Round(time.Millisecond).String()
			chaosRecoveryTimeSeconds.WithLabelValues(string(exp.Spec.ExperimentType)).Observe(recoveryDuration.Seconds())
		} else {
			// If not recovered yet, check elapsed time since experiment started.
			elapsedSinceStart := time.Since(exp.Status.StartedAt.Time)
			duration, _ := time.ParseDuration(exp.Spec.Duration)
			timeSinceExperimentEnd := elapsedSinceStart - duration

			if timeSinceExperimentEnd < recoveryTimeout {
				// Still within recovery window — requeue.
				logger.Info("waiting for recovery", "elapsed", timeSinceExperimentEnd, "timeout", recoveryTimeout)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			// Recovery timeout exceeded.
			exp.Status.RecoveryVerified = false
			exp.Status.RecoveryTime = "timeout"
			logger.Info("recovery verification timed out", "timeout", recoveryTimeout)
		}
	}

	// Check remediation test if requested.
	if exp.Spec.PostExperiment.RunRemediationTest {
		remediationApplied, err := r.checkRemediationApplied(ctx, exp)
		if err != nil {
			logger.Error(err, "failed to check remediation status")
		}
		if remediationApplied {
			exp.Status.Result = fmt.Sprintf("completed: %s experiment finished; remediation validation passed", exp.Spec.ExperimentType)
		} else {
			exp.Status.Result = fmt.Sprintf("completed: %s experiment finished; no remediation was applied during experiment", exp.Spec.ExperimentType)
		}
	} else {
		resultParts := []string{fmt.Sprintf("completed: %s experiment finished", exp.Spec.ExperimentType)}
		if exp.Status.RecoveryVerified {
			resultParts = append(resultParts, fmt.Sprintf("recovery verified in %s", exp.Status.RecoveryTime))
		} else if exp.Spec.PostExperiment.VerifyRecovery {
			resultParts = append(resultParts, "recovery NOT verified (timeout)")
		}
		exp.Status.Result = strings.Join(resultParts, "; ")
	}

	now := metav1.Now()
	exp.Status.State = platformv1alpha1.ChaosStateCompleted
	exp.Status.CompletedAt = &now
	chaosExperimentsTotal.WithLabelValues(string(exp.Spec.ExperimentType), "completed").Inc()

	logger.Info("chaos experiment completed",
		"type", exp.Spec.ExperimentType,
		"podsAffected", exp.Status.PodsAffected,
		"recoveryVerified", exp.Status.RecoveryVerified,
	)

	return ctrl.Result{}, r.Status().Update(ctx, exp)
}

// reconcileAborted cleans up resources for an aborted experiment.
func (r *ChaosReconciler) reconcileAborted(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if err := r.cleanupStressPods(ctx, exp.Namespace, exp.Name); err != nil {
		logger.Error(err, "failed to cleanup stress pods during abort handling")
	}
	if exp.Spec.ExperimentType == platformv1alpha1.ChaosTypeNetworkDelay || exp.Spec.ExperimentType == platformv1alpha1.ChaosTypeNetworkLoss {
		r.cleanupNetworkAnnotations(ctx, exp)
	}

	exp.Status.PostExperimentSnapshot = r.captureSnapshot(ctx, exp.Spec.Target)
	return ctrl.Result{}, r.Status().Update(ctx, exp)
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// captureSnapshot records the current state of the target deployment.
func (r *ChaosReconciler) captureSnapshot(ctx context.Context, target platformv1alpha1.ResourceRef) string {
	switch strings.ToLower(target.Kind) {
	case "deployment":
		var dep appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &dep); err != nil {
			return fmt.Sprintf("error capturing snapshot: %v", err)
		}
		return fmt.Sprintf("Deployment %s/%s: replicas=%d, ready=%d, available=%d, updated=%d",
			dep.Namespace, dep.Name,
			ptrInt32(dep.Spec.Replicas),
			dep.Status.ReadyReplicas,
			dep.Status.AvailableReplicas,
			dep.Status.UpdatedReplicas,
		)
	case "statefulset":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &sts); err != nil {
			return fmt.Sprintf("error capturing snapshot: %v", err)
		}
		return fmt.Sprintf("StatefulSet %s/%s: replicas=%d, ready=%d, current=%d, updated=%d",
			sts.Namespace, sts.Name,
			ptrInt32(sts.Spec.Replicas),
			sts.Status.ReadyReplicas,
			sts.Status.CurrentReplicas,
			sts.Status.UpdatedReplicas,
		)
	default:
		return fmt.Sprintf("unsupported target kind: %s", target.Kind)
	}
}

// selectRandomPods selects up to count pods randomly using crypto/rand.
func selectRandomPods(pods []corev1.Pod, count int) []corev1.Pod {
	if count >= len(pods) {
		result := make([]corev1.Pod, len(pods))
		copy(result, pods)
		return result
	}
	if count <= 0 {
		return nil
	}

	// Fisher-Yates shuffle using crypto/rand, then take first `count`.
	indices := make([]int, len(pods))
	for i := range indices {
		indices[i] = i
	}
	for i := len(indices) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			// Fallback: take first N if crypto/rand fails.
			break
		}
		j := int(n.Int64())
		indices[i], indices[j] = indices[j], indices[i]
	}

	result := make([]corev1.Pod, count)
	for i := 0; i < count; i++ {
		result[i] = pods[indices[i]]
	}
	return result
}

// createStressPod creates a stress-ng pod on a specific node.
func (r *ChaosReconciler) createStressPod(ctx context.Context, name, namespace, nodeName string, command []string, experimentName string) error {
	// Truncate name to fit Kubernetes 63-char limit.
	if len(name) > 63 {
		name = name[:63]
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				chaosLabelExperiment: experimentName,
				chaosLabelRole:       "stress",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      nodeName,
			Containers: []corev1.Container{
				{
					Name:    "stress",
					Image:   chaosStressImage,
					Command: command,
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						},
					},
				},
			},
			// Tolerate all taints so the stress pod can schedule on any node.
			Tolerations: []corev1.Toleration{
				{
					Operator: corev1.TolerationOpExists,
				},
			},
		},
	}

	if err := r.Create(ctx, pod); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil // Stress pod already running.
		}
		return err
	}
	return nil
}

// cleanupStressPods deletes all pods labeled with the experiment name.
func (r *ChaosReconciler) cleanupStressPods(ctx context.Context, namespace, experimentName string) error {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(namespace),
		client.MatchingLabels{chaosLabelExperiment: experimentName},
	); err != nil {
		return fmt.Errorf("listing stress pods: %w", err)
	}

	var errs []string
	gracePeriod := int64(0)
	for i := range podList.Items {
		if err := r.Delete(ctx, &podList.Items[i], &client.DeleteOptions{
			GracePeriodSeconds: &gracePeriod,
		}); err != nil && !errors.IsNotFound(err) {
			errs = append(errs, fmt.Sprintf("pod %s: %v", podList.Items[i].Name, err))
		}
	}

	// Also search in the target namespace if different from experiment namespace.
	if namespace != "" {
		var allNsPodList corev1.PodList
		if err := r.List(ctx, &allNsPodList,
			client.MatchingLabels{chaosLabelExperiment: experimentName},
		); err == nil {
			for i := range allNsPodList.Items {
				if allNsPodList.Items[i].Namespace == namespace {
					continue // Already handled above.
				}
				if err := r.Delete(ctx, &allNsPodList.Items[i], &client.DeleteOptions{
					GracePeriodSeconds: &gracePeriod,
				}); err != nil && !errors.IsNotFound(err) {
					errs = append(errs, fmt.Sprintf("pod %s/%s: %v", allNsPodList.Items[i].Namespace, allNsPodList.Items[i].Name, err))
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// cleanupNetworkAnnotations removes chaos network annotations from target pods.
func (r *ChaosReconciler) cleanupNetworkAnnotations(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) {
	logger := log.FromContext(ctx)
	pods, err := r.getTargetPods(ctx, exp.Spec.Target)
	if err != nil {
		logger.Error(err, "failed to list pods for annotation cleanup")
		return
	}

	for i := range pods {
		pod := &pods[i]
		if pod.Annotations == nil {
			continue
		}
		changed := false
		if _, ok := pod.Annotations[chaosAnnotationDelay]; ok {
			delete(pod.Annotations, chaosAnnotationDelay)
			changed = true
		}
		if _, ok := pod.Annotations[chaosAnnotationLoss]; ok {
			delete(pod.Annotations, chaosAnnotationLoss)
			changed = true
		}
		if changed {
			if err := r.Update(ctx, pod); err != nil {
				logger.Error(err, "failed to remove chaos annotation from pod", "pod", pod.Name)
			}
		}
	}
}

// checkDeploymentHealth returns true if the target deployment has all replicas ready.
func (r *ChaosReconciler) checkDeploymentHealth(ctx context.Context, target platformv1alpha1.ResourceRef) (bool, error) {
	switch strings.ToLower(target.Kind) {
	case "deployment":
		var dep appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &dep); err != nil {
			return false, err
		}
		desired := ptrInt32(dep.Spec.Replicas)
		return dep.Status.ReadyReplicas >= desired && dep.Status.AvailableReplicas >= desired, nil
	case "statefulset":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &sts); err != nil {
			return false, err
		}
		desired := ptrInt32(sts.Spec.Replicas)
		return sts.Status.ReadyReplicas >= desired, nil
	default:
		return false, fmt.Errorf("unsupported target kind for health check: %s", target.Kind)
	}
}

// isNamespaceAllowed checks if a namespace is permitted by the allowed/blocked lists.
func isNamespaceAllowed(namespace string, allowed, blocked []string) bool {
	// Check blocked namespaces first.
	for _, ns := range blocked {
		if ns == namespace {
			return false
		}
	}
	// If allowed list is empty, all non-blocked namespaces are allowed.
	if len(allowed) == 0 {
		return true
	}
	for _, ns := range allowed {
		if ns == namespace {
			return true
		}
	}
	return false
}

// getTargetPods returns all pods owned by the target resource.
func (r *ChaosReconciler) getTargetPods(ctx context.Context, target platformv1alpha1.ResourceRef) ([]corev1.Pod, error) {
	switch strings.ToLower(target.Kind) {
	case "deployment":
		var dep appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &dep); err != nil {
			return nil, err
		}
		// Find ReplicaSets owned by this deployment.
		var rsList appsv1.ReplicaSetList
		if err := r.List(ctx, &rsList, client.InNamespace(target.Namespace)); err != nil {
			return nil, err
		}
		var ownedRSNames []string
		for _, rs := range rsList.Items {
			for _, ref := range rs.OwnerReferences {
				if ref.UID == dep.UID {
					ownedRSNames = append(ownedRSNames, rs.Name)
				}
			}
		}
		// Find pods owned by those ReplicaSets.
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(target.Namespace)); err != nil {
			return nil, err
		}
		rsNameSet := make(map[string]bool)
		for _, name := range ownedRSNames {
			rsNameSet[name] = true
		}
		var pods []corev1.Pod
		for _, pod := range podList.Items {
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "ReplicaSet" && rsNameSet[ref.Name] {
					pods = append(pods, pod)
					break
				}
			}
		}
		return pods, nil

	case "statefulset":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Name: target.Name, Namespace: target.Namespace}, &sts); err != nil {
			return nil, err
		}
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(target.Namespace)); err != nil {
			return nil, err
		}
		var pods []corev1.Pod
		for _, pod := range podList.Items {
			for _, ref := range pod.OwnerReferences {
				if ref.UID == sts.UID {
					pods = append(pods, pod)
					break
				}
			}
		}
		return pods, nil

	default:
		return nil, fmt.Errorf("unsupported target kind: %s", target.Kind)
	}
}

// getTargetNodeName returns the node name where a target pod is running.
func (r *ChaosReconciler) getTargetNodeName(ctx context.Context, target platformv1alpha1.ResourceRef) (string, error) {
	pods, err := r.getTargetPods(ctx, target)
	if err != nil {
		return "", err
	}
	if len(pods) == 0 {
		return "", fmt.Errorf("no pods found for target %s/%s", target.Namespace, target.Name)
	}
	// Return the node of the first running pod.
	for _, pod := range pods {
		if pod.Spec.NodeName != "" {
			return pod.Spec.NodeName, nil
		}
	}
	return "", fmt.Errorf("no pod has a node assignment for %s/%s", target.Namespace, target.Name)
}

// countHealthyPods returns the number of healthy (Ready) pods and total pods for a target.
func (r *ChaosReconciler) countHealthyPods(ctx context.Context, target platformv1alpha1.ResourceRef) (int32, int32, error) {
	pods, err := r.getTargetPods(ctx, target)
	if err != nil {
		return 0, 0, err
	}
	healthy := int32(0)
	for _, pod := range pods {
		if chaosPodReady(&pod) {
			healthy++
		}
	}
	return healthy, int32(len(pods)), nil
}

// chaosPodReady checks if a pod has the Ready condition set to True.
func chaosPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// hasStressPods checks if there are any stress pods running for this experiment.
func (r *ChaosReconciler) hasStressPods(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) bool {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.MatchingLabels{chaosLabelExperiment: exp.Name},
	); err != nil {
		return false
	}
	return len(podList.Items) > 0
}

// checkApproval checks if an ApprovalRequest exists and is approved for this experiment.
func (r *ChaosReconciler) checkApproval(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (bool, error) {
	logger := log.FromContext(ctx)

	// Look for an existing ApprovalRequest for this experiment.
	approvalName := fmt.Sprintf("chaos-%s", exp.Name)
	var ar platformv1alpha1.ApprovalRequest
	err := r.Get(ctx, types.NamespacedName{Name: approvalName, Namespace: exp.Namespace}, &ar)
	if err != nil {
		if errors.IsNotFound(err) {
			// Create a new ApprovalRequest.
			ar = platformv1alpha1.ApprovalRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      approvalName,
					Namespace: exp.Namespace,
					Labels: map[string]string{
						chaosLabelExperiment: exp.Name,
					},
				},
				Spec: platformv1alpha1.ApprovalRequestSpec{
					IssueRef: platformv1alpha1.IssueRef{
						Name: fmt.Sprintf("chaos-experiment-%s", exp.Name),
					},
					RemediationPlanRef: exp.Name,
					PolicyRef:          "chaos-safety",
					RuleName:           "chaos-experiment-approval",
					Requester:          "chaos-controller",
					TimeoutMinutes:     30,
					RequiredApprovers:  1,
				},
			}
			if exp.Spec.LinkedIssueRef != nil {
				ar.Spec.IssueRef = *exp.Spec.LinkedIssueRef
			}
			if err := r.Create(ctx, &ar); err != nil {
				return false, fmt.Errorf("creating approval request: %w", err)
			}
			logger.Info("created approval request for chaos experiment", "approval", approvalName)
			return false, nil
		}
		return false, err
	}

	switch ar.Status.State {
	case platformv1alpha1.ApprovalStateApproved:
		return true, nil
	case platformv1alpha1.ApprovalStateRejected:
		// Fail the experiment if approval was rejected.
		return false, fmt.Errorf("approval rejected for experiment %s", exp.Name)
	case platformv1alpha1.ApprovalStateExpired:
		return false, fmt.Errorf("approval expired for experiment %s", exp.Name)
	default:
		// Still pending.
		return false, nil
	}
}

// detectNewIssue checks if any new Issues have been created targeting the same resource since the experiment started.
func (r *ChaosReconciler) detectNewIssue(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (string, error) {
	var issues platformv1alpha1.IssueList
	if err := r.List(ctx, &issues, client.InNamespace(exp.Spec.Target.Namespace)); err != nil {
		return "", err
	}

	for _, issue := range issues.Items {
		// Check if the issue targets the same resource and was created after the experiment started.
		if issue.Spec.Resource.Kind == exp.Spec.Target.Kind &&
			issue.Spec.Resource.Name == exp.Spec.Target.Name &&
			issue.Spec.Resource.Namespace == exp.Spec.Target.Namespace &&
			exp.Status.StartedAt != nil &&
			issue.CreationTimestamp.After(exp.Status.StartedAt.Time) {
			return issue.Name, nil
		}
	}
	return "", nil
}

// checkRemediationApplied checks if any successful remediation was applied for the linked issue during the experiment.
func (r *ChaosReconciler) checkRemediationApplied(ctx context.Context, exp *platformv1alpha1.ChaosExperiment) (bool, error) {
	if exp.Spec.LinkedIssueRef == nil {
		return false, nil
	}

	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{
		Name:      exp.Spec.LinkedIssueRef.Name,
		Namespace: exp.Namespace,
	}, &issue); err != nil {
		return false, err
	}

	// A remediation was considered applied if the issue moved to Resolved.
	return issue.Status.State == platformv1alpha1.IssueStateResolved, nil
}

// transitionToFailed sets the experiment state to Failed with a reason.
func (r *ChaosReconciler) transitionToFailed(ctx context.Context, exp *platformv1alpha1.ChaosExperiment, reason string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("chaos experiment failed", "reason", reason)

	now := metav1.Now()
	exp.Status.State = platformv1alpha1.ChaosStateFailed
	exp.Status.Result = reason
	exp.Status.CompletedAt = &now
	exp.Status.PostExperimentSnapshot = r.captureSnapshot(ctx, exp.Spec.Target)
	chaosExperimentsTotal.WithLabelValues(string(exp.Spec.ExperimentType), "failed").Inc()

	// Clean up any stress pods that might have been created.
	if err := r.cleanupStressPods(ctx, exp.Namespace, exp.Name); err != nil {
		logger.Error(err, "failed to cleanup stress pods during failure handling")
	}

	return ctrl.Result{}, r.Status().Update(ctx, exp)
}

// getParamInt extracts an integer parameter with a default value.
func (r *ChaosReconciler) getParamInt(params map[string]string, key string, defaultVal int) int {
	if v, ok := params[key]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// ptrInt32 dereferences an int32 pointer, returning 1 if nil (default replica count).
func ptrInt32(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

// SetupWithManager sets up the controller with the Manager.
func (r *ChaosReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ChaosExperiment{}).
		Complete(r)
}
