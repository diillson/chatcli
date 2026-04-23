/*
 * ChatCLI - Scheduler: configuration.
 *
 * All operator-tunable knobs live here. Environment variables are
 * resolved once at Scheduler construction (via LoadConfigFromEnv);
 * subsequent changes require a restart — on purpose, so a mid-flight
 * `/reload` doesn't mutate the rate-limiter or the data dir under a
 * running queue.
 *
 * Env vars (prefixed CHATCLI_SCHEDULER_):
 *
 *   ENABLED                 bool   — master kill switch (default true)
 *   DATA_DIR                path   — WAL + audit base dir
 *                                    (default ~/.chatcli/scheduler)
 *   MAX_JOBS                int    — max non-terminal jobs at once
 *                                    (default 256)
 *   DEFAULT_ACTION_TIMEOUT  dur    — default Action timeout (default 5m)
 *   DEFAULT_POLL_INTERVAL   dur    — default wait poll (default 5s)
 *   DEFAULT_WAIT_TIMEOUT    dur    — default wait timeout (default 30m)
 *   DEFAULT_MAX_POLLS       int    — default wait poll cap (default 0=unlim)
 *   DEFAULT_BACKOFF_INITIAL dur    — default retry initial (default 1s)
 *   DEFAULT_BACKOFF_MAX     dur    — default retry cap (default 5m)
 *   DEFAULT_BACKOFF_MULT    float  — default retry multiplier (default 2.0)
 *   DEFAULT_BACKOFF_JITTER  float  — default jitter fraction (default 0.2)
 *   DEFAULT_MAX_RETRIES     int    — default MaxRetries (default 3)
 *   DEFAULT_TTL             dur    — how long terminal jobs linger for
 *                                    /jobs history (default 24h)
 *   HISTORY_LIMIT           int    — max ExecutionResult per job
 *                                    (default 16)
 *
 *   ALLOW_AGENTS            bool   — may agents create jobs? (default true)
 *   ACTION_ALLOWLIST        csv    — action types that may be scheduled
 *                                    (default: slash_cmd,llm_prompt,
 *                                    agent_task,worker_dispatch,hook,
 *                                    noop,webhook,shell — shell still
 *                                    goes through CoderMode safety)
 *
 *   RATE_LIMIT_GLOBAL_RPS     float   (default 5.0)
 *   RATE_LIMIT_GLOBAL_BURST   int     (default 20)
 *   RATE_LIMIT_OWNER_RPS      float   (default 1.0)
 *   RATE_LIMIT_OWNER_BURST    int     (default 10)
 *
 *   BREAKER_FAILURE_THRESHOLD int    (default 5)
 *   BREAKER_WINDOW            dur    (default 60s)
 *   BREAKER_COOLDOWN          dur    (default 30s)
 *
 *   AUDIT_ENABLED            bool   (default true)
 *   AUDIT_MAX_SIZE_MB        int    (default 10)
 *   AUDIT_MAX_BACKUPS        int    (default 7)
 *   AUDIT_MAX_AGE_DAYS       int    (default 30)
 *
 *   DAEMON_SOCKET            path   (default ~/.chatcli/scheduler/daemon.sock)
 *   DAEMON_AUTO_CONNECT      bool   (default true)
 *
 *   SNAPSHOT_INTERVAL        dur    (default 5m)
 *   WAL_GC_INTERVAL          dur    (default 1h)
 *
 *   WORKER_COUNT             int    (default 4)
 *   WAIT_WORKER_COUNT        int    (default 8)
 */
package scheduler

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config bundles every scheduler knob. Callers typically build one via
// LoadConfigFromEnv() + overrides; tests construct literals.
type Config struct {
	Enabled bool
	DataDir string

	MaxJobs int

	DefaultActionTimeout  time.Duration
	DefaultPollInterval   time.Duration
	DefaultWaitTimeout    time.Duration
	DefaultMaxPolls       int
	DefaultBackoffInitial time.Duration
	DefaultBackoffMax     time.Duration
	DefaultBackoffMult    float64
	DefaultBackoffJitter  float64
	DefaultMaxRetries     int
	DefaultTTL            time.Duration
	HistoryLimit          int

	AllowAgents     bool
	ActionAllowlist map[ActionType]bool

	RateLimitGlobalRPS   float64
	RateLimitGlobalBurst int
	RateLimitOwnerRPS    float64
	RateLimitOwnerBurst  int

	BreakerConfig BreakerConfig

	AuditEnabled    bool
	AuditMaxSizeMB  int
	AuditMaxBackups int
	AuditMaxAgeDays int

	DaemonSocket      string
	DaemonAutoConnect bool

	SnapshotInterval time.Duration
	WALGCInterval    time.Duration

	WorkerCount     int
	WaitWorkerCount int
}

// DefaultConfig returns a production-safe baseline with all knobs set
// to the values documented at the top of the file.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Enabled: true,
		DataDir: filepath.Join(home, ".chatcli", "scheduler"),

		MaxJobs: 256,

		DefaultActionTimeout:  5 * time.Minute,
		DefaultPollInterval:   5 * time.Second,
		DefaultWaitTimeout:    30 * time.Minute,
		DefaultMaxPolls:       0,
		DefaultBackoffInitial: 1 * time.Second,
		DefaultBackoffMax:     5 * time.Minute,
		DefaultBackoffMult:    2.0,
		DefaultBackoffJitter:  0.2,
		DefaultMaxRetries:     3,
		DefaultTTL:            24 * time.Hour,
		HistoryLimit:          16,

		AllowAgents: true,
		ActionAllowlist: map[ActionType]bool{
			ActionSlashCmd:       true,
			ActionLLMPrompt:      true,
			ActionAgentTask:      true,
			ActionWorkerDispatch: true,
			ActionHook:           true,
			ActionNoop:           true,
			ActionWebhook:        true,
			ActionShell:          true, // gated by CoderMode
		},

		RateLimitGlobalRPS:   5.0,
		RateLimitGlobalBurst: 20,
		RateLimitOwnerRPS:    1.0,
		RateLimitOwnerBurst:  10,

		BreakerConfig: BreakerConfig{
			FailureThreshold:        5,
			Window:                  60 * time.Second,
			Cooldown:                30 * time.Second,
			HalfOpenSuccessRequired: 1,
		},

		AuditEnabled:    true,
		AuditMaxSizeMB:  10,
		AuditMaxBackups: 7,
		AuditMaxAgeDays: 30,

		DaemonSocket:      filepath.Join(home, ".chatcli", "scheduler", "daemon.sock"),
		DaemonAutoConnect: true,

		SnapshotInterval: 5 * time.Minute,
		WALGCInterval:    1 * time.Hour,

		WorkerCount:     4,
		WaitWorkerCount: 8,
	}
}

// LoadConfigFromEnv applies CHATCLI_SCHEDULER_* overrides on top of
// DefaultConfig().
func LoadConfigFromEnv() Config {
	c := DefaultConfig()

	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_ENABLED"); ok {
		c.Enabled = parseEnvBool(v, true)
	}
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_DATA_DIR"); ok && strings.TrimSpace(v) != "" {
		c.DataDir = v
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_MAX_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxJobs = n
		}
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_DEFAULT_ACTION_TIMEOUT"); ok {
		c.DefaultActionTimeout = d
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_DEFAULT_POLL_INTERVAL"); ok {
		c.DefaultPollInterval = d
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_DEFAULT_WAIT_TIMEOUT"); ok {
		c.DefaultWaitTimeout = d
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_DEFAULT_MAX_POLLS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.DefaultMaxPolls = n
		}
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_INITIAL"); ok {
		c.DefaultBackoffInitial = d
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MAX"); ok {
		c.DefaultBackoffMax = d
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MULT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 1 {
			c.DefaultBackoffMult = f
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_JITTER"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 0.5 {
			c.DefaultBackoffJitter = f
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_DEFAULT_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.DefaultMaxRetries = n
		}
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_DEFAULT_TTL"); ok {
		c.DefaultTTL = d
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_HISTORY_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.HistoryLimit = n
		}
	}
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_ALLOW_AGENTS"); ok {
		c.AllowAgents = parseEnvBool(v, true)
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_ACTION_ALLOWLIST"); v != "" {
		allow := make(map[ActionType]bool)
		for _, t := range strings.Split(v, ",") {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			allow[ActionType(t)] = true
		}
		if len(allow) > 0 {
			c.ActionAllowlist = allow
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.RateLimitGlobalRPS = f
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.RateLimitGlobalBurst = n
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_RPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.RateLimitOwnerRPS = f
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_BURST"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.RateLimitOwnerBurst = n
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_BREAKER_FAILURE_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.BreakerConfig.FailureThreshold = n
		}
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_BREAKER_WINDOW"); ok {
		c.BreakerConfig.Window = d
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_BREAKER_COOLDOWN"); ok {
		c.BreakerConfig.Cooldown = d
	}
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_AUDIT_ENABLED"); ok {
		c.AuditEnabled = parseEnvBool(v, true)
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_AUDIT_MAX_SIZE_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.AuditMaxSizeMB = n
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_AUDIT_MAX_BACKUPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.AuditMaxBackups = n
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_AUDIT_MAX_AGE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.AuditMaxAgeDays = n
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_DAEMON_SOCKET"); v != "" {
		c.DaemonSocket = v
	}
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_DAEMON_AUTO_CONNECT"); ok {
		c.DaemonAutoConnect = parseEnvBool(v, true)
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_SNAPSHOT_INTERVAL"); ok {
		c.SnapshotInterval = d
	}
	if d, ok := parseEnvDuration("CHATCLI_SCHEDULER_WAL_GC_INTERVAL"); ok {
		c.WALGCInterval = d
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_WORKER_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.WorkerCount = n
		}
	}
	if v := os.Getenv("CHATCLI_SCHEDULER_WAIT_WORKER_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.WaitWorkerCount = n
		}
	}
	return c
}

// parseEnvBool interprets "1/true/yes/on" as true and "0/false/no/off"
// as false. Any other value falls back to def.
func parseEnvBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// parseEnvDuration is a thin convenience wrapper.
func parseEnvDuration(key string) (time.Duration, bool) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false
	}
	return d, true
}

// budgetDefaults returns a Budget primed from Config defaults. Used by
// Scheduler.Enqueue to fill job.Budget's zero fields.
func (c Config) budgetDefaults() Budget {
	return Budget{
		ActionTimeout:  c.DefaultActionTimeout,
		MaxRetries:     c.DefaultMaxRetries,
		BackoffInitial: c.DefaultBackoffInitial,
		BackoffMax:     c.DefaultBackoffMax,
		BackoffMult:    c.DefaultBackoffMult,
		BackoffJitter:  c.DefaultBackoffJitter,
		WaitTimeout:    c.DefaultWaitTimeout,
		PollInterval:   c.DefaultPollInterval,
		MaxPolls:       c.DefaultMaxPolls,
	}
}
