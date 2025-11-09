package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

const apiBaseURL = "https://en.wikipedia.org/w/api.php"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@wikipedia",
			Description: "Busca artigos na Wikipedia ou lê o resumo de um artigo específico.",
			Usage:       "@wikipedia <termo_de_busca> | @wikipedia --read \"<título_exato>\"",
			Version:     "1.0.0",
		}
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	readFlag := flag.String("read", "", "Título exato do artigo da Wikipedia para ler o resumo.")
	flag.Parse()

	if *readFlag != "" {
		readArticle(*readFlag)
		return
	}

	// Se não for --read, assume o modo de busca.
	searchTerm := strings.Join(flag.Args(), " ")
	if searchTerm == "" {
		fmt.Fprintln(os.Stderr, "Erro: Termo de busca ou flag --read obrigatório.")
		os.Exit(1)
	}
	searchArticles(searchTerm)
}

// searchArticles busca por um termo e retorna uma lista de títulos correspondentes.
func searchArticles(term string) {
	params := url.Values{}
	params.Add("action", "opensearch")
	params.Add("search", term)
	params.Add("limit", "5")
	params.Add("namespace", "0")
	params.Add("format", "json")

	apiURL := fmt.Sprintf("%s?%s", apiBaseURL, params.Encode())
	body, err := makeAPIRequest(apiURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao buscar na Wikipedia: %v", err)
		os.Exit(1)
	}

	var searchResult []interface{}
	if err := json.Unmarshal(body, &searchResult); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao decodificar a resposta de busca da API: %v", err)
		os.Exit(1)
	}

	if len(searchResult) < 2 {
		fmt.Println("Nenhum resultado encontrado para sua busca.")
		return
	}

	titles, ok := searchResult[1].([]interface{})
	if !ok || len(titles) == 0 {
		fmt.Println("Nenhum resultado encontrado para sua busca.")
		return
	}

	var builder strings.Builder
	builder.WriteString("Resultados da busca na Wikipedia (títulos exatos para usar com --read):\n")
	for i, title := range titles {
		builder.WriteString(fmt.Sprintf("%d. \"%s\"\n", i+1, title.(string)))
	}

	fmt.Print(builder.String())
}

// readArticle busca o resumo (extrato) de um artigo com título exato.
func readArticle(title string) {
	cleanTitle := strings.Trim(title, "\"")

	params := url.Values{}
	params.Add("action", "query")
	params.Add("format", "json")
	params.Add("titles", cleanTitle)
	params.Add("prop", "extracts")
	params.Add("exintro", "true")     // Pega apenas a introdução/resumo
	params.Add("explaintext", "true") // Retorna como texto puro, não HTML
	params.Add("redirects", "1")      // Segue redirecionamentos

	apiURL := fmt.Sprintf("%s?%s", apiBaseURL, params.Encode())
	body, err := makeAPIRequest(apiURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao ler artigo da Wikipedia: %v", err)
		os.Exit(1)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao decodificar a resposta do artigo da API: %v", err)
		os.Exit(1)
	}

	pages, ok := result["query"].(map[string]interface{})["pages"].(map[string]interface{})
	if !ok || len(pages) == 0 {
		fmt.Println("Artigo não encontrado.")
		return
	}

	// Itera sobre as páginas (geralmente só uma)
	for _, page := range pages {
		pageData := page.(map[string]interface{})
		extract, ok := pageData["extract"].(string)
		if ok && extract != "" {
			fmt.Printf("Resumo do Artigo: %s\n\n", pageData["title"].(string))
			fmt.Println(extract)
			return
		}
	}

	fmt.Println("Não foi possível encontrar um resumo para este artigo.")
}

// makeAPIRequest é uma função helper para fazer chamadas HTTP.
func makeAPIRequest(url string) ([]byte, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}

	// 1. Cria a requisição (Request) em vez de usar http.Get diretamente.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("erro ao criar requisição: %w", err)
	}

	// 2. Define um User-Agent personalizado.
	req.Header.Set("User-Agent", "ChatCLI-Wikipedia-Plugin/1.0 (https://github.com/diillson/chatcli; chatcli@diillson.github.io/chatcli)")
	// (Nota: É uma boa prática incluir um link para o projeto e um email de contato)

	// 3. Executa a requisição.
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API retornou status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
