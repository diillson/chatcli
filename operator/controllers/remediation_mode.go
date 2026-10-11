/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// RemediationMode is how much the operator may act on its own.
type RemediationMode string

const (
	// RemediationModeObserve analyzes Issues and never runs a plan: an
	// analyzed Issue is escalated to a human.
	RemediationModeObserve RemediationMode = "observe"
	// RemediationModeApprove runs a plan only when a configured gate (an
	// ApprovalPolicy auto rule or the decision engine) allows it, and parks
	// every other plan, and every high-risk action, for a human. The
	// default.
	RemediationModeApprove RemediationMode = "approve"
	// RemediationModeAuto runs every plan no gate parks, high-risk actions
	// included. Only by explicit choice.
	RemediationModeAuto RemediationMode = "auto"

	// RemediationModeEnv names the environment variable that sets the mode.
	RemediationModeEnv = "CHATCLI_OPERATOR_REMEDIATION_MODE"

	// RemediationModePolicyName marks an ApprovalRequest raised because the
	// remediation mode requires a human, not because a policy matched.
	RemediationModePolicyName = "remediation-mode"
)

// ParseRemediationMode reads the CHATCLI_OPERATOR_REMEDIATION_MODE value.
// Empty means approve.
func ParseRemediationMode(v string) (RemediationMode, error) {
	switch m := RemediationMode(strings.ToLower(strings.TrimSpace(v))); m {
	case "":
		return RemediationModeApprove, nil
	case RemediationModeObserve, RemediationModeApprove, RemediationModeAuto:
		return m, nil
	}
	return "", fmt.Errorf("unknown remediation mode %q: want %q, %q or %q",
		v, RemediationModeObserve, RemediationModeApprove, RemediationModeAuto)
}

// effective returns the mode in force: an unset mode is approve, so a
// reconciler built without one fails closed.
func (m RemediationMode) effective() RemediationMode {
	switch m {
	case RemediationModeObserve, RemediationModeAuto:
		return m
	default:
		return RemediationModeApprove
	}
}

// highRiskActions are the action types whose blast radius reaches beyond
// the Issue's workload: nodes, Secrets, arbitrary manifests and traffic
// policy. Outside auto mode they always wait for a human.
var highRiskActions = map[platformv1alpha1.RemediationActionType]bool{
	platformv1alpha1.ActionCordonNode:                  true,
	platformv1alpha1.ActionUncordonNode:                true,
	platformv1alpha1.ActionDrainNode:                   true,
	platformv1alpha1.ActionCordonAndDeleteDaemonSetPod: true,
	platformv1alpha1.ActionRotateSecret:                true,
	platformv1alpha1.ActionApplyManifest:               true,
	platformv1alpha1.ActionUpdateIngress:               true,
	platformv1alpha1.ActionPatchNetworkPolicy:          true,
	platformv1alpha1.ActionCustom:                      true,
}

// isHighRiskAction reports whether an action type always needs a human
// outside auto mode.
func isHighRiskAction(t platformv1alpha1.RemediationActionType) bool {
	return highRiskActions[t]
}

// firstHighRiskAction returns the first high-risk action of a list.
func firstHighRiskAction(actions []platformv1alpha1.RemediationAction) (platformv1alpha1.RemediationActionType, bool) {
	for _, a := range actions {
		if isHighRiskAction(a.Type) {
			return a.Type, true
		}
	}
	return "", false
}

// remediationMode is the mode in force for this reconciler.
func (r *RemediationReconciler) remediationMode() RemediationMode {
	return r.RemediationMode.effective()
}

// failObservedPlan fails a plan that reached Pending while the operator only
// observes: nothing runs, and the result says how to let it run.
func (r *RemediationReconciler) failObservedPlan(ctx context.Context, plan *platformv1alpha1.RemediationPlan) (ctrl.Result, error) {
	log.FromContext(ctx).Info("Remediation mode is observe; the plan is not executed", "plan", plan.Name)
	plan.Status.State = platformv1alpha1.RemediationStateFailed
	plan.Status.Result = fmt.Sprintf("Not executed: the operator runs in remediation mode %q. Set %s=approve (or the chart value remediationMode) to run plans after human approval.",
		RemediationModeObserve, RemediationModeEnv)
	return ctrl.Result{}, r.Status().Update(ctx, plan)
}

// gateByRemediationMode is the last gate before a plan runs. In auto mode it
// lets the plan through. Otherwise a plan with a high-risk action always
// waits for a human, and so does a plan no configured gate allowed:
// allowedByGate is true when an ApprovalPolicy auto rule or the decision
// engine explicitly allowed it. Agentic plans carry no actions yet; each
// of their steps is gated on its own when the AI proposes it.
func (r *RemediationReconciler) gateByRemediationMode(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue, insight *platformv1alpha1.AIInsight, allowedByGate bool) (bool, ctrl.Result, error) {
	if r.remediationMode() == RemediationModeAuto || plan.Spec.AgenticMode {
		return false, ctrl.Result{}, nil
	}
	var reason string
	if t, ok := firstHighRiskAction(plan.Spec.Actions); ok {
		reason = fmt.Sprintf("Action %s is high risk: in remediation mode %q it always needs a human approval (%s=auto lifts this)",
			t, RemediationModeApprove, RemediationModeEnv)
	} else if !allowedByGate {
		reason = fmt.Sprintf("No ApprovalPolicy auto rule or decision engine allowed this plan, and remediation mode %q runs only allowed plans: a human must approve it (%s=auto runs ungated plans)",
			RemediationModeApprove, RemediationModeEnv)
	} else {
		return false, ctrl.Result{}, nil
	}
	decision := &DecisionResult{
		RequiresApproval:   true,
		Mode:               DecisionModeApproval,
		RiskAssessment:     string(issue.Spec.Severity),
		AdjustedConfidence: float64(insight.Status.Confidence),
		Reason:             reason,
	}
	return r.parkUnderSyntheticPolicy(ctx, plan, issue, insight, RemediationModePolicyName, decision)
}
