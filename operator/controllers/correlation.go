package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// CorrelationEngine groups raw Anomaly signals into Issues.
type CorrelationEngine struct {
	client client.Client
}

// NewCorrelationEngine returns a new CorrelationEngine.
func NewCorrelationEngine(c client.Client) *CorrelationEngine {
	return &CorrelationEngine{client: c}
}

// FindExistingIssue returns an active (non-terminal) Issue for the given resource, if one exists.
func (ce *CorrelationEngine) FindExistingIssue(ctx context.Context, resource platformv1alpha1.ResourceRef) (*platformv1alpha1.Issue, error) {
	var list platformv1alpha1.IssueList
	if err := ce.client.List(ctx, &list, client.InNamespace(resource.Namespace)); err != nil {
		return nil, fmt.Errorf("listing issues: %w", err)
	}

	for i := range list.Items {
		issue := &list.Items[i]
		if issue.Spec.Resource.Kind == resource.Kind &&
			issue.Spec.Resource.Name == resource.Name &&
			issue.Spec.Resource.Namespace == resource.Namespace &&
			!isTerminalIssueState(issue.Status.State) {
			return issue, nil
		}
	}
	return nil, nil
}

// FindRelatedAnomalies returns uncorrelated anomalies for the same resource within the given time window.
func (ce *CorrelationEngine) FindRelatedAnomalies(ctx context.Context, resource platformv1alpha1.ResourceRef, window time.Duration) ([]platformv1alpha1.Anomaly, error) {
	var list platformv1alpha1.AnomalyList
	if err := ce.client.List(ctx, &list, client.InNamespace(resource.Namespace)); err != nil {
		return nil, fmt.Errorf("listing anomalies: %w", err)
	}

	cutoff := time.Now().Add(-window)
	var related []platformv1alpha1.Anomaly
	for _, a := range list.Items {
		if a.Status.Correlated {
			continue
		}
		if a.Spec.Resource.Kind != resource.Kind ||
			a.Spec.Resource.Name != resource.Name ||
			a.Spec.Resource.Namespace != resource.Namespace {
			continue
		}
		if a.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		related = append(related, a)
	}

	sort.Slice(related, func(i, j int) bool {
		return related[i].CreationTimestamp.Before(&related[j].CreationTimestamp)
	})
	return related, nil
}

// CalculateRiskScore computes a 0-100 risk score based on the number and type of anomalies.
func (ce *CorrelationEngine) CalculateRiskScore(anomalies []platformv1alpha1.Anomaly) int32 {
	if len(anomalies) == 0 {
		return 0
	}

	var score float64
	for _, a := range anomalies {
		score += signalWeight(a.Spec.SignalType)
	}

	// Normalize: cap at 100
	result := int32(score)
	if result > 100 {
		result = 100
	}
	return result
}

// DetermineSeverity maps signal type and risk score to issue severity.
func (ce *CorrelationEngine) DetermineSeverity(signalType platformv1alpha1.AnomalySignalType, riskScore int32) platformv1alpha1.IssueSeverity {
	// Critical signals always produce high+ severity
	switch signalType {
	case platformv1alpha1.SignalOOMKill:
		return platformv1alpha1.IssueSeverityCritical
	}

	if riskScore >= 80 {
		return platformv1alpha1.IssueSeverityCritical
	}
	if riskScore >= 60 {
		return platformv1alpha1.IssueSeverityHigh
	}
	if riskScore >= 30 {
		return platformv1alpha1.IssueSeverityMedium
	}
	return platformv1alpha1.IssueSeverityLow
}

// DetermineSource maps anomaly source to issue source.
func (ce *CorrelationEngine) DetermineSource(source platformv1alpha1.AnomalySource) platformv1alpha1.IssueSource {
	switch source {
	case platformv1alpha1.AnomalySourcePrometheus:
		return platformv1alpha1.IssueSourcePrometheus
	case platformv1alpha1.AnomalySourceEvents:
		return platformv1alpha1.IssueSourceEvents
	case platformv1alpha1.AnomalySourceLogs:
		return platformv1alpha1.IssueSourceLogs
	case platformv1alpha1.AnomalySourceWebhook:
		return platformv1alpha1.IssueSourceWebhook
	case platformv1alpha1.AnomalySourceWatcher:
		return platformv1alpha1.IssueSourceWatcher
	default:
		return platformv1alpha1.IssueSourcePrometheus
	}
}

// GenerateIncidentID generates a unique incident ID: INC-YYYYMMDD-NNN.
func (ce *CorrelationEngine) GenerateIncidentID(ctx context.Context, namespace string) (string, error) {
	datePrefix := time.Now().Format("20060102")

	var list platformv1alpha1.IssueList
	if err := ce.client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return "", fmt.Errorf("listing issues for INC-ID: %w", err)
	}

	maxSeq := 0
	prefix := fmt.Sprintf("INC-%s-", datePrefix)
	for _, issue := range list.Items {
		if labels := issue.Labels; labels != nil {
			if id, ok := labels["platform.chatcli.io/inc-id"]; ok {
				if len(id) > len(prefix) && id[:len(prefix)] == prefix {
					var seq int
					if _, err := fmt.Sscanf(id[len(prefix):], "%d", &seq); err == nil && seq > maxSeq {
						maxSeq = seq
					}
				}
			}
		}
	}

	return fmt.Sprintf("INC-%s-%03d", datePrefix, maxSeq+1), nil
}

// MarkAnomalyCorrelated sets the anomaly as correlated with the given Issue.
func (ce *CorrelationEngine) MarkAnomalyCorrelated(ctx context.Context, anomaly *platformv1alpha1.Anomaly, issueName string) error {
	anomaly.Status.Correlated = true
	anomaly.Status.IssueRef = &platformv1alpha1.IssueRef{Name: issueName}
	return ce.client.Status().Update(ctx, anomaly)
}

// GetIssue fetches an Issue by name.
func (ce *CorrelationEngine) GetIssue(ctx context.Context, name, namespace string) (*platformv1alpha1.Issue, error) {
	var issue platformv1alpha1.Issue
	if err := ce.client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &issue); err != nil {
		return nil, err
	}
	return &issue, nil
}

func signalWeight(signalType platformv1alpha1.AnomalySignalType) float64 {
	switch signalType {
	case platformv1alpha1.SignalOOMKill:
		return 40
	case platformv1alpha1.SignalErrorRate:
		return 30
	case platformv1alpha1.SignalPodRestart:
		return 25
	case platformv1alpha1.SignalLatency:
		return 20
	case platformv1alpha1.SignalCPUHigh:
		return 15
	case platformv1alpha1.SignalMemoryHigh:
		return 15
	case platformv1alpha1.SignalPodNotReady:
		return 20
	case platformv1alpha1.SignalDeployFail:
		return 25
	default:
		return 10
	}
}

// FindRecentlyResolvedIssue returns a resolved Issue for the given resource if it was
// resolved within the cooldown window. This prevents re-triggering on stale alerts.
func (ce *CorrelationEngine) FindRecentlyResolvedIssue(ctx context.Context, resource platformv1alpha1.ResourceRef, cooldown time.Duration) (*platformv1alpha1.Issue, error) {
	var list platformv1alpha1.IssueList
	if err := ce.client.List(ctx, &list, client.InNamespace(resource.Namespace)); err != nil {
		return nil, fmt.Errorf("listing issues: %w", err)
	}

	cutoff := time.Now().Add(-cooldown)
	for i := range list.Items {
		issue := &list.Items[i]
		if issue.Spec.Resource.Kind == resource.Kind &&
			issue.Spec.Resource.Name == resource.Name &&
			issue.Spec.Resource.Namespace == resource.Namespace &&
			issue.Status.State == platformv1alpha1.IssueStateResolved &&
			issue.Status.ResolvedAt != nil &&
			issue.Status.ResolvedAt.Time.After(cutoff) {
			return issue, nil
		}
	}
	return nil, nil
}

func isTerminalIssueState(state platformv1alpha1.IssueState) bool {
	switch state {
	case platformv1alpha1.IssueStateResolved, platformv1alpha1.IssueStateEscalated, platformv1alpha1.IssueStateFailed:
		return true
	}
	return false
}
