/*
 * ChatCLI - MCP ServerConfig JSON marshaling, helpers and validation
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Lives next to types.go but isolates the cross-cutting concerns —
 * round-tripping unknown JSON keys, expanding env-var references in
 * filesystem paths, resolving per-server timeouts, and folding
 * AutoApprove/AlwaysAllow into a single runtime set — so the schema
 * file stays purely declarative.
 */
package mcp

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

// Default timeouts. Both can be overridden per-server through the
// Timeout / InitTimeout fields on ServerConfig.
//
//   - DefaultRequestTimeout covers a worst-case `npx -y <pkg>` cold
//     start that has to fetch the MCP server package; subsequent
//     calls hit the npm cache and respond in milliseconds.
//   - DefaultInitializeTimeout matches the SSE endpoint-event wait
//     and accommodates servers that perform credential bootstrap
//     during the MCP `initialize` handshake.
const (
	DefaultRequestTimeout    = 60 * time.Second
	DefaultInitializeTimeout = 10 * time.Second
)

// Wildcard expands to "every tool on this server" in AutoApprove /
// AlwaysAllow / EnabledTools / DisabledTools.
const Wildcard = "*"

// knownConfigKeys caches the JSON tag names that ServerConfig
// declares as typed fields. Built once at package init via reflection
// so future schema additions stay a single-source-of-truth on the
// struct tags — no parallel literal list to maintain.
var knownConfigKeys = buildKnownConfigKeys()

func buildKnownConfigKeys() map[string]struct{} {
	t := reflect.TypeOf(ServerConfig{})
	keys := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		if name != "" {
			keys[name] = struct{}{}
		}
	}
	return keys
}

// UnmarshalJSON decodes a ServerConfig and captures every key not
// covered by a typed field into Extensions. The double-pass approach
// (typed decode + raw-map decode) keeps the implementation immune to
// schema drift on the struct above without manually enumerating
// known keys here.
func (c *ServerConfig) UnmarshalJSON(data []byte) error {
	// Inner alias prevents infinite recursion through this method
	// when json.Unmarshal sees the receiver type.
	type alias ServerConfig
	inner := (*alias)(c)
	if err := json.Unmarshal(data, inner); err != nil {
		return err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var ext map[string]json.RawMessage
	for k, v := range raw {
		if _, known := knownConfigKeys[k]; known {
			continue
		}
		if ext == nil {
			ext = make(map[string]json.RawMessage)
		}
		ext[k] = v
	}
	c.Extensions = ext
	return nil
}

// MarshalJSON re-emits the typed fields plus any captured Extensions.
// An Extensions entry never overwrites a known field, so a user that
// hand-edited mcp_servers.json with a stray "command": "..." inside
// Extensions cannot escape the typed semantics on the next save.
func (c ServerConfig) MarshalJSON() ([]byte, error) {
	type alias ServerConfig
	base, err := json.Marshal(alias(c))
	if err != nil {
		return nil, err
	}
	if len(c.Extensions) == 0 {
		return base, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range c.Extensions {
		if _, known := knownConfigKeys[k]; known {
			continue
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}

// RequestTimeout returns the per-call timeout for this server's RPC
// requests. Zero or negative Timeout falls back to DefaultRequestTimeout.
func (c ServerConfig) RequestTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultRequestTimeout
	}
	return time.Duration(c.Timeout) * time.Second
}

// InitializeTimeout returns the timeout for the MCP initialize
// handshake (and for the SSE endpoint-event wait). Zero or negative
// InitTimeout falls back to DefaultInitializeTimeout.
func (c ServerConfig) InitializeTimeout() time.Duration {
	if c.InitTimeout <= 0 {
		return DefaultInitializeTimeout
	}
	return time.Duration(c.InitTimeout) * time.Second
}

// AutoApproveSet folds AutoApprove and AlwaysAllow into one runtime
// set with whitespace-trimmed, case-sensitive keys. Callers consult
// the set instead of the raw slices so the agent gate does not have
// to know that AlwaysAllow exists as an alias.
func (c ServerConfig) AutoApproveSet() map[string]struct{} {
	out := make(map[string]struct{}, len(c.AutoApprove)+len(c.AlwaysAllow))
	addTrimmed(out, c.AutoApprove)
	addTrimmed(out, c.AlwaysAllow)
	return out
}

// MatchesAutoApprove reports whether the named tool should bypass the
// approval gate. Tool names are matched literally and with the
// "mcp_" prefix stripped — the agent loop uses the prefixed form
// internally, but users naturally write the bare name in configs.
func (c ServerConfig) MatchesAutoApprove(toolName string) bool {
	if c.Trust {
		return true
	}
	set := c.AutoApproveSet()
	if _, star := set[Wildcard]; star {
		return true
	}
	if _, ok := set[toolName]; ok {
		return true
	}
	if _, ok := set[strings.TrimPrefix(toolName, "mcp_")]; ok {
		return true
	}
	return false
}

// IsChannelSubscribed reports whether the named channel should be
// delivered into the ring for this server. Empty/missing Channels
// list means "accept everything"; otherwise the entry must appear
// in the list (with "*" matching any channel). Whitespace is
// trimmed so users editing JSON by hand can be sloppy.
func (c ServerConfig) IsChannelSubscribed(channel string) bool {
	if len(c.Channels) == 0 {
		return true
	}
	for _, entry := range c.Channels {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == Wildcard || entry == channel {
			return true
		}
	}
	return false
}

// IsToolVisible reports whether a tool should be exposed to the LLM.
// EnabledTools is an allowlist with strict precedence: when non-empty,
// only listed tools are visible. Otherwise DisabledTools acts as a
// blocklist. Wildcard "*" is honored in both lists for symmetry with
// AutoApprove. Empty config → tool is visible.
func (c ServerConfig) IsToolVisible(toolName string) bool {
	bare := strings.TrimPrefix(toolName, "mcp_")
	if len(c.EnabledTools) > 0 {
		return listMatches(c.EnabledTools, toolName, bare)
	}
	if listMatches(c.DisabledTools, toolName, bare) {
		return false
	}
	return true
}

// ResolveCwd expands env vars and a leading "~/" in Cwd. Returns the
// empty string when no Cwd was configured so the caller (the stdio
// transport) can leave exec.Cmd.Dir untouched and inherit the
// parent's working directory.
func (c ServerConfig) ResolveCwd() string {
	if c.Cwd == "" {
		return ""
	}
	return expandPath(c.Cwd)
}

// ResolveHeaders returns a copy of Headers with values env-expanded.
// Returns nil when Headers is empty so callers can range over the
// result safely without an extra length check.
func (c ServerConfig) ResolveHeaders() map[string]string {
	if len(c.Headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		out[k] = os.ExpandEnv(v)
	}
	return out
}

// ApplyAuth attaches the configured Authorization (or custom header)
// to the request. No-op when Auth is nil or Type is unset, so the
// caller can wire this unconditionally.
//
// Bearer:  Authorization: Bearer <Token>
// Basic:   req.SetBasicAuth(Username, Password)
// Header:  <Header>: <Token>     (defaults Header to "X-API-Key")
//
// Token / Username / Password / Header are env-expanded at apply
// time so a rotated secret in the shell is picked up by the next
// RPC without reloading the config.
func (a *AuthConfig) ApplyAuth(req *http.Request) {
	if a == nil || req == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(a.Type)) {
	case "bearer":
		if tok := os.ExpandEnv(a.Token); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	case "basic":
		user := os.ExpandEnv(a.Username)
		pass := os.ExpandEnv(a.Password)
		if user != "" || pass != "" {
			req.SetBasicAuth(user, pass)
		}
	case "header":
		name := strings.TrimSpace(os.ExpandEnv(a.Header))
		if name == "" {
			name = "X-API-Key"
		}
		if tok := os.ExpandEnv(a.Token); tok != "" {
			req.Header.Set(name, tok)
		}
	}
}

// expandPath runs os.ExpandEnv plus a leading "~/" → "$HOME/"
// substitution. Used for fields where shell conventions apply
// (currently Cwd). Falls back gracefully when HOME is unresolvable.
func expandPath(path string) string {
	expanded := os.ExpandEnv(path)
	if expanded == "~" || strings.HasPrefix(expanded, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = home + expanded[1:]
		}
	}
	return expanded
}

// addTrimmed inserts every non-empty, whitespace-trimmed entry of
// list into set. Empties are silently dropped because the
// stringly-typed JSON occasionally produces them (trailing commas,
// blank lines in YAML-derived JSON, …).
func addTrimmed(set map[string]struct{}, list []string) {
	for _, name := range list {
		if name = strings.TrimSpace(name); name != "" {
			set[name] = struct{}{}
		}
	}
}

// listMatches reports whether name (or its bare form) appears in
// list, with Wildcard short-circuiting on the first hit.
func listMatches(list []string, name, bare string) bool {
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == Wildcard || entry == name || entry == bare {
			return true
		}
	}
	return false
}
