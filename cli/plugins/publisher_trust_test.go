/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trustSandbox isolates HOME (user keys, pins, local key) and the managed
// key directory.
func trustSandbox(t *testing.T) (managed string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	managed = filepath.Join(t.TempDir(), "managed-keys")
	orig := managedKeysDir
	managedKeysDir = func() string { return managed }
	t.Cleanup(func() { managedKeysDir = orig })
	return managed
}

// publisher creates a signing key and a signed bundle named name in a
// fresh directory; it returns the bundle path (binary) and the key dir.
func publisher(t *testing.T, name, content string) (bin, keyDir string) {
	t.Helper()
	keyDir = t.TempDir()
	if _, _, err := GenerateSigningKeyPair(keyDir); err != nil {
		t.Fatal(err)
	}
	return signedBundle(t, keyDir, name, content), keyDir
}

func signedBundle(t *testing.T, keyDir, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SignPlugin(bin, filepath.Join(keyDir, SigningKeyFileName), bin+".sig"); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(filepath.Join(keyDir, SigningPubFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".pub", pub, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestTrustOnFirstUseThenPinned(t *testing.T) {
	trustSandbox(t)
	ctx := context.Background()
	dest := t.TempDir()
	bin, keyDir := publisher(t, "acme-tool", "v1")

	asked := 0
	confirm := func(name string, p Publisher) bool {
		asked++
		if name != "acme-tool" || !strings.HasPrefix(p.Fingerprint, "SHA256:") || p.Trusted {
			t.Fatalf("confirm got name=%q publisher=%+v", name, p)
		}
		return true
	}
	res, err := InstallBundle(ctx, bin, InstallOptions{Kind: KindTool, DestDir: dest, Confirm: confirm})
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 || !res.NewlyTrusted || PinnedPublisher(KindTool, "acme-tool") != res.Publisher.Fingerprint {
		t.Fatalf("first install: asked=%d result=%+v", asked, res)
	}
	if err := NewPluginVerifier().VerifyPlugin(res.Path); err != nil {
		t.Fatalf("installed plugin does not verify: %v", err)
	}

	// An update from the same publisher installs without asking.
	v2 := signedBundle(t, keyDir, "acme-tool", "v2")
	if _, err := InstallBundle(ctx, v2, InstallOptions{Kind: KindTool, DestDir: dest, Confirm: confirm}); err != nil || asked != 1 {
		t.Fatalf("update from a trusted publisher: err=%v asked=%d", err, asked)
	}

	// A bundle for the same plugin signed by someone else is refused, even
	// if the person would have said yes.
	evil, _ := publisher(t, "acme-tool", "evil")
	_, err = InstallBundle(ctx, evil, InstallOptions{Kind: KindTool, DestDir: dest, Confirm: func(string, Publisher) bool { return true }})
	if !errors.Is(err, ErrPublisherChanged) {
		t.Fatalf("key change err = %v, want ErrPublisherChanged", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "acme-tool")); string(data) != "v2" {
		t.Fatalf("the refused bundle replaced the installed binary: %q", data)
	}

	// Pins are per kind: a provider plugin with the same file name is a
	// different plugin.
	if PinnedPublisher(KindProvider, "acme-tool") != "" {
		t.Fatal("pins leaked across kinds")
	}
}

func TestUnattendedInstallNeedsTrustFingerprint(t *testing.T) {
	trustSandbox(t)
	ctx := context.Background()
	bin, _ := publisher(t, "acme-provider", "p1")

	_, err := InstallBundle(ctx, bin, InstallOptions{Kind: KindProvider, DestDir: t.TempDir()})
	if !errors.Is(err, ErrPublisherNotTrusted) || !strings.Contains(err.Error(), "--trust SHA256:") {
		t.Fatalf("unattended without --trust = %v", err)
	}
	fp := strings.Fields(err.Error()[strings.Index(err.Error(), "--trust ")+len("--trust "):])[0]

	if _, err := InstallBundle(ctx, bin, InstallOptions{Kind: KindProvider, DestDir: t.TempDir(), Trust: "SHA256:wrong"}); !errors.Is(err, ErrPublisherNotTrusted) {
		t.Fatalf("wrong --trust = %v", err)
	}
	res, err := InstallBundle(ctx, bin, InstallOptions{Kind: KindProvider, DestDir: t.TempDir(), Trust: fp})
	if err != nil || !res.NewlyTrusted {
		t.Fatalf("matching --trust: %+v, %v", res, err)
	}
}

func TestManagedKeysAreTrustedWithoutCopying(t *testing.T) {
	managed := trustSandbox(t)
	bin, keyDir := publisher(t, "corp-provider", "c1")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(filepath.Join(keyDir, SigningPubFileName))
	if err := os.WriteFile(filepath.Join(managed, "corp.pub"), pub, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(bin + ".pub") // the bundle need not carry the key

	res, err := InstallBundle(context.Background(), bin, InstallOptions{Kind: KindProvider, DestDir: t.TempDir()})
	if err != nil || res.NewlyTrusted || !res.Publisher.Trusted {
		t.Fatalf("managed-key install: %+v, %v", res, err)
	}
	if err := NewPluginVerifier().VerifyPlugin(res.Path); err != nil {
		t.Fatalf("verifier ignores managed keys: %v", err)
	}
	var sources []string
	for _, k := range ListTrustedKeys() {
		sources = append(sources, k.Source)
	}
	if strings.Join(sources, ",") != "managed" {
		t.Fatalf("trusted key sources = %v", sources)
	}
}

func TestUnsignedBundlePolicy(t *testing.T) {
	trustSandbox(t)
	bin := filepath.Join(t.TempDir(), "raw-tool")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBundle(context.Background(), bin, InstallOptions{Kind: KindTool, DestDir: t.TempDir()}); !errors.Is(err, ErrPluginNotTrusted) {
		t.Fatalf("unsigned without opt-in = %v", err)
	}
	res, err := InstallBundle(context.Background(), bin, InstallOptions{Kind: KindTool, DestDir: t.TempDir(), AllowUnsigned: true})
	if err != nil || !res.Publisher.Unsigned {
		t.Fatalf("unsigned with opt-in: %+v, %v", res, err)
	}
}

func TestTamperedBundleIsRejected(t *testing.T) {
	trustSandbox(t)
	bin, _ := publisher(t, "acme-tool", "genuine")
	if err := os.WriteFile(bin, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := InstallBundle(context.Background(), bin, InstallOptions{Kind: KindTool, DestDir: t.TempDir(), Confirm: func(string, Publisher) bool { return true }})
	if !errors.Is(err, ErrPluginNotTrusted) {
		t.Fatalf("tampered bundle = %v", err)
	}
}

func TestLocalSigningKeyMakesSourceBuildsVerify(t *testing.T) {
	trustSandbox(t)
	bin := filepath.Join(t.TempDir(), "built-here")
	if err := os.WriteFile(bin, []byte("compiled"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SignWithLocalKey(bin); err != nil {
		t.Fatal(err)
	}
	if err := NewPluginVerifier().VerifyPlugin(bin); err != nil {
		t.Fatalf("locally signed build does not verify: %v", err)
	}
	// Reusing the key keeps it stable.
	k1, _ := EnsureLocalSigningKey()
	k2, _ := EnsureLocalSigningKey()
	if k1 != k2 {
		t.Fatal("local key path changed between calls")
	}
}

func TestInstallBundleOverHTTP(t *testing.T) {
	trustSandbox(t)
	bin, _ := publisher(t, "net-tool", "from-the-network")
	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(bin))))
	defer srv.Close()

	res, err := InstallBundle(context.Background(), srv.URL+"/net-tool", InstallOptions{
		Kind: KindTool, DestDir: t.TempDir(), Confirm: func(string, Publisher) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(res.Path); string(data) != "from-the-network" {
		t.Fatalf("installed content = %q", data)
	}
	if _, err := InstallBundle(context.Background(), srv.URL+"/missing", InstallOptions{Kind: KindTool, DestDir: t.TempDir()}); err == nil {
		t.Fatal("a missing binary must fail")
	}
	if err := fetch(context.Background(), "http://example.com/x", filepath.Join(t.TempDir(), "x"), true); err == nil {
		t.Fatal("plain http to a remote host must be refused")
	}
}

func TestBundleName(t *testing.T) {
	cases := map[string]string{
		"/tmp/x/acme-tool":                       "acme-tool",
		"https://example.com/releases/acme-tool": "acme-tool",
		"https://example.com/":                   "",
		"/tmp/.hidden":                           "",
		"/tmp/bad name":                          "",
	}
	for in, want := range cases {
		got := bundleName(in)
		if want != "" && strings.TrimSuffix(got, ".exe") != want || want == "" && got != "" {
			t.Errorf("bundleName(%q) = %q, want %q", in, got, want)
		}
	}
}
