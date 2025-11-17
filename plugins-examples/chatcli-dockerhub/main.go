package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Metadata: Contrato do plugin para IA/Pipeline
type Metadata struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Usage       string   `json:"usage"`
	Version     string   `json:"version"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples"`
}

// DockerConfig: Estrutura do ~/.docker/config.json
type DockerConfig struct {
	Auths map[string]struct {
		Auth string `json:"auth"`
	} `json:"auths"`
}

// RegistryResponse: Resposta genérica de registries
type RegistryResponse struct {
	Tags    []string `json:"tags"` // OCI padrão (GCR, GHCR, ACR, Harbor)
	Results []struct {
		Name string `json:"name"` // Docker Hub
	} `json:"results"`
}

func main() {
	// Modo metadata para IA/Pipeline descobrir capacidades
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@registry-tags",
			Description: "Busca tags de imagens em registries públicos e privados (Docker Hub, GCR, GHCR, ACR, Harbor, Artifactory)",
			Usage:       "@registry-tags <imagem> [--registry=<url>] [--username=<user>] [--password=<pass>] [--token=<token>]",
			Version:     "3.0.0",
			Tags:        []string{"docker", "registry", "container", "gcr", "ghcr", "acr", "harbor", "artifactory", "automation", "pipeline"},
			Examples: []string{
				"@registry-tags redis",
				"@registry-tags meuuser/imagem-privada --username=user --password=pass",
				"@registry-tags gcr.io/projeto/imagem --token=$GCR_TOKEN",
				"@registry-tags ghcr.io/user/repo --token=$GITHUB_TOKEN",
				"@registry-tags registry.empresa.com/app --username=$REG_USER --password=$REG_PASS",
			},
		}
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Parse de argumentos (sem interação)
	config := parseArgs(os.Args[1:])

	// Validação silenciosa
	if config.ImageName == "" {
		fmt.Fprintln(os.Stderr, "ERRO: Imagem não especificada")
		os.Exit(1)
	}

	// Detecta registry e normaliza URL
	registry, cleanImage := detectRegistry(config.ImageName, config.RegistryURL)
	config.RegistryURL = registry
	config.ImageName = cleanImage

	// Tenta obter credenciais (prioridade: args > env > docker config)
	if config.Username == "" && config.Password == "" && config.Token == "" {
		config.Username, config.Password = loadCredentials(config.RegistryURL)
	}

	// Busca tags
	tags := fetchTags(config)

	// Output limpo para pipeline/IA processar
	if len(tags) == 0 {
		fmt.Fprintln(os.Stderr, "AVISO: Nenhuma tag encontrada")
		os.Exit(0)
	}

	// Saída em JSON para pipelines (opcional via flag)
	if hasFlag(os.Args, "--json") {
		output := map[string]interface{}{
			"image":    config.ImageName,
			"registry": config.RegistryURL,
			"tags":     tags,
			"count":    len(tags),
		}
		json.NewEncoder(os.Stdout).Encode(output)
		return
	}

	// Saída padrão: uma tag por linha
	fmt.Println(strings.Join(tags, "\n"))
}

// Config: Configuração do plugin
type Config struct {
	ImageName   string
	RegistryURL string
	Username    string
	Password    string
	Token       string
}

// parseArgs: Parser de argumentos sem interação
func parseArgs(args []string) Config {
	config := Config{
		RegistryURL: "https://hub.docker.com",
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Flags com valores
		if strings.HasPrefix(arg, "--registry=") {
			config.RegistryURL = strings.TrimPrefix(arg, "--registry=")
		} else if strings.HasPrefix(arg, "--username=") {
			config.Username = strings.TrimPrefix(arg, "--username=")
		} else if strings.HasPrefix(arg, "--password=") {
			config.Password = strings.TrimPrefix(arg, "--password=")
		} else if strings.HasPrefix(arg, "--token=") {
			config.Token = strings.TrimPrefix(arg, "--token=")
		} else if !strings.HasPrefix(arg, "--") {
			// Primeiro argumento sem -- é a imagem
			if config.ImageName == "" {
				config.ImageName = arg
			}
		}
	}

	// Fallback para variáveis de ambiente
	if config.Username == "" {
		config.Username = os.Getenv("REGISTRY_USERNAME")
	}
	if config.Password == "" {
		config.Password = os.Getenv("REGISTRY_PASSWORD")
	}
	if config.Token == "" {
		config.Token = os.Getenv("REGISTRY_TOKEN")
	}

	return config
}

// detectRegistry: Detecta tipo de registry e normaliza URL
func detectRegistry(imageName, registryURL string) (string, string) {
	registries := map[string]string{
		"gcr.io/":                  "https://gcr.io",
		"ghcr.io/":                 "https://ghcr.io",
		"docker.io/":               "https://hub.docker.com",
		"registry.hub.docker.com/": "https://hub.docker.com",
	}

	// Detecta pelo prefixo da imagem
	for prefix, url := range registries {
		if strings.HasPrefix(imageName, prefix) {
			cleanImage := strings.TrimPrefix(imageName, prefix)
			return url, cleanImage
		}
	}

	// Detecta registry customizado no formato registry.com/namespace/image
	parts := strings.Split(imageName, "/")
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		// Parece ser um registry customizado
		registryHost := parts[0]
		cleanImage := strings.Join(parts[1:], "/")

		// Se não tem protocolo, adiciona https
		if !strings.HasPrefix(registryURL, "http") {
			registryURL = "https://" + registryHost
		}

		return registryURL, cleanImage
	}

	// Default: Docker Hub
	return registryURL, imageName
}

// loadCredentials: Carrega credenciais do ~/.docker/config.json
func loadCredentials(registryURL string) (string, string) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}

	configPath := filepath.Join(homeDir, ".docker", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", ""
	}

	var config DockerConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return "", ""
	}

	// Normaliza URL para match
	normalizedURL := strings.TrimPrefix(registryURL, "https://")
	normalizedURL = strings.TrimPrefix(normalizedURL, "http://")
	normalizedURL = strings.TrimSuffix(normalizedURL, "/")

	// Tenta encontrar credenciais
	for key, auth := range config.Auths {
		normalizedKey := strings.TrimPrefix(key, "https://")
		normalizedKey = strings.TrimPrefix(normalizedKey, "http://")
		normalizedKey = strings.TrimSuffix(normalizedKey, "/")

		if strings.Contains(normalizedKey, normalizedURL) || strings.Contains(normalizedURL, normalizedKey) {
			decoded, err := base64.StdEncoding.DecodeString(auth.Auth)
			if err != nil {
				continue
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				return parts[0], parts[1]
			}
		}
	}

	return "", ""
}

// fetchTags: Busca tags com autenticação
func fetchTags(config Config) []string {
	registryType := getRegistryType(config.RegistryURL)
	apiURL := buildAPIURL(registryType, config.RegistryURL, config.ImageName)

	// Cria requisição
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERRO: Falha ao criar requisição: %v\n", err)
		os.Exit(1)
	}

	// Adiciona autenticação
	addAuthentication(req, config)

	// Executa requisição
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:       10,
			IdleConnTimeout:    30 * time.Second,
			DisableCompression: false,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERRO: Falha na conexão: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	// Verifica status
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		fmt.Fprintln(os.Stderr, "ERRO: Autenticação falhou (401/403)")
		fmt.Fprintln(os.Stderr, "Configure: REGISTRY_USERNAME/PASSWORD ou REGISTRY_TOKEN")
		os.Exit(1)
	}

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "ERRO: API retornou %d: %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	// Parse resposta
	body, _ := io.ReadAll(resp.Body)
	return parseTags(body, registryType)
}

// getRegistryType: Identifica tipo de registry
func getRegistryType(url string) string {
	switch {
	case strings.Contains(url, "docker.com"):
		return "dockerhub"
	case strings.Contains(url, "gcr.io"):
		return "gcr"
	case strings.Contains(url, "ghcr.io"):
		return "ghcr"
	case strings.Contains(url, "azurecr.io"):
		return "acr"
	default:
		return "oci" // Padrão OCI (Harbor, Artifactory, etc)
	}
}

// buildAPIURL: Constrói URL da API baseado no tipo
func buildAPIURL(registryType, registryURL, imageName string) string {
	switch registryType {
	case "dockerhub":
		if !strings.Contains(imageName, "/") {
			imageName = "library/" + imageName
		}
		return fmt.Sprintf("%s/v2/repositories/%s/tags/?page_size=100", registryURL, imageName)
	default:
		// Padrão OCI (GCR, GHCR, ACR, Harbor, Artifactory)
		return fmt.Sprintf("%s/v2/%s/tags/list", registryURL, imageName)
	}
}

// addAuthentication: Adiciona headers de autenticação
func addAuthentication(req *http.Request, config Config) {
	if config.Token != "" {
		// Token direto (GHCR, GCR OAuth, Harbor token)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.Token))
	} else if config.Username != "" && config.Password != "" {
		// Basic Auth (Docker Hub, registries privados)
		auth := base64.StdEncoding.EncodeToString([]byte(config.Username + ":" + config.Password))
		req.Header.Set("Authorization", fmt.Sprintf("Basic %s", auth))
	}

	// Headers adicionais para compatibilidade
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "registry-tags-plugin/3.0.0")
}

// parseTags: Parse da resposta baseado no tipo de registry
func parseTags(body []byte, registryType string) []string {
	var response RegistryResponse
	if err := json.Unmarshal(body, &response); err != nil {
		fmt.Fprintf(os.Stderr, "ERRO: Falha ao parsear resposta: %v\n", err)
		return []string{}
	}

	if registryType == "dockerhub" {
		var tags []string
		for _, r := range response.Results {
			tags = append(tags, r.Name)
		}
		return tags
	}

	return response.Tags
}

// hasFlag: Verifica se flag existe
func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}
