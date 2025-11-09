package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

var titleRegex = regexp.MustCompile(`(?i)^\s*title\s*=\s*"(.*?)"`)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@lotus-scan",
			Description: "Escaneia um diretório de documentação Hugo/Lotus e retorna a estrutura de conteúdo.",
			Usage:       "@lotus-scan <caminho_para_docs>",
			Version:     "1.0.0",
		}
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Erro: Caminho para o diretório de documentação é obrigatório.")
		os.Exit(1)
	}
	rootDir := os.Args[1]

	var builder strings.Builder
	builder.WriteString("Estrutura da Documentação Atual:\n")

	walkErr := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Retorna o erro para parar o Walk, mas loga para o usuário saber onde falhou.
			fmt.Fprintf(os.Stderr, "Aviso: não foi possível acessar o caminho '%s': %v\n", path, err)
			return nil // Retorna nil para continuar andando, mas o erro foi logado.
		}

		// Ignora o próprio diretório raiz
		if path == rootDir {
			return nil
		}

		// Calcula o nível de profundidade para indentação
		rel, _ := filepath.Rel(rootDir, path)
		depth := len(strings.Split(rel, string(filepath.Separator)))
		indent := strings.Repeat("  ", depth-1)

		if info.IsDir() {
			// Para diretórios, podemos tentar ler o _index.md
			indexPath := filepath.Join(path, "_index.md")
			title := getTitleFromFrontMatter(indexPath)
			if title == "" {
				title = info.Name() // Fallback para o nome do diretório
			}
			builder.WriteString(fmt.Sprintf("%s- %s/ (Seção)\n", indent, title))
		} else if strings.HasSuffix(info.Name(), ".md") && info.Name() != "_index.md" {
			title := getTitleFromFrontMatter(path)
			if title == "" {
				title = info.Name()
			}
			builder.WriteString(fmt.Sprintf("%s  - %s (Página)\n", indent, title))
		}
		return nil
	})

	if walkErr != nil {
		fmt.Fprintf(os.Stderr, "Erro final ao percorrer o diretório: %v", walkErr)
		os.Exit(1)
	}

	fmt.Print(builder.String())
}

// getTitleFromFrontMatter lê um arquivo .md e extrai o título do front matter.
func getTitleFromFrontMatter(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	inFrontMatter := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "+++" {
			if inFrontMatter { // Fim do front matter
				break
			}
			inFrontMatter = true
			continue
		}
		if inFrontMatter {
			matches := titleRegex.FindStringSubmatch(line)
			if len(matches) > 1 {
				return matches[1]
			}
		}
	}
	return ""
}
