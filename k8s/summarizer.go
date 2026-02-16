/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Summarizer generates text summaries of the observability data for LLM context injection.
type Summarizer struct {
	store *ObservabilityStore
}

// NewSummarizer creates a new summarizer.
func NewSummarizer(store *ObservabilityStore) *Summarizer {
	return &Summarizer{store: store}
}

// GenerateContext creates a text block to prepend to LLM prompts.
// This gives the AI full context about the monitored Kubernetes resources.
func (s *Summarizer) GenerateContext() string {
	snap, ok := s.store.LatestSnapshot()
	if !ok {
		return "[K8s Watcher: No data collected yet]"
	}

	var b strings.Builder

	// Header
	d := snap.Deployment
	b.WriteString(fmt.Sprintf("[K8s Context: deployment/%s in namespace/%s]\n", d.Name, d.Namespace))
	b.WriteString(fmt.Sprintf("Collected at: %s\n\n", snap.Timestamp.Format(time.RFC3339)))

	// Deployment Status
	b.WriteString("## Deployment Status\n")
	b.WriteString(fmt.Sprintf("  Replicas: %d/%d ready, %d updated, %d available\n",
		d.ReadyReplicas, d.Replicas, d.UpdatedReplicas, d.AvailableReplicas))
	b.WriteString(fmt.Sprintf("  Strategy: %s\n", d.Strategy))
	if len(d.Conditions) > 0 {
		b.WriteString("  Conditions:\n")
		for _, c := range d.Conditions {
			b.WriteString(fmt.Sprintf("    - %s\n", c))
		}
	}

	// Pod Status
	b.WriteString(fmt.Sprintf("\n## Pods (%d total)\n", len(snap.Pods)))
	totalRestarts, restartsInWindow := s.store.GetRestartTrend()
	b.WriteString(fmt.Sprintf("  Total restarts: %d (delta in window: %d)\n", totalRestarts, restartsInWindow))

	for _, pod := range snap.Pods {
		readyStr := "Ready"
		if !pod.Ready {
			readyStr = "NOT READY"
		}
		line := fmt.Sprintf("  - %s: %s [%s] restarts=%d", pod.Name, pod.Phase, readyStr, pod.RestartCount)
		if pod.CPUUsage != "" {
			line += fmt.Sprintf(" cpu=%s mem=%s", pod.CPUUsage, pod.MemoryUsage)
		}
		b.WriteString(line + "\n")

		if pod.LastTerminated != nil {
			b.WriteString(fmt.Sprintf("    Last terminated: %s (exit code %d) at %s\n",
				pod.LastTerminated.Reason, pod.LastTerminated.ExitCode,
				pod.LastTerminated.EndedAt.Format(time.RFC3339)))
		}

		for _, cond := range pod.Conditions {
			b.WriteString(fmt.Sprintf("    Condition: %s\n", cond))
		}
	}

	// HPA
	if snap.HPA != nil {
		h := snap.HPA
		b.WriteString(fmt.Sprintf("\n## HPA (%s)\n", h.Name))
		b.WriteString(fmt.Sprintf("  Replicas: %d current, %d desired (min=%d, max=%d)\n",
			h.CurrentReplicas, h.DesiredReplicas, h.MinReplicas, h.MaxReplicas))
		for _, m := range h.CurrentMetrics {
			b.WriteString(fmt.Sprintf("  Metric: %s\n", m))
		}
	}

	// Recent Events
	if len(snap.Events) > 0 {
		b.WriteString(fmt.Sprintf("\n## Recent Events (%d)\n", len(snap.Events)))
		// Show last 10 events
		start := 0
		if len(snap.Events) > 10 {
			start = len(snap.Events) - 10
		}
		for _, ev := range snap.Events[start:] {
			age := time.Since(ev.Timestamp).Truncate(time.Second)
			b.WriteString(fmt.Sprintf("  [%s] %s %s: %s (%s ago)\n",
				ev.Type, ev.Object, ev.Reason, ev.Message, age))
		}
	}

	// Alerts
	alerts := s.store.GetAlerts()
	if len(alerts) > 0 {
		b.WriteString(fmt.Sprintf("\n## Active Alerts (%d)\n", len(alerts)))
		for _, a := range alerts {
			b.WriteString(fmt.Sprintf("  [%s] %s: %s (%s)\n",
				a.Severity, a.Type, a.Message, a.Object))
		}
	} else {
		b.WriteString("\n## Alerts: None active\n")
	}

	// Application Metrics (Prometheus)
	if snap.AppMetrics != nil && len(snap.AppMetrics.Metrics) > 0 {
		b.WriteString(fmt.Sprintf("\n## Application Metrics (%d)\n", len(snap.AppMetrics.Metrics)))
		names := make([]string, 0, len(snap.AppMetrics.Metrics))
		for k := range snap.AppMetrics.Metrics {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			b.WriteString(fmt.Sprintf("  %s: %.4g\n", name, snap.AppMetrics.Metrics[name]))
		}
	}

	// Error Logs
	errorLogs := s.store.GetErrorLogs(10)
	if len(errorLogs) > 0 {
		b.WriteString(fmt.Sprintf("\n## Recent Error Logs (%d)\n", len(errorLogs)))
		for _, log := range errorLogs {
			b.WriteString(fmt.Sprintf("  [%s] %s/%s: %s\n",
				log.Timestamp.Format("15:04:05"), log.PodName, log.Container, log.Line))
		}
	} else {
		b.WriteString("\n## Error Logs: None\n")
	}

	return b.String()
}

// GenerateStatusSummary creates a compact status line for display.
func (s *Summarizer) GenerateStatusSummary() string {
	snap, ok := s.store.LatestSnapshot()
	if !ok {
		return "No data"
	}

	d := snap.Deployment
	alerts := s.store.GetAlerts()
	stats := s.store.Stats()

	healthIcon := "healthy"
	if d.ReadyReplicas < d.Replicas {
		healthIcon = "degraded"
	}
	if len(alerts) > 0 {
		for _, a := range alerts {
			if a.Severity == SeverityCritical {
				healthIcon = "critical"
				break
			}
		}
	}

	return fmt.Sprintf("%s/%s: %d/%d pods ready | %s | %d alerts | %d snapshots collected",
		d.Namespace, d.Name, d.ReadyReplicas, d.Replicas, healthIcon, len(alerts), stats.SnapshotCount)
}

// MultiSummarizer generates budget-constrained context from multiple watch targets.
type MultiSummarizer struct {
	stores   map[string]*ObservabilityStore
	maxChars int
}

// NewMultiSummarizer creates a summarizer for multi-deployment watching.
func NewMultiSummarizer(stores map[string]*ObservabilityStore, maxChars int) *MultiSummarizer {
	if maxChars <= 0 {
		maxChars = 32000
	}
	return &MultiSummarizer{
		stores:   stores,
		maxChars: maxChars,
	}
}

// GenerateContext produces a budget-constrained context block for all targets.
// Unhealthy targets get detailed context, healthy targets get compact one-liners.
// If total exceeds maxChars, progressively compresses from the healthiest targets.
func (ms *MultiSummarizer) GenerateContext() string {
	if len(ms.stores) == 0 {
		return "[K8s Watcher: No targets configured]"
	}

	// Single target: delegate to standard Summarizer (no budget overhead)
	if len(ms.stores) == 1 {
		for _, store := range ms.stores {
			return NewSummarizer(store).GenerateContext()
		}
	}

	scores := ms.scoreTargets()
	if len(scores) == 0 {
		return "[K8s Watcher: No data collected yet]"
	}

	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].AlertCount > scores[j].AlertCount
	})

	var b strings.Builder
	b.WriteString(fmt.Sprintf("[K8s Multi-Watcher: %d targets monitored]\n\n", len(scores)))

	remaining := ms.maxChars - b.Len()

	type targetContext struct {
		key     string
		score   int
		text    string
		compact string
	}

	targets := make([]targetContext, 0, len(scores))
	for _, s := range scores {
		store := ms.stores[s.Key]
		sum := NewSummarizer(store)

		tc := targetContext{
			key:     s.Key,
			score:   s.Score,
			compact: "- " + sum.GenerateStatusSummary() + "\n",
		}

		if s.Score >= 1 {
			tc.text = sum.GenerateContext() + "\n"
		} else {
			tc.text = tc.compact
		}

		targets = append(targets, tc)
	}

	total := 0
	for _, tc := range targets {
		total += len(tc.text)
	}

	// If over budget, compress from the bottom (healthiest first)
	if total > remaining {
		for i := len(targets) - 1; i >= 0 && total > remaining; i-- {
			if targets[i].text != targets[i].compact {
				total -= len(targets[i].text)
				targets[i].text = targets[i].compact
				total += len(targets[i].text)
			}
		}
	}

	// If still over, omit healthiest targets entirely
	if total > remaining {
		for i := len(targets) - 1; i >= 0 && total > remaining; i-- {
			total -= len(targets[i].text)
			targets[i].text = ""
		}
	}

	// Build output: detailed targets first
	hasDetailed := false
	for _, tc := range targets {
		if tc.text != "" && tc.text != tc.compact {
			if !hasDetailed {
				b.WriteString("--- Targets Requiring Attention ---\n\n")
				hasDetailed = true
			}
			b.WriteString(tc.text)
		}
	}

	// Then compact targets
	hasCompact := false
	for _, tc := range targets {
		if tc.text != "" && tc.text == tc.compact {
			if !hasCompact {
				b.WriteString("--- Healthy Targets ---\n")
				hasCompact = true
			}
			b.WriteString(tc.text)
		}
	}

	return b.String()
}

// GenerateStatusSummary produces a multi-target compact status line.
func (ms *MultiSummarizer) GenerateStatusSummary() string {
	totalTargets := len(ms.stores)
	healthy, warning, critical := 0, 0, 0

	for _, store := range ms.stores {
		snap, ok := store.LatestSnapshot()
		if !ok {
			continue
		}
		alerts := store.GetAlerts()
		d := snap.Deployment

		isCritical, isWarning := false, false
		for _, a := range alerts {
			if a.Severity == SeverityCritical {
				isCritical = true
			}
			if a.Severity == SeverityWarning {
				isWarning = true
			}
		}
		if d.ReadyReplicas < d.Replicas {
			isWarning = true
		}

		switch {
		case isCritical:
			critical++
		case isWarning:
			warning++
		default:
			healthy++
		}
	}

	return fmt.Sprintf("Watching %d targets: %d healthy, %d warning, %d critical",
		totalTargets, healthy, warning, critical)
}

// scoreTargets evaluates the health of each target.
func (ms *MultiSummarizer) scoreTargets() []TargetHealthScore {
	var scores []TargetHealthScore
	for key, store := range ms.stores {
		snap, ok := store.LatestSnapshot()
		if !ok {
			continue
		}
		alerts := store.GetAlerts()
		errorLogs := store.GetErrorLogs(1)

		score := 0
		d := snap.Deployment

		for _, a := range alerts {
			if a.Severity == SeverityCritical {
				score = 2
				break
			}
		}
		if score < 2 {
			if d.ReadyReplicas < d.Replicas {
				score = 1
			}
			for _, a := range alerts {
				if a.Severity == SeverityWarning && score < 1 {
					score = 1
				}
			}
			if len(errorLogs) > 0 && score < 1 {
				score = 1
			}
		}

		scores = append(scores, TargetHealthScore{
			Key:        key,
			Score:      score,
			AlertCount: len(alerts),
		})
	}
	return scores
}
