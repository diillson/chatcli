/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func writeFakePlugin(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	return path
}

// The whole flow the documentation described and no command implemented:
// generate a key, sign a binary, register the public key, verify.
func TestSupplyChain_SignTrustVerifyRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "")

	keyDir := filepath.Join(home, "keys")
	keyPath, pubPath, err := GenerateSigningKeyPair(keyDir)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	binDir := t.TempDir()
	bin := writeFakePlugin(t, binDir, "demo", "echo hi")

	sigPath, err := SignPlugin(bin, keyPath, "")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if sigPath != bin+SignatureSuffix {
		t.Errorf("signature landed at %s, want %s", sigPath, bin+SignatureSuffix)
	}

	// Not yet trusted: a signature nobody vouches for is not a signature.
	if err := NewPluginVerifier().VerifyPlugin(bin); err == nil {
		t.Fatal("verified against an empty trust store")
	}

	if _, err := TrustPublicKey(pubPath, "acme"); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if err := NewPluginVerifier().VerifyPlugin(bin); err != nil {
		t.Fatalf("signed and trusted plugin failed to verify: %v", err)
	}
}

// A signature covers the bytes, so changing them must break it.
func TestSupplyChain_TamperedBinaryFailsVerification(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	keyPath, pubPath, err := GenerateSigningKeyPair(filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	bin := writeFakePlugin(t, binDir, "demo", "echo hi")
	if _, err := SignPlugin(bin, keyPath, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := TrustPublicKey(pubPath, "acme"); err != nil {
		t.Fatal(err)
	}
	if err := NewPluginVerifier().VerifyPlugin(bin); err != nil {
		t.Fatalf("baseline verification failed: %v", err)
	}

	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	if err := NewPluginVerifier().VerifyPlugin(bin); err == nil {
		t.Fatal("a replaced binary still verified against the old signature")
	}
}

// Regenerating over a key in use silently invalidates every signature made
// with it.
func TestSupplyChain_KeygenRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := GenerateSigningKeyPair(dir); err != nil {
		t.Fatal(err)
	}
	_, _, err := GenerateSigningKeyPair(dir)
	if err == nil {
		t.Fatal("keygen overwrote an existing signing key")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unhelpful error: %v", err)
	}
}

// A trusted-key name must not be able to write outside the trust store.
func TestSupplyChain_TrustRejectsPathEscapingNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_, pubPath, err := GenerateSigningKeyPair(filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../escape", "a/b", "..", ".", "with space", "key;rm"} {
		if _, err := TrustPublicKey(pubPath, name); err == nil {
			t.Errorf("accepted trusted-key name %q", name)
		}
	}

	// An empty name is not an escape — it means "derive one from the file",
	// and the derived name must land inside the trust store.
	dest, err := TrustPublicKey(pubPath, "")
	if err != nil {
		t.Fatalf("empty name should derive a default: %v", err)
	}
	trustDir, err := TrustedKeysDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dest) != trustDir {
		t.Errorf("derived name landed at %s, outside %s", dest, trustDir)
	}
}

// --- quarantine ---

func TestQuarantine_OffByDefault(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "")
	q := NewQuarantine(t.TempDir())
	if q.Enabled() {
		t.Fatal("quarantine must be off unless configured")
	}
	bin := writeFakePlugin(t, t.TempDir(), "demo", "echo hi")
	if admitted, _ := q.Admit(bin); !admitted {
		t.Fatal("a disabled quarantine must admit everything — turning it on is the operator's call")
	}
}

func TestQuarantine_HoldsANewlySeenBinaryThenAdmitsIt(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "1h")
	dir := t.TempDir()
	q := NewQuarantine(dir)
	bin := writeFakePlugin(t, dir, "demo", "echo hi")

	admitted, remaining := q.Admit(bin)
	if admitted {
		t.Fatal("a plugin seen for the first time was admitted immediately")
	}
	if remaining <= 0 || remaining > time.Hour {
		t.Errorf("remaining = %v, want a value inside the window", remaining)
	}

	// Same binary, still inside the window.
	if admitted, _ := q.Admit(bin); admitted {
		t.Fatal("admitted before the window elapsed")
	}

	// Once the window has passed, it loads without anyone intervening.
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "1ns")
	elapsed := NewQuarantine(dir)
	if admitted, _ := elapsed.Admit(bin); !admitted {
		t.Fatal("still held after the window elapsed")
	}
}

func TestQuarantine_ReleaseAdmitsImmediately(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")
	dir := t.TempDir()
	q := NewQuarantine(dir)
	bin := writeFakePlugin(t, dir, "demo", "echo hi")

	if admitted, _ := q.Admit(bin); admitted {
		t.Fatal("admitted on first sight")
	}
	if err := q.Release("demo"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if admitted, _ := q.Admit(bin); !admitted {
		t.Fatal("a released plugin is still held")
	}
}

// The release was for the bytes that were reviewed, not for the filename.
func TestQuarantine_ReplacedBinaryReentersQuarantine(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")
	dir := t.TempDir()
	q := NewQuarantine(dir)
	bin := writeFakePlugin(t, dir, "demo", "echo hi")

	_, _ = q.Admit(bin)
	if err := q.Release("demo"); err != nil {
		t.Fatal(err)
	}
	if admitted, _ := q.Admit(bin); !admitted {
		t.Fatal("released plugin should be admitted")
	}

	writeFakePlugin(t, dir, "demo", "echo pwned")
	if admitted, _ := q.Admit(bin); admitted {
		t.Fatal("a replaced binary inherited the release granted to the old one")
	}
}

func TestQuarantine_StateSurvivesAcrossProcesses(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")
	dir := t.TempDir()
	bin := writeFakePlugin(t, dir, "demo", "echo hi")

	first := NewQuarantine(dir)
	if admitted, _ := first.Admit(bin); admitted {
		t.Fatal("admitted on first sight")
	}

	// A second process must see the same first-seen timestamp, or restarting
	// ChatCLI would reset every waiting period.
	second := NewQuarantine(dir)
	entries := second.List()
	if len(entries) != 1 || entries[0].Name != "demo" {
		t.Fatalf("state did not survive: %+v", entries)
	}
	if entries[0].Remaining <= 0 {
		t.Error("the waiting period restarted in the new process")
	}
}

func TestQuarantine_UnparseableValueStaysOffAndIsReportable(t *testing.T) {
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "twenty-four-hours")
	q := NewQuarantine(t.TempDir())
	if q.Enabled() {
		t.Fatal("an unparseable window must not silently impose a delay")
	}
	raw, bad := ConfiguredButUnparseable()
	if !bad || raw != "twenty-four-hours" {
		t.Errorf("ConfiguredButUnparseable = (%q, %v), want the value reported as bad", raw, bad)
	}
}

func TestQuarantine_AcceptsOnAndOffSpellings(t *testing.T) {
	for _, c := range []struct {
		value string
		want  time.Duration
	}{
		{"on", DefaultQuarantineWindow},
		{"true", DefaultQuarantineWindow},
		{"off", 0},
		{"0", 0},
		{"", 0},
		{"30m", 30 * time.Minute},
	} {
		t.Setenv("CHATCLI_PLUGIN_QUARANTINE", c.value)
		if got := NewQuarantine(t.TempDir()).Window(); got != c.want {
			t.Errorf("CHATCLI_PLUGIN_QUARANTINE=%q -> %v, want %v", c.value, got, c.want)
		}
	}
}

// --- manager integration ---

// The loader's actual behavior: an unsigned plugin is held on first sight
// and loads once released, without the operator touching anything else.
func TestManager_QuarantineGatesTheLoader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")

	m, err := NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	writeFakePlugin(t, m.PluginsDir(), "demo", `[ "$1" = "--metadata" ] && echo '{"name":"@demo","version":"1.0.0","description":"d"}'`)

	m.Reload()
	if _, ok := m.GetPlugin("@demo"); ok {
		t.Fatal("a newly seen unsigned plugin loaded while quarantine was on")
	}

	if err := m.Quarantine().Release("demo"); err != nil {
		t.Fatalf("release: %v", err)
	}
	m.Reload()
	if _, ok := m.GetPlugin("@demo"); !ok {
		t.Fatal("a released plugin did not load")
	}
}

// With the gate off, nothing changes for anyone who was already running
// unsigned plugins.
func TestManager_QuarantineOffLoadsImmediately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "")

	m, err := NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	writeFakePlugin(t, m.PluginsDir(), "demo", `[ "$1" = "--metadata" ] && echo '{"name":"@demo","version":"1.0.0","description":"d"}'`)
	m.Reload()
	if _, ok := m.GetPlugin("@demo"); !ok {
		t.Fatal("quarantine being off must leave loading exactly as it was")
	}
}

// An uninstalled plugin must not leave a record that would admit a future
// binary of the same name without its own waiting period.
func TestManager_ForgetsRemovedPlugins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")

	m, err := NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	path := writeFakePlugin(t, m.PluginsDir(), "demo", `echo '{}'`)
	m.Reload()
	if len(m.Quarantine().List()) != 1 {
		t.Fatalf("expected one record, got %v", m.Quarantine().List())
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.Reload()
	if got := m.Quarantine().List(); len(got) != 0 {
		t.Fatalf("record survived removal: %v", got)
	}
}

// The behavior that existed before Inspect split fact from policy: a
// signed plugin on a machine with no trusted keys, where unsigned plugins
// are tolerated, still loads. Adding a .sig file must not be what stops a
// plugin from working.
func TestVerifier_SignedButNoTrustStoreBehavesLikeUnsigned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	keyPath, _, err := GenerateSigningKeyPair(filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	bin := writeFakePlugin(t, t.TempDir(), "demo", "echo hi")
	if _, err := SignPlugin(bin, keyPath, ""); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	if err := NewPluginVerifier().VerifyPlugin(bin); err != nil {
		t.Fatalf("a signed plugin with no trust store stopped loading: %v", err)
	}

	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "false")
	if err := NewPluginVerifier().VerifyPlugin(bin); err == nil {
		t.Fatal("loaded an unverifiable plugin while unsigned plugins are refused")
	}
}

// A signature that fails against keys the machine does trust is evidence of
// a problem, and stays refused whatever the unsigned policy says.
func TestVerifier_BadSignatureStaysRefusedEvenWhenUnsignedIsAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")

	keyPath, pubPath, err := GenerateSigningKeyPair(filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TrustPublicKey(pubPath, "acme"); err != nil {
		t.Fatal(err)
	}

	bin := writeFakePlugin(t, t.TempDir(), "demo", "echo hi")
	if _, err := SignPlugin(bin, keyPath, ""); err != nil {
		t.Fatal(err)
	}
	writeFakePlugin(t, filepath.Dir(bin), "demo", "echo pwned")

	if err := NewPluginVerifier().VerifyPlugin(bin); err == nil {
		t.Fatal("a tampered binary loaded because unsigned plugins are allowed")
	}
}

func TestVerifier_InspectReportsEachStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	keyPath, pubPath, err := GenerateSigningKeyPair(filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := writeFakePlugin(t, dir, "demo", "echo hi")

	if got, _ := NewPluginVerifier().Inspect(bin); got != StatusUnsigned {
		t.Errorf("no signature -> %v, want StatusUnsigned", got)
	}

	if _, err := SignPlugin(bin, keyPath, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := NewPluginVerifier().Inspect(bin); got != StatusUnverifiable {
		t.Errorf("signed, no trust store -> %v, want StatusUnverifiable", got)
	}

	if _, err := TrustPublicKey(pubPath, "acme"); err != nil {
		t.Fatal(err)
	}
	if got, _ := NewPluginVerifier().Inspect(bin); got != StatusVerified {
		t.Errorf("signed and trusted -> %v, want StatusVerified", got)
	}

	if err := os.WriteFile(bin+SignatureSuffix, []byte("bm90LWEtc2lnbmF0dXJl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := NewPluginVerifier().Inspect(bin); got != StatusUntrusted {
		t.Errorf("bad signature -> %v, want StatusUntrusted", got)
	}
}
