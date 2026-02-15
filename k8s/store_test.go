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
)

func makeSnapshot(t time.Time, pods int, restarts int32) ResourceSnapshot {
	p := make([]PodStatus, pods)
	for i := range p {
		p[i] = PodStatus{
			Name:         "pod-" + string(rune('a'+i)),
			Phase:        "Running",
			Ready:        true,
			RestartCount: restarts,
		}
	}
	return ResourceSnapshot{
		Timestamp: t,
		Deployment: DeploymentStatus{
			Name:          "myapp",
			Namespace:     "default",
			Replicas:      int32(pods),
			ReadyReplicas: int32(pods),
		},
		Pods: p,
	}
}

func TestNewObservabilityStore(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	assert.NotNil(t, store)
	assert.Equal(t, 10, store.maxSnapshots)
	assert.Equal(t, 100, store.maxLogs)
	assert.Equal(t, 2*time.Hour, store.window)
}

func TestAddSnapshot(t *testing.T) {
	store := NewObservabilityStore(3, 100, 2*time.Hour)

	now := time.Now()
	store.AddSnapshot(makeSnapshot(now, 2, 0))
	store.AddSnapshot(makeSnapshot(now.Add(30*time.Second), 2, 0))
	store.AddSnapshot(makeSnapshot(now.Add(60*time.Second), 2, 1))

	stats := store.Stats()
	assert.Equal(t, 3, stats.SnapshotCount)

	// Adding a 4th should evict the oldest
	store.AddSnapshot(makeSnapshot(now.Add(90*time.Second), 2, 2))
	stats = store.Stats()
	assert.Equal(t, 3, stats.SnapshotCount)

	snap, ok := store.LatestSnapshot()
	assert.True(t, ok)
	assert.Equal(t, int32(2), snap.Pods[0].RestartCount)
}

func TestLatestSnapshot_Empty(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	_, ok := store.LatestSnapshot()
	assert.False(t, ok)
}

func TestGetSnapshots_WindowFiltering(t *testing.T) {
	store := NewObservabilityStore(100, 100, 1*time.Minute)

	now := time.Now()
	// Old snapshot outside window
	store.AddSnapshot(makeSnapshot(now.Add(-5*time.Minute), 1, 0))
	// Recent snapshot inside window
	store.AddSnapshot(makeSnapshot(now, 1, 0))

	snaps := store.GetSnapshots()
	assert.Len(t, snaps, 1)
	assert.Equal(t, now.Unix(), snaps[0].Timestamp.Unix())
}

func TestAddLogs(t *testing.T) {
	store := NewObservabilityStore(10, 5, 2*time.Hour)

	logs := []LogEntry{
		{PodName: "pod-a", Line: "line1", Timestamp: time.Now()},
		{PodName: "pod-a", Line: "line2", Timestamp: time.Now()},
		{PodName: "pod-a", Line: "line3", Timestamp: time.Now()},
	}
	store.AddLogs(logs)
	assert.Equal(t, 3, store.Stats().LogCount)

	// Adding more logs should cap at maxLogs=5
	moreLogs := []LogEntry{
		{PodName: "pod-a", Line: "line4", Timestamp: time.Now()},
		{PodName: "pod-a", Line: "line5", Timestamp: time.Now()},
		{PodName: "pod-a", Line: "line6", Timestamp: time.Now()},
	}
	store.AddLogs(moreLogs)
	assert.Equal(t, 5, store.Stats().LogCount)

	// Should keep the most recent 5 entries
	recent := store.GetRecentLogs(10)
	assert.Len(t, recent, 5)
	assert.Equal(t, "line2", recent[0].Line)
	assert.Equal(t, "line6", recent[4].Line)
}

func TestGetRecentLogs_LessThanMax(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)
	store.AddLogs([]LogEntry{
		{PodName: "pod-a", Line: "line1"},
		{PodName: "pod-a", Line: "line2"},
	})

	recent := store.GetRecentLogs(5)
	assert.Len(t, recent, 2)
}

func TestGetErrorLogs(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)

	now := time.Now()
	store.AddLogs([]LogEntry{
		{PodName: "pod-a", Line: "INFO: starting", Timestamp: now, IsError: false},
		{PodName: "pod-a", Line: "ERROR: connection refused", Timestamp: now.Add(1 * time.Second), IsError: true},
		{PodName: "pod-a", Line: "INFO: retrying", Timestamp: now.Add(2 * time.Second), IsError: false},
		{PodName: "pod-a", Line: "FATAL: giving up", Timestamp: now.Add(3 * time.Second), IsError: true},
		{PodName: "pod-b", Line: "ERROR: disk full", Timestamp: now.Add(4 * time.Second), IsError: true},
	})

	errors := store.GetErrorLogs(10)
	assert.Len(t, errors, 3)
	// Should be in chronological order
	assert.Contains(t, errors[0].Line, "connection refused")
	assert.Contains(t, errors[1].Line, "giving up")
	assert.Contains(t, errors[2].Line, "disk full")
}

func TestGetErrorLogs_MaxLimit(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)

	store.AddLogs([]LogEntry{
		{PodName: "pod-a", Line: "error1", IsError: true},
		{PodName: "pod-a", Line: "error2", IsError: true},
		{PodName: "pod-a", Line: "error3", IsError: true},
	})

	errors := store.GetErrorLogs(2)
	assert.Len(t, errors, 2)
	// Should get the 2 most recent errors
	assert.Equal(t, "error2", errors[0].Line)
	assert.Equal(t, "error3", errors[1].Line)
}

func TestAddAlert(t *testing.T) {
	store := NewObservabilityStore(10, 100, 1*time.Minute)

	now := time.Now()

	// Add an old alert that's outside the window
	store.AddAlert(Alert{
		Timestamp: now.Add(-5 * time.Minute),
		Severity:  SeverityWarning,
		Type:      AlertHighRestarts,
		Message:   "old alert",
		Object:    "pod-a",
	})

	// Add a recent alert
	store.AddAlert(Alert{
		Timestamp: now,
		Severity:  SeverityCritical,
		Type:      AlertPodOOMKilled,
		Message:   "recent alert",
		Object:    "pod-b",
	})

	alerts := store.GetAlerts()
	assert.Len(t, alerts, 1)
	assert.Equal(t, "recent alert", alerts[0].Message)
}

func TestGetAlerts_WindowFiltering(t *testing.T) {
	store := NewObservabilityStore(10, 100, 1*time.Minute)

	now := time.Now()
	store.AddAlert(Alert{Timestamp: now, Message: "recent"})
	store.AddAlert(Alert{Timestamp: now.Add(-30 * time.Second), Message: "still fresh"})

	alerts := store.GetAlerts()
	assert.Len(t, alerts, 2)
}

func TestGetRestartTrend(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)

	total, delta := store.GetRestartTrend()
	assert.Equal(t, int32(0), total)
	assert.Equal(t, int32(0), delta)

	now := time.Now()
	store.AddSnapshot(makeSnapshot(now, 2, 3))
	total, delta = store.GetRestartTrend()
	assert.Equal(t, int32(6), total) // 2 pods x 3 restarts
	assert.Equal(t, int32(0), delta) // only 1 snapshot, no delta

	store.AddSnapshot(makeSnapshot(now.Add(30*time.Second), 2, 5))
	total, delta = store.GetRestartTrend()
	assert.Equal(t, int32(10), total) // 2 pods x 5 restarts
	assert.Equal(t, int32(4), delta)  // 10 - 6 = 4
}

func TestStats(t *testing.T) {
	store := NewObservabilityStore(10, 100, 2*time.Hour)

	stats := store.Stats()
	assert.Equal(t, 0, stats.SnapshotCount)
	assert.Equal(t, 0, stats.LogCount)
	assert.Equal(t, 0, stats.AlertCount)

	store.AddSnapshot(makeSnapshot(time.Now(), 1, 0))
	store.AddLogs([]LogEntry{{PodName: "p", Line: "l"}})
	store.AddAlert(Alert{Timestamp: time.Now(), Message: "a"})

	stats = store.Stats()
	assert.Equal(t, 1, stats.SnapshotCount)
	assert.Equal(t, 1, stats.LogCount)
	assert.Equal(t, 1, stats.AlertCount)
}
