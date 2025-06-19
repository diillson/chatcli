package version

import (
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

	// Corrigindo o erro de lint: verificar o erro do Close
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

	// Remover 'v' do início da tag, se houver
	latestVersion := strings.TrimPrefix(releaseInfo.TagName, "v")

	// Extrair a versão base atual sem prefixo 'v'
	currentVersionBase := extractBaseVersion(Version)

	// Verificar se é uma versão de desenvolvimento
	isDev := Version == "dev" || Version == "unknown"

	// Se for uma versão de desenvolvimento, sempre sugerir atualização
	if isDev {
		return latestVersion, true, nil
	}

	// Usar o método needsUpdate para uma comparação semântica adequada
	needsUpdate := needsUpdate(currentVersionBase, latestVersion)

	return latestVersion, needsUpdate, nil
}

// extractBaseVersion extrai a parte base da versão, sem prefixo 'v' e sem sufixos de desenvolvimento
// Exemplo: "v1.9.0-5-g1b6ecaa-dirty" -> "1.9.0"
func extractBaseVersion(version string) string {
	// Remover prefixo 'v' se existir
	version = strings.TrimPrefix(version, "v")

	// Se contém hífen, pegar apenas a parte antes do primeiro hífen
	if strings.Contains(version, "-") {
		version = strings.Split(version, "-")[0]
	}

	return version
}

// needsUpdate verifica semanticamente se a versão atual precisa ser atualizada
// comparando componente a componente (major.minor.patch)
func needsUpdate(currentVersion, latestVersion string) bool {
	// Tratar casos de versão vazia
	if currentVersion == "" {
		return true
	}

	// Extrair componentes semânticos (major.minor.patch)
	currentParts := strings.Split(currentVersion, ".")
	latestParts := strings.Split(latestVersion, ".")

	// Garantir que temos pelo menos 3 componentes em cada versão (major.minor.patch)
	for len(currentParts) < 3 {
		currentParts = append(currentParts, "0")
	}
	for len(latestParts) < 3 {
		latestParts = append(latestParts, "0")
	}

	// Comparar componente a componente
	for i := 0; i < 3; i++ {
		// Converter para inteiros com tratamento de erro
		current, currentErr := strconv.Atoi(currentParts[i])
		latest, latestErr := strconv.Atoi(latestParts[i])

		// Se não conseguir converter, considerar como 0
		if currentErr != nil {
			current = 0
		}
		if latestErr != nil {
			latest = 0
		}

		// Comparar os valores
		if latest > current {
			return true // Versão mais recente é maior
		} else if current > latest {
			return false // Versão atual é maior
		}
		// Se forem iguais, continua para o próximo componente
	}

	// Se chegou aqui, todos os componentes principais são iguais
	// Verificar se a versão mais recente tem mais componentes (para pré-releases como 1.2.3-beta)
	if len(latestParts) > 3 && len(currentParts) <= 3 {
		return true
	}

	// Versões são iguais ou a atual é potencialmente mais recente (desenvolvedor)
	return false
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

// Helper para exibir informações de build ao iniciar o aplicativo
func PrintStartupVersionInfo() {
	if Version != "dev" && Version != "unknown" {
		fmt.Printf("ChatCLI %s (commit: %s, built: %s)\n",
			Version, CommitHash, BuildDate)
		fmt.Println("Use '/version' para mais detalhes ou '--version' na linha de comando")
		fmt.Println("-----------------------------------------------------------")
	}
}
