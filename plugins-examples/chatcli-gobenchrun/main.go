package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Metadata (sem alterações)
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@go-bench-run",
			Description: "Executa um benchmark Go, gera um perfil de CPU e retorna o relatório do pprof.",
			Usage:       "@go-bench-run <caminho_do_arquivo_bench_test.go>",
			Version:     "1.1.0", // Versão incrementada
		}
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Erro: Uso incorreto. Esperado: @go-bench-run <caminho_do_arquivo_bench_test.go>")
		os.Exit(1)
	}
	benchFilePath := os.Args[1]

	// --- CORREÇÃO PRINCIPAL: Definir o diretório de trabalho ---
	// O diretório de trabalho será o diretório onde o arquivo de teste está.
	workDir := filepath.Dir(benchFilePath)

	// Define os nomes dos arquivos de perfil (relativos ao workDir)
	cpuProfile := "cpu.prof"
	cpuProfilePath := filepath.Join(workDir, cpuProfile) // Caminho completo para limpeza

	// Garante a limpeza dos arquivos gerados.
	defer os.Remove(cpuProfilePath)
	// O Go gera um binário de teste, vamos limpá-lo também.
	// O nome do binário é o nome do diretório + ".test".
	testBinaryName := filepath.Base(workDir) + ".test"
	defer os.Remove(filepath.Join(workDir, testBinaryName))

	// 1. Executa o benchmark com a flag -cpuprofile.
	// O comando será executado DENTRO do diretório 'workDir'.
	benchCmd := exec.Command("go", "test", "-bench=.", "-run=^$", "-count=1", "-cpuprofile", cpuProfile)
	benchCmd.Dir = workDir // <<--- ESTA LINHA RESOLVE O PROBLEMA
	if output, err := benchCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao executar o benchmark em '%s': %v\nSaída do Comando:\n%s", workDir, err, string(output))
		os.Exit(1)
	}

	// 2. Analisa o perfil gerado.
	// Como o `go test` rodou em `workDir`, o `cpu.prof` foi criado lá.
	// O `pprof` também precisa rodar no mesmo diretório para encontrar os fontes.
	pprofCmd := exec.Command("go", "tool", "pprof", "-top", "-cum", cpuProfile)
	pprofCmd.Dir = workDir // <<--- E ESTA LINHA GARANTE A CONSISTÊNCIA
	output, err := pprofCmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao analisar o perfil com pprof em '%s': %v\nSaída do Comando:\n%s", workDir, err, string(output))
		os.Exit(1)
	}

	// 3. Retorna o relatório do pprof para a IA.
	fmt.Print(string(output))
}
