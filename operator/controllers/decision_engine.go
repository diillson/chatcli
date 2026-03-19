package controllers

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// DecisionEngine implements confidence-based auto-remediation decisions.
type DecisionEngine struct{}

// DecisionResult contains the auto-remediation decision.
type DecisionResult struct {
	Allowed            bool
	Reason             string
	AdjustedConfidence float64
	RequiresApproval   bool
	RiskAssessment     string
}

// ShouldAutoRemediate evaluates whether the proposed remediation should be auto-executed.
func (de *DecisionEngine) ShouldAutoRemediate(ctx context.Context, c client.Client, issue *platformv1alpha1.Issue, insight *platformv1alpha1.AIInsight, actions []platformv1alpha1.RemediationAction) (*DecisionResult, error) {
	result := &DecisionResult{}

	// Circuit breaker check
	open, reason, err := de.IsCircuitBreakerOpen(ctx, c, issue.Namespace)
	if err != nil {
		return nil, fmt.Errorf("circuit breaker check: %w", err)
	}
	if open {
		result.Allowed = false
		result.RequiresApproval = true
		result.Reason = fmt.Sprintf("Circuit breaker open: %s", reason)
		result.RiskAssessment = "critical"
		return result, nil
	}

	// Base confidence from AI
	confidence := float64(insight.Status.Confidence)

	// Historical success rate adjustment
	if len(actions) > 0 {
		rate, count, err := de.GetHistoricalSuccessRate(ctx, c, actions[0].Type, issue.Namespace)
		if err == nil && count >= 3 {
			if rate >= 0.9 {
				confidence += 0.1
			} else if rate >= 0.7 {
				confidence += 0.05
			} else if rate < 0.5 {
				confidence -= 0.1
			}
		}
	}

	// Pattern match boost
	ps := NewPatternStore(c)
	if _, boost, err := ps.FindMatchingPattern(ctx, issue); err == nil {
		confidence += boost
	}

	// Time of day adjustment
	hour := time.Now().UTC().Hour()
	if hour < 9 || hour >= 18 {
		confidence -= 0.05
	}

	// Active issues penalty
	activeCount, err := de.CountActiveIssues(ctx, c, issue.Namespace)
	if err == nil && activeCount > 3 {
		penalty := float64(activeCount-3) * 0.02
		if penalty > 0.1 {
			penalty = 0.1
		}
		confidence -= penalty
	}

	// Severity multiplier
	switch issue.Spec.Severity {
	case platformv1alpha1.IssueSeverityCritical:
		confidence -= 0.1
	case platformv1alpha1.IssueSeverityHigh:
		confidence -= 0.05
	case platformv1alpha1.IssueSeverityLow:
		confidence += 0.05
	}

	if confidence > 1.0 {
		confidence = 1.0
	}
	if confidence < 0.0 {
		confidence = 0.0
	}
	result.AdjustedConfidence = confidence

	// Decision thresholds
	sev := issue.Spec.Severity
	switch {
	case confidence >= 0.95 && sev == platformv1alpha1.IssueSeverityLow:
		result.Allowed = true
		result.Reason = fmt.Sprintf("Auto-approved: high confidence (%.2f) + low severity", confidence)
		result.RiskAssessment = "low"
	case confidence >= 0.85 && sev == platformv1alpha1.IssueSeverityMedium:
		result.Allowed = true
		result.Reason = fmt.Sprintf("Auto-approved with notification: confidence %.2f + medium severity", confidence)
		result.RiskAssessment = "medium"
	case confidence >= 0.80 && sev == platformv1alpha1.IssueSeverityHigh:
		result.RequiresApproval = true
		result.Reason = fmt.Sprintf("Approval required: confidence %.2f + high severity", confidence)
		result.RiskAssessment = "high"
	default:
		result.RequiresApproval = true
		if sev == platformv1alpha1.IssueSeverityCritical {
			result.Reason = fmt.Sprintf("Manual approval required: critical severity (confidence %.2f)", confidence)
			result.RiskAssessment = "critical"
		} else {
			result.Reason = fmt.Sprintf("Manual approval required: low confidence (%.2f)", confidence)
			result.RiskAssessment = "high"
		}
	}

	return result, nil
}

// GetHistoricalSuccessRate calculates success rate for an action type in the last 30 days.
func (de *DecisionEngine) GetHistoricalSuccessRate(ctx context.Context, c client.Client, actionType platformv1alpha1.RemediationActionType, namespace string) (float64, int, error) {
	var plans platformv1alpha1.RemediationPlanList
	if err := c.List(ctx, &plans, client.InNamespace(namespace)); err != nil {
		return 0, 0, err
	}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	var completed, failed int
	for _, plan := range plans.Items {
		if plan.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		match := false
		for _, a := range plan.Spec.Actions {
			if a.Type == actionType {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		switch plan.Status.State {
		case platformv1alpha1.RemediationStateCompleted:
			completed++
		case platformv1alpha1.RemediationStateFailed, platformv1alpha1.RemediationStateRolledBack:
			failed++
		}
	}
	total := completed + failed
	if total == 0 {
		return 0, 0, nil
	}
	return float64(completed) / float64(total), total, nil
}

// CountActiveIssues counts non-terminal issues in a namespace.
func (de *DecisionEngine) CountActiveIssues(ctx context.Context, c client.Client, namespace string) (int, error) {
	var issues platformv1alpha1.IssueList
	if err := c.List(ctx, &issues, client.InNamespace(namespace)); err != nil {
		return 0, err
	}
	count := 0
	for _, iss := range issues.Items {
		if !isTerminalIssueState(iss.Status.State) {
			count++
		}
	}
	return count, nil
}

// IsCircuitBreakerOpen checks if too many remediations have failed recently.
func (de *DecisionEngine) IsCircuitBreakerOpen(ctx context.Context, c client.Client, namespace string) (bool, string, error) {
	var plans platformv1alpha1.RemediationPlanList
	if err := c.List(ctx, &plans, client.InNamespace(namespace)); err != nil {
		return false, "", err
	}
	cutoff := time.Now().Add(-1 * time.Hour)
	failedCount := 0
	for _, plan := range plans.Items {
		if plan.Status.CompletedAt == nil || plan.Status.CompletedAt.Time.Before(cutoff) {
			continue
		}
		if plan.Status.State == platformv1alpha1.RemediationStateFailed || plan.Status.State == platformv1alpha1.RemediationStateRolledBack {
			failedCount++
		}
	}
	if failedCount >= 3 {
		return true, fmt.Sprintf("%d remediations failed in last hour", failedCount), nil
	}
	return false, "", nil
}
