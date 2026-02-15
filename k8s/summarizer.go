/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"fmt"
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
