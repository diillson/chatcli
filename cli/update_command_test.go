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
	"time"

	"github.com/diillson/chatcli/i18n"
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
	t.Setenv("CHATCLI_UPDATE_RETRY_DELAY", "0")
	t.Setenv("CHATCLI_UPDATE_RETRIES", "")
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
	// Every attempt failed: each retry was announced and the closing line
	// points at the proxy settings.
	if n := strings.Count(out, i18n.T("update.retry.step.check")); n != update.DefaultRetryAttempts {
		t.Fatalf("want %d retry notices for the check, got %d: %q", update.DefaultRetryAttempts, n, out)
	}
	if !strings.Contains(out, "HTTPS_PROXY") {
		t.Fatalf("a check that failed every attempt must point at the proxy settings: %q", out)
	}
}

// A proxy that drops the first two release checks: the third answers, the
// update goes on, and the user saw both retries.
func TestHandleUpdateCommandRetriesTheReleaseCheck(t *testing.T) {
	cli := minimalCLI(t)
	applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
	calls := 0
	version.FetchLatestReleaseImpl = func(_ context.Context) (version.ReleaseInfo, error) {
		calls++
		if calls < 3 {
			return version.ReleaseInfo{}, errors.New("proxyconnect tcp: connection reset by peer")
		}
		return version.ReleaseInfo{TagName: "v1.6.0", PublishedAt: "2026-07-24T00:00:00Z"}, nil
	}

	out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })

	if calls != 3 || applied.Load() != 1 {
		t.Fatalf("checks=%d applied=%d, want 3 and 1: %q", calls, applied.Load(), out)
	}
	if n := strings.Count(out, "connection reset by peer"); n != 2 {
		t.Fatalf("want 2 retry notices, got %d: %q", n, out)
	}
	if strings.Contains(out, "HTTPS_PROXY") {
		t.Fatalf("a check that recovered must not print the give-up hint: %q", out)
	}
}

// The retries Apply makes reach the user through the notifier /update
// passes, with the step, the attempt count and the pause.
func TestUpdateNotifierPrintsRetries(t *testing.T) {
	out := captureStdout(t, func() {
		updateNotifier{}.NotifyRetry(update.RetryNotice{Step: "download", Attempt: 1, Max: 4, Delay: 5 * time.Second, Err: errors.New("unexpected EOF")})
	})
	for _, want := range []string{i18n.T("update.retry.step.download"), "1", "4", "unexpected EOF", "5s"} {
		if !strings.Contains(out, want) {
			t.Fatalf("retry notice missing %q: %q", want, out)
		}
	}
	var _ update.RetryNotifier = updateNotifier{}
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

// RunUpdateOneShot é a superfície de `chatcli update`: o erro devolvido é o
// contrato de exit code para scripts — nil em "atualizado"/"já na última"/
// check, erro em falha de checagem, canal manual pendente e falha de apply.
func TestRunUpdateOneShotExitContract(t *testing.T) {
	logger := minimalCLI(t).logger

	t.Run("applies and returns nil", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		var err error
		captureStdout(t, func() { err = RunUpdateOneShot(context.Background(), logger, false) })
		if err != nil || applied.Load() != 1 {
			t.Fatalf("esperado apply com sucesso, err=%v applied=%d", err, applied.Load())
		}
	})

	t.Run("up to date returns nil", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.5.0", nil)
		var err error
		captureStdout(t, func() { err = RunUpdateOneShot(context.Background(), logger, false) })
		if err != nil || applied.Load() != 0 {
			t.Fatalf("já atualizado deve ser sucesso sem apply, err=%v applied=%d", err, applied.Load())
		}
	})

	t.Run("check only never applies and returns nil", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		var err error
		captureStdout(t, func() { err = RunUpdateOneShot(context.Background(), logger, true) })
		if err != nil || applied.Load() != 0 {
			t.Fatalf("check não aplica e é sucesso, err=%v applied=%d", err, applied.Load())
		}
	})

	t.Run("manual channel with pending update returns ErrManualMethod", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodDocker, "1.5.0", "1.6.0", nil)
		var err error
		captureStdout(t, func() { err = RunUpdateOneShot(context.Background(), logger, false) })
		if !errors.Is(err, update.ErrManualMethod) || applied.Load() != 0 {
			t.Fatalf("canal manual pendente deve retornar ErrManualMethod, err=%v applied=%d", err, applied.Load())
		}
	})

	t.Run("apply failure bubbles up", func(t *testing.T) {
		wantErr := errors.New("boom")
		withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", wantErr)
		var err error
		captureStdout(t, func() { err = RunUpdateOneShot(context.Background(), logger, false) })
		if !errors.Is(err, wantErr) {
			t.Fatalf("falha do apply deve subir, err=%v", err)
		}
	})

	t.Run("disabled version check returns error", func(t *testing.T) {
		withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "true")
		var err error
		captureStdout(t, func() { err = RunUpdateOneShot(context.Background(), logger, false) })
		if err == nil {
			t.Fatal("checagem desabilitada não tem release para aplicar — deve retornar erro")
		}
	})
}

// RunUpdateSubcommand é o corpo de `chatcli update`: parsing de args e o
// mapeamento erro→exit code (0 ok, 1 falha, 2 uso inválido).
func TestRunUpdateSubcommandExitCodes(t *testing.T) {
	logger := minimalCLI(t).logger

	t.Run("no args applies and exits 0", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		var code int
		captureStdout(t, func() { code = RunUpdateSubcommand(context.Background(), logger, nil) })
		if code != 0 || applied.Load() != 1 {
			t.Fatalf("esperado exit 0 com apply, code=%d applied=%d", code, applied.Load())
		}
	})

	t.Run("check variants never apply and exit 0", func(t *testing.T) {
		for _, arg := range []string{"check", "--check", "-check"} {
			applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
			var code int
			captureStdout(t, func() { code = RunUpdateSubcommand(context.Background(), logger, []string{arg}) })
			if code != 0 || applied.Load() != 0 {
				t.Fatalf("%s: esperado exit 0 sem apply, code=%d applied=%d", arg, code, applied.Load())
			}
		}
	})

	t.Run("apply failure exits 1", func(t *testing.T) {
		withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", errors.New("boom"))
		var code int
		captureStdout(t, func() { code = RunUpdateSubcommand(context.Background(), logger, nil) })
		if code != 1 {
			t.Fatalf("falha do apply deve sair 1, code=%d", code)
		}
	})

	t.Run("invalid arg exits 2 without touching the network", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		code := RunUpdateSubcommand(context.Background(), logger, []string{"bogus"})
		if code != 2 || applied.Load() != 0 {
			t.Fatalf("arg inválido deve sair 2 sem fluxo, code=%d applied=%d", code, applied.Load())
		}
	})
}

// UpdateSubcommandMain cobre o boot completo do subcomando (dotenv, i18n,
// tema, logger, config) e delega ao contrato de exit code — o main só chama
// os.Exit no retorno.
func TestUpdateSubcommandMainBootsAndDelegates(t *testing.T) {
	t.Run("check exits 0 without applying", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		var code int
		captureStdout(t, func() { code = UpdateSubcommandMain([]string{"check"}) })
		if code != 0 || applied.Load() != 0 {
			t.Fatalf("check deve sair 0 sem apply, code=%d applied=%d", code, applied.Load())
		}
	})

	t.Run("invalid arg exits 2", func(t *testing.T) {
		withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		if code := UpdateSubcommandMain([]string{"bogus"}); code != 2 {
			t.Fatalf("arg inválido deve sair 2, code=%d", code)
		}
	})

	t.Run("CHATCLI_DOTENV path is expanded and loaded", func(t *testing.T) {
		applied := withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
		dir := t.TempDir()
		envFile := filepath.Join(dir, "custom.env")
		if err := os.WriteFile(envFile, []byte("CHATCLI_TEST_SENTINEL=loaded\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CHATCLI_DOTENV", envFile)
		// godotenv.Load não sobrescreve env presente (mesmo vazia): o t.Setenv
		// registra o restore e o Unsetenv garante a sentinela ausente de fato.
		t.Setenv("CHATCLI_TEST_SENTINEL", "")
		_ = os.Unsetenv("CHATCLI_TEST_SENTINEL")

		var code int
		captureStdout(t, func() { code = UpdateSubcommandMain([]string{"check"}) })
		if code != 0 || applied.Load() != 0 {
			t.Fatalf("check com dotenv custom deve sair 0, code=%d applied=%d", code, applied.Load())
		}
		if os.Getenv("CHATCLI_TEST_SENTINEL") != "loaded" {
			t.Fatal("dotenv apontado por CHATCLI_DOTENV deve ser carregado no boot")
		}
	})
}

func TestPrintUpdateErrorMapsTypedErrors(t *testing.T) {
	cli := minimalCLI(t)
	info := update.Info{Method: update.MethodReleaseBinary, ExecPath: "/usr/local/bin/chatcli"}
	logger := cli.logger

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
		{"no release keys", update.ErrNoReleaseKeys, "go install github.com/diillson/chatcli@latest"},
		{"no signature", &update.SignatureUnavailableError{Tag: "v1.6.0"}, "checksums.txt.sig"},
		{"bad signature", &update.SignatureInvalidError{Tag: "v1.6.0"}, "release key"},
		{"bad tag", &update.InvalidTagError{Tag: "v9.9.9/../x"}, "v9.9.9/../x"},
		{"generic", errors.New("boom"), "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStdout(t, func() { printUpdateError(logger, tc.err, info, "1.6.0") })
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

// A 404 on the release check is GitHub's answer, not the network's: it is
// shown at once, with no retries and no proxy hint; an intermittent 403 is
// retried and the user sees the real status in every notice.
func TestHandleUpdateCommandCheckStatuses(t *testing.T) {
	t.Run("404 stops at once", func(t *testing.T) {
		cli := minimalCLI(t)
		withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
		calls := 0
		version.FetchLatestReleaseImpl = func(_ context.Context) (version.ReleaseInfo, error) {
			calls++
			return version.ReleaseInfo{}, &version.ReleaseStatusError{StatusCode: 404}
		}
		out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })
		if calls != 1 || !strings.Contains(out, "status 404") || strings.Contains(out, "HTTPS_PROXY") {
			t.Fatalf("calls=%d out=%q", calls, out)
		}
	})
	t.Run("intermittent 403 is retried", func(t *testing.T) {
		cli := minimalCLI(t)
		applied := withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
		calls := 0
		version.FetchLatestReleaseImpl = func(_ context.Context) (version.ReleaseInfo, error) {
			calls++
			if calls == 1 {
				return version.ReleaseInfo{}, &version.ReleaseStatusError{StatusCode: 403}
			}
			return version.ReleaseInfo{TagName: "v1.6.0", PublishedAt: "2026-07-24T00:00:00Z"}, nil
		}
		out := captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })
		if calls != 2 || applied.Load() != 1 || !strings.Contains(out, "status 403") {
			t.Fatalf("calls=%d applied=%d out=%q", calls, applied.Load(), out)
		}
	})
	t.Run("CHATCLI_UPDATE_RETRIES=0 tries once", func(t *testing.T) {
		cli := minimalCLI(t)
		withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)
		t.Setenv("CHATCLI_UPDATE_RETRIES", "0")
		calls := 0
		version.FetchLatestReleaseImpl = func(_ context.Context) (version.ReleaseInfo, error) {
			calls++
			return version.ReleaseInfo{}, errors.New("connection reset by peer")
		}
		_ = captureStdout(t, func() { cli.handleUpdateCommand(context.Background(), "/update") })
		if calls != 1 {
			t.Fatalf("calls=%d, want 1", calls)
		}
	})
}
