/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// signedRelease builds a publisher key and a signed bundle (binary, .sig,
// .pub) the way a release would ship it.
func signedRelease(t *testing.T, name string) string {
	t.Helper()
	keys := t.TempDir()
	if _, _, err := plugins.GenerateSigningKeyPair(keys); err != nil {
		t.Fatal(err)
	}
	rel := t.TempDir()
	bin := filepath.Join(rel, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.SignPlugin(bin, filepath.Join(keys, plugins.SigningKeyFileName), bin+".sig"); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(filepath.Join(keys, plugins.SigningPubFileName))
	if err := os.WriteFile(bin+".pub", pub, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestPluginInstallAndTrustList(t *testing.T) {
	i18n.Init()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "")
	bin := signedRelease(t, "acme-tool")

	err := RunPluginCLI([]string{"install", bin})
	if err == nil {
		t.Fatal("unattended install of an unknown publisher must fail")
	}
	fp := regexp.MustCompile(`SHA256:[A-Za-z0-9+/]+`).FindString(err.Error())
	if fp == "" {
		t.Fatalf("the refusal must name the fingerprint: %v", err)
	}
	if err := RunPluginCLI([]string{"install", bin, "--trust", fp}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".chatcli", "plugins", filepath.Base(bin))); err != nil {
		t.Fatalf("plugin not installed: %v", err)
	}
	if err := RunPluginCLI([]string{"trust", "list"}); err != nil {
		t.Fatal(err)
	}
	if err := RunPluginCLI([]string{"install"}); err == nil {
		t.Fatal("install without a source must fail")
	}
}

func TestProviderInstallCommand(t *testing.T) {
	i18n.Init()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bin := signedRelease(t, "acme-provider")

	var out, errb bytes.Buffer
	if code := RunProviderCLI(t.Context(), []string{"install", bin}, &out, &errb); code != providerExitFail {
		t.Fatalf("unattended unknown publisher = %d", code)
	}
	fp := regexp.MustCompile(`SHA256:[A-Za-z0-9+/]+`).FindString(errb.String())
	out.Reset()
	if code := RunProviderCLI(t.Context(), []string{"install", bin, "--trust", fp}, &out, &errb); code != providerExitOK {
		t.Fatalf("install with --trust = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "acme-provider") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".chatcli", "providers", filepath.Base(bin)+".sig")); err != nil {
		t.Fatalf("signature not installed next to the provider: %v", err)
	}
}
