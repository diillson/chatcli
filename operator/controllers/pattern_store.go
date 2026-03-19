package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const patternStoreConfigMap = "chatcli-pattern-store"

type IncidentPattern struct {
	Fingerprint           string    `json:"fingerprint"`
	SignalType            string    `json:"signalType"`
	ResourceKind          string    `json:"resourceKind"`
	Severity              string    `json:"severity"`
	SuccessfulActions     []string  `json:"successfulActions"`
	SuccessCount          int32     `json:"successCount"`
	FailureCount          int32     `json:"failureCount"`
	AverageResolutionSecs float64   `json:"averageResolutionSecs"`
	LastSeenAt            time.Time `json:"lastSeenAt"`
	ConfidenceBoost       float64   `json:"confidenceBoost"`
}

type PatternStore struct {
	client client.Client
}

func NewPatternStore(c client.Client) *PatternStore {
	return &PatternStore{client: c}
}

func BuildFingerprint(signalType, resourceKind, severity string) string {
	data := fmt.Sprintf("%s|%s|%s", strings.ToLower(signalType), strings.ToLower(resourceKind), strings.ToLower(severity))
	hash := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", hash[:12])
}

func (ps *PatternStore) RecordResolution(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) error {
	fp := BuildFingerprint(issue.Spec.SignalType, issue.Spec.Resource.Kind, string(issue.Spec.Severity))
	cm, err := ps.getOrCreateCM(ctx, issue.Namespace)
	if err != nil {
		return err
	}
	pattern := ps.load(cm, fp)
	if pattern == nil {
		pattern = &IncidentPattern{Fingerprint: fp, SignalType: issue.Spec.SignalType, ResourceKind: issue.Spec.Resource.Kind, Severity: string(issue.Spec.Severity)}
	}

	var successActions []string
	if plan.Spec.AgenticMode {
		for _, step := range plan.Spec.AgenticHistory {
			if step.Action != nil && !strings.HasPrefix(step.Observation, "FAILED:") {
				successActions = append(successActions, string(step.Action.Type))
			}
		}
	} else {
		for _, a := range plan.Spec.Actions {
			successActions = append(successActions, string(a.Type))
		}
	}

	actionSet := make(map[string]bool)
	for _, a := range pattern.SuccessfulActions {
		actionSet[a] = true
	}
	for _, a := range successActions {
		actionSet[a] = true
	}
	pattern.SuccessfulActions = make([]string, 0, len(actionSet))
	for a := range actionSet {
		pattern.SuccessfulActions = append(pattern.SuccessfulActions, a)
	}

	pattern.SuccessCount++
	pattern.LastSeenAt = time.Now()

	if issue.Status.DetectedAt != nil && issue.Status.ResolvedAt != nil {
		dur := issue.Status.ResolvedAt.Time.Sub(issue.Status.DetectedAt.Time).Seconds()
		total := pattern.AverageResolutionSecs * float64(pattern.SuccessCount-1)
		pattern.AverageResolutionSecs = (total + dur) / float64(pattern.SuccessCount)
	}

	tot := pattern.SuccessCount + pattern.FailureCount
	if tot > 0 {
		pattern.ConfidenceBoost = float64(pattern.SuccessCount) / float64(tot) * 0.15
	}
	return ps.save(ctx, issue.Namespace, pattern)
}

func (ps *PatternStore) RecordFailure(ctx context.Context, issue *platformv1alpha1.Issue, plan *platformv1alpha1.RemediationPlan) error {
	fp := BuildFingerprint(issue.Spec.SignalType, issue.Spec.Resource.Kind, string(issue.Spec.Severity))
	cm, err := ps.getOrCreateCM(ctx, issue.Namespace)
	if err != nil {
		return err
	}
	pattern := ps.load(cm, fp)
	if pattern == nil {
		pattern = &IncidentPattern{Fingerprint: fp, SignalType: issue.Spec.SignalType, ResourceKind: issue.Spec.Resource.Kind, Severity: string(issue.Spec.Severity)}
	}
	pattern.FailureCount++
	pattern.LastSeenAt = time.Now()
	tot := pattern.SuccessCount + pattern.FailureCount
	if tot > 0 {
		pattern.ConfidenceBoost = float64(pattern.SuccessCount) / float64(tot) * 0.15
	}
	return ps.save(ctx, issue.Namespace, pattern)
}

func (ps *PatternStore) FindMatchingPattern(ctx context.Context, issue *platformv1alpha1.Issue) (*IncidentPattern, float64, error) {
	fp := BuildFingerprint(issue.Spec.SignalType, issue.Spec.Resource.Kind, string(issue.Spec.Severity))
	cm, err := ps.getOrCreateCM(ctx, issue.Namespace)
	if err != nil {
		return nil, 0, err
	}
	pattern := ps.load(cm, fp)
	if pattern == nil || pattern.SuccessCount < 2 {
		return pattern, 0, nil
	}
	return pattern, pattern.ConfidenceBoost, nil
}

func (ps *PatternStore) GetPatterns(ctx context.Context, namespace string) ([]IncidentPattern, error) {
	cm, err := ps.getOrCreateCM(ctx, namespace)
	if err != nil {
		return nil, err
	}
	var patterns []IncidentPattern
	for _, v := range cm.Data {
		var p IncidentPattern
		if json.Unmarshal([]byte(v), &p) == nil {
			patterns = append(patterns, p)
		}
	}
	return patterns, nil
}

func (ps *PatternStore) getOrCreateCM(ctx context.Context, ns string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := ps.client.Get(ctx, types.NamespacedName{Name: patternStoreConfigMap, Namespace: ns}, cm)
	if err == nil {
		return cm, nil
	}
	if !errors.IsNotFound(err) {
		return nil, err
	}
	cm = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: patternStoreConfigMap, Namespace: ns,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator", "app.kubernetes.io/component": "pattern-store"}},
		Data: make(map[string]string),
	}
	if err := ps.client.Create(ctx, cm); err != nil {
		if errors.IsAlreadyExists(err) {
			_ = ps.client.Get(ctx, types.NamespacedName{Name: patternStoreConfigMap, Namespace: ns}, cm)
			return cm, nil
		}
		return nil, err
	}
	return cm, nil
}

func (ps *PatternStore) load(cm *corev1.ConfigMap, fp string) *IncidentPattern {
	if cm.Data == nil {
		return nil
	}
	data, ok := cm.Data[fp]
	if !ok {
		return nil
	}
	var p IncidentPattern
	if json.Unmarshal([]byte(data), &p) != nil {
		return nil
	}
	return &p
}

func (ps *PatternStore) save(ctx context.Context, ns string, pattern *IncidentPattern) error {
	cm, err := ps.getOrCreateCM(ctx, ns)
	if err != nil {
		return err
	}
	data, err := json.Marshal(pattern)
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[pattern.Fingerprint] = string(data)
	return ps.client.Update(ctx, cm)
}
