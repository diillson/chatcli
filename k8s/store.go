/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"sync"
	"time"
)

// ObservabilityStore stores collected observability data with a fixed-size ring buffer.
type ObservabilityStore struct {
	snapshots    []ResourceSnapshot
	logs         []LogEntry
	alerts       []Alert
	maxSnapshots int
	maxLogs      int
	window       time.Duration // only keep data within this window
	mu           sync.RWMutex
}

// NewObservabilityStore creates a new store with the given capacity.
func NewObservabilityStore(maxSnapshots, maxLogs int, window time.Duration) *ObservabilityStore {
	return &ObservabilityStore{
		snapshots:    make([]ResourceSnapshot, 0, maxSnapshots),
		logs:         make([]LogEntry, 0, maxLogs),
		alerts:       make([]Alert, 0),
		maxSnapshots: maxSnapshots,
		maxLogs:      maxLogs,
		window:       window,
	}
}

// AddSnapshot adds a resource snapshot to the store.
func (s *ObservabilityStore) AddSnapshot(snap ResourceSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.snapshots = append(s.snapshots, snap)
	if len(s.snapshots) > s.maxSnapshots {
		s.snapshots = s.snapshots[1:]
	}
}

// AddLogs adds log entries to the store.
func (s *ObservabilityStore) AddLogs(entries []LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logs = append(s.logs, entries...)
	if len(s.logs) > s.maxLogs {
		s.logs = s.logs[len(s.logs)-s.maxLogs:]
	}
}

// AddAlert adds an alert to the store.
func (s *ObservabilityStore) AddAlert(alert Alert) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.alerts = append(s.alerts, alert)
	// Keep only alerts within the window
	cutoff := time.Now().Add(-s.window)
	filtered := s.alerts[:0]
	for _, a := range s.alerts {
		if a.Timestamp.After(cutoff) {
			filtered = append(filtered, a)
		}
	}
	s.alerts = filtered
}

// LatestSnapshot returns the most recent snapshot, if any.
func (s *ObservabilityStore) LatestSnapshot() (ResourceSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.snapshots) == 0 {
		return ResourceSnapshot{}, false
	}
	return s.snapshots[len(s.snapshots)-1], true
}

// GetSnapshots returns all snapshots within the observation window.
func (s *ObservabilityStore) GetSnapshots() []ResourceSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().Add(-s.window)
	result := make([]ResourceSnapshot, 0)
	for _, snap := range s.snapshots {
		if snap.Timestamp.After(cutoff) {
			result = append(result, snap)
		}
	}
	return result
}

// GetAlerts returns all active alerts within the observation window.
func (s *ObservabilityStore) GetAlerts() []Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().Add(-s.window)
	result := make([]Alert, 0)
	for _, a := range s.alerts {
		if a.Timestamp.After(cutoff) {
			result = append(result, a)
		}
	}
	return result
}

// GetRecentLogs returns the most recent log entries (up to maxLines).
func (s *ObservabilityStore) GetRecentLogs(maxLines int) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.logs) <= maxLines {
		result := make([]LogEntry, len(s.logs))
		copy(result, s.logs)
		return result
	}
	result := make([]LogEntry, maxLines)
	copy(result, s.logs[len(s.logs)-maxLines:])
	return result
}

// GetErrorLogs returns only error log entries within the window.
func (s *ObservabilityStore) GetErrorLogs(maxLines int) []LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]LogEntry, 0)
	for i := len(s.logs) - 1; i >= 0 && len(result) < maxLines; i-- {
		if s.logs[i].IsError {
			result = append(result, s.logs[i])
		}
	}
	// Reverse to maintain chronological order
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// GetRestartTrend returns the total restart count over time from snapshots.
func (s *ObservabilityStore) GetRestartTrend() (totalRestarts int32, restartsInWindow int32) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.snapshots) == 0 {
		return 0, 0
	}

	latest := s.snapshots[len(s.snapshots)-1]
	for _, pod := range latest.Pods {
		totalRestarts += pod.RestartCount
	}

	if len(s.snapshots) > 1 {
		first := s.snapshots[0]
		var firstRestarts int32
		for _, pod := range first.Pods {
			firstRestarts += pod.RestartCount
		}
		restartsInWindow = totalRestarts - firstRestarts
	}

	return totalRestarts, restartsInWindow
}

// Stats returns summary statistics for display.
func (s *ObservabilityStore) Stats() StoreStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return StoreStats{
		SnapshotCount: len(s.snapshots),
		LogCount:      len(s.logs),
		AlertCount:    len(s.alerts),
	}
}

// StoreStats holds store statistics.
type StoreStats struct {
	SnapshotCount int
	LogCount      int
	AlertCount    int
}
