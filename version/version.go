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

	// Obter a versão atual de forma mais robusta
	currentVersionFull, _, _ := GetBuildInfo()

	// Se não conseguimos determinar a versão atual com segurança,
	// evitamos falso-positivo (não afirmar que há atualização).
	if currentVersionFull == "" ||
		currentVersionFull == "dev" ||
		currentVersionFull == "unknown" ||
		currentVersionFull == "(devel)" {
		return latestVersion, false, nil
	}

	// Pseudo-version (ex.: 0.0.0-yyyymmddhhmmss-abcdef)
	trimmedFull := strings.TrimPrefix(currentVersionFull, "v")
	if strings.HasPrefix(trimmedFull, "0.0.0-") {
		// Conservador: não afirmar que há atualização
		return latestVersion, false, nil
	}

	// Extrair a versão base (ex.: "v1.9.0-5-gxxxx" -> "1.9.0")
	currentVersionBase := extractBaseVersion(currentVersionFull)
	if currentVersionBase == "" ||
		currentVersionBase == "dev" ||
		currentVersionBase == "unknown" {
		return latestVersion, false, nil
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
	// Remove prefixo "v" se houver
	currentVersion = strings.TrimPrefix(currentVersion, "v")
	latestVersion = strings.TrimPrefix(latestVersion, "v")

	// Tratar casos de versão vazia ou de desenvolvimento
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "unknown" {
		// Não é possível determinar, não force update
		return false
	}

	// Tratar pseudo-version: v0.0.0-yyyymmddhhmmss-abcdef123456
	if strings.HasPrefix(currentVersion, "0.0.0-") {
		// Opcional: tente extrair o commit hash e comparar com o da release
		// Se quiser, pode retornar false aqui para não sugerir update
		return false
	}

	// Extrair componentes semânticos (major.minor.patch)
	currentParts := strings.Split(currentVersion, ".")
	latestParts := strings.Split(latestVersion, ".")

	for len(currentParts) < 3 {
		currentParts = append(currentParts, "0")
	}
	for len(latestParts) < 3 {
		latestParts = append(latestParts, "0")
	}

	for i := 0; i < 3; i++ {
		current, currentErr := strconv.Atoi(currentParts[i])
		latest, latestErr := strconv.Atoi(latestParts[i])
		if currentErr != nil {
			current = 0
		}
		if latestErr != nil {
			latest = 0
		}
		if latest > current {
			return true
		} else if current > latest {
			return false
		}
	}

	if len(latestParts) > 3 && len(currentParts) <= 3 {
		return true
	}

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
				result.WriteString(fmt.Sprintf("   Execute 'go install github.com/diillson/chatcli@%s' para atualizar.\n Pressione ENTER para continuar", latestVersion))
			} else {
				result.WriteString("\n✅ Está usando a versão mais recente.\n Pressione ENTER para continuar.")
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

	if version == "dev" || version == "unknown" ||
		commitHash == "unknown" || buildDate == "unknown" {

		if info, ok := debug.ReadBuildInfo(); ok {
			// Versão do módulo (ex: "v1.2.3" ou "v0.0.0-20240620123456-abcdef123456")
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

// Helper para exibir informações de build ao iniciar o aplicativo
func PrintStartupVersionInfo() {
	v, c, d := GetBuildInfo()
	if v == "" || v == "unknown" {
		v = Version
		c = CommitHash
		d = BuildDate
	}
	if v != "" && v != "dev" && v != "unknown" {
		fmt.Printf("ChatCLI %s (commit: %s, built: %s)\n", v, c, d)
		fmt.Println("Use '/version' para mais detalhes ou 'chatcli --version' na linha de comando")
		fmt.Println("-----------------------------------------------------------")
	}
}
