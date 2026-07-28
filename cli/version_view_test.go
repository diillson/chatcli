/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
)

// stubInstallChannel fixa o canal detectado sem tocar em brew/docker/fs.
func stubInstallChannel(t *testing.T, method update.Method) {
	t.Helper()
	orig := detectInstallFn
	t.Cleanup(func() { detectInstallFn = orig })
	detectInstallFn = func() update.Info {
		return update.Info{Method: method, ExecPath: "/fake/bin/chatcli"}
	}
}

func TestFormatVersionReportUpdateAvailable(t *testing.T) {
	stubInstallChannel(t, update.MethodHomebrew)

	out := FormatVersionReport(version.Report{
		Current:     version.VersionInfo{Version: "1.5.0", CommitHash: "abc1234", BuildDate: "2026-07-24 00:00:00"},
		Latest:      "1.6.0",
		NeedsUpdate: true,
		Notes:       "### Features\n\n* nova feature de update ([#1242](https://github.com/diillson/chatcli/pull/1242))\n",
		ReleaseURL:  "https://github.com/diillson/chatcli/releases/tag/v1.6.0",
	})

	for _, expect := range []string{
		"v1.5.0",                                // versão instalada
		"abc1234",                               // carimbo de build
		"Homebrew",                              // canal detectado
		"v1.6.0",                                // última release
		"/update",                               // convite ao comando nativo
		"brew upgrade diillson/chatcli/chatcli", // comando do canal
		"nova feature de update (#1242)",        // release notes limpas
		"releases/tag/v1.6.0",                   // link da release
	} {
		if !strings.Contains(out, expect) {
			t.Fatalf("card deve conter %q, saída: %q", expect, out)
		}
	}
}

func TestFormatVersionReportUpToDate(t *testing.T) {
	stubInstallChannel(t, update.MethodGoInstall)

	out := FormatVersionReport(version.Report{
		Current: version.VersionInfo{Version: "1.6.0", CommitHash: "abc1234", BuildDate: "2026-07-24 00:00:00"},
		Latest:  "1.6.0",
	})

	if !strings.Contains(out, "latest version") {
		t.Fatalf("card deve confirmar que está atualizado, saída: %q", out)
	}
	if strings.Contains(out, "/update'") || strings.Contains(out, "What's new") {
		t.Fatalf("sem update não há convite nem novidades, saída: %q", out)
	}
}

func TestFormatVersionReportCheckError(t *testing.T) {
	stubInstallChannel(t, update.MethodReleaseBinary)

	out := FormatVersionReport(version.Report{
		Current:  version.VersionInfo{Version: "1.5.0"},
		CheckErr: errors.New("github fora do ar"),
	})

	if !strings.Contains(out, "github fora do ar") {
		t.Fatalf("erro de checagem deve aparecer no card, saída: %q", out)
	}
}

func TestFormatVersionReportCheckDisabled(t *testing.T) {
	stubInstallChannel(t, update.MethodSourceBuild)

	out := FormatVersionReport(version.Report{
		Current: version.VersionInfo{Version: "dev"},
	})

	if !strings.Contains(out, "CHATCLI_DISABLE_VERSION_CHECK") {
		t.Fatalf("checagem desabilitada deve ser explicada, saída: %q", out)
	}
}
