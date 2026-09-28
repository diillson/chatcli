package controllers

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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

	// annotationPolicyCounted marks a finished request whose result was
	// already added to its ApprovalPolicy counters; its value is the result.
	// Finished requests are reconciled again on every change, and without
	// the mark each pass counted them once more.
	annotationPolicyCounted = "platform.chatcli.io/policy-counted"

	// Decision values recorded in status.decisions[].decision.
	ApprovalDecisionApproved = "approved"
	ApprovalDecisionRejected = "rejected"

	// autoPolicyApprover is the approver recorded for an auto approval.
	autoPolicyApprover = "auto-policy"

	// conditionChangeWindow reports, on a request whose rule has a change
	// window, whether an approved decision is waiting for the window.
	conditionChangeWindow = "ChangeWindow"

	// apiKeyApproverMarker separates the typed name from the API key that
	// authenticated a REST decision: "alice (api-key: sre-team)".
	apiKeyApproverMarker = " (api-key: "
)

var (
	// ErrApprovalNotPending is returned when a decision targets a request
	// that is already Approved, Rejected or Expired.
	ErrApprovalNotPending = stderrors.New("approval request is not pending")
	// ErrApproverAlreadyDecided is returned when the same approver already
	// recorded a decision on the request.
	ErrApproverAlreadyDecided = stderrors.New("approver already recorded a decision on this request")
	// ErrInvalidApprovalDecision is returned for an empty approver or a
	// decision other than approved/rejected.
	ErrInvalidApprovalDecision = stderrors.New("invalid approval decision")
)

// ApprovalReconciler reconciles ApprovalRequest objects.
type ApprovalReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	BlastRadiusPredictor *BlastRadiusPredictor // Predicts impact of remediation actions before approval
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

// FormatAPIKeyApprover builds the approver recorded for a decision taken
// through the REST API: the name the person typed, followed by the
// identity of the API key that authenticated the call.
func FormatAPIKeyApprover(keyIdentity, name string) string {
	return strings.TrimSpace(name) + apiKeyApproverMarker + strings.TrimSpace(keyIdentity) + ")"
}

// ApproverPrincipal returns the identity a quorum counts once. For a REST
// decision it is the API key, not the typed name: one credential is one
// approver however many names are typed with it. Otherwise it is the
// approver name, case-insensitive.
func ApproverPrincipal(approver string) string {
	approver = strings.TrimSpace(approver)
	if i := strings.LastIndex(approver, apiKeyApproverMarker); i >= 0 && strings.HasSuffix(approver, ")") {
		key := strings.TrimSpace(approver[i+len(apiKeyApproverMarker) : len(approver)-1])
		if key != "" {
			return "api-key:" + strings.ToLower(key)
		}
	}
	return strings.ToLower(approver)
}

// AppendApprovalDecision records one approver's decision on a pending
// request, in memory; the caller persists it with a status update. The
// ApprovalReconciler then evaluates the decisions against the rule
// (quorum, change window), so every path that decides (annotations, REST
// API, dashboard) goes through the same evaluation.
func AppendApprovalDecision(ar *platformv1alpha1.ApprovalRequest, approver, decision, reason string, at metav1.Time) error {
	if ar.Status.State != "" && ar.Status.State != platformv1alpha1.ApprovalStatePending {
		return fmt.Errorf("%w: state is %s", ErrApprovalNotPending, ar.Status.State)
	}
	approver = strings.TrimSpace(approver)
	if approver == "" || (decision != ApprovalDecisionApproved && decision != ApprovalDecisionRejected) {
		return fmt.Errorf("%w: approver %q, decision %q", ErrInvalidApprovalDecision, approver, decision)
	}
	principal := ApproverPrincipal(approver)
	for _, d := range ar.Status.Decisions {
		if ApproverPrincipal(d.Approver) == principal {
			return fmt.Errorf("%w: %s", ErrApproverAlreadyDecided, d.Approver)
		}
	}
	ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
		Approver:  approver,
		Decision:  decision,
		Reason:    strings.TrimSpace(reason),
		Timestamp: at,
	})
	return nil
}

// approvalVerdict is the outcome of the decisions recorded so far.
type approvalVerdict struct {
	rejected  bool
	rejector  string
	approved  bool
	auto      bool
	approvals int
	required  int
}

// tallyDecisions evaluates the recorded decisions: any rejection rejects,
// and the request is approved once enough distinct approvers approved.
func tallyDecisions(ar *platformv1alpha1.ApprovalRequest, rule *platformv1alpha1.ApprovalRule) approvalVerdict {
	v := approvalVerdict{required: requiredApprovals(ar, rule)}
	seen := make(map[string]bool, len(ar.Status.Decisions))
	for _, d := range ar.Status.Decisions {
		switch d.Decision {
		case ApprovalDecisionRejected:
			if !v.rejected {
				v.rejected, v.rejector = true, d.Approver
			}
		case ApprovalDecisionApproved:
			p := ApproverPrincipal(d.Approver)
			if p != "" && !seen[p] {
				seen[p] = true
				v.approvals++
			}
		}
	}
	v.approved = !v.rejected && v.approvals >= v.required
	return v
}

// requiredApprovals is 1 for manual and auto rules, and the request's
// requiredApprovers (at least 1) for quorum rules.
func requiredApprovals(ar *platformv1alpha1.ApprovalRequest, rule *platformv1alpha1.ApprovalRule) int {
	if rule.Mode == platformv1alpha1.ApprovalModeQuorum && ar.Spec.RequiredApprovers > 1 {
		return int(ar.Spec.RequiredApprovers)
	}
	return 1
}

// approvalRule returns the rule a pending request is evaluated under. A
// request raised by the operator itself follows the built-in rule. When
// the policy or the rule is gone, the request is evaluated with its own
// timeout and required approvers, so it still needs a human and still
// expires instead of waiting forever. A read error is returned: the
// request stays pending and is retried.
func (r *ApprovalReconciler) approvalRule(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (*platformv1alpha1.ApprovalRule, error) {
	if IsSyntheticApprovalPolicy(ar.Spec.PolicyRef) {
		return DecisionEngineRule(), nil
	}
	logger := log.FromContext(ctx)
	var policy platformv1alpha1.ApprovalPolicy
	err := r.Get(ctx, types.NamespacedName{Name: ar.Spec.PolicyRef, Namespace: ar.Namespace}, &policy)
	switch {
	case errors.IsNotFound(err):
		logger.Info("ApprovalPolicy not found; evaluating the request with its own timeout and required approvers",
			"policy", ar.Spec.PolicyRef, "request", ar.Name)
		return requestOwnRule(ar), nil
	case err != nil:
		return nil, err
	}
	for i := range policy.Spec.Rules {
		if policy.Spec.Rules[i].Name == ar.Spec.RuleName {
			return &policy.Spec.Rules[i], nil
		}
	}
	logger.Info("Rule not found in ApprovalPolicy; evaluating the request with its own timeout and required approvers",
		"policy", ar.Spec.PolicyRef, "rule", ar.Spec.RuleName, "request", ar.Name)
	return requestOwnRule(ar), nil
}

// requestOwnRule is the rule for a request whose policy or rule is gone:
// human approval (quorum when the request asks for more than one), the
// request's own timeout, no change window and no auto approval.
func requestOwnRule(ar *platformv1alpha1.ApprovalRequest) *platformv1alpha1.ApprovalRule {
	mode := platformv1alpha1.ApprovalModeManual
	if ar.Spec.RequiredApprovers > 1 {
		mode = platformv1alpha1.ApprovalModeQuorum
	}
	return &platformv1alpha1.ApprovalRule{
		Name:              ar.Spec.RuleName,
		Mode:              mode,
		RequiredApprovers: ar.Spec.RequiredApprovers,
		TimeoutMinutes:    ar.Spec.TimeoutMinutes,
	}
}

func (r *ApprovalReconciler) reconcilePending(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	rule, err := r.approvalRule(ctx, ar)
	if err != nil {
		logger.Error(err, "Failed to read ApprovalPolicy; the request stays pending", "policy", ar.Spec.PolicyRef)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	now := time.Now()
	window := evaluateChangeWindow(rule.ChangeWindow, ar.CreationTimestamp.Time, now)
	if window.err != nil {
		logger.Error(window.err, "Change window is invalid: no decision can take effect and the timeout runs on wall-clock time",
			"request", ar.Name, "rule", rule.Name)
	}

	r.predictBlastRadius(ctx, ar)

	consumed, changed := ingestDecisionAnnotations(ctx, ar)
	verdict := tallyDecisions(ar, rule)
	if !verdict.rejected && !verdict.approved && rule.Mode == platformv1alpha1.ApprovalModeAuto && rule.AutoApproveConditions != nil {
		met, reasons := r.autoApproveConditionsMet(ctx, ar, rule.AutoApproveConditions)
		if met {
			verdict.approved, verdict.auto = true, true
		} else {
			logger.Info("Auto-approve conditions not met, a human decides", "request", ar.Name, "reasons", strings.Join(reasons, "; "))
		}
	}

	switch {
	case verdict.rejected:
		logger.Info("Request rejected", "request", ar.Name, "rejector", verdict.rejector)
		return r.finish(ctx, ar, rule, platformv1alpha1.ApprovalStateRejected, consumed)

	case verdict.approved && window.open:
		if verdict.auto {
			ar.Status.AutoApproved = true
			ar.Status.Decisions = append(ar.Status.Decisions, platformv1alpha1.ApprovalDecision{
				Approver:  autoPolicyApprover,
				Decision:  ApprovalDecisionApproved,
				Reason:    "All auto-approve conditions met",
				Timestamp: metav1.Now(),
			})
		}
		if rule.ChangeWindow != nil {
			setChangeWindowCondition(ar, metav1.ConditionTrue, "WithinChangeWindow", "Approved inside the change window")
		}
		logger.Info("Request approved", "request", ar.Name, "approvals", verdict.approvals, "required", verdict.required, "auto", verdict.auto)
		return r.finish(ctx, ar, rule, platformv1alpha1.ApprovalStateApproved, consumed)

	case verdict.approved:
		// Decided, waiting only for the window: the request does not expire
		// while it waits, and turns Approved when the window opens.
		msg := fmt.Sprintf("Approved by %d of %d required approver(s); waiting for the change window to open", verdict.approvals, verdict.required)
		if window.err != nil {
			msg = fmt.Sprintf("Approved, but the change window is invalid (%v); the decision cannot take effect", window.err)
		}
		if setChangeWindowCondition(ar, metav1.ConditionFalse, "OutsideChangeWindow", msg) {
			changed = true
		}
		logger.Info("Approved outside the change window, waiting", "request", ar.Name)
		if err := r.persistPending(ctx, ar, changed, consumed); err != nil {
			return ctrl.Result{}, err
		}
		if window.err != nil && window.decisionTime >= requestTimeout(ar) {
			return r.finish(ctx, ar, rule, platformv1alpha1.ApprovalStateExpired, nil)
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	// Undecided: the decision clock runs only while the change window is
	// open, so a request raised outside the window keeps its full timeout
	// for when the approvers can act on it.
	if window.decisionTime >= requestTimeout(ar) {
		logger.Info("Approval request expired", "request", ar.Name, "decisionTime", window.decisionTime, "timeout", requestTimeout(ar))
		return r.finish(ctx, ar, rule, platformv1alpha1.ApprovalStateExpired, consumed)
	}
	if err := r.persistPending(ctx, ar, changed, consumed); err != nil {
		return ctrl.Result{}, err
	}
	if rule.Mode == platformv1alpha1.ApprovalModeQuorum {
		logger.Info("Waiting for quorum", "request", ar.Name, "approvals", verdict.approvals, "required", verdict.required)
	}
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// requestTimeout is the decision time a request allows.
func requestTimeout(ar *platformv1alpha1.ApprovalRequest) time.Duration {
	return time.Duration(ar.Spec.TimeoutMinutes) * time.Minute
}

// ingestDecisionAnnotations moves approve/reject annotations into
// status.decisions (in memory) and returns the annotation keys consumed.
// A second decision from the same approver is dropped.
func ingestDecisionAnnotations(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (consumed []string, changed bool) {
	logger := log.FromContext(ctx)
	annotations := ar.GetAnnotations()
	for _, in := range []struct{ key, decision string }{
		{annotationApprove, ApprovalDecisionApproved},
		{annotationReject, ApprovalDecisionRejected},
	} {
		value, ok := annotations[in.key]
		if !ok {
			continue
		}
		consumed = append(consumed, in.key)
		approver, reason := parseDecisionAnnotation(value)
		if err := AppendApprovalDecision(ar, approver, in.decision, reason, metav1.Now()); err != nil {
			logger.Info("Ignoring decision annotation", "request", ar.Name, "annotation", in.key, "reason", err.Error())
			continue
		}
		changed = true
	}
	return consumed, changed
}

// persistPending writes the decisions of a request that stays pending and
// then drops the annotations they came from. Status first: an annotation
// removed before its decision is stored would lose the decision, and an
// Update of the object would overwrite the in-memory status with the
// stored one before the status write.
func (r *ApprovalReconciler) persistPending(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, changed bool, consumed []string) error {
	if changed {
		if ar.Status.State == "" {
			ar.Status.State = platformv1alpha1.ApprovalStatePending
		}
		if err := r.Status().Update(ctx, ar); err != nil {
			return err
		}
	}
	return r.dropAnnotations(ctx, ar, consumed)
}

// finish moves a request to a final state, records the metrics once (on
// the transition), and drops the consumed decision annotations.
func (r *ApprovalReconciler) finish(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, rule *platformv1alpha1.ApprovalRule, state platformv1alpha1.ApprovalRequestState, consumed []string) (ctrl.Result, error) {
	now := metav1.Now()
	ar.Status.State = state
	result := ApprovalDecisionApproved
	switch state {
	case platformv1alpha1.ApprovalStateApproved:
		ar.Status.ApprovedAt = &now
	case platformv1alpha1.ApprovalStateRejected:
		ar.Status.RejectedAt = &now
		result = ApprovalDecisionRejected
	case platformv1alpha1.ApprovalStateExpired:
		ar.Status.ExpiredAt = &now
		result = "expired"
	}
	if err := r.Status().Update(ctx, ar); err != nil {
		return ctrl.Result{}, err
	}
	mode := approvalMetricMode(rule, ar.Status.AutoApproved)
	approvalsTotal.WithLabelValues(mode, result).Inc()
	approvalDuration.WithLabelValues(mode).Observe(time.Since(ar.CreationTimestamp.Time).Seconds())
	if err := r.dropAnnotations(ctx, ar, consumed); err != nil {
		log.FromContext(ctx).Error(err, "Failed to remove consumed decision annotations", "request", ar.Name)
	}
	return ctrl.Result{}, nil
}

// approvalMetricMode is the mode label of approvals_total: "auto" for an
// auto approval, the rule's mode otherwise (an auto rule whose conditions
// did not hold was decided by a human, "manual").
func approvalMetricMode(rule *platformv1alpha1.ApprovalRule, autoApproved bool) string {
	switch {
	case autoApproved:
		return string(platformv1alpha1.ApprovalModeAuto)
	case rule.Mode == platformv1alpha1.ApprovalModeQuorum:
		return string(platformv1alpha1.ApprovalModeQuorum)
	default:
		return string(platformv1alpha1.ApprovalModeManual)
	}
}

// dropAnnotations removes annotation keys through a merge patch, which
// leaves the status alone.
func (r *ApprovalReconciler) dropAnnotations(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	base := ar.DeepCopy()
	annotations := ar.GetAnnotations()
	for _, k := range keys {
		delete(annotations, k)
	}
	ar.SetAnnotations(annotations)
	return r.Patch(ctx, ar, client.MergeFrom(base))
}

// setChangeWindowCondition sets the ChangeWindow condition and reports
// whether it changed.
func setChangeWindowCondition(ar *platformv1alpha1.ApprovalRequest, status metav1.ConditionStatus, reason, message string) bool {
	return meta.SetStatusCondition(&ar.Status.Conditions, metav1.Condition{
		Type:               conditionChangeWindow,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ar.Generation,
	})
}

// predictBlastRadius stores the predicted impact on the request as
// annotations, once. It is advisory: failures are logged and ignored.
func (r *ApprovalReconciler) predictBlastRadius(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) {
	if r.BlastRadiusPredictor == nil || ar.Annotations["platform.chatcli.io/blast-radius"] != "" || isChaosApprovalRequest(ar) {
		return
	}
	logger := log.FromContext(ctx)
	predCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	plan, err := r.findRemediationPlan(predCtx, ar)
	if err != nil || plan == nil || len(plan.Spec.Actions) == 0 {
		return
	}
	var issue platformv1alpha1.Issue
	if err := r.Get(predCtx, types.NamespacedName{Name: plan.Spec.IssueRef.Name, Namespace: ar.Namespace}, &issue); err != nil {
		return
	}
	prediction, err := r.BlastRadiusPredictor.PredictImpact(predCtx, issue.Spec.Resource, &plan.Spec.Actions[0])
	if err != nil || prediction == nil {
		return
	}
	base := ar.DeepCopy()
	if ar.Annotations == nil {
		ar.Annotations = make(map[string]string)
	}
	ar.Annotations["platform.chatcli.io/blast-radius"] = prediction.FormatForAI()
	ar.Annotations["platform.chatcli.io/blast-risk-level"] = prediction.RiskLevel
	if err := r.Patch(ctx, ar, client.MergeFrom(base)); err != nil {
		logger.Error(err, "Failed to update approval with blast radius", "approval", ar.Name)
		return
	}
	logger.Info("Blast radius predicted", "approval", ar.Name, "risk", prediction.RiskLevel)
}

// autoApproveConditionsMet checks every auto-approve condition. The
// severity is the Issue's: a request whose Issue cannot be read is not
// auto-approved.
func (r *ApprovalReconciler) autoApproveConditionsMet(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, conditions *platformv1alpha1.AutoApproveConditions) (bool, []string) {
	ev := ar.Spec.Evidence
	if ev == nil {
		return false, []string{"no evidence provided"}
	}
	var reasons []string
	if ev.AIConfidence < conditions.MinConfidence {
		reasons = append(reasons, fmt.Sprintf("confidence %.2f < min %.2f", ev.AIConfidence, conditions.MinConfidence))
	}
	if ev.HistoricalSuccessRate < conditions.HistoricalSuccessRate {
		reasons = append(reasons, fmt.Sprintf("success rate %.2f < min %.2f", ev.HistoricalSuccessRate, conditions.HistoricalSuccessRate))
	}
	var issue platformv1alpha1.Issue
	if err := r.Get(ctx, types.NamespacedName{Name: ar.Spec.IssueRef.Name, Namespace: ar.Namespace}, &issue); err != nil {
		reasons = append(reasons, fmt.Sprintf("issue severity unknown: %v", err))
	} else if issueSeverityRank(issue.Spec.Severity) > severityMaxRank(conditions.MaxSeverity) {
		reasons = append(reasons, fmt.Sprintf("severity %s exceeds max %s", issue.Spec.Severity, conditions.MaxSeverity))
	}
	return len(reasons) == 0, reasons
}

func (r *ApprovalReconciler) reconcileApproved(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Remove approval-pending annotation from RemediationPlan
	if !isChaosApprovalRequest(ar) {
		var plan platformv1alpha1.RemediationPlan
		err := r.Get(ctx, types.NamespacedName{Name: ar.Spec.RemediationPlanRef, Namespace: ar.Namespace}, &plan)
		switch {
		case errors.IsNotFound(err):
			logger.Info("RemediationPlan not found, skipping annotation removal")
		case err != nil:
			return ctrl.Result{}, err
		default:
			if _, exists := plan.GetAnnotations()[annotationApprovalPending]; exists {
				base := plan.DeepCopy()
				delete(plan.Annotations, annotationApprovalPending)
				if err := r.Patch(ctx, &plan, client.MergeFrom(base)); err != nil {
					return ctrl.Result{}, err
				}
				logger.Info("Removed approval-pending annotation from plan", "plan", plan.Name)
			}
		}
	}

	return ctrl.Result{}, r.countOnce(ctx, ar, ApprovalDecisionApproved)
}

func (r *ApprovalReconciler) reconcileRejected(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Annotate RemediationPlan with rejection reason
	if !isChaosApprovalRequest(ar) {
		var plan platformv1alpha1.RemediationPlan
		err := r.Get(ctx, types.NamespacedName{Name: ar.Spec.RemediationPlanRef, Namespace: ar.Namespace}, &plan)
		switch {
		case errors.IsNotFound(err):
			logger.Info("RemediationPlan not found, skipping rejection annotation")
		case err != nil:
			return ctrl.Result{}, err
		default:
			var rejectionReasons []string
			for _, d := range ar.Status.Decisions {
				if d.Decision == ApprovalDecisionRejected {
					rejectionReasons = append(rejectionReasons, fmt.Sprintf("%s: %s", d.Approver, d.Reason))
				}
			}
			reason := strings.Join(rejectionReasons, "; ")
			_, pending := plan.GetAnnotations()[annotationApprovalPending]
			if pending || plan.GetAnnotations()["platform.chatcli.io/rejection-reason"] != reason {
				base := plan.DeepCopy()
				if plan.Annotations == nil {
					plan.Annotations = make(map[string]string)
				}
				plan.Annotations["platform.chatcli.io/rejection-reason"] = reason
				delete(plan.Annotations, annotationApprovalPending)
				if err := r.Patch(ctx, &plan, client.MergeFrom(base)); err != nil {
					return ctrl.Result{}, err
				}
			}
		}
	}

	return ctrl.Result{}, r.countOnce(ctx, ar, ApprovalDecisionRejected)
}

func (r *ApprovalReconciler) reconcileExpired(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (ctrl.Result, error) {
	return ctrl.Result{}, r.countOnce(ctx, ar, "expired")
}

// countOnce adds a finished request to its policy counters once: the
// request is marked after the counters are written, and a marked request
// is skipped. Only a failure to write the mark right after a successful
// counter write can count a request twice.
func (r *ApprovalReconciler) countOnce(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, result string) error {
	if _, counted := ar.GetAnnotations()[annotationPolicyCounted]; counted {
		return nil
	}
	if err := r.updatePolicyCounters(ctx, ar, result); err != nil {
		log.FromContext(ctx).Error(err, "Failed to update policy counters", "request", ar.Name, "policy", ar.Spec.PolicyRef)
		return err
	}
	base := ar.DeepCopy()
	if ar.Annotations == nil {
		ar.Annotations = make(map[string]string)
	}
	ar.Annotations[annotationPolicyCounted] = result
	return r.Patch(ctx, ar, client.MergeFrom(base))
}

// updatePolicyCounters adds the result to the policy's counters. A request
// without an ApprovalPolicy object (synthetic, chaos, or a deleted policy)
// has nothing to count.
func (r *ApprovalReconciler) updatePolicyCounters(ctx context.Context, ar *platformv1alpha1.ApprovalRequest, result string) error {
	if IsSyntheticApprovalPolicy(ar.Spec.PolicyRef) {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var policy platformv1alpha1.ApprovalPolicy
		if err := r.Get(ctx, types.NamespacedName{Name: ar.Spec.PolicyRef, Namespace: ar.Namespace}, &policy); err != nil {
			if errors.IsNotFound(err) {
				return nil
			}
			return err
		}
		switch result {
		case ApprovalDecisionApproved:
			policy.Status.TotalApproved++
			if ar.Status.AutoApproved {
				policy.Status.TotalAutoApproved++
			}
		case ApprovalDecisionRejected:
			policy.Status.TotalRejected++
		case "expired":
			policy.Status.TotalExpired++
		}
		return r.Status().Update(ctx, &policy)
	})
}

// changeWindowState is where a request stands against its rule's window.
type changeWindowState struct {
	// open reports whether a decision may take effect now.
	open bool
	// decisionTime is how much of the request's timeout has run: the time
	// the window was open since the request was created, or the wall-clock
	// age without a window (or with an invalid one, so it still expires).
	decisionTime time.Duration
	err          error
}

// evaluateChangeWindow places a request created at created against the
// window at now.
func evaluateChangeWindow(cw *platformv1alpha1.ChangeWindowSpec, created, now time.Time) changeWindowState {
	age := now.Sub(created)
	if cw == nil {
		return changeWindowState{open: true, decisionTime: age}
	}
	loc, err := validateChangeWindow(cw)
	if err != nil {
		return changeWindowState{open: false, decisionTime: age, err: err}
	}
	return changeWindowState{
		open:         windowOpenAt(cw, now.In(loc)),
		decisionTime: windowOpenDuration(cw, loc, created, now),
	}
}

// validateChangeWindow rejects a window that can never open: an unknown
// timezone, no valid day, or startHour equal to endHour (end is exclusive).
func validateChangeWindow(cw *platformv1alpha1.ChangeWindowSpec) (*time.Location, error) {
	loc, err := time.LoadLocation(cw.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", cw.Timezone, err)
	}
	if cw.StartHour == cw.EndHour {
		return nil, fmt.Errorf("change window startHour equals endHour (%d): the window never opens", cw.StartHour)
	}
	for _, d := range cw.AllowedDays {
		for wd := time.Sunday; wd <= time.Saturday; wd++ {
			if strings.EqualFold(d, wd.String()) {
				return loc, nil
			}
		}
	}
	return nil, fmt.Errorf("change window allows no valid day (%v)", cw.AllowedDays)
}

// windowOpenAt reports whether t (already in the window's timezone) is
// inside the window: its day is allowed and its hour is in
// [startHour, endHour), or outside [endHour, startHour) for an overnight
// window (startHour > endHour).
func windowOpenAt(cw *platformv1alpha1.ChangeWindowSpec, t time.Time) bool {
	dayAllowed := false
	for _, d := range cw.AllowedDays {
		if strings.EqualFold(d, t.Weekday().String()) {
			dayAllowed = true
			break
		}
	}
	if !dayAllowed {
		return false
	}
	hour := clampInt32(t.Hour())
	if cw.StartHour <= cw.EndHour {
		return hour >= cw.StartHour && hour < cw.EndHour
	}
	return hour >= cw.StartHour || hour < cw.EndHour
}

// windowOpenDuration sums the time the window was open between from and
// to, stepping hour by hour (windows are whole hours in their timezone).
func windowOpenDuration(cw *platformv1alpha1.ChangeWindowSpec, loc *time.Location, from, to time.Time) time.Duration {
	var total time.Duration
	for t := from.In(loc); t.Before(to); {
		next := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		if !next.After(t) {
			next = t.Add(time.Hour)
		}
		if next.After(to) {
			next = to
		}
		if windowOpenAt(cw, t) {
			total += next.Sub(t)
		}
		t = next
	}
	return total
}

// parseDecisionAnnotation parses "approver:reason" format.
func parseDecisionAnnotation(value string) (approver, reason string) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(value), ""
}

// issueSeverityRank ranks an Issue severity for comparison with
// maxSeverity. An unknown severity ranks above critical, so it never
// passes a maximum.
func issueSeverityRank(sev platformv1alpha1.IssueSeverity) int {
	if rank := severityMaxRank(sev); rank > 0 {
		return rank
	}
	return 5
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

// findRemediationPlan finds the RemediationPlan associated with an ApprovalRequest.
func (r *ApprovalReconciler) findRemediationPlan(ctx context.Context, ar *platformv1alpha1.ApprovalRequest) (*platformv1alpha1.RemediationPlan, error) {
	if ar.Spec.RemediationPlanRef == "" {
		return nil, nil
	}
	var plan platformv1alpha1.RemediationPlan
	if err := r.Get(ctx, types.NamespacedName{Name: ar.Spec.RemediationPlanRef, Namespace: ar.Namespace}, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ApprovalReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.ApprovalRequest{}).
		Complete(r)
}
