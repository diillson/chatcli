package controllers

import (
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// ConvergenceDetector analyzes agentic remediation loops for convergence, oscillation, and progress.
type ConvergenceDetector struct{}

// IsConverged returns true if the last 3 observations are identical, indicating no progress.
func (cd *ConvergenceDetector) IsConverged(history []platformv1alpha1.AgenticStep) (bool, string) {
	if len(history) < 3 {
		return false, ""
	}
	last3 := history[len(history)-3:]
	obs0 := normalizeObs(last3[0].Observation)
	obs1 := normalizeObs(last3[1].Observation)
	obs2 := normalizeObs(last3[2].Observation)
	if obs0 == obs1 && obs1 == obs2 && obs0 != "" {
		return true, fmt.Sprintf("Last 3 observations identical: %q", obs0[:min(80, len(obs0))])
	}
	return false, ""
}

// IsOscillating returns true if actions alternate in an A→B→A→B pattern.
func (cd *ConvergenceDetector) IsOscillating(history []platformv1alpha1.AgenticStep) (bool, string) {
	if len(history) < 4 {
		return false, ""
	}
	last4 := history[len(history)-4:]
	a0 := actionStr(last4[0].Action)
	a1 := actionStr(last4[1].Action)
	a2 := actionStr(last4[2].Action)
	a3 := actionStr(last4[3].Action)
	if a0 != "" && a1 != "" && a0 == a2 && a1 == a3 && a0 != a1 {
		return true, fmt.Sprintf("Oscillating between %s and %s", a0, a1)
	}
	return false, ""
}

// ShouldStop combines convergence, oscillation, timeout, and failure checks.
func (cd *ConvergenceDetector) ShouldStop(history []platformv1alpha1.AgenticStep, elapsed time.Duration) (bool, string) {
	if converged, reason := cd.IsConverged(history); converged {
		return true, "Converged: " + reason
	}
	if oscillating, reason := cd.IsOscillating(history); oscillating {
		return true, "Oscillating: " + reason
	}
	if elapsed > 8*time.Minute {
		return true, fmt.Sprintf("Approaching timeout: %s elapsed (limit: 10m)", elapsed.Round(time.Second))
	}
	// Check if last 5 actions all failed
	if len(history) >= 5 {
		allFailed := true
		for _, step := range history[len(history)-5:] {
			if step.Action == nil || !strings.HasPrefix(step.Observation, "FAILED:") {
				allFailed = false
				break
			}
		}
		if allFailed {
			return true, "Last 5 actions all failed"
		}
	}
	return false, ""
}

// EstimateProgress returns a 0.0-1.0 progress estimate based on the agentic history.
func (cd *ConvergenceDetector) EstimateProgress(history []platformv1alpha1.AgenticStep) float64 {
	if len(history) == 0 {
		return 0.0
	}
	successCount := 0
	totalActions := 0
	for _, step := range history {
		if step.Action != nil {
			totalActions++
			if !strings.HasPrefix(step.Observation, "FAILED:") {
				successCount++
			}
		}
	}
	if totalActions == 0 {
		return 0.1
	}
	baseProgress := float64(successCount) / float64(totalActions) * 0.7

	// Bonus if latest observation suggests improvement
	lastObs := history[len(history)-1].Observation
	bonus := 0.0
	if strings.Contains(lastObs, "SUCCESS") || strings.Contains(lastObs, "healthy") || strings.Contains(lastObs, "ready") {
		bonus = 0.3
	}

	progress := baseProgress + bonus
	if progress > 1.0 {
		progress = 1.0
	}
	return progress
}

func normalizeObs(s string) string {
	return strings.TrimSpace(strings.ToLower(s))
}

func actionStr(a *platformv1alpha1.RemediationAction) string {
	if a == nil {
		return ""
	}
	return string(a.Type)
}
