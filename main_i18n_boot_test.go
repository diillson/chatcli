package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadDotenvThenI18n_ReadsLangFromDotenv proves the bootstrap order the
// Windows bug report hinged on: a CHATCLI_LANG that lives ONLY in the dotenv
// file must be in the process environment by the time i18n.Init latches the
// language. We assert the observable half of that contract — after the
// bootstrap the variable is loaded — since the once-guard may already have
// fired in this test binary.
func TestLoadDotenvThenI18n_ReadsLangFromDotenv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "boot.env")
	if err := os.WriteFile(envFile, []byte("CHATCLI_LANG=pt-BR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHATCLI_DOTENV", envFile)
	t.Setenv("CHATCLI_LANG", "")
	_ = os.Unsetenv("CHATCLI_LANG")

	boot := loadDotenvThenI18n()

	if boot.path != envFile {
		t.Fatalf("path = %q, want %q", boot.path, envFile)
	}
	if boot.loadErr != nil {
		t.Fatalf("loadErr = %v", boot.loadErr)
	}
	if got := os.Getenv("CHATCLI_LANG"); got != "pt-BR" {
		t.Fatalf("CHATCLI_LANG = %q after bootstrap, want pt-BR (must load BEFORE i18n.Init)", got)
	}
}

// TestLoadDotenvThenI18n_MissingFileIsNotFatal covers the default ".env"
// shape: a missing file surfaces as os.IsNotExist, which entrypoints ignore.
func TestLoadDotenvThenI18n_MissingFileIsNotFatal(t *testing.T) {
	t.Setenv("CHATCLI_DOTENV", filepath.Join(t.TempDir(), "nope.env"))
	boot := loadDotenvThenI18n()
	if boot.loadErr == nil || !os.IsNotExist(boot.loadErr) {
		t.Fatalf("loadErr = %v, want IsNotExist", boot.loadErr)
	}
	if boot.expandErr != nil {
		t.Fatalf("expandErr = %v", boot.expandErr)
	}
}

// TestReportDotenvBootstrapBranches drives both warning branches through the
// reporter so the deferred-warning contract stays covered.
func TestReportDotenvBootstrapBranches(t *testing.T) {
	reportDotenvBootstrap(dotenvBootstrap{path: "x.env"})                            // silent
	reportDotenvBootstrap(dotenvBootstrap{path: "x.env", expandErr: os.ErrInvalid})  // expand warning
	reportDotenvBootstrap(dotenvBootstrap{path: "x.env", loadErr: os.ErrPermission}) // load warning
	reportDotenvBootstrap(dotenvBootstrap{path: "x.env", loadErr: os.ErrNotExist})   // missing file: silent
}
