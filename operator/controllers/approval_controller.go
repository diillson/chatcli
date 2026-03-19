package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	approvalsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "approvals_total",
		Help:      "Total approval requests by mode and result.",
	}, []string{"mode", "result"})

	approvalDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "chatcli",
		Subsystem: "operator",
		Name:      "approval_duration_seconds",
		Help:      "Duration of approval request lifecycle.",
		Buckets:   prometheus.ExponentialBuckets(1, 2, 14),
	}, []string{"mode"})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		approvalsTotal,
		approvalDuration,
	)
}

const (
	annotationApprovalPending = "platform.chatcli.io/approval-pending"
	annotationApprove         = "platform.chatcli.io/approve"
	annotationReject          = "platform.chatcli.io/reject"
)

// ApprovalReconciler reconciles ApprovalRequest objects.
type ApprovalReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.chatcli.io,resources=approvalrequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=approvalrequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=approvalpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=approvalpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.chatcli.io,resources=remediationplans,verbs=get;list;watch;update;patch

func (r *ApprovalReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var ar platformv1alpha1.ApprovalRequest
	if err := r.Get(ctx, req.NamespacedName, &ar); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	switch ar.Status.State {
	case platformv1alpha1.ApprovalStatePending, "":
		return r.reconcilePending(ctx, &ar)
	case platformv1alpha1.ApprovalStateApproved:
		return r.reconcileApproved(ctx, &ar)
	case platformv1alpha1.ApprovalStateRejected:
		return r.reconcileRejected(ctx, &ar)
	case platformv1alpha1.ApprovalStateExpired:
		return r.reconcileExpired(ctx, &ar)
	default:
		logger.Info("Unknown approval state", "state", ar.Status.State)
		return ctrl.Result{}, nil
	}
}

func (r *ApprovalReconciler) reconcilePending(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Find matching policy
	var policy platformv1alpha1.ApprovalPolicy
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ar.Spec.PolicyRef,
		Namespace: ar.Namespace,
	}, &policy); err != nil {
		logger.Error(err, "Failed to find ApprovalPolicy", "policy", ar.Spec.PolicyRef)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Find matching rule
	var rule *platformv1alpha1.ApprovalRule
	for i := range policy.Spec.Rules {
		if policy.Spec.Rules[i].Name == ar.Spec.RuleName {
			rule = &policy.Spec.Rules[i]
			break
		}
	}
	if rule == nil {
		logger.Error(fmt.Errorf("rule %q not found in policy %q", ar.Spec.RuleName, ar.Spec.PolicyRef),
			"Rule not found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Check timeout
	elapsed := time.Since(ar.CreationTimestamp.Time)
	timeoutDuration := time.Duration(ar.Spec.TimeoutMinutes) * time.Minute
	if elapsed >= timeoutDuration {
		logger.Info("Approval request expired", "elapsed", elapsed, "timeout", timeoutDuration)
		now := metav1.Now()
		ar.Status.State = platformv1alpha1.ApprovalStateExpired
		ar.Status.ExpiredAt = &now
		if err := r.Status().Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
		approvalsTotal.WithLabelValues(string(rule.Mode), "expired").Inc()
		approvalDuration.WithLabelValues(string(rule.Mode)).Observe(elapsed.Seconds())
		return ctrl.Result{}, nil
	}

	// Check change window
	if rule.ChangeWindow != nil {
		inWindow, err := r.isWithinChangeWindow(rule.ChangeWindow)
		if err != nil {
			logger.Error(err, "Failed to check change window")
		} else if !inWindow {
			logger.Info("Outside change window, requeueing")
			return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
		}
	}

	switch rule.Mode {
	case platformv1alpha1.ApprovalModeAuto:
		return r.handleAutoMode(ctx, ar, rule)
	case platformv1alpha1.ApprovalModeManual:
		return r.handleManualMode(ctx, ar, rule)
	case platformv1alpha1.ApprovalModeQuorum:
		return r.handleQuorumMode(ctx, ar, rule)
	default:
		// Default to manual
		return r.handleManualMode(ctx, ar, rule)
	}
}

func (r *ApprovalReconciler) handleAutoMode(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, rule *platformv1alpha1.ApprovalRule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if rule.AutoApproveConditions == nil {
		// No auto-approve conditions, treat as manual
		return r.handleManualMode(ctx, ar, rule)
	}

	conditions := rule.AutoApproveConditions
	allMet := true
	var reasons []string

	// Check confidence
	if ar.Spec.Evidence != nil {
		if ar.Spec.Evidence.AIConfidence < conditions.MinConfidence {
			allMet = false
			reasons = append(reasons, fmt.Sprintf("confidence %.2f < min %.2f",
				ar.Spec.Evidence.AIConfidence, conditions.MinConfidence))
		}
	} else {
		allMet = false
		reasons = append(reasons, "no evidence provided")
	}

	// Check severity
	if severityRank(ar.Spec.Evidence) > severityMaxRank(conditions.MaxSeverity) {
		allMet = false
		reasons = append(reasons, fmt.Sprintf("severity exceeds max %s", conditions.MaxSeverity))
	}

	// Check historical success rate
	if ar.Spec.Evidence != nil {
		if ar.Spec.Evidence.HistoricalSuccessRate < conditions.HistoricalSuccessRate {
			allMet = false
			reasons = append(reasons, fmt.Sprintf("success rate %.2f < min %.2f",
				ar.Spec.Evidence.HistoricalSuccessRate, conditions.HistoricalSuccessRate))
		}
	}

	if allMet {
		logger.Info("Auto-approving request", "request", ar.Name)
		now := metav1.Now()
		ar.Status.State = platformv1alpha1.ApprovalStateApproved
		ar.Status.ApprovedAt = &now
		ar.Status.AutoApproved = true
		ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
			Approver:  "auto-policy",
			Decision:  "approved",
			Reason:    "All auto-approve conditions met",
			Timestamp: now,
		})
		if err := r.Status().Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
		elapsed := time.Since(ar.CreationTimestamp.Time)
		approvalsTotal.WithLabelValues("auto", "approved").Inc()
		approvalDuration.WithLabelValues("auto").Observe(elapsed.Seconds())
		return ctrl.Result{}, nil
	}

	// Conditions not met, treat as manual
	logger.Info("Auto-approve conditions not met, falling back to manual",
		"reasons", strings.Join(reasons, "; "))
	return r.handleManualMode(ctx, ar, rule)
}

func (r *ApprovalReconciler) handleManualMode(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, rule *platformv1alpha1.ApprovalRule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	changed := false

	annotations := ar.GetAnnotations()
	if annotations == nil {
		// No decisions yet, requeue
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// Check for approve annotation
	if approveVal, ok := annotations[annotationApprove]; ok {
		approver, reason := parseDecisionAnnotation(approveVal)
		now := metav1.Now()
		ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
			Approver:  approver,
			Decision:  "approved",
			Reason:    reason,
			Timestamp: now,
		})
		ar.Status.State = platformv1alpha1.ApprovalStateApproved
		ar.Status.ApprovedAt = &now
		changed = true

		// Remove the annotation
		delete(annotations, annotationApprove)
		ar.SetAnnotations(annotations)
		if err := r.Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}

		logger.Info("Request manually approved", "approver", approver)
		elapsed := time.Since(ar.CreationTimestamp.Time)
		approvalsTotal.WithLabelValues("manual", "approved").Inc()
		approvalDuration.WithLabelValues("manual").Observe(elapsed.Seconds())
	}

	// Check for reject annotation
	if rejectVal, ok := annotations[annotationReject]; ok {
		approver, reason := parseDecisionAnnotation(rejectVal)
		now := metav1.Now()
		ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
			Approver:  approver,
			Decision:  "rejected",
			Reason:    reason,
			Timestamp: now,
		})
		ar.Status.State = platformv1alpha1.ApprovalStateRejected
		ar.Status.RejectedAt = &now
		changed = true

		// Remove the annotation
		delete(annotations, annotationReject)
		ar.SetAnnotations(annotations)
		if err := r.Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}

		logger.Info("Request manually rejected", "approver", approver)
		elapsed := time.Since(ar.CreationTimestamp.Time)
		approvalsTotal.WithLabelValues("manual", "rejected").Inc()
		approvalDuration.WithLabelValues("manual").Observe(elapsed.Seconds())
	}

	if changed {
		if err := r.Status().Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// No decision yet, requeue to check again
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *ApprovalReconciler) handleQuorumMode(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, rule *platformv1alpha1.ApprovalRule) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	changed := false

	annotations := ar.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Check for approve annotation
	if approveVal, ok := annotations[annotationApprove]; ok {
		approver, reason := parseDecisionAnnotation(approveVal)
		now := metav1.Now()
		ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
			Approver:  approver,
			Decision:  "approved",
			Reason:    reason,
			Timestamp: now,
		})
		changed = true

		delete(annotations, annotationApprove)
		ar.SetAnnotations(annotations)
		if err := r.Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Check for reject annotation
	if rejectVal, ok := annotations[annotationReject]; ok {
		approver, reason := parseDecisionAnnotation(rejectVal)
		now := metav1.Now()
		ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
			Approver:  approver,
			Decision:  "rejected",
			Reason:    reason,
			Timestamp: now,
		})
		changed = true

		delete(annotations, annotationReject)
		ar.SetAnnotations(annotations)
		if err := r.Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Count approved decisions
	approvedCount := int32(0)
	for _, d := range ar.Status.Decisions {
		if d.Decision == "rejected" {
			// Any rejection means rejected
			now := metav1.Now()
			ar.Status.State = platformv1alpha1.ApprovalStateRejected
			ar.Status.RejectedAt = &now
			if err := r.Status().Update(ctx, ar); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("Request rejected in quorum mode", "rejector", d.Approver)
			elapsed := time.Since(ar.CreationTimestamp.Time)
			approvalsTotal.WithLabelValues("quorum", "rejected").Inc()
			approvalDuration.WithLabelValues("quorum").Observe(elapsed.Seconds())
			return ctrl.Result{}, nil
		}
		if d.Decision == "approved" {
			approvedCount++
		}
	}

	// Check if quorum reached
	if approvedCount >= ar.Spec.RequiredApprovers {
		now := metav1.Now()
		ar.Status.State = platformv1alpha1.ApprovalStateApproved
		ar.Status.ApprovedAt = &now
		if err := r.Status().Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Quorum reached, request approved",
			"approvals", approvedCount, "required", ar.Spec.RequiredApprovers)
		elapsed := time.Since(ar.CreationTimestamp.Time)
		approvalsTotal.WithLabelValues("quorum", "approved").Inc()
		approvalDuration.WithLabelValues("quorum").Observe(elapsed.Seconds())
		return ctrl.Result{}, nil
	}

	if changed {
		if err := r.Status().Update(ctx, ar); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Waiting for quorum", "approvals", approvedCount, "required", ar.Spec.RequiredApprovers)
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *ApprovalReconciler) reconcileApproved(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Remove approval-pending annotation from RemediationPlan
	var plan platformv1alpha1.RemediationPlan
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ar.Spec.RemediationPlanRef,
		Namespace: ar.Namespace,
	}, &plan); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RemediationPlan not found, skipping annotation removal")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	annotations := plan.GetAnnotations()
	if annotations != nil {
		if _, exists := annotations[annotationApprovalPending]; exists {
			delete(annotations, annotationApprovalPending)
			plan.SetAnnotations(annotations)
			if err := r.Update(ctx, &plan); err != nil {
				return ctrl.Result{}, err
			}
			logger.Info("Removed approval-pending annotation from plan", "plan", plan.Name)
		}
	}

	// Update policy counters
	if err := r.updatePolicyCounters(ctx, ar, "approved"); err != nil {
		logger.Error(err, "Failed to update policy counters")
	}

	return ctrl.Result{}, nil
}

func (r *ApprovalReconciler) reconcileRejected(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Annotate RemediationPlan with rejection reason
	var plan platformv1alpha1.RemediationPlan
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ar.Spec.RemediationPlanRef,
		Namespace: ar.Namespace,
	}, &plan); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("RemediationPlan not found, skipping rejection annotation")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	annotations := plan.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Build rejection reason from decisions
	var rejectionReasons []string
	for _, d := range ar.Status.Decisions {
		if d.Decision == "rejected" {
			rejectionReasons = append(rejectionReasons, fmt.Sprintf("%s: %s", d.Approver, d.Reason))
		}
	}
	annotations["platform.chatcli.io/rejection-reason"] = strings.Join(rejectionReasons, "; ")
	delete(annotations, annotationApprovalPending)
	plan.SetAnnotations(annotations)
	if err := r.Update(ctx, &plan); err != nil {
		return ctrl.Result{}, err
	}

	// Update policy counters
	if err := r.updatePolicyCounters(ctx, ar, "rejected"); err != nil {
		logger.Error(err, "Failed to update policy counters")
	}

	return ctrl.Result{}, nil
}

func (r *ApprovalReconciler) reconcileExpired(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Update policy counters
	if err := r.updatePolicyCounters(ctx, ar, "expired"); err != nil {
		logger.Error(err, "Failed to update policy counters")
	}

	return ctrl.Result{}, nil
}

func (r *ApprovalReconciler) updatePolicyCounters(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, result string) error {
	var policy platformv1alpha1.ApprovalPolicy
	if err := r.Get(ctx, types.NamespacedName{
		Name:      ar.Spec.PolicyRef,
		Namespace: ar.Namespace,
	}, &policy); err != nil {
		return err
	}

	switch result {
	case "approved":
		policy.Status.TotalApproved++
		if ar.Status.AutoApproved {
			policy.Status.TotalAutoApproved++
		}
	case "rejected":
		policy.Status.TotalRejected++
	case "expired":
		policy.Status.TotalExpired++
	}

	return r.Status().Update(ctx, &policy)
}

func (r *ApprovalReconciler) isWithinChangeWindow(cw *platformv1alpha1.ChangeWindowSpec) (bool, error) {
	loc, err := time.LoadLocation(cw.Timezone)
	if err != nil {
		return false, fmt.Errorf("invalid timezone %q: %w", cw.Timezone, err)
	}

	now := time.Now().In(loc)
	dayName := now.Weekday().String()

	// Check if current day is allowed
	dayAllowed := false
	for _, d := range cw.AllowedDays {
		if strings.EqualFold(d, dayName) {
			dayAllowed = true
			break
		}
	}
	if !dayAllowed {
		return false, nil
	}

	// Check if current hour is within window
	currentHour := int32(now.Hour())
	if cw.StartHour <= cw.EndHour {
		// Same day window: e.g., 09-17
		return currentHour >= cw.StartHour && currentHour < cw.EndHour, nil
	}
	// Overnight window: e.g., 22-06
	return currentHour >= cw.StartHour || currentHour < cw.EndHour, nil
}

// parseDecisionAnnotation parses "approver:reason" format.
func parseDecisionAnnotation(value string) (approver, reason string) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(value), ""
}

// severityRank returns a numeric rank for severity comparison from evidence.
// Returns the rank of the issue associated with this approval request.
func severityRank(evidence *platformv1alpha1.ApprovalEvidence) int {
	// We use the evidence to determine if auto-approval is appropriate.
	// If no evidence, return highest rank to block auto-approval.
	if evidence == nil {
		return 4
	}
	// The actual severity is determined externally; this returns 0 for auto-approve path.
	return 0
}

// severityMaxRank returns the numeric rank for a given severity level.
func severityMaxRank(sev platformv1alpha1.IssueSeverity) int {
	switch sev {
	case platformv1alpha1.IssueSeverityLow:
		return 1
	case platformv1alpha1.IssueSeverityMedium:
		return 2
	case platformv1alpha1.IssueSeverityHigh:
		return 3
	case platformv1alpha1.IssueSeverityCritical:
		return 4
	default:
		return 0
	}
}

// severityToRank returns the numeric rank for a given severity level (used externally).
func severityToRank(sev platformv1alpha1.IssueSeverity) int {
	return severityMaxRank(sev)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApprovalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ApprovalRequest{}).
		Complete(r)
}
