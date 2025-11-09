package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Metadata define a estrutura de descoberta do plugin, que é o "contrato"
// com o ChatCLI.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

func main() {
	// --- Contrato de Descoberta ---
	// Verifica se o primeiro argumento é --metadata. Se for, imprime o JSON
	// de metadados e encerra. Isso é como o ChatCLI descobre o que o plugin faz.
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@go-bench-gen",
			Description: "Gera um arquivo de benchmark (_test.go) para uma função Go específica em um arquivo.",
			Usage:       "@go-bench-gen <caminho_do_arquivo.go> <nome_da_funcao>",
			Version:     "1.1.0", // Versão atualizada para refletir melhorias
		}
		// Codifica os metadados diretamente para a saída padrão.
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro fatal ao gerar metadados JSON: %v", err)
			os.Exit(1)
		}
		return
	}

	// --- Lógica Principal do Plugin ---

	// Valida se o número correto de argumentos foi fornecido.
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "Erro: Uso incorreto. Esperado: @go-bench-gen <caminho_do_arquivo.go> <nome_da_funcao>")
		os.Exit(1)
	}
	filePath := os.Args[1]
	funcName := os.Args[2]

	// Garante que estamos trabalhando com um caminho absoluto para evitar ambiguidades.
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao obter o caminho absoluto para '%s': %v", filePath, err)
		os.Exit(1)
	}

	// Analisa o arquivo de código-fonte Go para construir uma Árvore de Sintaxe Abstrata (AST).
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, absFilePath, nil, parser.ParseComments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao analisar o arquivo Go '%s': %v", absFilePath, err)
		os.Exit(1)
	}

	// Percorre a AST para encontrar a declaração da função com o nome especificado.
	var funcDecl *ast.FuncDecl
	ast.Inspect(node, func(n ast.Node) bool {
		// Verifica se o nó atual é uma declaração de função e se o nome corresponde.
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == funcName {
			funcDecl = fn
			return false // Para a busca assim que encontrar a função.
		}
		return true // Continua a busca.
	})

	if funcDecl == nil {
		fmt.Fprintf(os.Stderr, "Erro: Função '%s' não encontrada no arquivo '%s'", funcName, absFilePath)
		os.Exit(1)
	}

	// Gera o código-fonte do benchmark em um buffer de bytes.
	var out bytes.Buffer
	out.WriteString(fmt.Sprintf("package %s\n\n", node.Name.Name))
	out.WriteString("import \"testing\"\n\n")
	out.WriteString(fmt.Sprintf("func Benchmark%s(b *testing.B) {\n", funcName))
	out.WriteString("    // Este benchmark foi gerado automaticamente pelo plugin @go-bench-gen do ChatCLI.\n")
	out.WriteString("    b.ReportAllocs()\n") // Adiciona medição de alocações de memória.
	out.WriteString("    b.ResetTimer()\n")   // Zera o timer antes do loop para medições mais precisas.
	out.WriteString("    for i := 0; i < b.N; i++ {\n")
	// Simplificação: assume que a função não tem argumentos ou retornos.
	// Uma versão profissional lidaria com a inicialização de parâmetros aqui.
	out.WriteString(fmt.Sprintf("        %s()\n", funcName))
	out.WriteString("    }\n")
	out.WriteString("}\n")

	// Formata o código Go gerado para garantir que ele esteja sintaticamente correto e legível.
	formatted, err := format.Source(out.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro interno ao formatar o código de benchmark gerado: %v", err)
		os.Exit(1)
	}

	// Constrói o nome do arquivo de benchmark (ex: main.go -> main_bench_test.go).
	benchFilePath := strings.TrimSuffix(absFilePath, ".go") + "_bench_test.go"
	if err := os.WriteFile(benchFilePath, formatted, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao salvar o arquivo de benchmark '%s': %v", benchFilePath, err)
		os.Exit(1)
	}

	// Retorna o caminho absoluto do arquivo gerado para a saída padrão.
	// Isso é crucial para que o agente do ChatCLI saiba qual arquivo usar no próximo passo.
	fmt.Print(benchFilePath)
}
