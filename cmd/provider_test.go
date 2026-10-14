/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/providerproto/conformance"
)

func buildEchoProvider(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "echo-provider")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-o", bin, "github.com/diillson/chatcli/pkg/providersdk/examples/echo").CombinedOutput()
	if err != nil {
		t.Fatalf("build echo provider: %v\n%s", err, out)
	}
	return bin
}

func TestRunProviderCLIUsageErrors(t *testing.T) {
	i18n.Init()
	ctx := context.Background()
	cases := []struct {
		args []string
		want int
	}{
		{nil, providerExitUsage},
		{[]string{"--help"}, providerExitOK},
		{[]string{"bogus"}, providerExitUsage},
		{[]string{"test"}, providerExitUsage},
		{[]string{"test", "--input", "novalue", "x"}, providerExitUsage},
		{[]string{"test", "--credential-file", filepath.Join(t.TempDir(), "missing.json"), "x"}, providerExitUsage},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		if got := RunProviderCLI(ctx, tc.args, &out, &errb); got != tc.want {
			t.Errorf("RunProviderCLI(%q) = %d, want %d (stderr: %s)", tc.args, got, tc.want, errb.String())
		}
	}
}

func TestRunProviderTestAgainstEcho(t *testing.T) {
	i18n.Init()
	bin := buildEchoProvider(t)

	var out, errb bytes.Buffer
	code := RunProviderCLI(context.Background(), []string{"test", "--json", "--input", "token=demo", bin}, &out, &errb)
	if code != providerExitOK {
		t.Fatalf("exit = %d, stderr: %s\n%s", code, errb.String(), out.String())
	}
	var rep conformance.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json output is not a report: %v\n%s", err, out.String())
	}
	if !rep.Passed() || rep.Plugin == nil || rep.Plugin.Provider.Name != "ECHO" {
		t.Fatalf("report = %+v", rep)
	}

	// A stored credential replaces the login.
	credPath := filepath.Join(t.TempDir(), "cred.json")
	if err := os.WriteFile(credPath, []byte(`{"token":"demo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := RunProviderCLI(context.Background(), []string{"test", "--credential-file", credPath, bin}, &out, &errb); code != providerExitOK {
		t.Fatalf("with credential file: exit = %d\n%s", code, out.String())
	}
}

func TestRunProviderTestMissingBinaryFails(t *testing.T) {
	i18n.Init()
	var out, errb bytes.Buffer
	if code := RunProviderCLI(context.Background(), []string{"test", filepath.Join(t.TempDir(), "nope")}, &out, &errb); code != providerExitFail {
		t.Fatalf("exit = %d, want %d", code, providerExitFail)
	}
}

// installEchoProvider builds the reference plugin into a temporary
// ~/.chatcli/providers and points HOME and the auth store at the sandbox.
func installEchoProvider(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CHATCLI_AUTH_DIR", filepath.Join(home, ".chatcli"))
	t.Setenv("CHATCLI_ALLOW_UNSIGNED_PLUGINS", "true")
	t.Setenv("CHATCLI_PLUGIN_QUARANTINE", "")
	dir := filepath.Join(home, ".chatcli", "providers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := buildEchoProvider(t)
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(bin)), data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestProviderListLoginLogout(t *testing.T) {
	i18n.Init()
	installEchoProvider(t)
	ctx := context.Background()

	var out, errb bytes.Buffer
	if code := RunProviderCLI(ctx, []string{"list"}, &out, &errb); code != providerExitOK {
		t.Fatalf("list exit %d: %s %s", code, out.String(), errb.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("ECHO")) {
		t.Fatalf("list did not show ECHO: %s", out.String())
	}

	out.Reset()
	errb.Reset()
	// Both orders work: the name first (how people type it) and last.
	for _, args := range [][]string{{"login", "echo", "--input", "token=demo"}, {"login", "--input", "token=demo", "echo"}} {
		if code := RunProviderCLI(ctx, args, &out, &errb); code != providerExitOK {
			t.Fatalf("%v exit %d: %s", args, code, errb.String())
		}
	}
	if code := RunProviderCLI(ctx, []string{"login", "echo", "extra"}, &out, &errb); code != providerExitUsage {
		t.Fatalf("login with two names = %d", code)
	}
	out.Reset()
	if code := RunProviderCLI(ctx, []string{"login"}, &out, &errb); code != providerExitUsage {
		t.Fatalf("login without a name = %d", code)
	}
	if code := RunProviderCLI(ctx, []string{"login", "nope"}, &out, &errb); code != providerExitFail {
		t.Fatalf("login to a missing plugin = %d", code)
	}
	if code := RunProviderCLI(ctx, []string{"logout", "echo"}, &out, &errb); code != providerExitOK {
		t.Fatalf("logout exit %d: %s", code, errb.String())
	}
	if code := RunProviderCLI(ctx, []string{"logout"}, &out, &errb); code != providerExitUsage {
		t.Fatalf("logout without a name = %d", code)
	}
}

func TestProviderListEmpty(t *testing.T) {
	i18n.Init()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	var out, errb bytes.Buffer
	if code := RunProviderCLI(context.Background(), []string{"list"}, &out, &errb); code != providerExitOK {
		t.Fatalf("exit %d", code)
	}
}
