package version

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
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

// CheckLatestVersion verifica a versão mais recente disponível no GitHub
// Retorna a versão mais recente e um booleano indicando se há uma atualização disponível
func CheckLatestVersion() (string, bool, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", LatestVersionURL, nil)
	if err != nil {
		return "", false, err
	}

	// Adicionar User-Agent para evitar problemas com a API do GitHub
	req.Header.Set("User-Agent", "ChatCLI-Version-Checker")

	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

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

	// Remover 'v' do início da tag, se houver
	latestVersion := strings.TrimPrefix(releaseInfo.TagName, "v")

	// Extrai apenas a parte da tag da versão atual
	// Para transformar algo como "v1.9.0-5-g1b6ecaa-dirty" em "1.9.0"
	currentVersionBase := Version
	if strings.Contains(Version, "-") {
		parts := strings.Split(Version, "-")
		currentVersionBase = strings.TrimPrefix(parts[0], "v")
	} else {
		currentVersionBase = strings.TrimPrefix(Version, "v")
	}

	// Verifique se estamos em um commit específico (dev build)
	isDev := Version == "dev" || strings.Contains(Version, "-dirty") || strings.Contains(Version, "-g")

	// Compara versões (simplificado - para uma comparação mais robusta,
	// considere usar um pacote como "github.com/hashicorp/go-version")

	// Se estamos em modo dev, só indica atualização se a versão base for diferente
	if isDev {
		// Se a versão base (como 1.9.0) for exatamente igual à última, não há atualização
		// pois estamos provavelmente trabalhando na próxima versão
		return latestVersion, currentVersionBase != latestVersion, nil
	}

	// Para versões de lançamento estáveis, comparação direta
	needsUpdate := currentVersionBase != latestVersion

	return latestVersion, needsUpdate, nil
}

// FormatVersionInfo retorna uma string formatada com as informações de versão
func FormatVersionInfo(info VersionInfo, includeLatest bool) string {
	var result strings.Builder

	// Obter informações de build de forma mais robusta
	version, commitHash, buildDate := GetBuildInfo()

	result.WriteString(fmt.Sprintf("📊 ChatCLI Versão: %s\n", version))
	result.WriteString(fmt.Sprintf("📌 Commit: %s\n", commitHash))

	if buildDate == "unknown" {
		// Se ainda não temos a data de build, usar a data de modificação do executável
		if execPath, err := os.Executable(); err == nil {
			if info, err := os.Stat(execPath); err == nil {
				modTime := info.ModTime()
				buildDate = fmt.Sprintf("%s (aproximado pela data do binário)",
					modTime.Format("2006-01-02 15:04:05"))
			}
		}
	}

	result.WriteString(fmt.Sprintf("🕒 Build: %s\n", buildDate))

	if includeLatest {
		latestVersion, hasUpdate, err := CheckLatestVersion()
		if err == nil {
			if hasUpdate {
				result.WriteString(fmt.Sprintf("\n🔔 Atualização disponível! Versão mais recente: %s\n", latestVersion))
				result.WriteString("   Execute 'go install github.com/diillson/chatcli@latest' para atualizar.\n")
			} else {
				result.WriteString("\n✅ Você está usando a versão mais recente.\n")
			}
		} else {
			result.WriteString(fmt.Sprintf("\n⚠️ Não foi possível verificar atualizações: %s\n", err.Error()))
		}
	}

	return result.String()
}

// GetBuildInfo obtém informações de build de forma mais robusta
func GetBuildInfo() (string, string, string) {
	version := Version
	commitHash := CommitHash
	buildDate := BuildDate

	// Se estamos usando valores padrão, tentar obter do build info
	if version == "dev" || commitHash == "unknown" || buildDate == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			// Procurar por informações do VCS nas configurações do build
			for _, setting := range info.Settings {
				switch setting.Key {
				case "vcs.revision":
					if commitHash == "unknown" {
						commitHash = setting.Value[:8] // Pegar apenas os primeiros 8 caracteres
					}
				case "vcs.time":
					if buildDate == "unknown" {
						// Converter para formato mais amigável
						if t, err := time.Parse(time.RFC3339, setting.Value); err == nil {
							buildDate = t.Format("2006-01-02 15:04:05")
						} else {
							buildDate = setting.Value
						}
					}
				}
			}

			// Se ainda não temos versão mas temos o módulo, usar isso
			if version == "dev" && info.Main.Version != "" {
				version = info.Main.Version
			}
		}
	}

	return version, commitHash, buildDate
}
