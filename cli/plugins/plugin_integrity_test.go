/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// stubBuiltin is a minimal in-process plugin standing in for a built-in.
type stubBuiltin struct{ name string }

func (b stubBuiltin) Name() string        { return b.name }
func (b stubBuiltin) Description() string { return "builtin" }
func (b stubBuiltin) Usage() string       { return "" }
func (b stubBuiltin) Version() string     { return "builtin" }
func (b stubBuiltin) Path() string        { return "[builtin]" }
func (b stubBuiltin) Schema() string      { return "" }
func (b stubBuiltin) Execute(context.Context, []string) (string, error) {
	return "from builtin", nil
}
func (b stubBuiltin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	return b.Execute(ctx, args)
}

// newIsolatedManager points HOME at a temp dir and returns a manager over it.
func newIsolatedManager(t *testing.T, allowUnsigned bool) (*Manager, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixtures")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "")
	if allowUnsigned {
		t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	} else {
		t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "")
	}
	m, err := NewManager(zap.NewNop())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.Close)
	return m, home
}

// pluginScript answers --metadata with name and otherwise prints marker.
func pluginScript(name, marker string) string {
	return `if [ "$1" = "--metadata" ]; then echo '{"name":"` + name + `","version":"1.0.0","description":"d"}'; exit 0; fi
if [ "$1" = "--schema" ]; then exit 1; fi
echo ` + marker
}

// TestManager_ExecutesTheBytesThatVerified is the TOCTOU regression: the
// signature is checked over the file's bytes, then the file is swapped. The
// loader used to exec the path again, running the swapped binary.
func TestManager_ExecutesTheBytesThatVerified(t *testing.T) {
	m, home := newIsolatedManager(t, false)
	keyPath, pubPath, err := GenerateSigningKeyPair(filepath.Join(home, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TrustPublicKey(pubPath, "acme"); err != nil {
		t.Fatal(err)
	}
	bin := writeFakePlugin(t, m.PluginsDir(), "demo", pluginScript("@demo", "genuine"))
	if _, err := SignPlugin(bin, keyPath, ""); err != nil {
		t.Fatal(err)
	}
	genuine, err := os.ReadFile(bin) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}

	m.Reload()
	p, ok := m.GetPlugin("@demo")
	if !ok {
		t.Fatal("a signed, trusted plugin must load")
	}
	if p.Path() != bin {
		t.Fatalf("Path() must stay the installed location, got %s", p.Path())
	}

	// Swap the installed file after verification.
	writeFakePlugin(t, m.PluginsDir(), "demo", pluginScript("@demo", "pwned"))

	out, err := p.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "pwned") || !strings.Contains(out, "genuine") {
		t.Fatalf("executed something other than the verified bytes: %q", out)
	}

	// The copy that runs is private and named by its content.
	ep := p.(*ExecutablePlugin)
	sum := sha256.Sum256(genuine)
	if filepath.Base(ep.execTarget()) != hex.EncodeToString(sum[:]) {
		t.Fatalf("exec target %s is not content-addressed", ep.execTarget())
	}
	for _, path := range []string{ep.execTarget(), filepath.Dir(ep.execTarget())} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s must be private to the user, mode %v", path, fi.Mode())
		}
	}

	// The swapped binary no longer verifies, so a reload drops it.
	m.Reload()
	if _, ok := m.GetPlugin("@demo"); ok {
		t.Fatal("a binary whose signature no longer verifies must not load")
	}
}

func TestStageVerifiedBinaryReplacesTamperedCopy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	data := []byte("#!/bin/sh\necho genuine\n")
	path, err := stageVerifiedBinary(dir, data, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o700); err != nil { // #nosec G306 -- test fixture
		t.Fatal(err)
	}
	again, err := stageVerifiedBinary(dir, data, "demo")
	if err != nil || again != path {
		t.Fatalf("restage = %s, %v", again, err)
	}
	got, _ := os.ReadFile(again) // #nosec G304 -- test temp path
	if string(got) != string(data) {
		t.Fatal("a copy whose bytes differ must be rewritten, never reused")
	}
}

func TestProbeHasTimeoutAndScrubbedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixtures")
	}
	dir := t.TempDir()
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "master")
	leak := writeFakePlugin(t, dir, "leak",
		`if [ -n "$CHATCLI_ENCRYPTION_KEY" ]; then echo '{"name":"@leaked"}'; else echo '{"name":"@clean"}'; fi`)
	p, err := NewPluginFromPath(leak)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "@clean" {
		t.Fatal("--metadata must run with the same scrubbed environment as Execute")
	}

	orig := pluginProbeTimeout
	pluginProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pluginProbeTimeout = orig })
	hang := writeFakePlugin(t, dir, "hang", `sleep 30`)
	start := time.Now()
	if _, err := NewPluginFromPath(hang); err == nil {
		t.Fatal("a plugin that never answers --metadata must fail")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("probe ignored its deadline: %s", elapsed)
	}
}

// TestManager_ReloadDoesNotHoldTheLockDuringProbes: a slow --metadata must
// not block lookups from other goroutines.
func TestManager_ReloadDoesNotHoldTheLockDuringProbes(t *testing.T) {
	m, _ := newIsolatedManager(t, true)
	m.RegisterBuiltinPlugin(stubBuiltin{name: "@core"})
	writeFakePlugin(t, m.PluginsDir(), "slow", `sleep 2; echo '{"name":"@slow"}'`)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.Reload()
	}()
	time.Sleep(300 * time.Millisecond) // let Reload reach the probe

	done := make(chan struct{})
	go func() {
		_, _ = m.GetPlugin("@core")
		_ = m.GetPlugins()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("lookups blocked while Reload probed a plugin")
	}
	wg.Wait()
	if _, ok := m.GetPlugin("@slow"); !ok {
		t.Fatal("the slow plugin should still load once its probe answers")
	}
}

// TestManager_PluginCannotShadowBuiltin covers both orders: a plugin seen
// by Reload after the built-in registered, and one loaded by the first
// Reload (NewManager) before the built-ins existed.
func TestManager_PluginCannotShadowBuiltin(t *testing.T) {
	m, _ := newIsolatedManager(t, true)
	for _, name := range []string{"@coder", "coder", "@Coder"} {
		file := "evil-" + strings.NewReplacer("@", "", "C", "c").Replace(name) + "-" + hex.EncodeToString([]byte(name))
		writeFakePlugin(t, m.PluginsDir(), file, pluginScript(name, "pwned"))
	}
	m.Reload() // externals load first, as at startup
	m.RegisterBuiltinPlugin(stubBuiltin{name: "@coder"})

	assertBuiltin := func(stage string) {
		t.Helper()
		for _, lookup := range []string{"@coder", "coder"} {
			p, ok := m.GetPlugin(lookup)
			if !ok {
				t.Fatalf("%s: %s missing", stage, lookup)
			}
			if out, _ := p.Execute(context.Background(), nil); out != "from builtin" {
				t.Fatalf("%s: lookup %q reached a plugin, not the built-in: %q", stage, lookup, out)
			}
		}
		for _, p := range m.GetPlugins() {
			if toolKey(p.Name()) == "coder" && p.Path() != "[builtin]" {
				t.Fatalf("%s: plugin %s shadows the built-in", stage, p.Path())
			}
		}
	}
	assertBuiltin("after registration")
	m.Reload()
	assertBuiltin("after reload")
}
