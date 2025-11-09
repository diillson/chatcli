package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Metadata: O contrato do plugin.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// Estrutura para decodificar a resposta da API do Docker Hub.
type DockerHubResponse struct {
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@dockerhub-tags",
			Description: "Busca e lista as tags disponíveis para uma imagem Docker no Docker Hub.",
			Usage:       "@dockerhub-tags <nome_da_imagem>",
			Version:     "1.0.0",
		}
		json.NewEncoder(os.Stdout).Encode(meta)
		return
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Erro: Nome da imagem é obrigatório. Ex: @dockerhub-tags redis")
		os.Exit(1)
	}
	imageName := os.Args[1]
	// Imagens oficiais geralmente estão sob o namespace 'library'.
	if !strings.Contains(imageName, "/") {
		imageName = "library/" + imageName
	}

	apiURL := fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/tags/?page_size=25", imageName)

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Get(apiURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao conectar à API do Docker Hub: %v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "API do Docker Hub retornou erro %d: %s", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var apiResponse DockerHubResponse
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao decodificar resposta da API: %v", err)
		os.Exit(1)
	}

	var tags []string
	for _, result := range apiResponse.Results {
		tags = append(tags, result.Name)
	}

	// Imprime a lista de tags, uma por linha, para a IA processar.
	fmt.Println(strings.Join(tags, "\n"))
}
