package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@lotus-write",
			Description: "Cria um novo arquivo de documentação .md com front matter para o tema Lotus/Hugo.",
			Usage:       "@lotus-write --path <caminho> --title <título> [--weight <peso>] (conteúdo via stdin)",
			Version:     "1.0.0",
		}
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Flags para os metadados do arquivo
	filePath := flag.String("path", "", "Caminho completo do arquivo .md a ser criado (obrigatório)")
	title := flag.String("title", "", "Título da página para o front matter (obrigatório)")
	weight := flag.Int("weight", 0, "Peso da página para ordenação (opcional)")
	flag.Parse()

	if *filePath == "" || *title == "" {
		fmt.Fprintln(os.Stderr, "Erro: As flags --path e --title são obrigatórias.")
		os.Exit(1)
	}

	// Lê o conteúdo do Markdown da entrada padrão (stdin)
	content, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao ler conteúdo do stdin: %v", err)
		os.Exit(1)
	}

	// Cria o diretório pai, se não existir
	dir := filepath.Dir(*filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao criar diretório '%s': %v", dir, err)
		os.Exit(1)
	}

	// Monta o front matter
	var frontMatter strings.Builder
	frontMatter.WriteString("+++\n")
	frontMatter.WriteString(fmt.Sprintf("title = \"%s\"\n", *title))
	if *weight > 0 {
		frontMatter.WriteString(fmt.Sprintf("weight = %d\n", *weight))
	}
	frontMatter.WriteString("+++\n\n")

	// Combina front matter e conteúdo
	finalContent := frontMatter.String() + string(content)

	// Escreve o arquivo final
	if err := os.WriteFile(*filePath, []byte(finalContent), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao escrever o arquivo '%s': %v", *filePath, err)
		os.Exit(1)
	}

	fmt.Printf("✅ Arquivo de documentação '%s' criado com sucesso.", *filePath)
}
