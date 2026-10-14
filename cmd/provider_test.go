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
