/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package k8s

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// ResourceWatcher monitors Kubernetes resources and collects observability data.
type ResourceWatcher struct {
	clientset     kubernetes.Interface
	metricsClient metricsv.Interface
	config        WatchConfig
	store         *ObservabilityStore
	logger        *zap.Logger

	// Collectors
	deployCollector  *DeploymentCollector
	eventCollector   *EventCollector
	logCollector     *LogCollector
	hpaCollector     *HPACollector
	metricsCollector *MetricsCollector
	nodeCollector    *NodeCollector
	promCollector    *PrometheusCollector // optional: app-level Prometheus metrics

	// Prometheus metrics recorder (optional)
	metricsRecorder WatcherMetricsRecorder
}

// NewResourceWatcher creates a new watcher for the given deployment.
func NewResourceWatcher(cfg WatchConfig, logger *zap.Logger) (*ResourceWatcher, error) {
	// Build Kubernetes config
	restConfig, err := buildKubeConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Try to create metrics client (optional - may not be available)
	var metricsClient metricsv.Interface
	mc, err := metricsv.NewForConfig(restConfig)
	if err == nil {
		metricsClient = mc
	} else {
		logger.Info("Metrics server not available, resource usage will not be collected")
	}

	// Calculate store capacity based on window and interval
	maxSnapshots := int(cfg.Window/cfg.Interval) + 1
	if maxSnapshots < 10 {
		maxSnapshots = 10
	}
	maxLogs := cfg.MaxLogLines * 10 // keep more logs in store than we collect per cycle

	store := NewObservabilityStore(maxSnapshots, maxLogs, cfg.Window)

	w := &ResourceWatcher{
		clientset:     clientset,
		metricsClient: metricsClient,
		config:        cfg,
		store:         store,
		logger:        logger,

		deployCollector:  NewDeploymentCollector(clientset, cfg.Namespace, cfg.Deployment, logger),
		eventCollector:   NewEventCollector(clientset, cfg.Namespace, cfg.Deployment, logger),
		logCollector:     NewLogCollector(clientset, cfg.Namespace, cfg.Deployment, cfg.MaxLogLines, logger),
		hpaCollector:     NewHPACollector(clientset, cfg.Namespace, cfg.Deployment, logger),
		metricsCollector: NewMetricsCollector(clientset, metricsClient, cfg.Namespace, cfg.Deployment, logger),
		nodeCollector:    NewNodeCollector(clientset, metricsClient, cfg.Namespace, cfg.Deployment, cfg.ResourceKind(), logger),
	}

	return w, nil
}

// Start begins the watch loop, collecting data at the configured interval.
// It blocks until the context is canceled.
func (w *ResourceWatcher) Start(ctx context.Context) error {
	w.logger.Info("Starting Kubernetes watcher",
		zap.String("deployment", w.config.Deployment),
		zap.String("namespace", w.config.Namespace),
		zap.Duration("interval", w.config.Interval),
		zap.Duration("window", w.config.Window),
	)

	// Initial collection
	if err := w.collect(ctx); err != nil {
		w.logger.Warn("Initial collection failed", zap.Error(err))
	}

	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("Watcher stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := w.collect(ctx); err != nil {
				w.logger.Warn("Collection cycle failed", zap.Error(err))
			}
		}
	}
}

// collect runs one full collection cycle.
func (w *ResourceWatcher) collect(ctx context.Context) error {
	w.logger.Debug("Running collection cycle")
	start := time.Now()
	target := w.config.Namespace + "/" + w.config.Deployment

	// 1. Collect deployment + pod status
	snap, err := w.deployCollector.Collect(ctx)
	if err != nil {
		if w.metricsRecorder != nil {
			w.metricsRecorder.IncrementCollectionErrors(target)
		}
		return fmt.Errorf("deployment collection failed: %w", err)
	}

	// 2. Enrich with metrics (CPU/Memory)
	snap.Pods = w.metricsCollector.Collect(ctx, snap.Pods)

	// 3. Collect events
	events, err := w.eventCollector.Collect(ctx)
	if err != nil {
		w.logger.Warn("Event collection failed", zap.Error(err))
	} else {
		snap.Events = events
	}

	// 4. Collect HPA status
	hpa, err := w.hpaCollector.Collect(ctx)
	if err != nil {
		w.logger.Debug("HPA collection failed", zap.Error(err))
	}
	snap.HPA = hpa

	// 4.5 Collect application metrics from Prometheus endpoint
	if w.promCollector != nil {
		snap.AppMetrics = w.promCollector.Collect(ctx)
	}

	// 4.6 Collect node health for nodes where target pods run
	if w.nodeCollector != nil {
		snap.Nodes = w.nodeCollector.Collect(ctx, snap.Pods)
	}

	// 5. Store snapshot
	w.store.AddSnapshot(*snap)

	// 6. Collect logs
	logs, err := w.logCollector.Collect(ctx)
	if err != nil {
		w.logger.Warn("Log collection failed", zap.Error(err))
	} else {
		w.store.AddLogs(logs)
	}

	// 7. Detect anomalies
	w.detectAnomalies(snap)

	// 8. Update Prometheus metrics
	if w.metricsRecorder != nil {
		w.metricsRecorder.ObserveCollectionDuration(target, time.Since(start).Seconds())
		w.metricsRecorder.SetPodsReady(w.config.Namespace, w.config.Deployment, float64(snap.Resource.ReadyReplicas))
		w.metricsRecorder.SetPodsDesired(w.config.Namespace, w.config.Deployment, float64(snap.Resource.Replicas))
		stats := w.store.Stats()
		w.metricsRecorder.SetSnapshotsStored(target, float64(stats.SnapshotCount))
		var totalRestarts int32
		for _, pod := range snap.Pods {
			totalRestarts += pod.RestartCount
		}
		w.metricsRecorder.SetPodRestarts(target, float64(totalRestarts))
	}

	w.logger.Debug("Collection cycle complete",
		zap.Int("pods", len(snap.Pods)),
		zap.Int("events", len(snap.Events)),
		zap.Int("logs", len(logs)),
	)

	return nil
}

// detectAnomalies checks the snapshot for common problems and creates alerts.
func (w *ResourceWatcher) detectAnomalies(snap *ResourceSnapshot) {
	now := time.Now()
	target := w.config.Namespace + "/" + w.config.Deployment

	for _, pod := range snap.Pods {
		// CrashLoopBackOff / High restarts
		if pod.RestartCount > 5 {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertHighRestarts,
				Message:   fmt.Sprintf("Pod %s has %d restarts", pod.Name, pod.RestartCount),
				Object:    pod.Name,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertHighRestarts))
			}
		}

		// OOMKilled
		if pod.LastTerminated != nil && pod.LastTerminated.Reason == "OOMKilled" {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertPodOOMKilled,
				Message:   fmt.Sprintf("Pod %s was OOMKilled (exit code %d)", pod.Name, pod.LastTerminated.ExitCode),
				Object:    pod.Name,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertPodOOMKilled))
			}
		}

		// Pod not ready
		if !pod.Ready && pod.Phase == "Running" {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertPodNotReady,
				Message:   fmt.Sprintf("Pod %s is running but not ready (%d/%d containers)", pod.Name, pod.ReadyCount, pod.ContainerCount),
				Object:    pod.Name,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertPodNotReady))
			}
		}
	}

	// Resource not at desired state (kind-aware)
	r := snap.Resource
	switch r.Kind {
	case "Deployment", "StatefulSet":
		if r.ReadyReplicas < r.Replicas {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertDeployFailing,
				Message:   fmt.Sprintf("%s %s has %d/%d replicas ready", r.Kind, r.Name, r.ReadyReplicas, r.Replicas),
				Object:    fmt.Sprintf("%s/%s", r.Kind, r.Name),
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertDeployFailing))
			}
		}
	case "DaemonSet":
		if r.ReadyReplicas < r.Replicas {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertDeployFailing,
				Message:   fmt.Sprintf("DaemonSet %s has %d/%d nodes ready", r.Name, r.ReadyReplicas, r.Replicas),
				Object:    fmt.Sprintf("DaemonSet/%s", r.Name),
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertDeployFailing))
			}
		}
	case "Job":
		if r.Failed > 0 {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertJobFailed,
				Message:   fmt.Sprintf("Job %s has %d failed pods (active=%d, succeeded=%d)", r.Name, r.Failed, r.Active, r.Succeeded),
				Object:    fmt.Sprintf("Job/%s", r.Name),
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertJobFailed))
			}
		}
	case "CronJob":
		if r.Suspended {
			// Don't alert for intentionally suspended CronJobs
		} else if r.LastScheduleTime != nil && time.Since(*r.LastScheduleTime) > 2*time.Hour {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertCronJobMissed,
				Message:   fmt.Sprintf("CronJob %s hasn't been scheduled for %s (schedule: %s)", r.Name, time.Since(*r.LastScheduleTime).Round(time.Minute), r.Schedule),
				Object:    fmt.Sprintf("CronJob/%s", r.Name),
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertCronJobMissed))
			}
		}
	default:
		// Backward compat: use legacy Deployment check
		d := snap.Deployment
		if d.ReadyReplicas < d.Replicas {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertDeployFailing,
				Message:   fmt.Sprintf("%s has %d/%d replicas ready", d.Name, d.ReadyReplicas, d.Replicas),
				Object:    d.Name,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertDeployFailing))
			}
		}
	}

	// Node-level anomalies
	w.detectNodeAnomalies(snap, now, target)
}

// detectNodeAnomalies checks node health and emits alerts for problems.
func (w *ResourceWatcher) detectNodeAnomalies(snap *ResourceSnapshot, now time.Time, target string) {
	for _, node := range snap.Nodes {
		nodeObj := fmt.Sprintf("Node/%s", node.Name)

		if !node.Ready {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertNodeNotReady,
				Message:   fmt.Sprintf("Node %s is NotReady", node.Name),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertNodeNotReady))
			}
		}

		if node.DiskPressure {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertDiskPressure,
				Message:   fmt.Sprintf("Node %s has DiskPressure", node.Name),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertDiskPressure))
			}
		}

		if node.MemoryPressure {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertMemoryPressure,
				Message:   fmt.Sprintf("Node %s has MemoryPressure", node.Name),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertMemoryPressure))
			}
		}

		if node.PIDPressure {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertPIDPressure,
				Message:   fmt.Sprintf("Node %s has PIDPressure", node.Name),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertPIDPressure))
			}
		}

		if node.NetworkUnavail {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertNetworkUnavail,
				Message:   fmt.Sprintf("Node %s has NetworkUnavailable", node.Name),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityCritical), string(AlertNetworkUnavail))
			}
		}

		if node.Unschedulable {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertNodeUnschedul,
				Message:   fmt.Sprintf("Node %s is cordoned (unschedulable)", node.Name),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertNodeUnschedul))
			}
		}

		// Pod capacity warning: node running >90% of pod capacity
		if node.PodCapacity > 0 && float64(node.PodCount)/float64(node.PodCapacity) > 0.9 {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertPodCapacityHigh,
				Message:   fmt.Sprintf("Node %s pod capacity at %d/%d (>90%%)", node.Name, node.PodCount, node.PodCapacity),
				Object:    nodeObj,
				Namespace: w.config.Namespace,
			})
			if w.metricsRecorder != nil {
				w.metricsRecorder.IncrementAlert(target, string(SeverityWarning), string(AlertPodCapacityHigh))
			}
		}
	}
}

// GetStore returns the observability store for reading data.
func (w *ResourceWatcher) GetStore() *ObservabilityStore {
	return w.store
}

// buildKubeConfig creates a Kubernetes REST config from kubeconfig or in-cluster.
func buildKubeConfig(kubeconfigPath string) (*rest.Config, error) {
	// Try in-cluster config first
	if kubeconfigPath == "" {
		config, err := rest.InClusterConfig()
		if err == nil {
			return config, nil
		}
		// Fall back to default kubeconfig path
		if home := homedir.HomeDir(); home != "" {
			kubeconfigPath = filepath.Join(home, ".kube", "config")
		}
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
}
