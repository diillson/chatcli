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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// downloadBaseURL é a raiz dos assets de release; var (não const) para os
// testes apontarem para um httptest.Server.
var downloadBaseURL = "https://github.com/diillson/chatcli/releases/download"

const (
	downloadTimeout = 5 * time.Minute
	// maxAssetSize é um teto de sanidade contra resposta malformada — os
	// binários reais ficam na casa de dezenas de MB.
	maxAssetSize = 512 << 20
	updaterUA    = "ChatCLI-Updater"
)

// AssetName resolve o nome do asset publicado na release para a plataforma
// atual. As quatro plataformas abaixo são exatamente as que o workflow de
// release compila e sobe.
func AssetName() (string, error) {
	return assetNameFor(runtime.GOOS, runtime.GOARCH)
}

func assetNameFor(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "linux/amd64":
		return "chatcli-linux-amd64", nil
	case "darwin/amd64":
		return "chatcli-darwin-amd64", nil
	case "darwin/arm64":
		return "chatcli-darwin-arm64", nil
	case "windows/amd64":
		return "chatcli-windows-amd64.exe", nil
	}
	return "", &UnsupportedPlatformError{OS: goos, Arch: goarch}
}

// SelfReplace baixa o asset da release tag para a plataforma atual, valida o
// SHA-256 contra o checksums.txt publicado e troca o binário em execPath de
// forma atômica. Em Unix o rename por cima é suficiente (o processo em
// execução segue no inode antigo); em Windows o binário atual é renomeado
// para .old antes — CleanupStaleArtifacts remove o resto no próximo boot.
// Sem checksums.txt na release (releases antigas) a instalação é recusada.
func SelfReplace(ctx context.Context, execPath, tag string) error {
	if execPath == "" {
		return fmt.Errorf("update: executable path unresolved")
	}
	asset, err := AssetName()
	if err != nil {
		return err
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}

	client := &http.Client{Timeout: downloadTimeout}

	expected, err := fetchExpectedChecksum(ctx, client, tag, asset)
	if err != nil {
		return err
	}

	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, ".chatcli-update-*")
	if err != nil {
		if os.IsPermission(err) {
			return &NotWritableError{Dir: dir}
		}
		return fmt.Errorf("update: creating staging file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	actual, err := downloadWithChecksum(ctx, client, tag, asset, tmp)
	if err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("update: finishing staging file: %w", err)
	}
	if actual != expected {
		_ = os.Remove(tmpName)
		return &ChecksumMismatchError{Asset: asset, Expected: expected, Actual: actual}
	}

	if err := os.Chmod(tmpName, 0o755); err != nil { // #nosec G302 -- binário executável
		_ = os.Remove(tmpName)
		return fmt.Errorf("update: marking staged binary executable: %w", err)
	}
	if err := replaceBinary(execPath, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// fetchExpectedChecksum baixa o checksums.txt da release e extrai o hash do
// asset desta plataforma. 404 vira ChecksumsUnavailableError — release
// anterior à publicação de checksums, sem instalação não verificada.
func fetchExpectedChecksum(ctx context.Context, client *http.Client, tag, asset string) (string, error) {
	body, status, err := httpGet(ctx, client, releaseAssetURL(tag, "checksums.txt"))
	if err != nil {
		return "", fmt.Errorf("update: fetching checksums.txt: %w", err)
	}
	if status == http.StatusNotFound {
		return "", &ChecksumsUnavailableError{Tag: tag}
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("update: fetching checksums.txt: status %d", status)
	}
	return parseChecksums(string(body), asset)
}

// downloadWithChecksum baixa o asset direto para dst calculando o SHA-256 em
// streaming, sem segurar o binário inteiro em memória.
func downloadWithChecksum(ctx context.Context, client *http.Client, tag, asset string, dst io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAssetURL(tag, asset), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", updaterUA)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: downloading %s: %w", asset, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: downloading %s: status %d", asset, resp.StatusCode)
	}

	hasher := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hasher), io.LimitReader(resp.Body, maxAssetSize+1))
	if err != nil {
		return "", fmt.Errorf("update: downloading %s: %w", asset, err)
	}
	if n > maxAssetSize {
		return "", fmt.Errorf("update: asset %s exceeds size limit", asset)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// httpGet executa um GET simples devolvendo corpo e status; 404 não é erro
// (o chamador decide), demais falhas de transporte são.
func httpGet(ctx context.Context, client *http.Client, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", updaterUA)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func releaseAssetURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/%s", downloadBaseURL, tag, name)
}

// ManualDownloadURL devolve a URL pública do asset desta plataforma na
// release tag — a camada cli usa para compor instruções de update manual
// (ex.: diretório do binário sem permissão de escrita).
func ManualDownloadURL(tag string) (string, error) {
	asset, err := AssetName()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return releaseAssetURL(tag, asset), nil
}

// parseChecksums lê o formato do sha256sum ("<hex>  <arquivo>", um por
// linha, com "*" opcional antes do nome no modo binário).
func parseChecksums(content, asset string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("update: checksums.txt has no entry for %s", asset)
}

// replaceBinary efetiva a troca. Unix: rename atômico por cima do alvo — o
// processo em execução continua válido no inode antigo. Windows: não se
// sobrescreve um executável em uso, mas PODE-SE renomeá-lo — o dance move o
// atual para .old e põe o novo no lugar, desfazendo em caso de falha.
func replaceBinary(target, staged string) error {
	if runtime.GOOS == "windows" {
		return swapViaOldRename(target, staged)
	}
	return os.Rename(staged, target)
}

// swapViaOldRename é o dance de troca do Windows, separado (e sem depender
// de runtime.GOOS) para ser testável em qualquer plataforma.
func swapViaOldRename(target, staged string) error {
	old := target + ".old"
	// Um .old de um update anterior já não está mais em execução; remove
	// para o rename abaixo não falhar.
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("update: parking current binary: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		// Desfaz para nunca deixar o usuário sem binário no lugar.
		_ = os.Rename(old, target)
		return fmt.Errorf("update: activating staged binary: %w", err)
	}
	return nil
}

// CleanupStaleArtifacts remove restos de updates anteriores: o .old do dance
// do Windows e staging files órfãos com mais de uma hora (nunca os recentes —
// outro processo pode estar no meio de um update). Best-effort, para o boot.
func CleanupStaleArtifacts(execPath string) {
	if execPath == "" {
		return
	}
	_ = os.Remove(execPath + ".old")
	stale, err := filepath.Glob(filepath.Join(filepath.Dir(execPath), ".chatcli-update-*"))
	if err != nil {
		return
	}
	for _, f := range stale {
		if info, err := os.Stat(f); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(f)
		}
	}
}
