package controllers

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8slabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// CheckApprovalRequired determines if a remediation plan requires approval
// by evaluating all enabled ApprovalPolicies in the namespace.
func CheckApprovalRequired(
	ctx context.Context,
	c client.Client,
	plan *platformv1alpha1.RemediationPlan,
	issue *platformv1alpha1.Issue,
	insight *platformv1alpha1.AIInsight,
) (bool, *platformv1alpha1.ApprovalPolicy, *platformv1alpha1.ApprovalRule, error) {

	var policyList platformv1alpha1.ApprovalPolicyList
	if err := c.List(ctx, &policyList, client.InNamespace(plan.Namespace)); err != nil {
		return false, nil, nil, fmt.Errorf("listing approval policies: %w", err)
	}

	for i := range policyList.Items {
		policy := &policyList.Items[i]
		if !policy.Spec.Enabled {
			continue
		}

		for j := range policy.Spec.Rules {
			rule := &policy.Spec.Rules[j]
			if ruleMatches(rule, issue, plan) {
				return true, policy, rule, nil
			}
		}
	}

	return false, nil, nil, nil
}

// ruleMatches checks if an approval rule's match criteria apply to the given issue and plan.
func ruleMatches(rule *platformv1alpha1.ApprovalRule, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) bool {
	match := rule.Match

	// Check severities (empty = all)
	if len(match.Severities) > 0 {
		found := false
		for _, s := range match.Severities {
			if s == issue.Spec.Severity {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check action types (empty = all)
	if len(match.ActionTypes) > 0 {
		actionMatched := false
		for _, planAction := range plan.Spec.Actions {
			for _, matchAction := range match.ActionTypes {
				if planAction.Type == matchAction {
					actionMatched = true
					break
				}
			}
			if actionMatched {
				break
			}
		}
		if !actionMatched {
			return false
		}
	}

	// Check namespaces (empty = all)
	if len(match.Namespaces) > 0 {
		found := false
		for _, ns := range match.Namespaces {
			if ns == issue.Spec.Resource.Namespace {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Check resource kinds (empty = all)
	if len(match.ResourceKinds) > 0 {
		found := false
		for _, kind := range match.ResourceKinds {
			if strings.EqualFold(kind, issue.Spec.Resource.Kind) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	return true
}

// CreateApprovalRequest creates an ApprovalRequest CR for a remediation plan that requires approval.
func CreateApprovalRequest(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	plan *platformv1alpha1.RemediationPlan,
	issue *platformv1alpha1.Issue,
	insight *platformv1alpha1.AIInsight,
	policy *platformv1alpha1.ApprovalPolicy,
	rule *platformv1alpha1.ApprovalRule,
) error {

	// Calculate blast radius
	blastRadius, err := CalculateBlastRadius(ctx, c, issue.Spec.Resource, plan.Spec.Actions)
	if err != nil {
		// Non-fatal: proceed without blast radius
		blastRadius = &platformv1alpha1.BlastRadiusAssessment{
			RiskLevel:   "unknown",
			Description: fmt.Sprintf("Failed to calculate blast radius: %v", err),
		}
	}

	// Calculate historical success rate
	successRate, previousAttempts := calculateHistoricalSuccessRateForPlan(ctx, c, plan)

	ar := &platformv1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("approval-%s", plan.Name),
			Namespace: plan.Namespace,
			Labels: map[string]string{
				"platform.chatcli.io/issue":            issue.Name,
				"platform.chatcli.io/remediation-plan": plan.Name,
				"platform.chatcli.io/policy":           policy.Name,
			},
		},
		Spec: platformv1alpha1.ApprovalRequestSpec{
			IssueRef:           plan.Spec.IssueRef,
			RemediationPlanRef: plan.Name,
			PolicyRef:          policy.Name,
			RuleName:           rule.Name,
			RequestedActions:   plan.Spec.Actions,
			Requester:          "chatcli-operator",
			BlastRadius:        blastRadius,
			Evidence: &platformv1alpha1.ApprovalEvidence{
				AIConfidence:          insight.Status.Confidence,
				AIAnalysis:            insight.Status.Analysis,
				HistoricalSuccessRate: successRate,
				PreviousAttempts:      previousAttempts,
			},
			TimeoutMinutes:    rule.TimeoutMinutes,
			RequiredApprovers: rule.RequiredApprovers,
		},
	}

	// Set owner reference to the RemediationPlan
	if err := ctrl.SetControllerReference(plan, ar, scheme); err != nil {
		return fmt.Errorf("setting owner reference: %w", err)
	}

	if err := c.Create(ctx, ar); err != nil {
		return fmt.Errorf("creating approval request: %w", err)
	}

	// Set initial status
	ar.Status.State = platformv1alpha1.ApprovalStatePending
	if err := c.Status().Update(ctx, ar); err != nil {
		return fmt.Errorf("updating approval request status: %w", err)
	}

	// Add approval-pending annotation to the plan
	annotations := plan.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationApprovalPending] = ar.Name
	plan.SetAnnotations(annotations)
	if err := c.Update(ctx, plan); err != nil {
		return fmt.Errorf("annotating remediation plan: %w", err)
	}

	return nil
}

// IsApprovalPending checks if a remediation plan has a pending approval.
func IsApprovalPending(plan *platformv1alpha1.RemediationPlan) bool {
	annotations := plan.GetAnnotations()
	if annotations == nil {
		return false
	}
	_, exists := annotations[annotationApprovalPending]
	return exists
}

// CalculateBlastRadius assesses the potential impact of remediation actions
// on a given resource by querying related pods and services.
func CalculateBlastRadius(
	ctx context.Context,
	c client.Client,
	resource platformv1alpha1.ResourceRef,
	actions []platformv1alpha1.RemediationAction,
) (*platformv1alpha1.BlastRadiusAssessment, error) {

	assessment := &platformv1alpha1.BlastRadiusAssessment{
		AffectedNamespaces: []string{resource.Namespace},
	}

	// Query pods owned by the deployment
	affectedPods := int32(0)
	affectedServices := int32(0)

	if strings.EqualFold(resource.Kind, "Deployment") {
		// Get the deployment to find its selector
		var deployment appsv1.Deployment
		if err := c.Get(ctx, client.ObjectKey{
			Name:      resource.Name,
			Namespace: resource.Namespace,
		}, &deployment); err != nil {
			return nil, fmt.Errorf("getting deployment %s/%s: %w", resource.Namespace, resource.Name, err)
		}

		// Count pods matching the deployment's selector
		if deployment.Spec.Selector != nil {
			selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
			if err != nil {
				return nil, fmt.Errorf("parsing label selector: %w", err)
			}

			var podList corev1.PodList
			if err := c.List(ctx, &podList,
				client.InNamespace(resource.Namespace),
				client.MatchingLabelsSelector{Selector: selector},
			); err != nil {
				return nil, fmt.Errorf("listing pods: %w", err)
			}
			affectedPods = int32(len(podList.Items))

			// Find services that select these pods
			var serviceList corev1.ServiceList
			if err := c.List(ctx, &serviceList, client.InNamespace(resource.Namespace)); err != nil {
				return nil, fmt.Errorf("listing services: %w", err)
			}

			podLabels := deployment.Spec.Template.Labels
			for _, svc := range serviceList.Items {
				if len(svc.Spec.Selector) == 0 {
					continue
				}
				svcSelector := k8slabels.SelectorFromSet(svc.Spec.Selector)
				if svcSelector.Matches(k8slabels.Set(podLabels)) {
					affectedServices++
				}
			}
		}

		// Use replicas as fallback if no pods found
		if affectedPods == 0 && deployment.Spec.Replicas != nil {
			affectedPods = *deployment.Spec.Replicas
		}
	} else {
		// For non-deployment resources, count pods in namespace as approximation
		var podList corev1.PodList
		if err := c.List(ctx, &podList, client.InNamespace(resource.Namespace)); err != nil {
			return nil, fmt.Errorf("listing pods in namespace: %w", err)
		}

		// Filter pods by owner reference name
		for _, pod := range podList.Items {
			for _, ref := range pod.OwnerReferences {
				if ref.Name == resource.Name {
					affectedPods++
					break
				}
			}
		}
	}

	assessment.AffectedPods = affectedPods
	assessment.AffectedServices = affectedServices

	// Determine risk level
	switch {
	case affectedPods > 10:
		assessment.RiskLevel = "critical"
		assessment.Description = fmt.Sprintf(
			"Critical blast radius: %d pods and %d services affected across %s",
			affectedPods, affectedServices, resource.Namespace)
	case affectedPods > 5:
		assessment.RiskLevel = "high"
		assessment.Description = fmt.Sprintf(
			"High blast radius: %d pods and %d services affected",
			affectedPods, affectedServices)
	case affectedPods > 2:
		assessment.RiskLevel = "medium"
		assessment.Description = fmt.Sprintf(
			"Medium blast radius: %d pods and %d services affected",
			affectedPods, affectedServices)
	default:
		assessment.RiskLevel = "low"
		assessment.Description = fmt.Sprintf(
			"Low blast radius: %d pods and %d services affected",
			affectedPods, affectedServices)
	}

	// Factor in action types
	for _, action := range actions {
		switch action.Type {
		case platformv1alpha1.ActionRollbackDeployment:
			if assessment.RiskLevel == "low" {
				assessment.RiskLevel = "medium"
				assessment.Description += "; rollback action increases risk"
			}
		case platformv1alpha1.ActionScaleDeployment:
			// Scaling is relatively safe
		case platformv1alpha1.ActionCustom:
			if assessment.RiskLevel == "low" || assessment.RiskLevel == "medium" {
				assessment.RiskLevel = "high"
				assessment.Description += "; custom action increases risk"
			}
		}
	}

	return assessment, nil
}

// calculateHistoricalSuccessRateForPlan computes the success rate for the action types in a plan.
func calculateHistoricalSuccessRateForPlan(
	ctx context.Context,
	c client.Client,
	plan *platformv1alpha1.RemediationPlan,
) (float64, int32) {

	if len(plan.Spec.Actions) == 0 {
		return 0.0, 0
	}

	var planList platformv1alpha1.RemediationPlanList
	if err := c.List(ctx, &planList, client.InNamespace(plan.Namespace)); err != nil {
		return 0.0, 0
	}

	totalCompleted := 0
	totalSucceeded := 0

	for _, existingPlan := range planList.Items {
		if existingPlan.Name == plan.Name {
			continue
		}
		if existingPlan.Status.State != platformv1alpha1.RemediationStateCompleted &&
			existingPlan.Status.State != platformv1alpha1.RemediationStateFailed &&
			existingPlan.Status.State != platformv1alpha1.RemediationStateRolledBack {
			continue
		}

		// Check if any action types match
		for _, planAction := range plan.Spec.Actions {
			for _, existingAction := range existingPlan.Spec.Actions {
				if planAction.Type == existingAction.Type {
					totalCompleted++
					if existingPlan.Status.State == platformv1alpha1.RemediationStateCompleted {
						totalSucceeded++
					}
					break
				}
			}
		}
	}

	if totalCompleted == 0 {
		return 0.0, 0
	}

	return float64(totalSucceeded) / float64(totalCompleted), int32(totalCompleted)
}
