/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

func TestInstallErrorMessageKeepsTheFacts(t *testing.T) {
	i18n.Init()
	cases := []*plugins.PublisherError{
		{Err: plugins.ErrPluginNotTrusted, Reason: plugins.ReasonUnsigned, Plugin: "p"},
		{Err: plugins.ErrPluginNotTrusted, Reason: plugins.ReasonBadSignature, Plugin: "p"},
		{Err: plugins.ErrPublisherChanged, Reason: plugins.ReasonKeyChanged, Plugin: "p", Pinned: "SHA256:old", Fingerprint: "SHA256:new"},
		{Err: plugins.ErrPublisherNotTrusted, Reason: plugins.ReasonTrustMismatch, Plugin: "p", Given: "SHA256:given", Fingerprint: "SHA256:new"},
		{Err: plugins.ErrPublisherNotTrusted, Reason: plugins.ReasonNoBundledKey, Plugin: "p"},
		{Err: plugins.ErrPublisherNotTrusted, Reason: plugins.ReasonNotTrusted, Plugin: "p", Fingerprint: "SHA256:new"},
	}
	for _, pe := range cases {
		msg := InstallErrorMessage(pe)
		if strings.HasPrefix(msg, "plugin.install.") {
			t.Errorf("%s: untranslated key %q", pe.Reason, msg)
		}
		for _, fact := range []string{pe.Pinned, pe.Fingerprint, pe.Given} {
			if fact != "" && !strings.Contains(msg, fact) {
				t.Errorf("%s: message %q lost %q", pe.Reason, msg, fact)
			}
		}
	}
	if msg := InstallErrorMessage(errors.New("disk full")); !strings.Contains(msg, "disk full") {
		t.Errorf("generic error lost its cause: %q", msg)
	}
}

func TestBundleSourceDetection(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		args []string
		want string
		ok   bool
	}{
		{[]string{bin}, bin, true},
		{[]string{"--binary", "https://example.com/tool"}, "https://example.com/tool", true},
		{[]string{"https://github.com/acme/tool"}, "", false},
		{[]string{filepath.Join(t.TempDir(), "absent")}, "", false},
	}
	for _, tc := range cases {
		got, ok := bundleSource(tc.args)
		if got != tc.want || ok != tc.ok {
			t.Errorf("bundleSource(%v) = %q,%v want %q,%v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPrintInstallResult(t *testing.T) {
	i18n.Init()
	var b bytes.Buffer
	PrintInstallResult(&b, plugins.InstallResult{Path: "/x/tool", NewlyTrusted: true, Publisher: plugins.Publisher{Fingerprint: "SHA256:abc"}})
	PrintInstallResult(&b, plugins.InstallResult{Path: "/x/tool", Publisher: plugins.Publisher{Fingerprint: "SHA256:abc", Trusted: true}})
	PrintInstallResult(&b, plugins.InstallResult{Path: "/x/raw", Publisher: plugins.Publisher{Unsigned: true}})
	if !strings.Contains(b.String(), "SHA256:abc") || !strings.Contains(b.String(), "/x/raw") {
		t.Fatalf("output = %q", b.String())
	}
	if PublisherConfirm() != nil {
		t.Fatal("go test has no terminal: no interactive confirmation")
	}
}
