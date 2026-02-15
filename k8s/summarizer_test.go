/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGenerateContext_NoData(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	ctx := summarizer.GenerateContext()
	assert.Equal(t, "[K8s Watcher: No data collected yet]", ctx)
}

func TestGenerateContext_WithDeployment(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	now := time.Now()
	snap := ResourceSnapshot{
		Timestamp: now,
		Deployment: DeploymentStatus{
			Name:              "myapp",
			Namespace:         "production",
			Replicas:          3,
			ReadyReplicas:     3,
			UpdatedReplicas:   3,
			AvailableReplicas: 3,
			Strategy:          "RollingUpdate",
			Conditions:        []string{"Available=True (MinimumReplicasAvailable)"},
		},
		Pods: []PodStatus{
			{
				Name:         "myapp-abc",
				Phase:        "Running",
				Ready:        true,
				RestartCount: 0,
				CPUUsage:     "45m",
				MemoryUsage:  "120Mi",
			},
			{
				Name:         "myapp-def",
				Phase:        "Running",
				Ready:        true,
				RestartCount: 2,
			},
		},
	}
	store.AddSnapshot(snap)

	ctx := summarizer.GenerateContext()

	// Header
	assert.Contains(t, ctx, "[K8s Context: deployment/myapp in namespace/production]")

	// Deployment status
	assert.Contains(t, ctx, "## Deployment Status")
	assert.Contains(t, ctx, "Replicas: 3/3 ready")
	assert.Contains(t, ctx, "Strategy: RollingUpdate")
	assert.Contains(t, ctx, "Available=True (MinimumReplicasAvailable)")

	// Pods
	assert.Contains(t, ctx, "## Pods (2 total)")
	assert.Contains(t, ctx, "myapp-abc: Running [Ready]")
	assert.Contains(t, ctx, "cpu=45m mem=120Mi")
	assert.Contains(t, ctx, "myapp-def: Running [Ready] restarts=2")

	// No alerts
	assert.Contains(t, ctx, "## Alerts: None active")

	// No error logs
	assert.Contains(t, ctx, "## Error Logs: None")
}

func TestGenerateContext_WithPodNotReady(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	snap := ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      2,
			ReadyReplicas: 1,
		},
		Pods: []PodStatus{
			{Name: "myapp-abc", Phase: "Running", Ready: true},
			{Name: "myapp-def", Phase: "Running", Ready: false, Conditions: []string{"Ready=False (ContainersNotReady)"}},
		},
	}
	store.AddSnapshot(snap)

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "NOT READY")
	assert.Contains(t, ctx, "ContainersNotReady")
}

func TestGenerateContext_WithTerminatedPod(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	terminatedAt := time.Now().Add(-5 * time.Minute)
	snap := ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:      "myapp",
			Namespace: "default",
			Replicas:  1,
		},
		Pods: []PodStatus{
			{
				Name:         "myapp-abc",
				Phase:        "Running",
				Ready:        true,
				RestartCount: 3,
				LastTerminated: &TerminationInfo{
					Reason:   "OOMKilled",
					ExitCode: 137,
					EndedAt:  terminatedAt,
				},
			},
		},
	}
	store.AddSnapshot(snap)

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "Last terminated: OOMKilled (exit code 137)")
}

func TestGenerateContext_WithHPA(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	snap := ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:      "myapp",
			Namespace: "default",
			Replicas:  3,
		},
		Pods: []PodStatus{},
		HPA: &HPAStatus{
			Name:            "myapp-hpa",
			MinReplicas:     2,
			MaxReplicas:     10,
			CurrentReplicas: 3,
			DesiredReplicas: 3,
			CurrentMetrics:  []string{"cpu: current=65%"},
		},
	}
	store.AddSnapshot(snap)

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "## HPA (myapp-hpa)")
	assert.Contains(t, ctx, "Replicas: 3 current, 3 desired (min=2, max=10)")
	assert.Contains(t, ctx, "cpu: current=65%")
}

func TestGenerateContext_WithEvents(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	snap := ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:      "myapp",
			Namespace: "default",
			Replicas:  1,
		},
		Pods: []PodStatus{},
		Events: []K8sEvent{
			{
				Timestamp: time.Now().Add(-30 * time.Second),
				Type:      "Normal",
				Reason:    "Pulled",
				Message:   "Successfully pulled image",
				Object:    "Pod/myapp-abc",
			},
			{
				Timestamp: time.Now().Add(-10 * time.Second),
				Type:      "Warning",
				Reason:    "BackOff",
				Message:   "Back-off restarting failed container",
				Object:    "Pod/myapp-abc",
			},
		},
	}
	store.AddSnapshot(snap)

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "## Recent Events (2)")
	assert.Contains(t, ctx, "Pulled")
	assert.Contains(t, ctx, "BackOff")
}

func TestGenerateContext_EventsTruncatedToLast10(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	events := make([]K8sEvent, 15)
	for i := range events {
		events[i] = K8sEvent{
			Timestamp: time.Now(),
			Type:      "Normal",
			Reason:    "Test",
			Message:   "event message",
			Object:    "Pod/myapp",
		}
	}

	snap := ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "myapp", Namespace: "default"},
		Events:     events,
	}
	store.AddSnapshot(snap)

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "## Recent Events (15)")
	// Count event lines in the output
	lines := strings.Split(ctx, "\n")
	eventLines := 0
	for _, l := range lines {
		if strings.Contains(l, "[Normal]") {
			eventLines++
		}
	}
	assert.Equal(t, 10, eventLines)
}

func TestGenerateContext_WithAlerts(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "myapp", Namespace: "default"},
	})

	store.AddAlert(Alert{
		Timestamp: time.Now(),
		Severity:  SeverityCritical,
		Type:      AlertPodOOMKilled,
		Message:   "Pod myapp-abc was OOMKilled",
		Object:    "myapp-abc",
	})

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "## Active Alerts (1)")
	assert.Contains(t, ctx, "[CRITICAL] OOMKilled: Pod myapp-abc was OOMKilled")
}

func TestGenerateContext_WithErrorLogs(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "myapp", Namespace: "default"},
	})

	store.AddLogs([]LogEntry{
		{PodName: "myapp-abc", Container: "app", Line: "ERROR: connection refused", Timestamp: time.Now(), IsError: true},
		{PodName: "myapp-abc", Container: "app", Line: "INFO: retrying", Timestamp: time.Now(), IsError: false},
	})

	ctx := summarizer.GenerateContext()
	assert.Contains(t, ctx, "## Recent Error Logs (1)")
	assert.Contains(t, ctx, "myapp-abc/app: ERROR: connection refused")
}

func TestGenerateStatusSummary_NoData(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	summary := summarizer.GenerateStatusSummary()
	assert.Equal(t, "No data", summary)
}

func TestGenerateStatusSummary_Healthy(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "production",
			Replicas:      3,
			ReadyReplicas: 3,
		},
		Pods: []PodStatus{
			{Name: "p1", Ready: true},
			{Name: "p2", Ready: true},
			{Name: "p3", Ready: true},
		},
	})

	summary := summarizer.GenerateStatusSummary()
	assert.Contains(t, summary, "production/myapp")
	assert.Contains(t, summary, "3/3 pods ready")
	assert.Contains(t, summary, "healthy")
	assert.Contains(t, summary, "0 alerts")
}

func TestGenerateStatusSummary_Degraded(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      3,
			ReadyReplicas: 2,
		},
	})

	summary := summarizer.GenerateStatusSummary()
	assert.Contains(t, summary, "2/3 pods ready")
	assert.Contains(t, summary, "degraded")
}

func TestGenerateStatusSummary_Critical(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	summarizer := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      3,
			ReadyReplicas: 3,
		},
	})

	store.AddAlert(Alert{
		Timestamp: time.Now(),
		Severity:  SeverityCritical,
		Type:      AlertPodOOMKilled,
		Message:   "Pod OOM",
	})

	summary := summarizer.GenerateStatusSummary()
	assert.Contains(t, summary, "critical")
	assert.Contains(t, summary, "1 alerts")
}
