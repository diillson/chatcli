/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

// withTempPluginHome points every path the plugin commands touch at a temp
// HOME, so a test never writes into the developer's real trust store.
func withTempPluginHome(t *testing.T) string {
	t.Helper()
	i18n.Init()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeExecutable(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	return path
}

// The walkthrough the documentation described, driven through the real
// command surface rather than the library underneath it.
func TestRunPluginCLI_KeygenSignTrustVerify(t *testing.T) {
	home := withTempPluginHome(t)
	keyDir := filepath.Join(home, "keys")

	if err := RunPluginCLI([]string{"keygen", "--output", keyDir}); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keyPath := filepath.Join(keyDir, "plugin-signing.key")
	pubPath := filepath.Join(keyDir, "plugin-signing.pub")
	for _, p := range []string{keyPath, pubPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("keygen did not write %s: %v", p, err)
		}
	}
	if info, err := os.Stat(keyPath); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("private key mode = %v, want 0600", info.Mode().Perm())
	}

	bin := writeExecutable(t, filepath.Join(home, "demo"), "echo hi")

	// Unsigned: verify has to say which of the failures happened.
	err := RunPluginCLI([]string{"verify", "--binary", bin})
	if err == nil || !strings.Contains(err.Error(), "assinatura") && !strings.Contains(err.Error(), "signature") {
		t.Fatalf("verify on an unsigned binary: %v", err)
	}

	if err := RunPluginCLI([]string{"sign", "--binary", bin, "--key", keyPath}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := os.Stat(bin + ".sig"); err != nil {
		t.Fatalf("signature not written: %v", err)
	}

	// Signed but not trusted is still not verified.
	if err := RunPluginCLI([]string{"verify", "--binary", bin}); err == nil {
		t.Fatal("verified with an empty trust store")
	}

	if err := RunPluginCLI([]string{"trust", "--key", pubPath, "--name", "acme"}); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".chatcli", "trusted-keys", "acme.pub")); err != nil {
		t.Fatalf("trusted key not registered: %v", err)
	}

	if err := RunPluginCLI([]string{"verify", "--binary", bin}); err != nil {
		t.Fatalf("signed and trusted binary failed to verify: %v", err)
	}
}

func TestRunPluginCLI_SignToExplicitOutput(t *testing.T) {
	home := withTempPluginHome(t)
	if err := RunPluginCLI([]string{"keygen", "--output", filepath.Join(home, "keys")}); err != nil {
		t.Fatal(err)
	}
	bin := writeExecutable(t, filepath.Join(home, "demo"), "echo hi")
	out := filepath.Join(home, "elsewhere.sig")

	if err := RunPluginCLI([]string{
		"sign", "--binary", bin,
		"--key", filepath.Join(home, "keys", "plugin-signing.key"),
		"--output", out,
	}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("--output ignored: %v", err)
	}
}

// keygen with no --output must still land somewhere predictable rather than
// failing, because that is the first command anyone runs.
func TestRunPluginCLI_KeygenDefaultsUnderHome(t *testing.T) {
	home := withTempPluginHome(t)
	if err := RunPluginCLI([]string{"keygen"}); err != nil {
		t.Fatalf("keygen with no --output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".chatcli", "plugin-keys", "plugin-signing.key")); err != nil {
		t.Fatalf("default key location not used: %v", err)
	}
}

func TestRunPluginCLI_MissingFlagsAreRefused(t *testing.T) {
	withTempPluginHome(t)
	cases := [][]string{
		{"sign"},
		{"sign", "--binary", "x"},
		{"sign", "--key", "k"},
		{"verify"},
		{"trust"},
	}
	for _, args := range cases {
		if err := RunPluginCLI(args); err == nil {
			t.Errorf("%v was accepted with a required flag missing", args)
		}
	}
}

func TestRunPluginCLI_UnknownVerbAndHelp(t *testing.T) {
	withTempPluginHome(t)
	if err := RunPluginCLI([]string{"nonsense"}); err == nil {
		t.Error("an unknown subcommand was accepted")
	}
	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"-h"}} {
		if err := RunPluginCLI(args); err != nil {
			t.Errorf("%v should print usage, got %v", args, err)
		}
	}
}

func TestRunPluginCLI_Quarantine(t *testing.T) {
	home := withTempPluginHome(t)
	pluginsDir := filepath.Join(home, ".chatcli", "plugins")
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Off by default: listing says so instead of pretending to be empty.
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "")
	if err := RunPluginCLI([]string{"quarantine"}); err != nil {
		t.Fatalf("quarantine list with the gate off: %v", err)
	}

	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "24h")
	if err := RunPluginCLI([]string{"quarantine", "list"}); err != nil {
		t.Fatalf("quarantine list: %v", err)
	}
	if err := RunPluginCLI([]string{"quarantine", "release"}); err == nil {
		t.Error("release with no name was accepted")
	}
	if err := RunPluginCLI([]string{"quarantine", "release", "not-there"}); err == nil {
		t.Error("released a plugin that was never quarantined")
	}
	if err := RunPluginCLI([]string{"quarantine", "nonsense"}); err == nil {
		t.Error("an unknown quarantine subcommand was accepted")
	}
}

func TestRunPluginCLI_SignFailsOnBadKeyMaterial(t *testing.T) {
	home := withTempPluginHome(t)
	bin := writeExecutable(t, filepath.Join(home, "demo"), "echo hi")

	junk := filepath.Join(home, "junk.key")
	if err := os.WriteFile(junk, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunPluginCLI([]string{"sign", "--binary", bin, "--key", junk}); err == nil {
		t.Error("signed with a key file that holds no key")
	}
	if err := RunPluginCLI([]string{"sign", "--binary", filepath.Join(home, "nope"), "--key", junk}); err == nil {
		t.Error("signed a binary that does not exist")
	}
	if err := RunPluginCLI([]string{"trust", "--key", junk}); err == nil {
		t.Error("trusted a file that holds no public key")
	}
}
