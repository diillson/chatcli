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

	if err := fs.Parse(args); err != nil {
		return err
	}

	if opts.Deployment == "" {
		PrintWatchUsage()
		return fmt.Errorf("deployment name is required (use --deployment)")
	}

	// Create the K8s watcher
	watchCfg := k8s.WatchConfig{
		Deployment:  opts.Deployment,
		Namespace:   opts.Namespace,
		Interval:    opts.Interval,
		Window:      opts.Window,
		MaxLogLines: opts.MaxLogLines,
		Kubeconfig:  opts.Kubeconfig,
	}

	watcher, err := k8s.NewResourceWatcher(watchCfg, logger)
	if err != nil {
		return fmt.Errorf("failed to create K8s watcher: %w", err)
	}

	store := watcher.GetStore()
	summarizer := k8s.NewSummarizer(store)

	// Start the watcher in background
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()

	watcherReady := make(chan struct{}, 1)
	go func() {
		// Signal ready after first collection (or timeout)
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			timeout := time.After(10 * time.Second)
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

		if err := watcher.Start(watchCtx); err != nil && err != context.Canceled {
			logger.Error("K8s watcher stopped with error", zap.Error(err))
		}
	}()

	fmt.Printf("Starting K8s watcher for deployment/%s in namespace/%s ...\n", opts.Deployment, opts.Namespace)
	fmt.Printf("Collection interval: %s, observation window: %s\n", opts.Interval, opts.Window)

	// Wait for first data collection
	select {
	case <-watcherReady:
		if _, ok := store.LatestSnapshot(); ok {
			fmt.Println("Initial data collected. K8s context will be injected into all prompts.")
		} else {
			fmt.Println("Watcher started (initial collection pending).")
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	// One-shot mode: send prompt with K8s context and exit
	if opts.Prompt != "" {
		return runWatchOneShot(ctx, opts, summarizer, llmMgr, logger)
	}

	// Interactive mode: start ChatCLI with K8s context injection
	chatCLI, err := cli.NewChatCLI(llmMgr, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize ChatCLI: %w", err)
	}

	// Apply provider/model overrides
	if err := chatCLI.ApplyOverrides(llmMgr, opts.Provider, opts.Model); err != nil {
		return fmt.Errorf("failed to apply provider/model overrides: %w", err)
	}

	// Wire up K8s context injection
	chatCLI.WatcherContextFunc = summarizer.GenerateContext
	chatCLI.SetWatching(true, summarizer.GenerateStatusSummary)

	fmt.Println("Type your questions about the deployment. K8s context is automatically included.")
	fmt.Printf("Use /watch to see current status.\n\n")

	chatCLI.Start(ctx)
	return nil
}

// runWatchOneShot sends a single prompt with K8s context and prints the response.
func runWatchOneShot(ctx context.Context, opts *WatchOptions, summarizer *k8s.Summarizer, llmMgr manager.LLMManager, logger *zap.Logger) error {
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

	// Build prompt with K8s context
	k8sContext := summarizer.GenerateContext()
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
	fmt.Println(`Usage: chatcli watch --deployment <name> [flags]

Monitor a Kubernetes deployment and use AI to analyze its health.

The watcher continuously collects deployment status, pod metrics, events,
logs, and HPA data. This context is automatically injected into LLM prompts,
enabling AI-assisted Kubernetes troubleshooting.

Required:
  --deployment <name>     Deployment name to monitor

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

Examples:
  # Interactive monitoring
  chatcli watch --deployment myapp --namespace production

  # One-shot question
  chatcli watch --deployment myapp -p "Is the deployment healthy?"

  # Custom collection settings
  chatcli watch --deployment myapp --interval 10s --window 30m

  # With specific LLM provider
  chatcli watch --deployment myapp --provider CLAUDEAI --model claude-sonnet-4-5-20250929`)
}
