/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package k8s

import (
	"testing"
	"time"
)

// countingRecorder counts IncrementAlert calls per alert type.
type countingRecorder struct {
	alerts map[string]int
}

func (r *countingRecorder) ObserveCollectionDuration(string, float64) {}
func (r *countingRecorder) IncrementCollectionErrors(string)          {}
func (r *countingRecorder) IncrementAlert(_, _, alertType string) {
	r.alerts[alertType]++
}
func (r *countingRecorder) SetPodsReady(string, string, float64)   {}
func (r *countingRecorder) SetPodsDesired(string, string, float64) {}
func (r *countingRecorder) SetSnapshotsStored(string, float64)     {}
func (r *countingRecorder) SetPodRestarts(string, float64)         {}

// A standing condition is seen on every poll cycle, and the store keeps one
// alert for it; alerts_total must count that one alert, not one per cycle.
func TestDetectAnomalies_AlertMetricSkipsDeduplicatedRepeats(t *testing.T) {
	w := newTestWatcher()
	rec := &countingRecorder{alerts: map[string]int{}}
	w.metricsRecorder = rec

	snap := &ResourceSnapshot{
		Timestamp: time.Now(),
		Resource:  ResourceStatus{Kind: "Deployment", Name: "myapp", Replicas: 2, ReadyReplicas: 1},
		Pods: []PodStatus{
			{Name: "myapp-abc", Phase: "Running", Ready: true, RestartCount: 10},
		},
	}
	for cycle := 0; cycle < 3; cycle++ {
		w.detectAnomalies(snap)
	}

	if got := rec.alerts[string(AlertHighRestarts)]; got != 1 {
		t.Errorf("high-restarts alerts counted %d times over 3 cycles, want 1", got)
	}
	if got := rec.alerts[string(AlertDeployFailing)]; got != 1 {
		t.Errorf("replicas-not-ready alerts counted %d times over 3 cycles, want 1", got)
	}
	if got := len(w.store.GetAlerts()); got != 2 {
		t.Errorf("store holds %d alerts, want 2", got)
	}

	// A new object is a new alert and is counted.
	snap.Pods = append(snap.Pods, PodStatus{Name: "myapp-def", Phase: "Running", Ready: true, RestartCount: 7})
	w.detectAnomalies(snap)
	if got := rec.alerts[string(AlertHighRestarts)]; got != 2 {
		t.Errorf("after a second restarting pod: counted %d, want 2", got)
	}
}

// Every kind-specific and node-level check goes through the same gate: one
// count per new alert across cycles, whatever the resource kind.
func TestDetectAnomalies_AlertMetricOncePerConditionAcrossKinds(t *testing.T) {
	stale := time.Now().Add(-3 * time.Hour)
	node := NodeStatus{
		Name: "worker-1", Ready: false, Unschedulable: true,
		DiskPressure: true, MemoryPressure: true, PIDPressure: true, NetworkUnavail: true,
		PodCount: 95, PodCapacity: 100,
	}
	cases := []struct {
		name     string
		resource ResourceStatus
		want     map[AlertType]int
	}{
		{
			name:     "daemonset",
			resource: ResourceStatus{Kind: "DaemonSet", Name: "log-agent", Replicas: 3, ReadyReplicas: 2},
			want:     map[AlertType]int{AlertDeployFailing: 1},
		},
		{
			name:     "job",
			resource: ResourceStatus{Kind: "Job", Name: "migrate", Failed: 1},
			want:     map[AlertType]int{AlertJobFailed: 1},
		},
		{
			name:     "cronjob",
			resource: ResourceStatus{Kind: "CronJob", Name: "nightly", Schedule: "0 2 * * *", LastScheduleTime: &stale},
			want:     map[AlertType]int{AlertCronJobMissed: 1},
		},
		{
			name:     "legacy deployment alias",
			resource: ResourceStatus{},
			want:     map[AlertType]int{AlertDeployFailing: 1},
		},
	}
	nodeWant := map[AlertType]int{
		AlertNodeNotReady: 1, AlertNodeUnschedul: 1, AlertDiskPressure: 1, AlertMemoryPressure: 1,
		AlertPIDPressure: 1, AlertNetworkUnavail: 1, AlertPodCapacityHigh: 1,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWatcher()
			rec := &countingRecorder{alerts: map[string]int{}}
			w.metricsRecorder = rec
			snap := &ResourceSnapshot{
				Timestamp:  time.Now(),
				Resource:   tc.resource,
				Deployment: DeploymentStatus{Name: "legacy", Replicas: 2, ReadyReplicas: 1},
				Nodes:      []NodeStatus{node},
			}
			for cycle := 0; cycle < 3; cycle++ {
				w.detectAnomalies(snap)
			}
			for typ, n := range tc.want {
				if got := rec.alerts[string(typ)]; got != n {
					t.Errorf("%s counted %d times over 3 cycles, want %d", typ, got, n)
				}
			}
			for typ, n := range nodeWant {
				if got := rec.alerts[string(typ)]; got != n {
					t.Errorf("node %s counted %d times over 3 cycles, want %d", typ, got, n)
				}
			}
			if got := len(w.store.GetAlerts()); got != len(tc.want)+len(nodeWant) {
				t.Errorf("store holds %d alerts, want %d", got, len(tc.want)+len(nodeWant))
			}
		})
	}
}
