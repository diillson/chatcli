/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package version

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

var (
	// Essas variáveis serão preenchidas durante a compilação via ldflags
	Version    = "dev"
	CommitHash = "unknown"
	BuildDate  = "unknown"

	// URL para verificar a versão mais recente (GitHub API)
	LatestVersionURL = "https://api.github.com/repos/diillson/chatcli/releases/latest"
)

// CheckLatestVersionImpl é a implementação injetável para checagem de versão (pode ser mocked)
var CheckLatestVersionImpl = func(ctx context.Context) (string, bool, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	url := os.Getenv("CHATCLI_LATEST_VERSION_URL")
	if url == "" {
		url = LatestVersionURL
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", false, err
	}

	req.Header.Set("User-Agent", "ChatCLI-Version-Checker")

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "Erro ao fechar response body: %v\n", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("erro ao verificar versão: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, err
	}

	var releaseInfo struct {
		TagName string `json:"tag_name"`
	}

	if err := json.Unmarshal(body, &releaseInfo); err != nil {
		return "", false, err
	}

	latestVersion := strings.TrimPrefix(releaseInfo.TagName, "v")
	currentVersionFull, _, _ := GetBuildInfo()
	currentVersionBase := ExtractBaseVersion(currentVersionFull)
	needsUpdate := NeedsUpdate(currentVersionBase, latestVersion)

	return latestVersion, needsUpdate, nil
}

// GetBuildInfoImpl é a implementação injetável para GetBuildInfo (pode ser mocked)
var GetBuildInfoImpl = func() (string, string, string) {
	version := Version
	commitHash := CommitHash
	buildDate := BuildDate

	if version == "dev" || version == "unknown" ||
		commitHash == "unknown" || buildDate == "unknown" {

		if info, ok := debug.ReadBuildInfo(); ok {
			// Versão do módulo
			if (version == "dev" || version == "unknown") && info.Main.Version != "" && info.Main.Version != "(devel)" {
				version = strings.TrimPrefix(info.Main.Version, "v")
			}
			// Commit hash de pseudo-version
			if (commitHash == "unknown" || len(commitHash) < 7) && info.Main.Version != "" {
				parts := strings.Split(info.Main.Version, "-")
				if len(parts) >= 3 {
					possibleCommit := parts[len(parts)-1]
					if len(possibleCommit) >= 7 {
						commitHash = possibleCommit
					}
				}
			}
			// Build date do VCS info
			if buildDate == "unknown" {
				for _, setting := range info.Settings {
					if setting.Key == "vcs.time" {
						if t, err := time.Parse(time.RFC3339, setting.Value); err == nil {
							buildDate = t.Format("2006-01-02 15:04:05")
						} else {
							buildDate = setting.Value
						}
					}
				}
			}
		}
	}
	// Fallback: data de modificação do binário
	if buildDate == "unknown" {
		if execPath, err := os.Executable(); err == nil {
			if info, err := os.Stat(execPath); err == nil {
				modTime := info.ModTime()
				buildDate = fmt.Sprintf("%s (aproximado pela data do binário)", modTime.Format("2006-01-02 15:04:05"))
			}
		}
	}
	return version, commitHash, buildDate
}

// Info retorna informações estruturadas sobre a versão atual
type VersionInfo struct {
	Version    string `json:"version"`
	CommitHash string `json:"commit_hash"`
	BuildDate  string `json:"build_date"`
}

// GetCurrentVersion retorna as informações de versão atuais
func GetCurrentVersion() VersionInfo {
	return VersionInfo{
		Version:    Version,
		CommitHash: CommitHash,
		BuildDate:  BuildDate,
	}
}

// ExtractBaseVersion extrai a parte base da versão, sem prefixo 'v' e sem sufixos de desenvolvimento
// Exemplo: "v1.9.0-5-g1b6ecaa-dirty" -> "1.9.0"
func ExtractBaseVersion(version string) string {
	// Remover prefixo 'v' se existir
	version = strings.TrimPrefix(version, "v")

	// Se contém hífen, pegar apenas a parte antes do primeiro hífen
	if strings.Contains(version, "-") {
		version = strings.Split(version, "-")[0]
	}

	return version
}

// NeedsUpdate verifica semanticamente se a versão atual precisa ser atualizada.
func NeedsUpdate(currentVersion, latestVersion string) bool {
	// Remove prefixo "v" se houver
	currentVersion = strings.TrimPrefix(currentVersion, "v")
	latestVersion = strings.TrimPrefix(latestVersion, "v")

	// Tratar casos de versão de desenvolvimento que não devem ser comparados
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "unknown" || strings.HasPrefix(currentVersion, "0.0.0-") {
		return false
	}

	// Separar a versão principal dos sufixos de pré-lançamento
	currentParts := strings.SplitN(currentVersion, "-", 2)
	latestParts := strings.SplitN(latestVersion, "-", 2)

	currentBase := currentParts[0]
	latestBase := latestParts[0]

	// Comparar as partes numéricas (major.minor.patch)
	currentNumeric := strings.Split(currentBase, ".")
	latestNumeric := strings.Split(latestBase, ".")

	for len(currentNumeric) < 3 {
		currentNumeric = append(currentNumeric, "0")
	}
	for len(latestNumeric) < 3 {
		latestNumeric = append(latestNumeric, "0")
	}

	for i := 0; i < 3; i++ {
		current, _ := strconv.Atoi(currentNumeric[i])
		latest, _ := strconv.Atoi(latestNumeric[i])
		if latest > current {
			return true // Versão mais recente é numericamente maior
		}
		if current > latest {
			return false // Versão atual já é maior
		}
	}

	// Se as partes numéricas são iguais, comparar os sufixos de pré-lançamento
	// Regra SemVer: uma versão sem sufixo é sempre mais nova que uma com sufixo.

	hasCurrentSuffix := len(currentParts) > 1
	hasLatestSuffix := len(latestParts) > 1

	if hasCurrentSuffix && !hasLatestSuffix {
		return true // Ex: current é 1.2.3-alpha, latest é 1.2.3 (precisa atualizar)
	}

	if !hasCurrentSuffix && hasLatestSuffix {
		return false // Ex: current é 1.2.3, latest é 1.2.3-beta (não precisa "atualizar")
	}

	if hasCurrentSuffix && hasLatestSuffix {
		// Compara os sufixos alfabeticamente. "beta" > "alpha"
		return latestParts[1] > currentParts[1]
	}

	// Se chegou aqui, as versões são idênticas
	return false
}

// ansiColor aplica uma cor ANSI simples (para uso em FormatVersionInfo sem depender de cli)
func ansiColor(text string, code string) string {
	return fmt.Sprintf("\033[%sm%s\033[0m", code, text)
}

// Formatação de cores simples (equivalentes às de cli/colors.go)
const (
	ansiLime   = "92" // Verde claro
	ansiCyan   = "36" // Ciano
	ansiGray   = "90" // Cinza
	ansiGreen  = "32" // Verde
	ansiYellow = "33" // Amarelo
	ansiBold   = "1"  // Negrito (pode ser combinado: "1;92" para bold+lime)
)

// FormatVersionInfo retorna uma string formatada com as informações de versão
func FormatVersionInfo(info VersionInfo, latest string, hasUpdate bool, checkErr error) string {
	var result strings.Builder

	// Cabeçalho
	result.WriteString("\n" + ansiColor("Informações da Versão do ChatCLI", "1;92") + "\n") // Bold + Lime
	result.WriteString(ansiColor("Aqui está um resumo da versão atual, build e status de atualizações.", ansiGray) + "\n")

	// --- Versão Atual ---
	result.WriteString("\n  " + ansiColor("Versão Atual", ansiLime) + "\n")
	result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Versão:", ansiCyan), ansiColor(info.Version, ansiGray)))
	result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Commit Hash:", ansiCyan), ansiColor(info.CommitHash, ansiGray)))
	result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Data de Build:", ansiCyan), ansiColor(info.BuildDate, ansiGray)))

	// --- Atualizações ---
	result.WriteString("\n  " + ansiColor("Status de Atualizações", ansiLime) + "\n")
	if checkErr != nil {
		result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Erro na Verificação:", ansiCyan), ansiColor(fmt.Sprintf("Não foi possível verificar: %v", checkErr), ansiYellow)))
	} else {
		result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Versão Mais Recente:", ansiCyan), ansiColor(latest, ansiGray)))
		if hasUpdate {
			result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Atualização:", ansiCyan), ansiColor("Disponível! Atualize para a versão mais recente.", ansiGreen)))
		} else {
			result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Atualização:", ansiCyan), ansiColor("Você está na versão mais recente.", ansiGreen)))
		}
	}

	// --- Dica de Atualização ---
	result.WriteString("\n  " + ansiColor("Como Atualizar", ansiLime) + "\n")
	result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Comando:", ansiCyan), ansiColor("go install github.com/diillson/chatcli@latest", ansiGray)))
	result.WriteString(fmt.Sprintf("    %s    %s\n", ansiColor("Dica:", ansiCyan), ansiColor("Ou use 'git pull' no repositório clonado.", ansiGray)))

	result.WriteString("\n") // Espaço final
	return result.String()
}

// GetBuildInfo é o wrapper exportado
func GetBuildInfo() (string, string, string) {
	return GetBuildInfoImpl()
}

// CheckLatestVersionWithContext é o wrapper exportado (mantém a API inalterada)
func CheckLatestVersionWithContext(ctx context.Context) (string, bool, error) {
	return CheckLatestVersionImpl(ctx)
}
