/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGenerateContext_JobKind(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Resource: ResourceStatus{
			Kind:      "Job",
			Name:      "batch-job",
			Namespace: "jobs",
			Active:    1,
			Succeeded: 4,
			Failed:    2,
			Suspended: true,
		},
	})

	ctx := s.GenerateContext()
	assert.Contains(t, ctx, "[K8s Context: job/batch-job in namespace/jobs]")
	assert.Contains(t, ctx, "## Job Status")
	assert.Contains(t, ctx, "Active: 1, Succeeded: 4, Failed: 2")
	assert.Contains(t, ctx, "State: SUSPENDED")
}

func TestGenerateContext_CronJobKind(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	last := time.Now().Add(-1 * time.Hour)
	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Resource: ResourceStatus{
			Kind:             "CronJob",
			Name:             "nightly",
			Namespace:        "ops",
			Schedule:         "0 2 * * *",
			Active:           2,
			Suspended:        false,
			LastScheduleTime: &last,
		},
	})

	ctx := s.GenerateContext()
	assert.Contains(t, ctx, "## CronJob Status")
	assert.Contains(t, ctx, "Schedule: 0 2 * * *, Active Jobs: 2")
	assert.Contains(t, ctx, "Last scheduled:")
}

func TestGenerateContext_DaemonSetKind(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Resource: ResourceStatus{
			Kind:              "DaemonSet",
			Name:              "log-agent",
			Namespace:         "kube-system",
			Replicas:          5,
			ReadyReplicas:     4,
			UpdatedReplicas:   5,
			AvailableReplicas: 4,
			UnavailableCount:  1,
			Strategy:          "RollingUpdate",
		},
	})

	ctx := s.GenerateContext()
	assert.Contains(t, ctx, "## DaemonSet Status")
	assert.Contains(t, ctx, "Nodes: 4/5 ready, 5 updated, 4 available, 1 unavailable")
	assert.Contains(t, ctx, "Update Strategy: RollingUpdate")
}

func TestGenerateContext_WithNodes(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "myapp", Namespace: "default"},
		Nodes: []NodeStatus{
			{
				Name:              "node-a",
				Ready:             true,
				CPUUsage:          "1200m",
				CPUAllocatable:    "4",
				MemoryUsage:       "3Gi",
				MemoryAllocatable: "8Gi",
				PodCount:          12,
				PodCapacity:       110,
				KubeletVersion:    "v1.30.0",
			},
			{
				Name:           "node-b",
				Ready:          false,
				Unschedulable:  true,
				DiskPressure:   true,
				MemoryPressure: true,
				PIDPressure:    true,
				NetworkUnavail: true,
				Conditions:     []string{"Ready=False"},
			},
		},
	})

	ctx := s.GenerateContext()
	assert.Contains(t, ctx, "## Nodes (2)")
	assert.Contains(t, ctx, "node-a: Ready")
	assert.Contains(t, ctx, "cpu=1200m/4 mem=3Gi/8Gi")
	assert.Contains(t, ctx, "pods=12/110 k8s=v1.30.0")
	assert.Contains(t, ctx, "node-b: NOT READY")
	assert.Contains(t, ctx, "[CORDONED]")
	assert.Contains(t, ctx, "[DiskPressure]")
	assert.Contains(t, ctx, "[MemoryPressure]")
	assert.Contains(t, ctx, "[PIDPressure]")
	assert.Contains(t, ctx, "[NetworkUnavailable]")
	assert.Contains(t, ctx, "Ready=False")
}

func TestGenerateContext_WithAppMetrics(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "myapp", Namespace: "default"},
		AppMetrics: &AppMetrics{
			Timestamp: time.Now(),
			Metrics: map[string]float64{
				"http_requests_total": 1234,
				"error_rate":          0.05,
			},
		},
	})

	ctx := s.GenerateContext()
	assert.Contains(t, ctx, "## Application Metrics (2)")
	assert.Contains(t, ctx, "error_rate:")
	assert.Contains(t, ctx, "http_requests_total:")
	// Metrics rendered in sorted order: error_rate before http_requests_total.
	assert.Less(t, strings.Index(ctx, "error_rate"), strings.Index(ctx, "http_requests_total"))
}

func TestGenerateStatusSummary_JobKind(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Resource: ResourceStatus{
			Kind:      "Job",
			Name:      "j",
			Namespace: "ns",
			Active:    1,
			Succeeded: 3,
			Failed:    0,
		},
	})

	summary := s.GenerateStatusSummary()
	assert.Contains(t, summary, "ns/j (Job)")
	assert.Contains(t, summary, "active=1 succeeded=3 failed=0")
}

func TestGenerateStatusSummary_CronJobKind(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	s := NewSummarizer(store)

	store.AddSnapshot(ResourceSnapshot{
		Timestamp: time.Now(),
		Resource: ResourceStatus{
			Kind:      "CronJob",
			Name:      "cj",
			Namespace: "ns",
			Schedule:  "*/5 * * * *",
			Active:    0,
			Suspended: true,
		},
	})

	summary := s.GenerateStatusSummary()
	assert.Contains(t, summary, "ns/cj (CronJob)")
	assert.Contains(t, summary, "schedule=*/5 * * * *")
	assert.Contains(t, summary, "suspended=true")
}

func TestMultiSummarizer_EmptyAndSingle(t *testing.T) {
	// Empty.
	empty := NewMultiSummarizer(map[string]*ObservabilityStore{}, 0)
	assert.Contains(t, empty.GenerateContext(), "No targets configured")

	// Single delegates to standard summarizer.
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	store.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "solo", Namespace: "default", Replicas: 1, ReadyReplicas: 1},
	})
	single := NewMultiSummarizer(map[string]*ObservabilityStore{"a": store}, 0)
	out := single.GenerateContext()
	assert.Contains(t, out, "[K8s Context: deployment/solo")
}

func TestMultiSummarizer_MultipleTargets(t *testing.T) {
	healthy := NewObservabilityStore(10, 100, 2*time.Hour)
	healthy.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "ok", Namespace: "default", Replicas: 2, ReadyReplicas: 2},
	})

	degraded := NewObservabilityStore(10, 100, 2*time.Hour)
	degraded.AddSnapshot(ResourceSnapshot{
		Timestamp:  time.Now(),
		Deployment: DeploymentStatus{Name: "bad", Namespace: "default", Replicas: 3, ReadyReplicas: 1},
	})
	degraded.AddAlert(Alert{
		Timestamp: time.Now(),
		Severity:  SeverityCritical,
		Type:      AlertPodOOMKilled,
		Message:   "boom",
	})

	ms := NewMultiSummarizer(map[string]*ObservabilityStore{
		"ok":  healthy,
		"bad": degraded,
	}, 32000)

	ctx := ms.GenerateContext()
	assert.Contains(t, ctx, "[K8s Multi-Watcher: 2 targets monitored]")
	assert.Contains(t, ctx, "Targets Requiring Attention")

	status := ms.GenerateStatusSummary()
	assert.Contains(t, status, "Watching 2 targets")
	assert.Contains(t, status, "1 critical")
}

func TestMultiSummarizer_BudgetCompression(t *testing.T) {
	// Two unhealthy targets with a tiny budget force compression/omission paths.
	mk := func(name string, ready, total int32) *ObservabilityStore {
		st := NewObservabilityStore(10, 100, 2*time.Hour)
		st.AddSnapshot(ResourceSnapshot{
			Timestamp:  time.Now(),
			Deployment: DeploymentStatus{Name: name, Namespace: "default", Replicas: total, ReadyReplicas: ready},
		})
		return st
	}

	ms := NewMultiSummarizer(map[string]*ObservabilityStore{
		"a": mk("a", 1, 3),
		"b": mk("b", 1, 3),
	}, 200) // very small budget

	ctx := ms.GenerateContext()
	assert.Contains(t, ctx, "[K8s Multi-Watcher: 2 targets monitored]")
	// Budget is tight, so output must stay near the cap.
	assert.LessOrEqual(t, len(ctx), 600)
}
