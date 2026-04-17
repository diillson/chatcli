package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// TestMain ensures i18n is initialized before any test runs so T() returns
// real translations instead of the raw key strings.
func TestMain(m *testing.M) {
	// Force English for stable, locale-independent test assertions.
	_ = os.Setenv("CHATCLI_LANG", "en")
	i18n.Init()
	os.Exit(m.Run())
}

// captureStdout swaps os.Stdout for a pipe, runs fn, and returns the
// captured output. Used to assert that each /config section renders without
// panics and emits meaningful text.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	<-done
	return buf.String()
}

// minimalCLI builds a ChatCLI with the smallest set of non-nil dependencies
// each section needs. Sections MUST handle nil sub-managers gracefully; this
// test enforces that contract by leaving most fields zero-valued.
func minimalCLI(t *testing.T) *ChatCLI {
	t.Helper()
	logger, _ := zap.NewDevelopment()
	return &ChatCLI{
		logger:         logger,
		Provider:       "OPENAI",
		Model:          "gpt-4o",
		historyManager: NewHistoryManager(logger),
	}
}

// Each section must not panic with a minimal CLI, even when sub-managers
// (mcp, hooks, skills, persona, memory, cost, context) are all nil.
func TestConfigSections_NoPanicWithMinimalCLI(t *testing.T) {
	cases := []struct {
		name string
		fn   func(*ChatCLI)
	}{
		{"panorama", func(c *ChatCLI) { c.showConfigPanorama() }},
		{"general", func(c *ChatCLI) { c.showConfigGeneral() }},
		{"providers", func(c *ChatCLI) { c.showConfigProviders() }},
		{"agent", func(c *ChatCLI) { c.showConfigAgent() }},
		{"resilience", func(c *ChatCLI) { c.showConfigResilience() }},
		{"auth", func(c *ChatCLI) { c.showConfigAuth() }},
		{"security", func(c *ChatCLI) { c.showConfigSecurity() }},
		{"server", func(c *ChatCLI) { c.showConfigServer() }},
		{"session", func(c *ChatCLI) { c.showConfigSession() }},
		{"integrations", func(c *ChatCLI) { c.showConfigIntegrations() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := minimalCLI(t)
			out := captureStdout(t, func() { tc.fn(c) })
			if strings.TrimSpace(out) == "" {
				t.Errorf("section %q produced empty output", tc.name)
			}
		})
	}
}

// routeConfigCommand dispatches on the subsection name. An unknown name
// prints a hint without panicking, and routing is case-insensitive.
func TestRouteConfigCommand(t *testing.T) {
	c := minimalCLI(t)

	tests := []struct {
		name      string
		args      []string
		wantToken string // substring expected in captured output
	}{
		{"empty → panorama", []string{}, "PANORAMA"},
		{"general", []string{"general"}, "GENERAL"},
		{"AGENT caps", []string{"AGENT"}, "AGENT RUNTIME"},
		{"unknown section", []string{"bogus"}, "Unknown section"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { c.routeConfigCommand(tt.args) })
			if !strings.Contains(out, tt.wantToken) {
				t.Errorf("expected output to contain %q, got:\n%s", tt.wantToken, out)
			}
		})
	}
}

// Server section short-circuits with a "skipped" notice when no
// server-mode envs are set. When any of them is set it prints the full
// block — asserted by checking for a JWT/gRPC-related label.
func TestConfigServer_SkippedAndActive(t *testing.T) {
	c := minimalCLI(t)

	// Snapshot and clear known server envs for the "skipped" case.
	envs := []string{
		"CHATCLI_SERVER_TOKEN", "CHATCLI_BIND_ADDRESS", "CHATCLI_GRPC_REFLECTION",
		"CHATCLI_JWT_SECRET", "CHATCLI_RATE_LIMIT_RPS", "CHATCLI_FALLBACK_PROVIDERS",
		"CHATCLI_FALLBACK_ENABLED", "CHATCLI_AUDIT_LOG_PATH",
		"CHATCLI_WATCH_DEPLOYMENT", "CHATCLI_WATCH_NAMESPACE", "CHATCLI_AIOPS_PORT",
		"CHATCLI_SERVER_TLS_CERT", "CHATCLI_SERVER_TLS_KEY",
		"CHATCLI_JWT_ISSUER", "CHATCLI_JWT_AUDIENCE",
	}
	saved := make(map[string]string, len(envs))
	for _, k := range envs {
		saved[k] = os.Getenv(k)
		_ = os.Unsetenv(k)
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, v)
			}
		}
	}()

	out := captureStdout(t, func() { c.showConfigServer() })
	if !strings.Contains(out, "skipping") {
		t.Errorf("expected skip notice when no server envs set; got:\n%s", out)
	}

	_ = os.Setenv("CHATCLI_BIND_ADDRESS", "0.0.0.0:50051")
	out = captureStdout(t, func() { c.showConfigServer() })
	if !strings.Contains(out, "gRPC server") {
		t.Errorf("expected full server block when a server env is set; got:\n%s", out)
	}
}

// Per-agent overrides are collected from os.Environ() with sort by key.
// The snapshot/restore pattern keeps the test hermetic.
func TestCollectPerAgentOverrides(t *testing.T) {
	for _, k := range []string{"CHATCLI_AGENT_ALPHA_MODEL", "CHATCLI_AGENT_BETA_EFFORT", "CHATCLI_AGENT_MODEL"} {
		_ = os.Unsetenv(k)
	}
	_ = os.Setenv("CHATCLI_AGENT_ALPHA_MODEL", "gpt-4o")
	_ = os.Setenv("CHATCLI_AGENT_BETA_EFFORT", "extended")
	_ = os.Setenv("CHATCLI_AGENT_MODEL", "ignored-global") // excluded by name
	defer func() {
		_ = os.Unsetenv("CHATCLI_AGENT_ALPHA_MODEL")
		_ = os.Unsetenv("CHATCLI_AGENT_BETA_EFFORT")
		_ = os.Unsetenv("CHATCLI_AGENT_MODEL")
	}()

	got := collectPerAgentOverrides()
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(got), got)
	}
	if got[0].key != "CHATCLI_AGENT_ALPHA_MODEL" || got[1].key != "CHATCLI_AGENT_BETA_EFFORT" {
		t.Errorf("unexpected sort order: %+v", got)
	}
	if got[0].val != "gpt-4o" {
		t.Errorf("unexpected value for ALPHA_MODEL: %s", got[0].val)
	}
}

// envBool maps a variety of truthy/falsy strings + default fallback.
func TestEnvBool(t *testing.T) {
	const k = "CHATCLI_TEST_BOOL_XYZ"
	defer func() { _ = os.Unsetenv(k) }()

	cases := []struct {
		in   string
		want string
	}{
		{"true", "enabled"},
		{"1", "enabled"},
		{"YES", "enabled"},
		{"false", "disabled"},
		{"0", "disabled"},
		{"", "(default)"},
		{"weirdvalue", "weirdvalue"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if tc.in == "" {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, tc.in)
			}
			got := envBool(k)
			if got != tc.want {
				t.Errorf("envBool(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
