package scheduler

import (
	"testing"
	"time"
)

func TestApplyIntEnv(t *testing.T) {
	cases := []struct {
		name     string
		val      string
		validate func(int) bool
		want     int // start value is 7; want is the final value
	}{
		{"unset keeps default", "", nil, 7},
		{"valid no validate", "42", nil, 42},
		{"non-numeric ignored", "abc", nil, 7},
		{"validate rejects", "-3", intPositive, 7},
		{"validate accepts", "3", intPositive, 3},
		{"non-negative accepts zero", "0", intNonNegative, 0},
		{"non-negative rejects neg", "-1", intNonNegative, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const key = "CHATCLI_TEST_APPLY_INT"
			if c.val == "" {
				t.Setenv(key, "") // ensure empty/unset path
			} else {
				t.Setenv(key, c.val)
			}
			got := 7
			applyIntEnv(key, c.validate, func(n int) { got = n })
			if got != c.want {
				t.Errorf("applyIntEnv(%q)=%d want %d", c.val, got, c.want)
			}
		})
	}
}

func TestApplyFloatEnv(t *testing.T) {
	cases := []struct {
		name     string
		val      string
		validate func(float64) bool
		want     float64 // start value 1.5
	}{
		{"unset keeps default", "", nil, 1.5},
		{"valid no validate", "3.25", nil, 3.25},
		{"non-numeric ignored", "xx", nil, 1.5},
		{"validate rejects", "0.5", func(f float64) bool { return f >= 1 }, 1.5},
		{"validate accepts", "2.0", func(f float64) bool { return f >= 1 }, 2.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			const key = "CHATCLI_TEST_APPLY_FLOAT"
			t.Setenv(key, c.val)
			got := 1.5
			applyFloatEnv(key, c.validate, func(f float64) { got = f })
			if got != c.want {
				t.Errorf("applyFloatEnv(%q)=%v want %v", c.val, got, c.want)
			}
		})
	}
}

func TestApplyDurationEnv(t *testing.T) {
	const key = "CHATCLI_TEST_APPLY_DUR"

	t.Run("valid", func(t *testing.T) {
		t.Setenv(key, "90s")
		got := time.Duration(0)
		applyDurationEnv(key, func(d time.Duration) { got = d })
		if got != 90*time.Second {
			t.Errorf("got %v want 90s", got)
		}
	})

	t.Run("invalid ignored", func(t *testing.T) {
		t.Setenv(key, "not-a-duration")
		got := 5 * time.Second
		applyDurationEnv(key, func(d time.Duration) { got = d })
		if got != 5*time.Second {
			t.Errorf("got %v want 5s (unchanged)", got)
		}
	})

	t.Run("empty ignored", func(t *testing.T) {
		t.Setenv(key, "")
		got := 5 * time.Second
		applyDurationEnv(key, func(d time.Duration) { got = d })
		if got != 5*time.Second {
			t.Errorf("got %v want 5s (unchanged)", got)
		}
	})
}

func TestParseEnvBool(t *testing.T) {
	cases := []struct {
		in   string
		def  bool
		want bool
	}{
		{"1", false, true},
		{"true", false, true},
		{"YES", false, true},
		{"on", false, true},
		{"0", true, false},
		{"false", true, false},
		{"NO", true, false},
		{"off", true, false},
		{"garbage", true, true},
		{"garbage", false, false},
		{"  true  ", false, true},
	}
	for _, c := range cases {
		if got := parseEnvBool(c.in, c.def); got != c.want {
			t.Errorf("parseEnvBool(%q, %v)=%v want %v", c.in, c.def, got, c.want)
		}
	}
}

func TestLoadConfigFromEnv_Defaults(t *testing.T) {
	// With no env vars set, LoadConfigFromEnv must match DefaultConfig in the
	// scalar knobs.
	for _, k := range []string{
		"CHATCLI_SCHEDULER_ENABLED", "CHATCLI_SCHEDULER_MAX_JOBS",
		"CHATCLI_SCHEDULER_ALLOW_AGENTS", "CHATCLI_SCHEDULER_ACTION_ALLOWLIST",
		"CHATCLI_SCHEDULER_DATA_DIR", "CHATCLI_SCHEDULER_WORKER_COUNT",
	} {
		t.Setenv(k, "")
	}
	c := LoadConfigFromEnv()
	def := DefaultConfig()
	if c.MaxJobs != def.MaxJobs {
		t.Errorf("MaxJobs=%d want %d", c.MaxJobs, def.MaxJobs)
	}
	if c.Enabled != def.Enabled {
		t.Errorf("Enabled=%v want %v", c.Enabled, def.Enabled)
	}
	if c.WorkerCount != def.WorkerCount {
		t.Errorf("WorkerCount=%d want %d", c.WorkerCount, def.WorkerCount)
	}
}

func TestLoadCoreEnv_Overrides(t *testing.T) {
	t.Setenv("CHATCLI_SCHEDULER_ENABLED", "false")
	t.Setenv("CHATCLI_SCHEDULER_DATA_DIR", "/tmp/sched-data")
	t.Setenv("CHATCLI_SCHEDULER_MAX_JOBS", "99")
	t.Setenv("CHATCLI_SCHEDULER_ALLOW_AGENTS", "no")
	t.Setenv("CHATCLI_SCHEDULER_ACTION_ALLOWLIST", "noop, llm_prompt , ,shell")

	c := DefaultConfig()
	loadCoreEnv(&c)

	if c.Enabled {
		t.Error("Enabled should be false")
	}
	if c.DataDir != "/tmp/sched-data" {
		t.Errorf("DataDir=%q", c.DataDir)
	}
	if c.MaxJobs != 99 {
		t.Errorf("MaxJobs=%d want 99", c.MaxJobs)
	}
	if c.AllowAgents {
		t.Error("AllowAgents should be false")
	}
	// allowlist should contain exactly noop, llm_prompt, shell (blanks dropped)
	if len(c.ActionAllowlist) != 3 {
		t.Errorf("allowlist size=%d want 3: %v", len(c.ActionAllowlist), c.ActionAllowlist)
	}
	if !c.ActionAllowlist[ActionNoop] || !c.ActionAllowlist[ActionLLMPrompt] || !c.ActionAllowlist[ActionShell] {
		t.Errorf("allowlist missing expected entries: %v", c.ActionAllowlist)
	}
}

func TestLoadDefaultsEnv_Overrides(t *testing.T) {
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_ACTION_TIMEOUT", "2m")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_POLL_INTERVAL", "10s")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_WAIT_TIMEOUT", "15m")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_MAX_POLLS", "5")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_INITIAL", "2s")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MAX", "10m")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MULT", "3.0")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_JITTER", "0.3")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_MAX_RETRIES", "7")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_TTL", "48h")
	t.Setenv("CHATCLI_SCHEDULER_HISTORY_LIMIT", "32")

	c := DefaultConfig()
	loadDefaultsEnv(&c)

	if c.DefaultActionTimeout != 2*time.Minute {
		t.Errorf("ActionTimeout=%v", c.DefaultActionTimeout)
	}
	if c.DefaultPollInterval != 10*time.Second {
		t.Errorf("PollInterval=%v", c.DefaultPollInterval)
	}
	if c.DefaultWaitTimeout != 15*time.Minute {
		t.Errorf("WaitTimeout=%v", c.DefaultWaitTimeout)
	}
	if c.DefaultMaxPolls != 5 {
		t.Errorf("MaxPolls=%d", c.DefaultMaxPolls)
	}
	if c.DefaultBackoffInitial != 2*time.Second {
		t.Errorf("BackoffInitial=%v", c.DefaultBackoffInitial)
	}
	if c.DefaultBackoffMax != 10*time.Minute {
		t.Errorf("BackoffMax=%v", c.DefaultBackoffMax)
	}
	if c.DefaultBackoffMult != 3.0 {
		t.Errorf("BackoffMult=%v", c.DefaultBackoffMult)
	}
	if c.DefaultBackoffJitter != 0.3 {
		t.Errorf("BackoffJitter=%v", c.DefaultBackoffJitter)
	}
	if c.DefaultMaxRetries != 7 {
		t.Errorf("MaxRetries=%d", c.DefaultMaxRetries)
	}
	if c.DefaultTTL != 48*time.Hour {
		t.Errorf("TTL=%v", c.DefaultTTL)
	}
	if c.HistoryLimit != 32 {
		t.Errorf("HistoryLimit=%d", c.HistoryLimit)
	}
}

func TestLoadDefaultsEnv_RejectsOutOfRange(t *testing.T) {
	// Jitter must be in [0, 0.5]; mult must be >= 1 — out-of-range values
	// are ignored and the defaults survive.
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_JITTER", "0.9")
	t.Setenv("CHATCLI_SCHEDULER_DEFAULT_BACKOFF_MULT", "0.5")

	c := DefaultConfig()
	loadDefaultsEnv(&c)

	if c.DefaultBackoffJitter != 0.2 {
		t.Errorf("jitter should stay default 0.2, got %v", c.DefaultBackoffJitter)
	}
	if c.DefaultBackoffMult != 2.0 {
		t.Errorf("mult should stay default 2.0, got %v", c.DefaultBackoffMult)
	}
}

func TestLoadRateLimitEnv_Overrides(t *testing.T) {
	t.Setenv("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_RPS", "12.5")
	t.Setenv("CHATCLI_SCHEDULER_RATE_LIMIT_GLOBAL_BURST", "50")
	t.Setenv("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_RPS", "3.0")
	t.Setenv("CHATCLI_SCHEDULER_RATE_LIMIT_OWNER_BURST", "25")

	c := DefaultConfig()
	loadRateLimitEnv(&c)

	if c.RateLimitGlobalRPS != 12.5 {
		t.Errorf("GlobalRPS=%v", c.RateLimitGlobalRPS)
	}
	if c.RateLimitGlobalBurst != 50 {
		t.Errorf("GlobalBurst=%d", c.RateLimitGlobalBurst)
	}
	if c.RateLimitOwnerRPS != 3.0 {
		t.Errorf("OwnerRPS=%v", c.RateLimitOwnerRPS)
	}
	if c.RateLimitOwnerBurst != 25 {
		t.Errorf("OwnerBurst=%d", c.RateLimitOwnerBurst)
	}
}

func TestLoadBreakerEnv_Overrides(t *testing.T) {
	t.Setenv("CHATCLI_SCHEDULER_BREAKER_FAILURE_THRESHOLD", "9")
	t.Setenv("CHATCLI_SCHEDULER_BREAKER_WINDOW", "120s")
	t.Setenv("CHATCLI_SCHEDULER_BREAKER_COOLDOWN", "45s")

	c := DefaultConfig()
	loadBreakerEnv(&c)

	if c.BreakerConfig.FailureThreshold != 9 {
		t.Errorf("FailureThreshold=%d", c.BreakerConfig.FailureThreshold)
	}
	if c.BreakerConfig.Window != 120*time.Second {
		t.Errorf("Window=%v", c.BreakerConfig.Window)
	}
	if c.BreakerConfig.Cooldown != 45*time.Second {
		t.Errorf("Cooldown=%v", c.BreakerConfig.Cooldown)
	}
}

func TestLoadAuditEnv_Overrides(t *testing.T) {
	t.Setenv("CHATCLI_SCHEDULER_AUDIT_ENABLED", "off")
	t.Setenv("CHATCLI_SCHEDULER_AUDIT_MAX_SIZE_MB", "25")
	t.Setenv("CHATCLI_SCHEDULER_AUDIT_MAX_BACKUPS", "3")
	t.Setenv("CHATCLI_SCHEDULER_AUDIT_MAX_AGE_DAYS", "14")

	c := DefaultConfig()
	loadAuditEnv(&c)

	if c.AuditEnabled {
		t.Error("AuditEnabled should be false")
	}
	if c.AuditMaxSizeMB != 25 {
		t.Errorf("MaxSizeMB=%d", c.AuditMaxSizeMB)
	}
	if c.AuditMaxBackups != 3 {
		t.Errorf("MaxBackups=%d", c.AuditMaxBackups)
	}
	if c.AuditMaxAgeDays != 14 {
		t.Errorf("MaxAgeDays=%d", c.AuditMaxAgeDays)
	}
}

func TestLoadDaemonEnv_Overrides(t *testing.T) {
	t.Setenv("CHATCLI_SCHEDULER_DAEMON_SOCKET", "/tmp/s.sock")
	t.Setenv("CHATCLI_SCHEDULER_DAEMON_AUTO_CONNECT", "false")
	t.Setenv("CHATCLI_SCHEDULER_SNAPSHOT_INTERVAL", "2m")
	t.Setenv("CHATCLI_SCHEDULER_WAL_GC_INTERVAL", "30m")
	t.Setenv("CHATCLI_SCHEDULER_WORKER_COUNT", "8")
	t.Setenv("CHATCLI_SCHEDULER_WAIT_WORKER_COUNT", "16")

	c := DefaultConfig()
	loadDaemonEnv(&c)

	if c.DaemonSocket != "/tmp/s.sock" {
		t.Errorf("DaemonSocket=%q", c.DaemonSocket)
	}
	if c.DaemonAutoConnect {
		t.Error("DaemonAutoConnect should be false")
	}
	if c.SnapshotInterval != 2*time.Minute {
		t.Errorf("SnapshotInterval=%v", c.SnapshotInterval)
	}
	if c.WALGCInterval != 30*time.Minute {
		t.Errorf("WALGCInterval=%v", c.WALGCInterval)
	}
	if c.WorkerCount != 8 {
		t.Errorf("WorkerCount=%d", c.WorkerCount)
	}
	if c.WaitWorkerCount != 16 {
		t.Errorf("WaitWorkerCount=%d", c.WaitWorkerCount)
	}
}

func TestConfigBudgetDefaults(t *testing.T) {
	c := DefaultConfig()
	b := c.budgetDefaults()
	if b.ActionTimeout != c.DefaultActionTimeout {
		t.Errorf("ActionTimeout=%v want %v", b.ActionTimeout, c.DefaultActionTimeout)
	}
	if b.MaxRetries != c.DefaultMaxRetries {
		t.Errorf("MaxRetries=%d want %d", b.MaxRetries, c.DefaultMaxRetries)
	}
	if b.BackoffMult != c.DefaultBackoffMult {
		t.Errorf("BackoffMult=%v want %v", b.BackoffMult, c.DefaultBackoffMult)
	}
	if b.WaitTimeout != c.DefaultWaitTimeout {
		t.Errorf("WaitTimeout=%v want %v", b.WaitTimeout, c.DefaultWaitTimeout)
	}
	if b.PollInterval != c.DefaultPollInterval {
		t.Errorf("PollInterval=%v want %v", b.PollInterval, c.DefaultPollInterval)
	}
	if b.MaxPolls != c.DefaultMaxPolls {
		t.Errorf("MaxPolls=%d want %d", b.MaxPolls, c.DefaultMaxPolls)
	}
}
