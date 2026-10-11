/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
	pb "github.com/diillson/chatcli/proto/chatcli/v1"
)

// annotationAgenticPendingStep holds the agentic step a plan is parked on:
// the exact action the AI proposed, which runs once its ApprovalRequest is
// approved. The AI is not asked again, so the action a human approved is
// the action that runs.
const annotationAgenticPendingStep = "platform.chatcli.io/agentic-pending-step"

// labelAgenticStep marks an ApprovalRequest raised for one agentic step.
const labelAgenticStep = "platform.chatcli.io/agentic-step"

// pendingAgenticStep is the content of annotationAgenticPendingStep.
type pendingAgenticStep struct {
	Step      int32                              `json:"step"`
	Approval  string                             `json:"approval"`
	Action    platformv1alpha1.RemediationAction `json:"action"`
	Reasoning string                             `json:"reasoning,omitempty"`
	ParkedAt  metav1.Time                        `json:"parkedAt"`
}

// pendingAgenticStepOf reads the step a plan is parked on, nil when none.
func pendingAgenticStepOf(plan *platformv1alpha1.RemediationPlan) (*pendingAgenticStep, error) {
	raw, ok := plan.GetAnnotations()[annotationAgenticPendingStep]
	if !ok {
		return nil, nil
	}
	var p pendingAgenticStep
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, fmt.Errorf("annotation %s: %w", annotationAgenticPendingStep, err)
	}
	if p.Approval == "" || p.Action.Type == "" {
		return nil, fmt.Errorf("annotation %s names no approval request or action", annotationAgenticPendingStep)
	}
	return &p, nil
}

// agenticStepApprovalName is the request one agentic step waits on.
func agenticStepApprovalName(plan *platformv1alpha1.RemediationPlan, step int32) string {
	return fmt.Sprintf("approval-%s-step-%d", plan.Name, step)
}

// describeAction renders an action and its params in a stable order, for
// the plan result and the logs.
func describeAction(a *platformv1alpha1.RemediationAction) string {
	keys := make([]string, 0, len(a.Params))
	for k := range a.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, 1+len(keys))
	parts = append(parts, string(a.Type))
	for _, k := range keys {
		parts = append(parts, k+"="+a.Params[k])
	}
	return strings.Join(parts, " ")
}

// executedAgenticActions returns the actions earlier steps of the loop took.
func executedAgenticActions(plan *platformv1alpha1.RemediationPlan) []platformv1alpha1.RemediationAction {
	var out []platformv1alpha1.RemediationAction
	for _, s := range plan.Spec.AgenticHistory {
		if s.Action != nil {
			out = append(out, *s.Action)
		}
	}
	return out
}

// validateAgenticStep runs the proposed action through the same safety
// validators a runbook plan passes, counting the actions the loop already
// took, so "at most one per plan" limits hold across steps.
func (r *RemediationReconciler) validateAgenticStep(plan *platformv1alpha1.RemediationPlan, action *platformv1alpha1.RemediationAction) error {
	actions := append(executedAgenticActions(plan), *action)
	return r.validateSafetyConstraints(actions, plan.Spec.SafetyConstraints)
}

// stepVerdict says whether one agentic step may run now.
type stepVerdict struct {
	park   bool
	policy *platformv1alpha1.ApprovalPolicy
	rule   *platformv1alpha1.ApprovalRule
	reason string
}

// agenticStepVerdict evaluates the approval gates against the single
// action the AI proposed: the ApprovalPolicies of the namespace (so rules
// matching action types now match), then the remediation mode. Outside
// auto mode a high-risk action always parks, and any other action parks
// unless a policy auto rule or the decision engine allows it. An error
// means the gate could not be evaluated and the step must not run.
func (r *RemediationReconciler) agenticStepVerdict(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue, insight *platformv1alpha1.AIInsight, action *platformv1alpha1.RemediationAction) (stepVerdict, error) {
	stepPlan := plan.DeepCopy()
	stepPlan.Spec.Actions = []platformv1alpha1.RemediationAction{*action}
	required, policy, rule, err := CheckApprovalRequired(ctx, r.Client, stepPlan, issue, insight)
	if err != nil {
		return stepVerdict{}, err
	}
	if required && (rule.Mode != platformv1alpha1.ApprovalModeAuto || rule.AutoApproveConditions != nil) {
		return stepVerdict{park: true, policy: policy, rule: rule,
			reason: fmt.Sprintf("policy %s, rule %s", policy.Name, rule.Name)}, nil
	}
	synthetic := func(reason string) stepVerdict {
		return stepVerdict{park: true, policy: SyntheticApprovalPolicy(RemediationModePolicyName), rule: DecisionEngineRule(), reason: reason}
	}
	switch {
	case r.remediationMode() == RemediationModeAuto:
		return stepVerdict{}, nil
	case isHighRiskAction(action.Type):
		return synthetic(fmt.Sprintf("%s is high risk; remediation mode %q needs a human for it", action.Type, RemediationModeApprove)), nil
	case required:
		return stepVerdict{}, nil
	case r.DecisionEngine != nil:
		decision, err := r.DecisionEngine.ShouldAutoRemediate(ctx, r.Client, issue, insight, stepPlan.Spec.Actions)
		if err != nil {
			return stepVerdict{}, err
		}
		if !decision.RequiresApproval {
			return stepVerdict{}, nil
		}
		return synthetic("decision engine: " + decision.Reason), nil
	default:
		return synthetic(fmt.Sprintf("no ApprovalPolicy auto rule or decision engine allows it, and remediation mode %q needs a human", RemediationModeApprove)), nil
	}
}

// loadInsightForGate reads the Issue's AIInsight for an approval gate. A
// missing insight is an empty one; any other read error is returned.
func (r *RemediationReconciler) loadInsightForGate(ctx context.Context, issue *platformv1alpha1.Issue) (*platformv1alpha1.AIInsight, error) {
	var insight platformv1alpha1.AIInsight
	if err := r.Get(ctx, types.NamespacedName{Name: issue.Name + "-insight", Namespace: issue.Namespace}, &insight); err != nil && !errors.IsNotFound(err) {
		return nil, err
	}
	return &insight, nil
}

// gateAgenticStep validates and gates the action the AI proposed. It
// returns handled=true when the step was rejected or parked, with what the
// caller must return; handled=false lets the step run now.
func (r *RemediationReconciler) gateAgenticStep(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue, resp *pb.AgenticStepResponse, currentStep int32, action *platformv1alpha1.RemediationAction) (bool, ctrl.Result, error) {
	if err := r.validateAgenticStep(plan, action); err != nil {
		remediationsTotal.WithLabelValues(string(action.Type), "rejected_safety").Inc()
		res, perr := r.recordRejectedAgenticStep(ctx, plan, resp, currentStep, action,
			fmt.Sprintf("REJECTED by the safety validator: %v", err))
		return true, res, perr
	}
	insight, err := r.loadInsightForGate(ctx, issue)
	if err != nil {
		return true, ctrl.Result{}, r.approvalGateUnavailable(ctx, plan, "reading the AIInsight for an agentic step", err)
	}
	verdict, err := r.agenticStepVerdict(ctx, plan, issue, insight, action)
	if err != nil {
		return true, ctrl.Result{}, r.approvalGateUnavailable(ctx, plan, "evaluating the agentic step", err)
	}
	if !verdict.park {
		return false, ctrl.Result{}, nil
	}
	res, perr := r.parkAgenticStep(ctx, plan, issue, insight, resp, currentStep, action, verdict)
	return true, res, perr
}

// recordRejectedAgenticStep records a step that did not run, so the next
// proposal sees why, and asks for another one.
func (r *RemediationReconciler) recordRejectedAgenticStep(ctx context.Context, plan *platformv1alpha1.RemediationPlan, resp *pb.AgenticStepResponse, currentStep int32, action *platformv1alpha1.RemediationAction, observation string) (ctrl.Result, error) {
	log.FromContext(ctx).Info("Agentic step rejected", "plan", plan.Name, "step", currentStep, "action", describeAction(action), "reason", observation)
	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber:  currentStep,
		AIMessage:   resp.Reasoning,
		Observation: observation,
		Timestamp:   metav1.Now(),
	})
	if res, err := r.persistAgenticStep(ctx, plan); err != nil || res.RequeueAfter > 0 {
		return res, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// parkAgenticStep raises the ApprovalRequest for one step, naming its
// concrete action, records the step on the plan and moves the plan to
// WaitingApproval. A request left over for the same step with another
// action (a park that failed half way) is replaced while still undecided.
func (r *RemediationReconciler) parkAgenticStep(ctx context.Context, plan *platformv1alpha1.RemediationPlan, issue *platformv1alpha1.Issue, insight *platformv1alpha1.AIInsight, resp *pb.AgenticStepResponse, currentStep int32, action *platformv1alpha1.RemediationAction, verdict stepVerdict) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	name := agenticStepApprovalName(plan, currentStep)
	actions := []platformv1alpha1.RemediationAction{*action}

	var stale platformv1alpha1.ApprovalRequest
	switch err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: plan.Namespace}, &stale); {
	case err == nil && !reflect.DeepEqual(stale.Spec.RequestedActions, actions):
		if stale.Status.State != "" && stale.Status.State != platformv1alpha1.ApprovalStatePending {
			return r.failAgenticStepPlan(ctx, plan, fmt.Sprintf("ApprovalRequest %s already holds a decision for another action; refusing to reuse it", name))
		}
		if err := r.Delete(ctx, &stale); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("replacing stale approval request %s: %w", name, err)
		}
	case err != nil && !errors.IsNotFound(err):
		return ctrl.Result{}, r.approvalGateUnavailable(ctx, plan, "reading the agentic step approval request", err)
	}

	pending, err := json.Marshal(pendingAgenticStep{
		Step: currentStep, Approval: name, Action: *action, Reasoning: resp.Reasoning, ParkedAt: metav1.Now(),
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := raiseApprovalRequest(ctx, r.Client, r.Scheme, approvalTarget{
		plan: plan, issue: issue, insight: insight, policy: verdict.policy, rule: verdict.rule,
		name: name, actions: actions,
		labels:          map[string]string{labelAgenticStep: strconv.Itoa(int(currentStep))},
		planAnnotations: map[string]string{annotationAgenticPendingStep: string(pending)},
	}); err != nil {
		logger.Error(err, "Failed to create the approval request for an agentic step; retrying, the step does not run", "plan", plan.Name, "step", currentStep)
		return ctrl.Result{RequeueAfter: conflictRetryDelay}, nil
	}
	r.auditApprovalRequested(ctx, plan, name)
	plan.Status.State = platformv1alpha1.RemediationStateWaitingApproval
	plan.Status.Result = fmt.Sprintf("Waiting for approval of agentic step %d: %s (%s)", currentStep, describeAction(action), verdict.reason)
	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Agentic step parked for approval", "plan", plan.Name, "step", currentStep, "action", describeAction(action), "approval", name)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// failAgenticStepPlan fails the plan with reason.
func (r *RemediationReconciler) failAgenticStepPlan(ctx context.Context, plan *platformv1alpha1.RemediationPlan, reason string) (ctrl.Result, error) {
	res, _, err := r.failAgenticPlan(ctx, plan, reason)
	return res, err
}

// resumeAgenticStep continues a plan parked on one step. The step runs only
// when its request is approved and still names the very action recorded on
// the plan; a rejection or an expiry fails the plan; an undecided request
// puts the plan back to waiting.
func (r *RemediationReconciler) resumeAgenticStep(ctx context.Context, plan *platformv1alpha1.RemediationPlan, pending *pendingAgenticStep, logger logr.Logger) (ctrl.Result, error) {
	var ar platformv1alpha1.ApprovalRequest
	if err := r.Get(ctx, types.NamespacedName{Name: pending.Approval, Namespace: plan.Namespace}, &ar); err != nil {
		if errors.IsNotFound(err) {
			return r.failAgenticStepPlan(ctx, plan, fmt.Sprintf("Approval request %s for agentic step %d was deleted before a decision", pending.Approval, pending.Step))
		}
		return ctrl.Result{}, err
	}
	switch ar.Status.State {
	case platformv1alpha1.ApprovalStateApproved:
	case platformv1alpha1.ApprovalStateRejected, platformv1alpha1.ApprovalStateExpired:
		by, reason := lastDecision(ar.Status.Decisions)
		return r.failAgenticStepPlan(ctx, plan, fmt.Sprintf("Agentic step %d (%s) was not approved: %s %s %s",
			pending.Step, describeAction(&pending.Action), ar.Status.State, by, reason))
	default:
		plan.Status.State = platformv1alpha1.RemediationStateWaitingApproval
		return ctrl.Result{RequeueAfter: 10 * time.Second}, r.Status().Update(ctx, plan)
	}
	if !reflect.DeepEqual(ar.Spec.RequestedActions, []platformv1alpha1.RemediationAction{pending.Action}) {
		return r.failAgenticStepPlan(ctx, plan, fmt.Sprintf("Approval request %s does not name the action recorded for agentic step %d; refusing to run it", ar.Name, pending.Step))
	}
	if err := r.validateAgenticStep(plan, &pending.Action); err != nil {
		return r.failAgenticStepPlan(ctx, plan, fmt.Sprintf("Agentic step %d failed the safety validator: %v", pending.Step, err))
	}
	issue, res, err := r.loadIssueForAgenticStep(ctx, plan)
	if err != nil || issue == nil {
		return res, err
	}

	approvedBy, _ := lastDecision(ar.Status.Decisions)
	action := pending.Action
	note := fmt.Sprintf("approved by %s through %s", approvedBy, ar.Name)

	// The step is taken off the plan before it runs, so a failed write
	// after the run can never make the approved action run twice.
	plan.Spec.AgenticHistory = append(plan.Spec.AgenticHistory, platformv1alpha1.AgenticStep{
		StepNumber:  pending.Step,
		AIMessage:   pending.Reasoning,
		Action:      &action,
		Observation: "RUNNING: " + note,
		Timestamp:   metav1.Now(),
	})
	delete(plan.Annotations, annotationAgenticPendingStep)
	// The time a human took to decide does not count against the loop's
	// wall-clock budget.
	startedAt := plan.Status.AgenticStartedAt
	waited := time.Since(pending.ParkedAt.Time)
	if err := r.Update(ctx, plan); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{RequeueAfter: conflictRetryDelay}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Info("Running approved agentic step", "plan", plan.Name, "step", pending.Step, "action", describeAction(&action), "approvedBy", approvedBy)
	observation := r.runAgenticAction(ctx, issue.Spec.Resource, &action)
	plan.Spec.AgenticHistory[len(plan.Spec.AgenticHistory)-1].Observation = fmt.Sprintf("%s (%s)", observation, note)
	stepCount := clampInt32(len(plan.Spec.AgenticHistory))
	if err := r.Update(ctx, plan); err != nil {
		return ctrl.Result{}, fmt.Errorf("recording the outcome of approved agentic step %d: %w", pending.Step, err)
	}
	shifted := metav1.Now()
	if startedAt != nil && waited > 0 {
		shifted = metav1.NewTime(startedAt.Add(waited))
	}
	plan.Status.AgenticStartedAt = &shifted
	plan.Status.AgenticStepCount = stepCount
	if err := r.Status().Update(ctx, plan); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}
