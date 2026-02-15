/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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
	}

	return w, nil
}

// Start begins the watch loop, collecting data at the configured interval.
// It blocks until the context is cancelled.
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

	// 1. Collect deployment + pod status
	snap, err := w.deployCollector.Collect(ctx)
	if err != nil {
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

	for _, pod := range snap.Pods {
		// CrashLoopBackOff / High restarts
		if pod.RestartCount > 5 {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertHighRestarts,
				Message:   fmt.Sprintf("Pod %s has %d restarts", pod.Name, pod.RestartCount),
				Object:    pod.Name,
			})
		}

		// OOMKilled
		if pod.LastTerminated != nil && pod.LastTerminated.Reason == "OOMKilled" {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityCritical,
				Type:      AlertPodOOMKilled,
				Message:   fmt.Sprintf("Pod %s was OOMKilled (exit code %d)", pod.Name, pod.LastTerminated.ExitCode),
				Object:    pod.Name,
			})
		}

		// Pod not ready
		if !pod.Ready && pod.Phase == "Running" {
			w.store.AddAlert(Alert{
				Timestamp: now,
				Severity:  SeverityWarning,
				Type:      AlertPodNotReady,
				Message:   fmt.Sprintf("Pod %s is running but not ready (%d/%d containers)", pod.Name, pod.ReadyCount, pod.ContainerCount),
				Object:    pod.Name,
			})
		}
	}

	// Deployment not at desired replicas
	d := snap.Deployment
	if d.ReadyReplicas < d.Replicas {
		w.store.AddAlert(Alert{
			Timestamp: now,
			Severity:  SeverityWarning,
			Type:      AlertDeployFailing,
			Message:   fmt.Sprintf("Deployment %s has %d/%d replicas ready", d.Name, d.ReadyReplicas, d.Replicas),
			Object:    d.Name,
		})
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
