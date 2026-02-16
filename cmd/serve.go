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
	"strconv"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/k8s"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/metrics"
	"github.com/diillson/chatcli/server"
	"go.uber.org/zap"
)

// ServeOptions holds the flags for the 'serve' subcommand.
type ServeOptions struct {
	Port        int
	Token       string
	CertFile    string
	KeyFile     string
	Provider    string
	Model       string
	MetricsPort int

	// K8s watcher integration (optional)
	WatchDeployment string
	WatchNamespace  string
	WatchInterval   time.Duration
	WatchWindow     time.Duration
	WatchMaxLogs    int
	WatchKubeconfig string
	WatchConfig     string // path to multi-target watch config YAML
}

// RunServe executes the 'chatcli serve' subcommand.
func RunServe(args []string, llmMgr manager.LLMManager, logger *zap.Logger) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)

	opts := &ServeOptions{}
	fs.IntVar(&opts.Port, "port", getEnvInt("CHATCLI_SERVER_PORT", 50051), "gRPC server port")
	fs.StringVar(&opts.Token, "token", os.Getenv("CHATCLI_SERVER_TOKEN"), "Authentication token (empty = no auth)")
	fs.StringVar(&opts.CertFile, "tls-cert", os.Getenv("CHATCLI_SERVER_TLS_CERT"), "TLS certificate file path")
	fs.StringVar(&opts.KeyFile, "tls-key", os.Getenv("CHATCLI_SERVER_TLS_KEY"), "TLS key file path")
	fs.StringVar(&opts.Provider, "provider", os.Getenv("LLM_PROVIDER"), "Default LLM provider")
	fs.StringVar(&opts.Model, "model", "", "Default LLM model")
	fs.IntVar(&opts.MetricsPort, "metrics-port", getEnvInt("CHATCLI_METRICS_PORT", 9090), "Prometheus metrics HTTP port (0 = disabled)")

	// K8s watcher flags
	fs.StringVar(&opts.WatchDeployment, "watch-deployment", os.Getenv("CHATCLI_WATCH_DEPLOYMENT"), "K8s deployment to monitor (enables watcher)")
	fs.StringVar(&opts.WatchNamespace, "watch-namespace", getEnvOrDefault("CHATCLI_WATCH_NAMESPACE", "default"), "K8s namespace for watcher")
	fs.DurationVar(&opts.WatchInterval, "watch-interval", getEnvDuration("CHATCLI_WATCH_INTERVAL", 30*time.Second), "Watcher collection interval")
	fs.DurationVar(&opts.WatchWindow, "watch-window", getEnvDuration("CHATCLI_WATCH_WINDOW", 2*time.Hour), "Watcher observation window")
	fs.IntVar(&opts.WatchMaxLogs, "watch-max-log-lines", getEnvInt("CHATCLI_WATCH_MAX_LOG_LINES", 100), "Max log lines per pod")
	fs.StringVar(&opts.WatchKubeconfig, "watch-kubeconfig", os.Getenv("CHATCLI_KUBECONFIG"), "Path to kubeconfig for watcher")
	fs.StringVar(&opts.WatchConfig, "watch-config", os.Getenv("CHATCLI_WATCH_CONFIG"), "Path to multi-target watch config YAML")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Resolve provider if not set
	if opts.Provider == "" {
		available := llmMgr.GetAvailableProviders()
		if len(available) > 0 {
			opts.Provider = available[0]
		}
	}

	// Resolve model name from the client if possible
	if opts.Model == "" && opts.Provider != "" {
		if c, err := llmMgr.GetClient(opts.Provider, ""); err == nil {
			opts.Model = c.GetModelName()
		}
	}

	// Create session manager for server-side session persistence
	sessionMgr, err := cli.NewSessionManager(logger)
	if err != nil {
		logger.Warn("Failed to initialize session manager, sessions will be unavailable", zap.Error(err))
	}

	cfg := server.Config{
		Port:        opts.Port,
		Token:       opts.Token,
		TLSCertFile: opts.CertFile,
		TLSKeyFile:  opts.KeyFile,
		Provider:    opts.Provider,
		Model:       opts.Model,
		MetricsPort: opts.MetricsPort,
	}

	srv := server.New(cfg, llmMgr, sessionMgr, logger)

	// Start K8s watcher(s) if configured
	if opts.WatchConfig != "" || opts.WatchDeployment != "" {
		var multiCfg k8s.MultiWatchConfig

		if opts.WatchConfig != "" {
			mcfg, err := k8s.LoadMultiWatchConfig(opts.WatchConfig)
			if err != nil {
				return fmt.Errorf("failed to load watch config: %w", err)
			}
			if opts.WatchKubeconfig != "" {
				mcfg.Kubeconfig = opts.WatchKubeconfig
			}
			multiCfg = *mcfg
		} else {
			// Legacy single-target mode (backwards compatible)
			watchCfg := k8s.WatchConfig{
				Deployment:  opts.WatchDeployment,
				Namespace:   opts.WatchNamespace,
				Interval:    opts.WatchInterval,
				Window:      opts.WatchWindow,
				MaxLogLines: opts.WatchMaxLogs,
				Kubeconfig:  opts.WatchKubeconfig,
			}
			multiCfg = k8s.SingleTargetToMulti(watchCfg)
		}

		mw, err := k8s.NewMultiWatcher(multiCfg, logger)
		if err != nil {
			return fmt.Errorf("failed to create K8s watcher: %w", err)
		}

		// Wire watcher Prometheus metrics if metrics are enabled
		if opts.MetricsPort > 0 {
			wm := metrics.NewWatcherMetrics()
			wm.TargetsMonitored.Set(float64(mw.TargetCount()))
			mw.SetMetrics(wm.Recorder())
		}

		stores := mw.GetStores()
		multiSum := k8s.NewMultiSummarizer(stores, multiCfg.MaxContextChars)

		srv.SetWatcher(server.WatcherConfig{
			ContextFunc: multiSum.GenerateContext,
			StatusFunc:  multiSum.GenerateStatusSummary,
			StatsFunc: func() (int, int, int) {
				totalAlerts, totalSnapshots, totalPods := 0, 0, 0
				for _, store := range stores {
					stats := store.Stats()
					totalAlerts += stats.AlertCount
					totalSnapshots += stats.SnapshotCount
					if snap, ok := store.LatestSnapshot(); ok {
						totalPods += len(snap.Pods)
					}
				}
				return totalAlerts, totalSnapshots, totalPods
			},
			Deployment: fmt.Sprintf("%d targets", mw.TargetCount()),
			Namespace:  "multi",
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			logger.Info("Starting K8s multi-watcher",
				zap.Int("targets", mw.TargetCount()),
				zap.Duration("interval", multiCfg.Interval),
			)
			if err := mw.Start(ctx); err != nil && err != context.Canceled {
				logger.Error("K8s multi-watcher stopped with error", zap.Error(err))
			}
		}()

		fmt.Printf("K8s watcher active: %d targets (interval: %s)\n",
			mw.TargetCount(), multiCfg.Interval)
	}

	return srv.Start()
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// PrintServeUsage prints help for the serve subcommand.
func PrintServeUsage() {
	fmt.Println(`Usage: chatcli serve [flags]

Start the ChatCLI gRPC server for remote access.

Flags:
  --port <int>        Server port (default: 50051, env: CHATCLI_SERVER_PORT)
  --token <string>    Authentication token (env: CHATCLI_SERVER_TOKEN)
  --tls-cert <path>   TLS certificate file (env: CHATCLI_SERVER_TLS_CERT)
  --tls-key <path>    TLS key file (env: CHATCLI_SERVER_TLS_KEY)
  --provider <name>   Default LLM provider (env: LLM_PROVIDER)
  --model <name>      Default LLM model
  --metrics-port <n>  Prometheus metrics HTTP port (default: 9090, 0=disabled, env: CHATCLI_METRICS_PORT)

  K8s Watcher (optional, enables K8s context injection for all remote clients):
  --watch-config <path>       Multi-target watch config YAML (env: CHATCLI_WATCH_CONFIG)
  --watch-deployment <name>   Single deployment to monitor (env: CHATCLI_WATCH_DEPLOYMENT)
  --watch-namespace <ns>      Namespace (default: "default", env: CHATCLI_WATCH_NAMESPACE)
  --watch-interval <dur>      Collection interval (default: 30s, env: CHATCLI_WATCH_INTERVAL)
  --watch-window <dur>        Observation window (default: 2h, env: CHATCLI_WATCH_WINDOW)
  --watch-max-log-lines <n>   Max log lines per pod (default: 100, env: CHATCLI_WATCH_MAX_LOG_LINES)
  --watch-kubeconfig <path>   Path to kubeconfig (env: CHATCLI_KUBECONFIG)

Examples:
  chatcli serve
  chatcli serve --port 8080 --token mysecret
  chatcli serve --tls-cert cert.pem --tls-key key.pem

  # Server with single-target K8s watcher
  chatcli serve --watch-deployment myapp --watch-namespace production

  # Server with multi-target K8s watcher (config file)
  chatcli serve --watch-config targets.yaml`)
}
