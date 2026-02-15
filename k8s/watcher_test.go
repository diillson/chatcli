/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

func newTestWatcher() *ResourceWatcher {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	return &ResourceWatcher{
		store:  store,
		logger: zap.NewNop(),
		config: WatchConfig{
			Deployment: "myapp",
			Namespace:  "default",
		},
	}
}

func TestDetectAnomalies_HighRestarts(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      1,
			ReadyReplicas: 1,
		},
		Pods: []PodStatus{
			{
				Name:         "myapp-abc",
				Phase:        "Running",
				Ready:        true,
				RestartCount: 10,
			},
		},
	}

	w.detectAnomalies(snap)

	alerts := w.store.GetAlerts()
	assert.NotEmpty(t, alerts)

	found := false
	for _, a := range alerts {
		if a.Type == AlertHighRestarts {
			found = true
			assert.Equal(t, SeverityCritical, a.Severity)
			assert.Contains(t, a.Message, "myapp-abc")
			assert.Contains(t, a.Message, "10 restarts")
		}
	}
	assert.True(t, found, "expected AlertHighRestarts alert")
}

func TestDetectAnomalies_NoAlertForLowRestarts(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      1,
			ReadyReplicas: 1,
		},
		Pods: []PodStatus{
			{Name: "myapp-abc", Phase: "Running", Ready: true, RestartCount: 3},
		},
	}

	w.detectAnomalies(snap)
	alerts := w.store.GetAlerts()
	assert.Empty(t, alerts)
}

func TestDetectAnomalies_OOMKilled(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      1,
			ReadyReplicas: 1,
		},
		Pods: []PodStatus{
			{
				Name:  "myapp-abc",
				Phase: "Running",
				Ready: true,
				LastTerminated: &TerminationInfo{
					Reason:   "OOMKilled",
					ExitCode: 137,
				},
			},
		},
	}

	w.detectAnomalies(snap)

	alerts := w.store.GetAlerts()
	found := false
	for _, a := range alerts {
		if a.Type == AlertPodOOMKilled {
			found = true
			assert.Equal(t, SeverityCritical, a.Severity)
			assert.Contains(t, a.Message, "OOMKilled")
			assert.Contains(t, a.Message, "exit code 137")
		}
	}
	assert.True(t, found, "expected AlertPodOOMKilled alert")
}

func TestDetectAnomalies_PodNotReady(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      2,
			ReadyReplicas: 1,
		},
		Pods: []PodStatus{
			{Name: "myapp-abc", Phase: "Running", Ready: true},
			{Name: "myapp-def", Phase: "Running", Ready: false, ContainerCount: 2, ReadyCount: 1},
		},
	}

	w.detectAnomalies(snap)

	alerts := w.store.GetAlerts()
	var notReadyAlerts []Alert
	var deployAlerts []Alert
	for _, a := range alerts {
		if a.Type == AlertPodNotReady {
			notReadyAlerts = append(notReadyAlerts, a)
		}
		if a.Type == AlertDeployFailing {
			deployAlerts = append(deployAlerts, a)
		}
	}

	assert.Len(t, notReadyAlerts, 1)
	assert.Equal(t, SeverityWarning, notReadyAlerts[0].Severity)
	assert.Contains(t, notReadyAlerts[0].Message, "myapp-def")
	assert.Contains(t, notReadyAlerts[0].Message, "1/2 containers")

	assert.Len(t, deployAlerts, 1)
	assert.Equal(t, SeverityWarning, deployAlerts[0].Severity)
	assert.Contains(t, deployAlerts[0].Message, "1/2 replicas ready")
}

func TestDetectAnomalies_DeploymentFailing(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      3,
			ReadyReplicas: 1,
		},
		Pods: []PodStatus{},
	}

	w.detectAnomalies(snap)

	alerts := w.store.GetAlerts()
	found := false
	for _, a := range alerts {
		if a.Type == AlertDeployFailing {
			found = true
			assert.Contains(t, a.Message, "1/3 replicas ready")
		}
	}
	assert.True(t, found, "expected AlertDeployFailing alert")
}

func TestDetectAnomalies_AllHealthy(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      3,
			ReadyReplicas: 3,
		},
		Pods: []PodStatus{
			{Name: "myapp-a", Phase: "Running", Ready: true, RestartCount: 0},
			{Name: "myapp-b", Phase: "Running", Ready: true, RestartCount: 1},
			{Name: "myapp-c", Phase: "Running", Ready: true, RestartCount: 0},
		},
	}

	w.detectAnomalies(snap)
	alerts := w.store.GetAlerts()
	assert.Empty(t, alerts)
}

func TestDetectAnomalies_MultipleIssues(t *testing.T) {
	w := newTestWatcher()

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      2,
			ReadyReplicas: 0,
		},
		Pods: []PodStatus{
			{
				Name:         "myapp-abc",
				Phase:        "Running",
				Ready:        false,
				RestartCount: 10,
				LastTerminated: &TerminationInfo{
					Reason:   "OOMKilled",
					ExitCode: 137,
				},
			},
			{
				Name:  "myapp-def",
				Phase: "Running",
				Ready: false,
			},
		},
	}

	w.detectAnomalies(snap)

	alerts := w.store.GetAlerts()
	// Should have: HighRestarts, OOMKilled, 2x PodNotReady, DeployFailing = 5 alerts
	assert.GreaterOrEqual(t, len(alerts), 4)

	types := make(map[AlertType]int)
	for _, a := range alerts {
		types[a.Type]++
	}
	assert.Equal(t, 1, types[AlertHighRestarts])
	assert.Equal(t, 1, types[AlertPodOOMKilled])
	assert.Equal(t, 2, types[AlertPodNotReady])
	assert.Equal(t, 1, types[AlertDeployFailing])
}

func TestGetStore(t *testing.T) {
	w := newTestWatcher()
	assert.NotNil(t, w.store)
	assert.Equal(t, w.store, w.GetStore())
}
