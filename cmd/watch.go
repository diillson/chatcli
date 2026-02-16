/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/k8s"
	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// WatchOptions holds the flags for the 'watch' subcommand.
type WatchOptions struct {
	Deployment  string
	Namespace   string
	Interval    time.Duration
	Window      time.Duration
	MaxLogLines int
	Kubeconfig  string
	Provider    string
	Model       string
	Prompt      string // one-shot mode
	MaxTokens   int
	ConfigFile  string // path to multi-target watch config YAML
}

// RunWatch executes the 'chatcli watch' subcommand.
func RunWatch(ctx context.Context, args []string, llmMgr manager.LLMManager, logger *zap.Logger) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)

	opts := &WatchOptions{}
	fs.StringVar(&opts.Deployment, "deployment", os.Getenv("CHATCLI_WATCH_DEPLOYMENT"), "Kubernetes deployment name to monitor")
	fs.StringVar(&opts.Namespace, "namespace", getEnvOrDefault("CHATCLI_WATCH_NAMESPACE", "default"), "Kubernetes namespace")
	fs.DurationVar(&opts.Interval, "interval", getEnvDuration("CHATCLI_WATCH_INTERVAL", 30*time.Second), "Data collection interval")
	fs.DurationVar(&opts.Window, "window", getEnvDuration("CHATCLI_WATCH_WINDOW", 2*time.Hour), "Observation window (how far back to keep data)")
	fs.IntVar(&opts.MaxLogLines, "max-log-lines", getEnvInt("CHATCLI_WATCH_MAX_LOG_LINES", 100), "Max log lines per pod per collection cycle")
	fs.StringVar(&opts.Kubeconfig, "kubeconfig", os.Getenv("CHATCLI_KUBECONFIG"), "Path to kubeconfig (empty = in-cluster or default)")
	fs.StringVar(&opts.Provider, "provider", os.Getenv("LLM_PROVIDER"), "LLM provider override")
	fs.StringVar(&opts.Model, "model", "", "LLM model override")
	fs.StringVar(&opts.Prompt, "p", "", "One-shot prompt with K8s context (sends and exits)")
	fs.IntVar(&opts.MaxTokens, "max-tokens", 0, "Max tokens for response")
	fs.StringVar(&opts.ConfigFile, "config", "", "Path to multi-target watch config YAML")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if opts.Deployment == "" && opts.ConfigFile == "" {
		PrintWatchUsage()
		return fmt.Errorf("deployment name or config file required (use --deployment or --config)")
	}

	// Build multi-watch config (single-target or multi-target)
	var multiCfg k8s.MultiWatchConfig

	if opts.ConfigFile != "" {
		mcfg, err := k8s.LoadMultiWatchConfig(opts.ConfigFile)
		if err != nil {
			return fmt.Errorf("failed to load watch config: %w", err)
		}
		if opts.Kubeconfig != "" {
			mcfg.Kubeconfig = opts.Kubeconfig
		}
		multiCfg = *mcfg
	} else {
		watchCfg := k8s.WatchConfig{
			Deployment:  opts.Deployment,
			Namespace:   opts.Namespace,
			Interval:    opts.Interval,
			Window:      opts.Window,
			MaxLogLines: opts.MaxLogLines,
			Kubeconfig:  opts.Kubeconfig,
		}
		multiCfg = k8s.SingleTargetToMulti(watchCfg)
	}

	mw, err := k8s.NewMultiWatcher(multiCfg, logger)
	if err != nil {
		return fmt.Errorf("failed to create K8s watcher: %w", err)
	}

	stores := mw.GetStores()
	multiSum := k8s.NewMultiSummarizer(stores, multiCfg.MaxContextChars)

	// Start the watcher in background
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	watcherReady := make(chan struct{}, 1)
	go func() {
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.After(10 * time.Second)
			for {
				select {
				case <-ticker.C:
					for _, store := range stores {
						if _, ok := store.LatestSnapshot(); ok {
							watcherReady <- struct{}{}
							return
						}
					}
				case <-timeout:
					watcherReady <- struct{}{}
					return
				}
			}
		}()

		if err := mw.Start(watchCtx); err != nil && err != context.Canceled {
			logger.Error("K8s watcher stopped with error", zap.Error(err))
		}
	}()

	fmt.Printf("Starting K8s watcher: %d targets (interval: %s, window: %s)\n",
		mw.TargetCount(), multiCfg.Interval, multiCfg.Window)

	// Wait for first data collection
	select {
	case <-watcherReady:
		fmt.Println("Initial data collected. K8s context will be injected into all prompts.")
	case <-ctx.Done():
		return ctx.Err()
	}

	// One-shot mode
	if opts.Prompt != "" {
		return runWatchOneShot(ctx, opts, multiSum, llmMgr, logger)
	}

	// Interactive mode
	chatCLI, err := cli.NewChatCLI(llmMgr, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize ChatCLI: %w", err)
	}

	if err := chatCLI.ApplyOverrides(llmMgr, opts.Provider, opts.Model); err != nil {
		return fmt.Errorf("failed to apply provider/model overrides: %w", err)
	}

	chatCLI.WatcherContextFunc = multiSum.GenerateContext
	chatCLI.SetWatching(true, multiSum.GenerateStatusSummary)

	fmt.Println("Type your questions about the deployments. K8s context is automatically included.")
	fmt.Printf("Use /watch to see current status.\n\n")

	chatCLI.Start(ctx)
	return nil
}

// contextGenerator provides a common interface for generating LLM context.
type contextGenerator interface {
	GenerateContext() string
}

// runWatchOneShot sends a single prompt with K8s context and prints the response.
func runWatchOneShot(ctx context.Context, opts *WatchOptions, cg contextGenerator, llmMgr manager.LLMManager, logger *zap.Logger) error {
	provider := opts.Provider
	if provider == "" {
		available := llmMgr.GetAvailableProviders()
		if len(available) == 0 {
			return fmt.Errorf("no LLM provider configured")
		}
		provider = available[0]
	}

	llmClient, err := llmMgr.GetClient(provider, opts.Model)
	if err != nil {
		return fmt.Errorf("failed to create LLM client: %w", err)
	}

	k8sContext := cg.GenerateContext()
	fullPrompt := k8sContext + "\n\nUser Question: " + opts.Prompt

	response, err := llmClient.SendPrompt(ctx, fullPrompt, nil, opts.MaxTokens)
	if err != nil {
		return fmt.Errorf("LLM request failed: %w", err)
	}

	fmt.Println(response)
	return nil
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// PrintWatchUsage prints help for the watch subcommand.
func PrintWatchUsage() {
	fmt.Println(`Usage: chatcli watch [--deployment <name> | --config <file>] [flags]

Monitor Kubernetes deployments and use AI to analyze their health.

The watcher continuously collects deployment status, pod metrics, events,
logs, HPA data, and application metrics (Prometheus). This context is
automatically injected into LLM prompts for AI-assisted troubleshooting.

Required (one of):
  --deployment <name>     Single deployment to monitor
  --config <file>         Multi-target watch config YAML

Flags:
  --namespace <ns>        Kubernetes namespace (default: "default", env: CHATCLI_WATCH_NAMESPACE)
  --interval <duration>   Collection interval (default: 30s, env: CHATCLI_WATCH_INTERVAL)
  --window <duration>     Observation window (default: 2h, env: CHATCLI_WATCH_WINDOW)
  --max-log-lines <int>   Max log lines per pod (default: 100, env: CHATCLI_WATCH_MAX_LOG_LINES)
  --kubeconfig <path>     Path to kubeconfig (env: CHATCLI_KUBECONFIG)
  --provider <name>       LLM provider (env: LLM_PROVIDER)
  --model <name>          LLM model
  -p <prompt>             One-shot: ask a question and exit
  --max-tokens <int>      Max tokens for response

Environment Variables:
  CHATCLI_WATCH_DEPLOYMENT    Deployment name
  CHATCLI_WATCH_NAMESPACE     Namespace (default: "default")
  CHATCLI_WATCH_INTERVAL      Collection interval (default: 30s)
  CHATCLI_WATCH_WINDOW        Observation window (default: 2h)
  CHATCLI_WATCH_MAX_LOG_LINES Max log lines per pod (default: 100)
  CHATCLI_KUBECONFIG          Path to kubeconfig

Config File Format (YAML):
  interval: "30s"
  window: "2h"
  maxLogLines: 100
  maxContextChars: 32000
  targets:
    - deployment: api-gateway
      namespace: production
      metricsPort: 9090
      metricsFilter: ["http_requests_total", "http_request_duration_*"]
    - deployment: auth-service
      namespace: production
    - deployment: worker
      namespace: batch

Examples:
  # Interactive monitoring (single deployment)
  chatcli watch --deployment myapp --namespace production

  # Interactive monitoring (multiple deployments)
  chatcli watch --config targets.yaml

  # One-shot question
  chatcli watch --deployment myapp -p "Is the deployment healthy?"

  # Multi-target one-shot
  chatcli watch --config targets.yaml -p "Which deployments need attention?"

  # Custom collection settings
  chatcli watch --deployment myapp --interval 10s --window 30m

  # With specific LLM provider
  chatcli watch --deployment myapp --provider CLAUDEAI --model claude-sonnet-4-5-20250929`)
}
