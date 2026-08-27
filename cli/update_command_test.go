/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
)

// withUpdateSeams stubs the install-channel detection, the update apply and
// the version seams (build info + latest-release fetch) so no test touches
// the network, brew or the go toolchain. HOME is redirected to a temp dir so
// the release cache and staging records land in an isolated ~/.chatcli.
func withUpdateSeams(t *testing.T, method update.Method, current, latest string, applyErr error) *atomic.Int32 {
	t.Helper()

	origDetect, origApply := detectInstallFn, applyUpdateFn
	origBuild, origFetch := version.GetBuildInfoImpl, version.FetchLatestReleaseImpl
	t.Cleanup(func() {
		detectInstallFn, applyUpdateFn = origDetect, origApply
		version.GetBuildInfoImpl, version.FetchLatestReleaseImpl = origBuild, origFetch
	})

	t.Setenv("HOME", t.TempDir())
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "")
	t.Setenv("CHATCLI_AUTO_UPDATE", "")

	detectInstallFn = func() update.Info {
		return update.Info{Method: method, ExecPath: "/fake/bin/chatcli"}
	}
	applied := &atomic.Int32{}
	applyUpdateFn = func(_ context.Context, _ update.Info, _ string, _ update.Options) error {
		applied.Add(1)
		return applyErr
	}
	version.GetBuildInfoImpl = func() (string, string, string) {
		return current, "abc1234", "2026-07-24 00:00:00"
	}
	version.FetchLatestReleaseImpl = func(_ context.Context) (version.ReleaseInfo, error) {
		return version.ReleaseInfo{TagName: "v" + latest, PublishedAt: "2026-07-24T00:00:00Z"}, nil
	}
	return applied
}

func TestHandleUpdateCommandUpToDate(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.5.0", nil)

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

	if !strings.Contains(out, "latest version") {
		t.Fatalf("esperado aviso de já-atualizado, saída: %q", out)
	}
	if applied.Load() != 0 {
		t.Fatal("nada pode ser aplicado quando já está na última versão")
	}
}

func TestHandleUpdateCommandCheckOnly(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update check") })

	if !strings.Contains(out, "/update") || !strings.Contains(out, "1.6.0") {
		t.Fatalf("check deve anunciar a versão nova e apontar o /update, saída: %q", out)
	}
	if applied.Load() != 0 {
		t.Fatal("/update check nunca aplica")
	}
}

func TestHandleUpdateCommandAppliesOnAutomatableChannel(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

	if applied.Load() != 1 {
		t.Fatal("canal go install com versão nova deveria aplicar")
	}
	if !strings.Contains(out, "go install github.com/diillson/chatcli@v1.6.0") {
		t.Fatalf("deve mostrar o comando do canal pinado na versão detectada, saída: %q", out)
	}
	if !strings.Contains(out, "restart") {
		t.Fatalf("sucesso deve pedir restart, saída: %q", out)
	}
}

func TestHandleUpdateCommandSelfReplaceShowsDownload(t *testing.T) {
	if _, err := update.AssetName(); err != nil {
		t.Skipf("plataforma sem asset de release: %v", err)
	}
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodReleaseBinary, "1.5.0", "1.6.0", nil)

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

	if applied.Load() != 1 {
		t.Fatal("binário de release com versão nova deveria aplicar via self-replace")
	}
	if !strings.Contains(out, "Downloading") {
		t.Fatalf("self-replace deve anunciar o download, saída: %q", out)
	}
}

func TestHandleUpdateCommandManualChannels(t *testing.T) {
	cases := []struct {
		method update.Method
		expect string
	}{
		{update.MethodDocker, "docker pull"},
		{update.MethodSourceBuild, "git pull"},
		{update.MethodUnknown, "releases/latest"},
	}
	for _, tc := range cases {
		t.Run(tc.method.String(), func(t *testing.T) {
			cli := minimalCLI(t)
			applied := withUpdateSeams(t, tc.method, "1.5.0", "1.6.0", nil)

			out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

			if applied.Load() != 0 {
				t.Fatalf("canal %s nunca aplica automaticamente", tc.method)
			}
			if !strings.Contains(out, tc.expect) {
				t.Fatalf("canal %s deve instruir update manual com %q, saída: %q", tc.method, tc.expect, out)
			}
		})
	}
}

func TestHandleUpdateCommandReportsCheckFailure(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
	version.FetchLatestReleaseImpl = func(_ context.Context) (version.ReleaseInfo, error) {
		return version.ReleaseInfo{}, errors.New("github fora do ar")
	}

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

	if !strings.Contains(out, "github fora do ar") {
		t.Fatalf("falha de checagem deve ser reportada, saída: %q", out)
	}
}

func TestHandleUpdateCommandWithVersionCheckDisabled(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "true")

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

	if !strings.Contains(out, "CHATCLI_DISABLE_VERSION_CHECK") {
		t.Fatalf("checagem desabilitada deve ser explicada, saída: %q", out)
	}
	if applied.Load() != 0 {
		t.Fatal("nada pode ser aplicado com a checagem desabilitada")
	}
}

func TestPrintUpdateErrorMapsTypedErrors(t *testing.T) {
	cli := minimalCLI(t)
	info := update.Info{Method: update.MethodReleaseBinary, ExecPath: "/usr/local/bin/chatcli"}

	cases := []struct {
		name   string
		err    error
		expect string
	}{
		{"not writable", &update.NotWritableError{Dir: "/usr/local/bin"}, "/usr/local/bin"},
		{"missing tool", &update.MissingToolError{Tool: "brew"}, "brew"},
		{"unsupported", &update.UnsupportedPlatformError{OS: "linux", Arch: "arm64"}, "linux/arm64"},
		{"no checksums", &update.ChecksumsUnavailableError{Tag: "v1.0.0"}, "checksums"},
		{"mismatch", &update.ChecksumMismatchError{Asset: "a", Expected: "x", Actual: "y"}, "Checksum"},
		{"generic", errors.New("boom"), "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() { cli.printUpdateError(tc.err, info, "1.6.0") })
			if !strings.Contains(out, tc.expect) {
				t.Fatalf("erro %s deve mencionar %q, saída: %q", tc.name, tc.expect, out)
			}
		})
	}
}

func TestBackgroundUpdateFlowStagesInAutoMode(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	t.Setenv("CHATCLI_AUTO_UPDATE", "auto")

	cli.backgroundUpdateFlow(context.Background())

	if applied.Load() != 1 {
		t.Fatal("modo auto em canal elegível deveria aplicar em background")
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".chatcli", "cache", "update-staged.json")); err != nil {
		t.Fatal("staging aplicado deve registrar o update-staged.json para o welcome anunciar")
	}
}

func TestBackgroundUpdateFlowRespectsNotifyMode(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	// default: notify — checa e cacheia, mas nunca aplica sozinho.

	cli.backgroundUpdateFlow(context.Background())

	if applied.Load() != 0 {
		t.Fatal("modo notify nunca aplica em background")
	}
}

func TestBackgroundUpdateFlowSkipsHomebrewInAutoMode(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
	t.Setenv("CHATCLI_AUTO_UPDATE", "auto")

	cli.backgroundUpdateFlow(context.Background())

	if applied.Load() != 0 {
		t.Fatal("homebrew nunca é atualizado silenciosamente em background")
	}
}

func TestShowConfigUpdateRenders(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)

	out := captureStdout(t, func() { cli.showConfigUpdate() })

	for _, expect := range []string{"CHATCLI_AUTO_UPDATE", "CHATCLI_DISABLE_VERSION_CHECK", "notify"} {
		if !strings.Contains(out, expect) {
			t.Fatalf("seção deve conter %q, saída: %q", expect, out)
		}
	}
}

func TestWelcomeAnnouncesAvailableUpdate(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	// Popula o cache em disco da release (é o que o welcome lê, sem rede).
	version.RefreshReleaseCacheIfStale(context.Background())

	out := captureStdout(t, func() { cli.PrintWelcomeScreen() })

	if !strings.Contains(out, "1.6.0") || !strings.Contains(out, "/update") {
		t.Fatalf("welcome deve anunciar a release nova e apontar o /update, saída: %q", out)
	}
}

func TestWelcomeAnnouncesStagedUpdateOnce(t *testing.T) {
	cli := minimalCLI(t)
	// Processo já está na versão nova: o staging materializou neste boot.
	withUpdateSeams(t, update.MethodGoInstall, "1.6.0", "1.6.0", nil)
	update.SaveStagedRecord(update.StagedRecord{From: "1.5.0", To: "1.6.0", Method: "go-install"})

	out := captureStdout(t, func() { cli.PrintWelcomeScreen() })
	if !strings.Contains(out, "1.6.0") {
		t.Fatalf("primeiro boot na versão nova deve anunciá-la, saída: %q", out)
	}

	// Segundo boot: o registro foi consumido, nada de anúncio repetido.
	out = captureStdout(t, func() { cli.PrintWelcomeScreen() })
	if strings.Contains(out, "Auto-updated") {
		t.Fatalf("anúncio de staging deve ser único, saída: %q", out)
	}
}
