package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

const seasonalPatternsCM = "chatcli-seasonal-patterns"

type NoiseReducer struct {
	client client.Client
}

type SeasonalPattern struct {
	SignalType  string                       `json:"signalType"`
	ResourceRef platformv1alpha1.ResourceRef `json:"resource"`
	HourOfDay   int                          `json:"hourOfDay"`
	DayOfWeek   string                       `json:"dayOfWeek"`
	Occurrences int                          `json:"occurrences"`
	FirstSeen   time.Time                    `json:"firstSeen"`
	LastSeen    time.Time                    `json:"lastSeen"`
}

func NewNoiseReducer(c client.Client) *NoiseReducer {
	return &NoiseReducer{client: c}
}

// ShouldSuppress determines whether an anomaly should be suppressed.
func (nr *NoiseReducer) ShouldSuppress(ctx context.Context, anomaly *platformv1alpha1.Anomaly) (bool, string, error) {
	// 1. Repetitive suppression
	if suppress, reason, err := nr.checkRepetitive(ctx, anomaly); err != nil {
		return false, "", err
	} else if suppress {
		return true, reason, nil
	}

	// 2. Seasonal pattern
	if suppress, reason, err := nr.checkSeasonal(ctx, anomaly); err != nil {
		return false, "", err
	} else if suppress {
		return true, reason, nil
	}

	// 3. Flap detection
	if suppress, reason, err := nr.checkFlapping(ctx, anomaly); err != nil {
		return false, "", err
	} else if suppress {
		return true, reason, nil
	}

	// 4. Alert fatigue
	score, err := nr.GetFatigueScore(ctx, anomaly.Spec.Resource)
	if err != nil {
		return false, "", err
	}
	if score > 80 {
		return true, fmt.Sprintf("Alert fatigue score %d/100 exceeds threshold", score), nil
	}

	return false, "", nil
}

func (nr *NoiseReducer) checkRepetitive(ctx context.Context, anomaly *platformv1alpha1.Anomaly) (bool, string, error) {
	var anomalies platformv1alpha1.AnomalyList
	if err := nr.client.List(ctx, &anomalies, client.InNamespace(anomaly.Namespace)); err != nil {
		return false, "", err
	}

	cutoff := time.Now().Add(-1 * time.Hour)
	count := 0
	for _, a := range anomalies.Items {
		if a.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		if a.Spec.SignalType == anomaly.Spec.SignalType &&
			a.Spec.Resource.Name == anomaly.Spec.Resource.Name &&
			a.Spec.Resource.Kind == anomaly.Spec.Resource.Kind {
			count++
		}
	}

	if count >= 5 {
		// Check if any issue has progressed (state changed) for this resource
		var issues platformv1alpha1.IssueList
		if err := nr.client.List(ctx, &issues, client.InNamespace(anomaly.Namespace)); err != nil {
			return false, "", err
		}
		hasProgress := false
		for _, iss := range issues.Items {
			if iss.Spec.Resource.Name == anomaly.Spec.Resource.Name &&
				(iss.Status.State == platformv1alpha1.IssueStateRemediating || iss.Status.State == platformv1alpha1.IssueStateAnalyzing) {
				hasProgress = true
				break
			}
		}
		if !hasProgress {
			return true, fmt.Sprintf("Repetitive: %d identical anomalies in last hour with no state change", count), nil
		}
	}
	return false, "", nil
}

func (nr *NoiseReducer) checkSeasonal(ctx context.Context, anomaly *platformv1alpha1.Anomaly) (bool, string, error) {
	patterns, err := nr.GetSeasonalPatterns(ctx, anomaly.Namespace)
	if err != nil {
		return false, "", err
	}

	now := time.Now()
	hourOfDay := now.Hour()
	dayOfWeek := now.Weekday().String()

	for _, p := range patterns {
		if p.SignalType != string(anomaly.Spec.SignalType) || p.ResourceRef.Name != anomaly.Spec.Resource.Name {
			continue
		}
		// Match if within ±30 minutes (±1 hour window)
		hourDiff := hourOfDay - p.HourOfDay
		if hourDiff < 0 {
			hourDiff = -hourDiff
		}
		if hourDiff <= 1 && p.DayOfWeek == dayOfWeek && p.Occurrences >= 3 {
			return true, fmt.Sprintf("Seasonal: pattern detected on %s ~%d:00 (%d occurrences)", dayOfWeek, p.HourOfDay, p.Occurrences), nil
		}
	}
	return false, "", nil
}

func (nr *NoiseReducer) checkFlapping(ctx context.Context, anomaly *platformv1alpha1.Anomaly) (bool, string, error) {
	var issues platformv1alpha1.IssueList
	if err := nr.client.List(ctx, &issues, client.InNamespace(anomaly.Namespace)); err != nil {
		return false, "", err
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	resolvedCount := 0
	for _, iss := range issues.Items {
		if iss.Spec.Resource.Name != anomaly.Spec.Resource.Name {
			continue
		}
		if iss.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		if iss.Status.State == platformv1alpha1.IssueStateResolved {
			resolvedCount++
		}
	}

	if resolvedCount >= 3 {
		return true, fmt.Sprintf("Flapping: %d resolved→detected cycles in 24h for this resource", resolvedCount), nil
	}
	return false, "", nil
}

// RecordAnomaly records the anomaly for seasonal pattern learning.
func (nr *NoiseReducer) RecordAnomaly(ctx context.Context, anomaly *platformv1alpha1.Anomaly) error {
	patterns, err := nr.GetSeasonalPatterns(ctx, anomaly.Namespace)
	if err != nil {
		return err
	}

	now := time.Now()
	hourOfDay := now.Hour()
	dayOfWeek := now.Weekday().String()

	found := false
	for i, p := range patterns {
		hourDiff := hourOfDay - p.HourOfDay
		if hourDiff < 0 {
			hourDiff = -hourDiff
		}
		if p.SignalType == string(anomaly.Spec.SignalType) &&
			p.ResourceRef.Name == anomaly.Spec.Resource.Name &&
			p.DayOfWeek == dayOfWeek && hourDiff <= 1 {
			patterns[i].Occurrences++
			patterns[i].LastSeen = now
			found = true
			break
		}
	}

	if !found {
		patterns = append(patterns, SeasonalPattern{
			SignalType:  string(anomaly.Spec.SignalType),
			ResourceRef: anomaly.Spec.Resource,
			HourOfDay:   hourOfDay,
			DayOfWeek:   dayOfWeek,
			Occurrences: 1,
			FirstSeen:   now,
			LastSeen:    now,
		})
	}

	return nr.saveSeasonalPatterns(ctx, anomaly.Namespace, patterns)
}

// GetSeasonalPatterns reads patterns from ConfigMap.
func (nr *NoiseReducer) GetSeasonalPatterns(ctx context.Context, namespace string) ([]SeasonalPattern, error) {
	cm := &corev1.ConfigMap{}
	err := nr.client.Get(ctx, types.NamespacedName{Name: seasonalPatternsCM, Namespace: namespace}, cm)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	data, ok := cm.Data["patterns"]
	if !ok {
		return nil, nil
	}

	var patterns []SeasonalPattern
	if err := json.Unmarshal([]byte(data), &patterns); err != nil {
		return nil, err
	}
	return patterns, nil
}

func (nr *NoiseReducer) saveSeasonalPatterns(ctx context.Context, namespace string, patterns []SeasonalPattern) error {
	data, err := json.Marshal(patterns)
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{}
	err = nr.client.Get(ctx, types.NamespacedName{Name: seasonalPatternsCM, Namespace: namespace}, cm)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: seasonalPatternsCM, Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "chatcli-operator"}},
			Data: map[string]string{"patterns": string(data)},
		}
		return nr.client.Create(ctx, cm)
	}

	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data["patterns"] = string(data)
	return nr.client.Update(ctx, cm)
}

// GetFatigueScore calculates alert fatigue score (0-100) for a resource.
func (nr *NoiseReducer) GetFatigueScore(ctx context.Context, resource platformv1alpha1.ResourceRef) (int, error) {
	var anomalies platformv1alpha1.AnomalyList
	if err := nr.client.List(ctx, &anomalies, client.InNamespace(resource.Namespace)); err != nil {
		return 0, err
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	var total, resolved int
	for _, a := range anomalies.Items {
		if a.CreationTimestamp.Time.Before(cutoff) || a.Spec.Resource.Name != resource.Name {
			continue
		}
		total++
		if a.Status.Correlated {
			resolved++
		}
	}

	if total == 0 {
		return 0, nil
	}

	// Volume factor: more alerts = higher fatigue
	volumeScore := total * 5
	if volumeScore > 50 {
		volumeScore = 50
	}

	// Auto-resolve factor: high auto-resolve = likely noise
	resolveRate := float64(resolved) / float64(total)
	resolveScore := int(resolveRate * 30)

	// Recency: more recent alerts = higher fatigue
	recencyScore := 20
	latestAge := time.Since(anomalies.Items[len(anomalies.Items)-1].CreationTimestamp.Time)
	if latestAge > 1*time.Hour {
		recencyScore = 10
	}
	if latestAge > 6*time.Hour {
		recencyScore = 5
	}

	score := volumeScore + resolveScore + recencyScore
	if score > 100 {
		score = 100
	}
	return score, nil
}
