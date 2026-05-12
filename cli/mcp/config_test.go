/*
 * ChatCLI - Tests for ServerConfig JSON round-trip, helpers and ACL
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Covers the surface added by the MCP server config extensions:
 *   - UnmarshalJSON / MarshalJSON preserving unknown keys verbatim
 *   - RequestTimeout / InitializeTimeout default-vs-override
 *   - AutoApproveSet folding AlwaysAllow into AutoApprove
 *   - MatchesAutoApprove (Trust, wildcard, mcp_-prefixed lookup)
 *   - IsToolVisible (EnabledTools precedence, wildcard, blocklist)
 *   - ResolveCwd / ResolveHeaders env-expansion
 *   - AuthConfig.ApplyAuth (bearer, basic, header, env-expanded)
 */
package mcp

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestServerConfig_JSONRoundTrip_PreservesExtensions(t *testing.T) {
	// Mix of typed fields (handled by struct tags) and unknown fields
	// (must land in Extensions and survive the round-trip byte-for-byte).
	in := `{
		"name": "fs",
		"command": "npx",
		"args": ["-y", "@modelcontextprotocol/server-filesystem"],
		"transport": "stdio",
		"enabled": true,
		"autoApprove": ["read_file"],
		"timeout": 45,
		"cwd": "/tmp",
		"thirdPartyFlag": true,
		"unknownObject": {"nested": [1, 2, 3]}
	}`

	var cfg ServerConfig
	if err := json.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if cfg.Name != "fs" || cfg.Timeout != 45 || len(cfg.AutoApprove) != 1 {
		t.Errorf("typed fields not parsed: %+v", cfg)
	}
	if cfg.Cwd != "/tmp" {
		t.Errorf("cwd = %q, want /tmp", cfg.Cwd)
	}
	if _, ok := cfg.Extensions["thirdPartyFlag"]; !ok {
		t.Errorf("unknown key thirdPartyFlag missing from Extensions: %+v", cfg.Extensions)
	}
	if _, ok := cfg.Extensions["unknownObject"]; !ok {
		t.Errorf("unknown nested object missing from Extensions")
	}
	// A typed key must NOT leak into Extensions.
	if _, leaked := cfg.Extensions["timeout"]; leaked {
		t.Errorf("typed key 'timeout' leaked into Extensions")
	}

	// Round-trip back to JSON and confirm the unknown keys survive.
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var verify map[string]json.RawMessage
	if err := json.Unmarshal(out, &verify); err != nil {
		t.Fatalf("verify unmarshal: %v", err)
	}
	for _, k := range []string{"thirdPartyFlag", "unknownObject"} {
		if _, ok := verify[k]; !ok {
			t.Errorf("round-trip dropped unknown key %q", k)
		}
	}
}

func TestServerConfig_MarshalJSON_ExtensionDoesNotShadowTypedField(t *testing.T) {
	// A handcrafted ServerConfig where Extensions contains "command"
	// must not let that value escape into the wire output and override
	// the typed Command field.
	cfg := ServerConfig{
		Name:    "evil",
		Command: "/bin/safe",
		Extensions: map[string]json.RawMessage{
			"command": json.RawMessage(`"/bin/sh"`),
		},
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var verify struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(out, &verify); err != nil {
		t.Fatal(err)
	}
	if verify.Command != "/bin/safe" {
		t.Errorf("Extensions clobbered typed command: got %q, want /bin/safe", verify.Command)
	}
}

func TestServerConfig_RequestTimeout_DefaultAndOverride(t *testing.T) {
	if got := (ServerConfig{}).RequestTimeout(); got != DefaultRequestTimeout {
		t.Errorf("zero Timeout should fall back to default; got %s", got)
	}
	if got := (ServerConfig{Timeout: 0}).RequestTimeout(); got != DefaultRequestTimeout {
		t.Errorf("explicit zero should fall back to default; got %s", got)
	}
	if got := (ServerConfig{Timeout: -5}).RequestTimeout(); got != DefaultRequestTimeout {
		t.Errorf("negative Timeout should fall back to default; got %s", got)
	}
	if got := (ServerConfig{Timeout: 12}).RequestTimeout(); got != 12*time.Second {
		t.Errorf("Timeout=12 should give 12s; got %s", got)
	}
}

func TestServerConfig_InitializeTimeout_DefaultAndOverride(t *testing.T) {
	if got := (ServerConfig{}).InitializeTimeout(); got != DefaultInitializeTimeout {
		t.Errorf("default not honored; got %s", got)
	}
	if got := (ServerConfig{InitTimeout: 30}).InitializeTimeout(); got != 30*time.Second {
		t.Errorf("InitTimeout=30 should give 30s; got %s", got)
	}
}

func TestServerConfig_AutoApproveSet_FoldsAlwaysAllow(t *testing.T) {
	cfg := ServerConfig{
		AutoApprove: []string{"read_file", "list_files"},
		AlwaysAllow: []string{"  read_file  ", "execute_command"}, // dup + whitespace + new
	}
	set := cfg.AutoApproveSet()
	gotKeys := make([]string, 0, len(set))
	for k := range set {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	want := []string{"execute_command", "list_files", "read_file"}
	if !reflect.DeepEqual(gotKeys, want) {
		t.Errorf("AutoApproveSet = %v, want %v", gotKeys, want)
	}
}

func TestServerConfig_MatchesAutoApprove(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		tool string
		want bool
	}{
		{
			name: "trust bypasses every check",
			cfg:  ServerConfig{Trust: true},
			tool: "anything",
			want: true,
		},
		{
			name: "wildcard matches everything",
			cfg:  ServerConfig{AutoApprove: []string{"*"}},
			tool: "mcp_read_file",
			want: true,
		},
		{
			name: "literal name matches",
			cfg:  ServerConfig{AutoApprove: []string{"read_file"}},
			tool: "read_file",
			want: true,
		},
		{
			name: "literal name matches after mcp_ prefix strip",
			cfg:  ServerConfig{AutoApprove: []string{"read_file"}},
			tool: "mcp_read_file",
			want: true,
		},
		{
			name: "different name is rejected",
			cfg:  ServerConfig{AutoApprove: []string{"read_file"}},
			tool: "write_file",
			want: false,
		},
		{
			name: "alwaysAllow alias also matches",
			cfg:  ServerConfig{AlwaysAllow: []string{"exec_command"}},
			tool: "exec_command",
			want: true,
		},
		{
			name: "empty config never matches",
			cfg:  ServerConfig{},
			tool: "anything",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.MatchesAutoApprove(tc.tool); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServerConfig_IsToolVisible(t *testing.T) {
	cases := []struct {
		name string
		cfg  ServerConfig
		tool string
		want bool
	}{
		{
			name: "no list → visible",
			cfg:  ServerConfig{},
			tool: "read_file",
			want: true,
		},
		{
			name: "enabledTools allowlist hides unlisted",
			cfg:  ServerConfig{EnabledTools: []string{"read_file"}},
			tool: "write_file",
			want: false,
		},
		{
			name: "enabledTools allowlist shows listed",
			cfg:  ServerConfig{EnabledTools: []string{"read_file"}},
			tool: "read_file",
			want: true,
		},
		{
			name: "enabledTools wildcard shows everything",
			cfg:  ServerConfig{EnabledTools: []string{"*"}},
			tool: "anything",
			want: true,
		},
		{
			name: "enabledTools takes precedence over disabledTools",
			cfg:  ServerConfig{EnabledTools: []string{"read_file"}, DisabledTools: []string{"read_file"}},
			tool: "read_file",
			want: true,
		},
		{
			name: "disabledTools hides listed",
			cfg:  ServerConfig{DisabledTools: []string{"dangerous"}},
			tool: "dangerous",
			want: false,
		},
		{
			name: "disabledTools wildcard hides everything",
			cfg:  ServerConfig{DisabledTools: []string{"*"}},
			tool: "anything",
			want: false,
		},
		{
			name: "mcp_ prefix is handled on tool name",
			cfg:  ServerConfig{DisabledTools: []string{"dangerous"}},
			tool: "mcp_dangerous",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsToolVisible(tc.tool); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServerConfig_ResolveCwd_ExpandsEnvAndTilde(t *testing.T) {
	t.Setenv("CHATCLI_TEST_CWD_BASE", "/var/lib/test")
	got := ServerConfig{Cwd: "${CHATCLI_TEST_CWD_BASE}/sub"}.ResolveCwd()
	if got != "/var/lib/test/sub" {
		t.Errorf("env-expansion: got %q, want /var/lib/test/sub", got)
	}

	t.Setenv("HOME", "/Users/example")
	if got := (ServerConfig{Cwd: "~/work"}).ResolveCwd(); got != "/Users/example/work" {
		t.Errorf("tilde-expansion: got %q, want /Users/example/work", got)
	}

	if got := (ServerConfig{}).ResolveCwd(); got != "" {
		t.Errorf("empty Cwd should resolve to empty; got %q", got)
	}
}

func TestServerConfig_ResolveHeaders_ExpandsValues(t *testing.T) {
	t.Setenv("CHATCLI_TEST_HEADER_VAL", "Token123")
	cfg := ServerConfig{
		Headers: map[string]string{
			"X-Token":      "${CHATCLI_TEST_HEADER_VAL}",
			"X-Static":     "literal",
			"X-Empty-Pass": "${CHATCLI_DOES_NOT_EXIST}",
		},
	}
	got := cfg.ResolveHeaders()
	if got["X-Token"] != "Token123" {
		t.Errorf("env-expanded header missing: %v", got)
	}
	if got["X-Static"] != "literal" {
		t.Errorf("literal header dropped: %v", got)
	}
	if _, ok := got["X-Empty-Pass"]; !ok {
		t.Errorf("undefined env var should still produce a key (empty value); got %v", got)
	}

	if (ServerConfig{}).ResolveHeaders() != nil {
		t.Errorf("empty Headers should return nil for easy range")
	}
}

func TestAuthConfig_ApplyAuth_NilSafe(t *testing.T) {
	var a *AuthConfig
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req) // must not panic
	if h := req.Header.Get("Authorization"); h != "" {
		t.Errorf("nil AuthConfig should not set headers; got %q", h)
	}
}

func TestAuthConfig_ApplyAuth_BearerWithEnvExpansion(t *testing.T) {
	t.Setenv("MY_TOKEN", "abc.def")
	a := &AuthConfig{Type: "bearer", Token: "${MY_TOKEN}"}
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req)
	if got := req.Header.Get("Authorization"); got != "Bearer abc.def" {
		t.Errorf("Authorization = %q, want Bearer abc.def", got)
	}
}

func TestAuthConfig_ApplyAuth_BasicWithEnvExpansion(t *testing.T) {
	t.Setenv("MY_USER", "alice")
	t.Setenv("MY_PASS", "wonderland")
	a := &AuthConfig{Type: "basic", Username: "${MY_USER}", Password: "${MY_PASS}"}
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req)
	user, pass, ok := req.BasicAuth()
	if !ok || user != "alice" || pass != "wonderland" {
		t.Errorf("basic auth not applied; user=%q pass=%q ok=%v", user, pass, ok)
	}
}

func TestAuthConfig_ApplyAuth_HeaderDefaultsToXApiKey(t *testing.T) {
	a := &AuthConfig{Type: "header", Token: "secret"}
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req)
	if got := req.Header.Get("X-API-Key"); got != "secret" {
		t.Errorf("default header should be X-API-Key=secret; got %q", got)
	}
}

func TestAuthConfig_ApplyAuth_HeaderCustomName(t *testing.T) {
	a := &AuthConfig{Type: "header", Header: "X-Custom-Auth", Token: "v"}
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req)
	if got := req.Header.Get("X-Custom-Auth"); got != "v" {
		t.Errorf("custom header not honored; got %q", got)
	}
}

func TestAuthConfig_ApplyAuth_EmptyTypeIsNoop(t *testing.T) {
	a := &AuthConfig{Token: "lonely-token"}
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req)
	if h := req.Header.Get("Authorization"); h != "" {
		t.Errorf("empty Type must not leak credentials; got %q", h)
	}
}

func TestAuthConfig_ApplyAuth_BearerWithUnsetEnvIsNoop(t *testing.T) {
	// Unset env var must NOT leak an empty "Bearer " header — that
	// would still pass a header-presence check on the server side and
	// confuse audit logs about whether auth was attempted.
	a := &AuthConfig{Type: "bearer", Token: "${CHATCLI_DOES_NOT_EXIST}"}
	req, _ := http.NewRequest("GET", "http://example", nil)
	a.ApplyAuth(req)
	if h := req.Header.Get("Authorization"); h != "" {
		t.Errorf("unset env should suppress the header entirely; got %q", h)
	}
}

func TestKnownConfigKeys_CoversEveryTaggedField(t *testing.T) {
	// Sanity: every typed JSON tag on ServerConfig is in knownConfigKeys
	// so unmarshaling does not accidentally route a typed key into
	// Extensions.
	for _, want := range []string{
		"name", "command", "args", "env", "transport", "url", "enabled",
		"overrides", "description", "cwd", "autoApprove", "alwaysAllow",
		"disabledTools", "timeout", "initTimeout", "headers", "auth",
		"enabledTools", "tags", "category", "trust",
	} {
		if _, ok := knownConfigKeys[want]; !ok {
			t.Errorf("knownConfigKeys missing %q — typed fields might leak into Extensions", want)
		}
	}
}
