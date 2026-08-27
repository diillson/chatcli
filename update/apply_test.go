/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestResolveMode(t *testing.T) {
	cases := []struct {
		autoUpdate   string
		disableCheck string
		want         Mode
	}{
		{"", "", ModeNotify},
		{"notify", "", ModeNotify},
		{"AUTO", "", ModeAuto},
		{"off", "", ModeOff},
		{"never", "", ModeOff},
		{"false", "", ModeOff},
		{"qualquer-coisa", "", ModeNotify},
		// Checagem de versão desligada implica updater desligado, mesmo com auto.
		{"auto", "true", ModeOff},
	}
	for _, tc := range cases {
		t.Setenv("CHATCLI_AUTO_UPDATE", tc.autoUpdate)
		t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", tc.disableCheck)
		if got := ResolveMode(); got != tc.want {
			t.Fatalf("ResolveMode(auto=%q, disable=%q) = %s, esperado %s",
				tc.autoUpdate, tc.disableCheck, got, tc.want)
		}
	}
}

func TestCommandFor(t *testing.T) {
	if got := strings.Join(CommandFor(MethodHomebrew), " "); got != "brew upgrade diillson/chatcli/chatcli" {
		t.Fatalf("comando brew inesperado: %q", got)
	}
	if got := strings.Join(CommandFor(MethodGoInstall), " "); got != "go install github.com/diillson/chatcli@latest" {
		t.Fatalf("comando go install inesperado: %q", got)
	}
	for _, m := range []Method{MethodReleaseBinary, MethodDocker, MethodSourceBuild, MethodUnknown} {
		if CommandFor(m) != nil {
			t.Fatalf("método %s não usa comando externo", m)
		}
	}
}

// O go install deve instalar exatamente a versão que a checagem anunciou
// (@vX.Y.Z), nunca delegar ao @latest do module proxy — que pode estar
// atrás do GitHub. Tag ausente ou malformada cai no @latest.
func TestGoInstallArgsVersionPinsDetectedTag(t *testing.T) {
	cases := []struct {
		tag  string
		want string
	}{
		{"1.6.0", "go install github.com/diillson/chatcli@v1.6.0"},
		{"v1.6.0", "go install github.com/diillson/chatcli@v1.6.0"},
		{"1.6.0-rc.1", "go install github.com/diillson/chatcli@v1.6.0-rc.1"},
		{"", "go install github.com/diillson/chatcli@latest"},
		{"latest", "go install github.com/diillson/chatcli@latest"},
		{"1.6", "go install github.com/diillson/chatcli@latest"},
		{"1.6.0 --flag", "go install github.com/diillson/chatcli@latest"},
	}
	for _, tc := range cases {
		if got := strings.Join(GoInstallArgsVersion(tc.tag), " "); got != tc.want {
			t.Fatalf("GoInstallArgsVersion(%q) = %q, esperado %q", tc.tag, got, tc.want)
		}
	}
}

func TestCommandForVersion(t *testing.T) {
	if got := strings.Join(CommandForVersion(MethodGoInstall, "1.6.0"), " "); got != "go install github.com/diillson/chatcli@v1.6.0" {
		t.Fatalf("comando go install pinado inesperado: %q", got)
	}
	// Brew não aceita pin por versão: segue o comando canônico do canal.
	if got := strings.Join(CommandForVersion(MethodHomebrew, "1.6.0"), " "); got != "brew upgrade diillson/chatcli/chatcli" {
		t.Fatalf("comando brew inesperado: %q", got)
	}
	if CommandForVersion(MethodReleaseBinary, "1.6.0") != nil {
		t.Fatal("release binary não usa comando externo")
	}
}

func TestApplyManualMethodsReturnErrManualMethod(t *testing.T) {
	for _, m := range []Method{MethodDocker, MethodSourceBuild, MethodUnknown} {
		err := Apply(context.Background(), Info{Method: m}, "v1.0.0", Options{})
		if !errors.Is(err, ErrManualMethod) {
			t.Fatalf("Apply(%s) = %v, esperado ErrManualMethod", m, err)
		}
	}
}

func TestApplyRunsChannelTool(t *testing.T) {
	origRun, origLook := runCommandFn, lookPathFn
	t.Cleanup(func() { runCommandFn, lookPathFn = origRun, origLook })

	var gotArgv []string
	lookPathFn = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	runCommandFn = func(_ context.Context, _ Options, name string, args ...string) error {
		gotArgv = append([]string{name}, args...)
		return nil
	}

	if err := Apply(context.Background(), Info{Method: MethodGoInstall}, "v1.0.0", Options{}); err != nil {
		t.Fatalf("Apply(go install): %v", err)
	}
	if strings.Join(gotArgv, " ") != "go install github.com/diillson/chatcli@v1.0.0" {
		t.Fatalf("argv executado: %v", gotArgv)
	}
}

func TestApplyReportsMissingTool(t *testing.T) {
	origLook := lookPathFn
	t.Cleanup(func() { lookPathFn = origLook })
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }

	err := Apply(context.Background(), Info{Method: MethodHomebrew}, "v1.0.0", Options{})
	var missing *MissingToolError
	if !errors.As(err, &missing) || missing.Tool != "brew" {
		t.Fatalf("esperado MissingToolError{brew}, veio %v", err)
	}
}

func withTempCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := cacheDirFn
	cacheDirFn = func() (string, error) { return dir, nil }
	t.Cleanup(func() { cacheDirFn = orig })
	return dir
}

func TestStagedRecordLifecycle(t *testing.T) {
	withTempCacheDir(t)

	rec := StagedRecord{From: "1.0.0", To: "1.1.0", Method: "release-binary", StagedAt: time.Now().UTC()}
	SaveStagedRecord(rec)

	// Boot ainda na versão antiga: o staging não materializou, registro fica.
	if _, ok := ConsumeStagedRecord("1.0.0"); ok {
		t.Fatal("registro não pode ser consumido antes da versão nova assumir")
	}
	// Primeiro boot na versão nova: consome e anuncia.
	got, ok := ConsumeStagedRecord("v1.1.0")
	if !ok || got.To != "1.1.0" {
		t.Fatalf("consumo esperado na versão nova; ok=%v rec=%+v", ok, got)
	}
	// Consumo é único.
	if _, ok := ConsumeStagedRecord("v1.1.0"); ok {
		t.Fatal("registro consumido não pode reaparecer")
	}
}

func TestStagedRecordExpires(t *testing.T) {
	dir := withTempCacheDir(t)

	SaveStagedRecord(StagedRecord{
		From: "1.0.0", To: "1.1.0", Method: "go-install",
		StagedAt: time.Now().UTC().Add(-8 * 24 * time.Hour),
	})
	if _, ok := ConsumeStagedRecord("1.0.0"); ok {
		t.Fatal("registro vencido não pode ser consumido")
	}
	if _, err := os.Stat(dir + "/update-staged.json"); err == nil {
		t.Fatal("registro vencido deveria ter sido removido")
	}
}

func TestTryAcquireAutoLock(t *testing.T) {
	withTempCacheDir(t)

	release, ok := TryAcquireAutoLock()
	if !ok {
		t.Fatal("primeiro lock deveria ser adquirido")
	}
	if _, ok := TryAcquireAutoLock(); ok {
		t.Fatal("lock concorrente deveria falhar")
	}
	release()
	release2, ok := TryAcquireAutoLock()
	if !ok {
		t.Fatal("lock deveria ser readquirível após release")
	}
	release2()
}

func TestTryAcquireAutoLockStealsStaleLock(t *testing.T) {
	dir := withTempCacheDir(t)

	lockPath := dir + "/update.lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, past, past); err != nil {
		t.Fatal(err)
	}

	release, ok := TryAcquireAutoLock()
	if !ok {
		t.Fatal("lock órfão vencido deveria ser roubado")
	}
	release()
}
