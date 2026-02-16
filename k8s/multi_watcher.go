/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package k8s

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

// MultiWatcher manages multiple ResourceWatchers sharing a single K8s clientset.
type MultiWatcher struct {
	watchers map[string]*ResourceWatcher
	stores   map[string]*ObservabilityStore
	config   MultiWatchConfig
	logger   *zap.Logger
	mu       sync.RWMutex

	clientset     kubernetes.Interface
	metricsClient metricsv.Interface
}

// NewMultiWatcher creates watchers for all targets in the config.
func NewMultiWatcher(cfg MultiWatchConfig, logger *zap.Logger) (*MultiWatcher, error) {
	if len(cfg.Targets) == 0 {
		return nil, fmt.Errorf("no watch targets configured")
	}
	if cfg.MaxContextChars <= 0 {
		cfg.MaxContextChars = 32000
	}

	restConfig, err := buildKubeConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8s client: %w", err)
	}

	var metricsClient metricsv.Interface
	mc, err := metricsv.NewForConfig(restConfig)
	if err == nil {
		metricsClient = mc
	} else {
		logger.Info("Metrics server not available")
	}

	mw := &MultiWatcher{
		watchers:      make(map[string]*ResourceWatcher),
		stores:        make(map[string]*ObservabilityStore),
		config:        cfg,
		logger:        logger,
		clientset:     clientset,
		metricsClient: metricsClient,
	}

	for _, target := range cfg.Targets {
		key := target.Key()
		watcher, store := mw.createWatcher(target, cfg)
		mw.watchers[key] = watcher
		mw.stores[key] = store
	}

	return mw, nil
}

// createWatcher builds a ResourceWatcher for a single target using shared clients.
func (mw *MultiWatcher) createWatcher(target WatchTarget, cfg MultiWatchConfig) (*ResourceWatcher, *ObservabilityStore) {
	maxSnapshots := int(cfg.Window/cfg.Interval) + 1
	if maxSnapshots < 10 {
		maxSnapshots = 10
	}
	maxLogs := cfg.MaxLogLines * 10

	store := NewObservabilityStore(maxSnapshots, maxLogs, cfg.Window)
	targetLogger := mw.logger.With(zap.String("target", target.Key()))

	w := &ResourceWatcher{
		clientset:     mw.clientset,
		metricsClient: mw.metricsClient,
		config: WatchConfig{
			Deployment:  target.Deployment,
			Namespace:   target.Namespace,
			Interval:    cfg.Interval,
			Window:      cfg.Window,
			MaxLogLines: cfg.MaxLogLines,
			Kubeconfig:  cfg.Kubeconfig,
		},
		store:  store,
		logger: targetLogger,

		deployCollector:  NewDeploymentCollector(mw.clientset, target.Namespace, target.Deployment, targetLogger),
		eventCollector:   NewEventCollector(mw.clientset, target.Namespace, target.Deployment, targetLogger),
		logCollector:     NewLogCollector(mw.clientset, target.Namespace, target.Deployment, cfg.MaxLogLines, targetLogger),
		hpaCollector:     NewHPACollector(mw.clientset, target.Namespace, target.Deployment, targetLogger),
		metricsCollector: NewMetricsCollector(mw.clientset, mw.metricsClient, target.Namespace, target.Deployment, targetLogger),
	}

	if target.MetricsPort > 0 {
		w.promCollector = NewPrometheusCollector(
			mw.clientset, target.Namespace, target.Deployment,
			target.MetricsPort, target.MetricsPath, target.MetricsFilter,
			targetLogger,
		)
	}

	return w, store
}

// Start launches all watchers in parallel goroutines. Blocks until ctx is cancelled.
func (mw *MultiWatcher) Start(ctx context.Context) error {
	mw.logger.Info("Starting multi-watcher", zap.Int("targets", len(mw.watchers)))

	var wg sync.WaitGroup
	for key, watcher := range mw.watchers {
		wg.Add(1)
		go func(k string, w *ResourceWatcher) {
			defer wg.Done()
			mw.logger.Info("Starting watcher", zap.String("target", k))
			if err := w.Start(ctx); err != nil && err != context.Canceled {
				mw.logger.Error("Watcher stopped with error", zap.String("target", k), zap.Error(err))
			}
		}(key, watcher)
	}

	wg.Wait()
	return ctx.Err()
}

// GetStores returns all stores keyed by target.
func (mw *MultiWatcher) GetStores() map[string]*ObservabilityStore {
	mw.mu.RLock()
	defer mw.mu.RUnlock()
	result := make(map[string]*ObservabilityStore, len(mw.stores))
	for k, v := range mw.stores {
		result[k] = v
	}
	return result
}

// TargetCount returns the number of watch targets.
func (mw *MultiWatcher) TargetCount() int {
	return len(mw.watchers)
}

// SetMetrics sets the metrics recorder on all managed watchers.
func (mw *MultiWatcher) SetMetrics(recorder WatcherMetricsRecorder) {
	mw.mu.Lock()
	defer mw.mu.Unlock()
	for _, w := range mw.watchers {
		w.metricsRecorder = recorder
	}
}
