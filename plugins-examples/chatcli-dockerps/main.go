package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

// Metadata define a estrutura do JSON de descoberta do plugin.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--metadata" {
		meta := Metadata{
			Name:        "@docker-ps",
			Description: "Lista contêineres Docker. Use --all para incluir contêineres parados.",
			Usage:       "@docker-ps [--all]",
			Version:     "0.1.0",
		}

		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Lógica Principal do Plugin ---
	// Os argumentos são todos os que vêm depois do nome do executável.
	args := os.Args[1:]

	dockerArgs := []string{"ps"}
	for _, arg := range args {
		if arg == "--all" || arg == "-a" {
			dockerArgs = append(dockerArgs, "-a")
		}
	}

	// Verifica se o Docker está acessível
	if err := exec.Command("docker", "info").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "Erro: O daemon do Docker não parece estar em execução ou acessível.")
		os.Exit(1)
	}

	cmd := exec.Command("docker", dockerArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao executar 'docker ps': %v\n%s", err, string(output))
		os.Exit(1)
	}

	// Imprime o resultado para o stdout, que será capturado pelo chatcli
	fmt.Print(string(output))
}
