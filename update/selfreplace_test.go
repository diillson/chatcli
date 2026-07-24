/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAssetNameFor(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:   "chatcli-linux-amd64",
		{"darwin", "amd64"}:  "chatcli-darwin-amd64",
		{"darwin", "arm64"}:  "chatcli-darwin-arm64",
		{"windows", "amd64"}: "chatcli-windows-amd64.exe",
	}
	for platform, want := range cases {
		got, err := assetNameFor(platform[0], platform[1])
		if err != nil || got != want {
			t.Fatalf("assetNameFor(%s/%s) = %q, %v; esperado %q", platform[0], platform[1], got, err, want)
		}
	}

	_, err := assetNameFor("linux", "arm64")
	var unsupported *UnsupportedPlatformError
	if !errors.As(err, &unsupported) {
		t.Fatalf("plataforma sem asset deve devolver UnsupportedPlatformError, veio %v", err)
	}
}

func TestParseChecksums(t *testing.T) {
	content := "abc123  chatcli-linux-amd64\nDEF456  *chatcli-darwin-arm64\n\nlinha invalida\n"

	if got, err := parseChecksums(content, "chatcli-linux-amd64"); err != nil || got != "abc123" {
		t.Fatalf("parse simples: got %q, %v", got, err)
	}
	// Modo binário do sha256sum prefixa o nome com "*"; o hash normaliza para minúsculas.
	if got, err := parseChecksums(content, "chatcli-darwin-arm64"); err != nil || got != "def456" {
		t.Fatalf("parse com asterisco: got %q, %v", got, err)
	}
	if _, err := parseChecksums(content, "chatcli-windows-amd64.exe"); err == nil {
		t.Fatal("asset ausente do checksums.txt deve ser erro")
	}
}

// newReleaseServer sobe um servidor fake de release e aponta downloadBaseURL
// para ele. assets mapeia nome → conteúdo; checksums é o corpo publicado
// (string vazia = 404).
func newReleaseServer(t *testing.T, tag, checksums string, assets map[string]string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		prefix := "/" + tag + "/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		if name == "checksums.txt" {
			if checksums == "" {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, checksums)
			return
		}
		body, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(mux)
	orig := downloadBaseURL
	downloadBaseURL = srv.URL
	t.Cleanup(func() {
		downloadBaseURL = orig
		srv.Close()
	})
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSelfReplaceEndToEnd(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("plataforma de teste sem asset de release: %v", err)
	}

	const newBinary = "novo binario v2"
	newReleaseServer(t, "v2.0.0",
		fmt.Sprintf("%s  %s\n", sha256Hex(newBinary), asset),
		map[string]string{asset: newBinary})

	dir := t.TempDir()
	target := filepath.Join(dir, "chatcli")
	if err := os.WriteFile(target, []byte("binario antigo v1"), 0o755); err != nil { // #nosec G306
		t.Fatal(err)
	}

	// Tag sem prefixo "v" cobre a normalização.
	if err := SelfReplace(context.Background(), target, "2.0.0"); err != nil {
		t.Fatalf("SelfReplace: %v", err)
	}

	got, err := os.ReadFile(target) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != newBinary {
		t.Fatalf("binário não foi substituído: %q", got)
	}
	// Nenhum staging file pode sobrar.
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".chatcli-update-*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging files órfãos: %v", leftovers)
	}
}

func TestSelfReplaceRejectsChecksumMismatch(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("plataforma de teste sem asset de release: %v", err)
	}

	newReleaseServer(t, "v2.0.0",
		fmt.Sprintf("%s  %s\n", sha256Hex("conteudo esperado"), asset),
		map[string]string{asset: "conteudo adulterado"})

	dir := t.TempDir()
	target := filepath.Join(dir, "chatcli")
	const original = "binario antigo"
	if err := os.WriteFile(target, []byte(original), 0o755); err != nil { // #nosec G306
		t.Fatal(err)
	}

	err = SelfReplace(context.Background(), target, "v2.0.0")
	var mismatch *ChecksumMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("esperado ChecksumMismatchError, veio %v", err)
	}
	// O binário original permanece intacto e o download é descartado.
	got, _ := os.ReadFile(target) // #nosec G304
	if string(got) != original {
		t.Fatal("binário original foi tocado num download inválido")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".chatcli-update-*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging files órfãos após mismatch: %v", leftovers)
	}
}

func TestSelfReplaceRefusesReleaseWithoutChecksums(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("plataforma de teste sem asset de release: %v", err)
	}
	newReleaseServer(t, "v1.0.0", "", map[string]string{asset: "qualquer"})

	target := filepath.Join(t.TempDir(), "chatcli")
	if err := os.WriteFile(target, []byte("antigo"), 0o755); err != nil { // #nosec G306
		t.Fatal(err)
	}

	err = SelfReplace(context.Background(), target, "v1.0.0")
	var unavailable *ChecksumsUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("esperado ChecksumsUnavailableError, veio %v", err)
	}
}

func TestSwapViaOldRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "chatcli.exe")
	staged := filepath.Join(dir, ".chatcli-update-1")
	if err := os.WriteFile(target, []byte("atual"), 0o755); err != nil { // #nosec G306
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("novo"), 0o755); err != nil { // #nosec G306
		t.Fatal(err)
	}

	if err := swapViaOldRename(target, staged); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "novo" { // #nosec G304
		t.Fatalf("target não recebeu o staged: %q", got)
	}
	if got, _ := os.ReadFile(target + ".old"); string(got) != "atual" { // #nosec G304
		t.Fatal("binário anterior deveria estar parkeado em .old")
	}
}

func TestSwapViaOldRenameRollsBackOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "chatcli.exe")
	if err := os.WriteFile(target, []byte("atual"), 0o755); err != nil { // #nosec G306
		t.Fatal(err)
	}

	// staged inexistente força a segunda rename a falhar → rollback.
	if err := swapViaOldRename(target, filepath.Join(dir, "nao-existe")); err == nil {
		t.Fatal("swap com staged inexistente deveria falhar")
	}
	if got, _ := os.ReadFile(target); string(got) != "atual" { // #nosec G304
		t.Fatal("rollback deveria restaurar o binário original")
	}
	if _, err := os.Stat(target + ".old"); err == nil {
		t.Fatal(".old não deveria sobrar após rollback")
	}
}

func TestCleanupStaleArtifacts(t *testing.T) {
	dir := t.TempDir()
	exec := filepath.Join(dir, "chatcli")
	oldFile := exec + ".old"
	stale := filepath.Join(dir, ".chatcli-update-stale")
	fresh := filepath.Join(dir, ".chatcli-update-fresh")
	for _, f := range []string{exec, oldFile, stale, fresh} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil { // #nosec G306
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, past, past); err != nil {
		t.Fatal(err)
	}

	CleanupStaleArtifacts(exec)

	if _, err := os.Stat(oldFile); err == nil {
		t.Fatal(".old deveria ter sido removido")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("staging velho deveria ter sido removido")
	}
	// Staging recente pode pertencer a outro processo no meio de um update.
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("staging recente não pode ser removido")
	}
	if _, err := os.Stat(exec); err != nil {
		t.Fatal("o binário em si nunca pode ser tocado pelo cleanup")
	}
}

func TestManualDownloadURL(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("plataforma de teste sem asset de release: %v", err)
	}
	url, err := ManualDownloadURL("1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	want := downloadBaseURL + "/v1.2.3/" + asset
	if url != want {
		t.Fatalf("ManualDownloadURL = %q, esperado %q", url, want)
	}
}
