package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Metadata define a estrutura de descoberta do plugin.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// HNItem representa uma notícia do Hacker News.
type HNItem struct {
	Title       string `json:"title"`
	Score       int    `json:"score"`
	By          string `json:"by"`
	Descendants int    `json:"descendants"` // Número de comentários
}

const (
	apiBaseURL        = "https://hacker-news.firebaseio.com/v0"
	topStoriesURL     = apiBaseURL + "/topstories.json"
	itemURLFormat     = apiBaseURL + "/item/%d.json"
	defaultStoryCount = 5
)

func main() {
	// --- Contrato de Descoberta ---
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@hackernews",
			Description: "Busca as N notícias mais populares (top stories) do Hacker News.",
			Usage:       "@hackernews <numero_de_noticias>",
			Version:     "1.0.0",
		}
		json.NewEncoder(os.Stdout).Encode(meta)
		return
	}

	// --- Lógica Principal ---
	storyCount := defaultStoryCount
	if len(os.Args) > 1 {
		if count, err := strconv.Atoi(os.Args[1]); err == nil && count > 0 {
			storyCount = count
		}
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}

	// 1. Buscar os IDs das principais notícias
	resp, err := httpClient.Get(topStoriesURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao buscar IDs das notícias: %v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var storyIDs []int
	if err := json.NewDecoder(resp.Body).Decode(&storyIDs); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao decodificar IDs das notícias: %v", err)
		os.Exit(1)
	}

	if len(storyIDs) < storyCount {
		storyCount = len(storyIDs)
	}

	// 2. Buscar os detalhes de cada notícia
	var output strings.Builder
	for i := 0; i < storyCount; i++ {
		itemURL := fmt.Sprintf(itemURLFormat, storyIDs[i])
		resp, err := httpClient.Get(itemURL)
		if err != nil {
			continue // Pula esta notícia em caso de erro
		}
		defer resp.Body.Close()

		var item HNItem
		if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
			continue
		}

		// 3. Formatar a saída para ser legível pela IA e por humanos
		output.WriteString(fmt.Sprintf("[%d] Título: %s\n", i+1, item.Title))
		output.WriteString(fmt.Sprintf("    Pontos: %d | Autor: %s | Comentários: %d\n", item.Score, item.By, item.Descendants))
	}

	fmt.Print(output.String())
}
