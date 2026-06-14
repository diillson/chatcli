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
			ActionAgentResume:    true,
			ActionParkPoll:       true,
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

// applyIntEnv parses key as an int and, when err==nil and the optional
// validate predicate (nil ⇒ always true) holds, stores it via set.
func applyIntEnv(key string, validate func(int) bool, set func(int)) {
	v := os.Getenv(key)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return
	}
	if validate != nil && !validate(n) {
		return
	}
	set(n)
}

// applyFloatEnv parses key as a float64 and, when err==nil and the optional
// validate predicate (nil ⇒ always true) holds, stores it via set.
func applyFloatEnv(key string, validate func(float64) bool, set func(float64)) {
	v := os.Getenv(key)
	if v == "" {
		return
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return
	}
	if validate != nil && !validate(f) {
		return
	}
	set(f)
}

// applyDurationEnv stores the parsed duration via set when key is set and valid.
func applyDurationEnv(key string, set func(time.Duration)) {
	if d, ok := parseEnvDuration(key); ok {
		set(d)
	}
}

func intPositive(n int) bool    { return n > 0 }
func intNonNegative(n int) bool { return n >= 0 }

// LoadConfigFromEnv applies CHATCLI_SCHEDULER_* overrides on top of
// DefaultConfig().
func LoadConfigFromEnv() Config {
	c := DefaultConfig()
	loadCoreEnv(&c)
	loadDefaultsEnv(&c)
	loadRateLimitEnv(&c)
	loadBreakerEnv(&c)
	loadAuditEnv(&c)
	loadDaemonEnv(&c)
	return c
}

func loadCoreEnv(c *Config) {
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_ENABLED"); ok {
		c.Enabled = parseEnvBool(v, true)
	}
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_DATA_DIR"); ok && strings.TrimSpace(v) != "" {
		c.DataDir = v
	}
	applyIntEnv("CHATCLI_SCHEDULER_MAX_JOBS", intPositive, func(n int) { c.MaxJobs = n })
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
}

func loadDefaultsEnv(c *Config) {
	applyDurationEnv("CHATCLI_SCHEDULER_DEFAULT_ACTION_TIMEOUT", func(d time.Duration) { c.DefaultActionTimeout = d })
	applyDurationEnv("CHATCLI_SCHEDULER_DEFAULT_POLL_INTERVAL", func(d time.Duration) { c.DefaultPollInterval = d })
	applyDurationEnv("CHATCLI_SCHEDULER_DEFAULT_WAIT_TIMEOUT", func(d time.Duration) { c.DefaultWaitTimeout = d })
	applyIntEnv("CHATCLI_SCHEDULER_DEFAULT_MAX_POLLS", intNonNegative, func(n int) { c.DefaultMaxPolls = n })
	applyDurationEnv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_INITIAL", func(d time.Duration) { c.DefaultBackoffInitial = d })
	applyDurationEnv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MAX", func(d time.Duration) { c.DefaultBackoffMax = d })
	applyFloatEnv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MULT", func(f float64) bool { return f >= 1 }, func(f float64) { c.DefaultBackoffMult = f })
	applyFloatEnv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_JITTER", func(f float64) bool { return f >= 0 && f <= 0.5 }, func(f float64) { c.DefaultBackoffJitter = f })
	applyIntEnv("CHATCLI_SCHEDULER_DEFAULT_MAX_RETRIES", intNonNegative, func(n int) { c.DefaultMaxRetries = n })
	applyDurationEnv("CHATCLI_SCHEDULER_DEFAULT_TTL", func(d time.Duration) { c.DefaultTTL = d })
	applyIntEnv("CHATCLI_SCHEDULER_HISTORY_LIMIT", intPositive, func(n int) { c.HistoryLimit = n })
}

func loadRateLimitEnv(c *Config) {
	applyFloatEnv("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_RPS", nil, func(f float64) { c.RateLimitGlobalRPS = f })
	applyIntEnv("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_BURST", nil, func(n int) { c.RateLimitGlobalBurst = n })
	applyFloatEnv("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_RPS", nil, func(f float64) { c.RateLimitOwnerRPS = f })
	applyIntEnv("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_BURST", nil, func(n int) { c.RateLimitOwnerBurst = n })
}

func loadBreakerEnv(c *Config) {
	applyIntEnv("CHATCLI_SCHEDULER_BREAKER_FAILURE_THRESHOLD", intPositive, func(n int) { c.BreakerConfig.FailureThreshold = n })
	applyDurationEnv("CHATCLI_SCHEDULER_BREAKER_WINDOW", func(d time.Duration) { c.BreakerConfig.Window = d })
	applyDurationEnv("CHATCLI_SCHEDULER_BREAKER_COOLDOWN", func(d time.Duration) { c.BreakerConfig.Cooldown = d })
}

func loadAuditEnv(c *Config) {
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_AUDIT_ENABLED"); ok {
		c.AuditEnabled = parseEnvBool(v, true)
	}
	applyIntEnv("CHATCLI_SCHEDULER_AUDIT_MAX_SIZE_MB", intPositive, func(n int) { c.AuditMaxSizeMB = n })
	applyIntEnv("CHATCLI_SCHEDULER_AUDIT_MAX_BACKUPS", intPositive, func(n int) { c.AuditMaxBackups = n })
	applyIntEnv("CHATCLI_SCHEDULER_AUDIT_MAX_AGE_DAYS", intPositive, func(n int) { c.AuditMaxAgeDays = n })
}

func loadDaemonEnv(c *Config) {
	if v := os.Getenv("CHATCLI_SCHEDULER_DAEMON_SOCKET"); v != "" {
		c.DaemonSocket = v
	}
	if v, ok := os.LookupEnv("CHATCLI_SCHEDULER_DAEMON_AUTO_CONNECT"); ok {
		c.DaemonAutoConnect = parseEnvBool(v, true)
	}
	applyDurationEnv("CHATCLI_SCHEDULER_SNAPSHOT_INTERVAL", func(d time.Duration) { c.SnapshotInterval = d })
	applyDurationEnv("CHATCLI_SCHEDULER_WAL_GC_INTERVAL", func(d time.Duration) { c.WALGCInterval = d })
	applyIntEnv("CHATCLI_SCHEDULER_WORKER_COUNT", intPositive, func(n int) { c.WorkerCount = n })
	applyIntEnv("CHATCLI_SCHEDULER_WAIT_WORKER_COUNT", intPositive, func(n int) { c.WaitWorkerCount = n })
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
