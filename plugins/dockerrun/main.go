package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
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
			Name:        "@docker-run",
			Description: "Inicia um contêiner Docker com base nos parâmetros fornecidos.",
			Usage:       "@docker-run --image <img_name> --tag <tag> --port <host:cont> --name <cont_name>",
			Version:     "1.0.0",
		}
		json.NewEncoder(os.Stdout).Encode(meta)
		return
	}

	// Usamos flags para tornar os argumentos explícitos e fáceis para a IA.
	image := flag.String("image", "", "Nome da imagem Docker")
	tag := flag.String("tag", "latest", "Tag da imagem")
	port := flag.String("port", "", "Mapeamento de porta (ex: 8080:80)")
	name := flag.String("name", "", "Nome para o contêiner")
	flag.Parse()

	if *image == "" || *port == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "Erro: As flags --image, --port, e --name são obrigatórias.")
		os.Exit(1)
	}

	imageWithTag := fmt.Sprintf("%s:%s", *image, *tag)
	args := []string{"run", "-d", "-p", *port, "--name", *name, imageWithTag}

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao executar 'docker run': %v\n%s", err, string(output))
		os.Exit(1)
	}

	fmt.Printf("Contêiner '%s' iniciado com sucesso. ID: %s", *name, string(output))
}
