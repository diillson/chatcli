/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"strings"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/k8s"
	"github.com/diillson/chatcli/llm/fallback"
	"github.com/diillson/chatcli/llm/manager"
	"github.com/diillson/chatcli/metrics"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/server"
	"go.uber.org/zap"
)

// ServerOptions holds the flags for the 'server' subcommand.
type ServerOptions struct {
	Port        int
	Token       string
	CertFile    string
	KeyFile     string
	Provider    string
	Model       string
	MetricsPort int

	// Fallback chain (optional)
	FallbackProviders    string
	FallbackMaxRetries   int
	FallbackCooldownBase time.Duration
	FallbackCooldownMax  time.Duration

	// MCP (optional)
	MCPConfigPath string

	// K8s watcher integration (optional)
	WatchDeployment string
	WatchNamespace  string
	WatchInterval   time.Duration
	WatchWindow     time.Duration
	WatchMaxLogs    int
	WatchKubeconfig string
	WatchConfig     string // path to multi-target watch config YAML
}

// RunServer executes the 'chatcli server' subcommand.
func RunServer(args []string, llmMgr manager.LLMManager, logger *zap.Logger) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)

	opts := &ServerOptions{}
	fs.IntVar(&opts.Port, "port", getEnvInt("CHATCLI_SERVER_PORT", 50051), "gRPC server port")
	fs.StringVar(&opts.Token, "token", os.Getenv("CHATCLI_SERVER_TOKEN"), "Authentication token (empty = no auth)")
	fs.StringVar(&opts.CertFile, "tls-cert", os.Getenv("CHATCLI_SERVER_TLS_CERT"), "TLS certificate file path")
	fs.StringVar(&opts.KeyFile, "tls-key", os.Getenv("CHATCLI_SERVER_TLS_KEY"), "TLS key file path")
	fs.StringVar(&opts.Provider, "provider", os.Getenv("LLM_PROVIDER"), "Default LLM provider")
	fs.StringVar(&opts.Model, "model", "", "Default LLM model")
	fs.IntVar(&opts.MetricsPort, "metrics-port", getEnvInt("CHATCLI_METRICS_PORT", 9090), "Prometheus metrics HTTP port (0 = disabled)")

	// Fallback chain flags
	fs.StringVar(&opts.FallbackProviders, "fallback-providers", os.Getenv("CHATCLI_FALLBACK_PROVIDERS"), "Comma-separated fallback providers (e.g. OPENAI,CLAUDEAI,GOOGLEAI)")
	fs.IntVar(&opts.FallbackMaxRetries, "fallback-max-retries", getEnvInt("CHATCLI_FALLBACK_MAX_RETRIES", 2), "Max retries per provider before fallback")
	fs.DurationVar(&opts.FallbackCooldownBase, "fallback-cooldown-base", getEnvDuration("CHATCLI_FALLBACK_COOLDOWN_BASE", 30*time.Second), "Base cooldown duration after provider failure")
	fs.DurationVar(&opts.FallbackCooldownMax, "fallback-cooldown-max", getEnvDuration("CHATCLI_FALLBACK_COOLDOWN_MAX", 5*time.Minute), "Maximum cooldown duration")

	// MCP flags
	fs.StringVar(&opts.MCPConfigPath, "mcp-config", os.Getenv("CHATCLI_MCP_CONFIG"), "Path to MCP servers config JSON")

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

	// Initialize plugin manager for remote discovery and execution
	pluginMgr, pluginErr := plugins.NewManager(logger)
	if pluginErr != nil {
		logger.Warn("Failed to initialize plugin manager, remote plugins will be unavailable", zap.Error(pluginErr))
	} else {
		pluginMgr.RegisterBuiltinPlugin(plugins.NewBuiltinCoderPlugin())
		srv.SetPluginManager(pluginMgr)
		defer pluginMgr.Close()
		logger.Info("Plugin manager initialized for remote discovery",
			zap.Int("plugins", len(pluginMgr.GetPlugins())),
			zap.String("dir", pluginMgr.PluginsDir()),
		)
	}

	// Initialize persona loader for remote agent/skill discovery
	personaLoader := persona.NewLoader(logger)
	srv.SetPersonaLoader(personaLoader)
	if agents, err := personaLoader.ListAgents(); err == nil {
		logger.Info("Persona loader initialized for remote discovery",
			zap.Int("agents", len(agents)),
		)
	}

	// Initialize fallback chain if configured
	if opts.FallbackProviders != "" {
		providers := strings.Split(opts.FallbackProviders, ",")
		var entries []fallback.FallbackEntry
		for i, p := range providers {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// Resolve model from env (CHATCLI_FALLBACK_MODEL_<PROVIDER>) or use default
			model := os.Getenv("CHATCLI_FALLBACK_MODEL_" + strings.ToUpper(p))
			if model == "" {
				model = opts.Model
			}
			c, err := llmMgr.GetClient(p, model)
			if err != nil {
				logger.Warn("fallback provider unavailable, skipping",
					zap.String("provider", p), zap.Error(err))
				continue
			}
			entries = append(entries, fallback.FallbackEntry{
				Provider: p,
				Model:    model,
				Client:   c,
				Priority: i,
			})
		}
		if len(entries) > 1 {
			chain := fallback.NewChain(logger, entries,
				fallback.WithMaxRetries(opts.FallbackMaxRetries),
				fallback.WithCooldown(opts.FallbackCooldownBase, opts.FallbackCooldownMax, 2.0),
			)
			srv.SetFallbackChain(chain)
			logger.Info("Fallback chain initialized",
				zap.Int("providers", len(entries)),
				zap.Strings("chain", func() []string {
					names := make([]string, len(entries))
					for i, e := range entries {
						names[i] = e.Provider
					}
					return names
				}()),
			)
		}
	}

	// Initialize MCP manager if configured
	if opts.MCPConfigPath != "" || os.Getenv("CHATCLI_MCP_ENABLED") == "true" {
		mcpMgr := mcp.NewManager(logger)
		configPath := opts.MCPConfigPath
		if configPath == "" {
			configPath = mcp.DefaultConfigPath()
		}
		if err := mcpMgr.LoadConfig(configPath); err != nil {
			logger.Warn("Failed to load MCP config, MCP tools will be unavailable",
				zap.String("config", configPath), zap.Error(err))
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := mcpMgr.StartAll(ctx); err != nil {
				logger.Warn("Failed to start MCP servers", zap.Error(err))
			} else {
				srv.SetMCPManager(mcpMgr)
				defer mcpMgr.StopAll()
				logger.Info("MCP manager initialized",
					zap.Int("servers", len(mcpMgr.GetServerStatus())),
					zap.Int("tools", len(mcpMgr.GetTools())),
				)
			}
		}
	}

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
			AlertsFunc: func() []server.AlertInfo {
				var all []server.AlertInfo
				for key, store := range stores {
					// Key format is "Kind/namespace/name" (e.g., "Deployment/production/api-gateway")
					parts := strings.SplitN(key, "/", 3)
					ns, deploy := "", ""
					if len(parts) == 3 {
						// kind = parts[0] (not used here, carried via alert.Object)
						ns = parts[1]
						deploy = parts[2]
					} else if len(parts) == 2 {
						// Backward compat: "namespace/name"
						ns = parts[0]
						deploy = parts[1]
					}
					for _, a := range store.GetAlerts() {
						all = append(all, server.AlertInfo{
							Type:       string(a.Type),
							Severity:   string(a.Severity),
							Message:    a.Message,
							Object:     a.Object,
							Namespace:  ns,
							Deployment: deploy,
							Timestamp:  a.Timestamp,
						})
					}
				}
				return all
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

// PrintServerUsage prints help for the server subcommand.
func PrintServerUsage() {
	fmt.Println(`Usage: chatcli server [flags]

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

  Provider Fallback Chain (optional, automatic failover between providers):
  --fallback-providers <list>   Comma-separated providers (env: CHATCLI_FALLBACK_PROVIDERS)
  --fallback-max-retries <n>    Max retries per provider (default: 2, env: CHATCLI_FALLBACK_MAX_RETRIES)
  --fallback-cooldown-base <d>  Base cooldown after failure (default: 30s, env: CHATCLI_FALLBACK_COOLDOWN_BASE)
  --fallback-cooldown-max <d>   Max cooldown duration (default: 5m, env: CHATCLI_FALLBACK_COOLDOWN_MAX)

  MCP (Model Context Protocol, optional):
  --mcp-config <path>           MCP servers config JSON (env: CHATCLI_MCP_CONFIG)

Examples:
  chatcli server
  chatcli server --port 8080 --token mysecret
  chatcli server --tls-cert cert.pem --tls-key key.pem

  # Server with provider fallback chain
  chatcli server --fallback-providers OPENAI,CLAUDEAI,GOOGLEAI,COPILOT

  # Server with MCP tools
  chatcli server --mcp-config ~/.chatcli/mcp_servers.json

  # Server with single-target K8s watcher
  chatcli server --watch-deployment myapp --watch-namespace production

  # Server with multi-target K8s watcher (config file)
  chatcli server --watch-config targets.yaml`)
}
