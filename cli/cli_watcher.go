package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/diillson/chatcli/k8s"
	"go.uber.org/zap"
)

// SetWatching configures the K8s watcher state for the CLI.
func (cli *ChatCLI) SetWatching(active bool, statusFunc func() string) {
	cli.isWatching = active
	cli.watchStatusFunc = statusFunc
}

// StartWatcher creates and starts a K8s watcher in background from interactive mode.
func (cli *ChatCLI) StartWatcher(ctx context.Context, cfg k8s.WatchConfig) error {
	if cli.isWatching {
		return fmt.Errorf("watcher already running, use /watch stop first")
	}

	watcher, err := k8s.NewResourceWatcher(cfg, cli.logger)
	if err != nil {
		return fmt.Errorf("failed to create K8s watcher: %w", err)
	}

	store := watcher.GetStore()
	summarizer := k8s.NewSummarizer(store)

	// The watcher runs in a detached background goroutine that must outlive
	// the /watch start command; its lifetime is governed by cli.watcherCancel
	// (invoked by StopWatcher), not by the per-command ctx. WithoutCancel
	// keeps inherited values while decoupling from the command's cancellation.
	watchCtx, watchCancel := context.WithCancel(context.WithoutCancel(ctx))
	cli.watcherCancel = watchCancel

	watcherReady := make(chan struct{}, 1)
	go func() {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.After(15 * time.Second)
			for {
				select {
				case <-ticker.C:
					if _, ok := store.LatestSnapshot(); ok {
						watcherReady <- struct{}{}
						return
					}
				case <-timeout:
					watcherReady <- struct{}{}
					return
				}
			}
		}()

		if err := watcher.Start(watchCtx); err != nil && !errors.Is(err, context.Canceled) {
			cli.logger.Error("K8s watcher stopped with error", zap.Error(err))
		}
	}()

	// Wait for first collection
	<-watcherReady

	cli.WatcherContextFunc = summarizer.GenerateContext
	cli.SetWatching(true, summarizer.GenerateStatusSummary)

	if _, ok := store.LatestSnapshot(); ok {
		cli.logger.Info("K8s watcher started with initial data",
			zap.String("deployment", cfg.Deployment),
			zap.String("namespace", cfg.Namespace))
	}

	return nil
}

// StopWatcher stops the running K8s watcher if any.
func (cli *ChatCLI) StopWatcher() {
	if cli.watcherCancel != nil {
		cli.watcherCancel()
		cli.watcherCancel = nil
	}
	cli.WatcherContextFunc = nil
	cli.SetWatching(false, nil)
}
