package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// stringslice é um tipo customizado para lidar com múltiplas flags com o mesmo nome.
type stringslice []string

func (s *stringslice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringslice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

// Metadata define a estrutura de descoberta do plugin.
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
			Description: "Inicia um contêiner Docker. Suporta flags para imagem, tag, porta, nome, variáveis de ambiente (-e) e volumes (-v).",
			Usage:       "@docker-run --image <img> --tag <tag> --port <p:p> --name <nome> [-e VAR=val] [-v /host:/cont]",
			Version:     "1.2.0", // Versão incrementada para refletir o suporte a volumes
		}
		if err := json.NewEncoder(os.Stdout).Encode(meta); err != nil {
			fmt.Fprintf(os.Stderr, "Erro ao gerar metadados JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Lógica Principal do Plugin com Suporte a Volumes ---
	image := flag.String("image", "", "Nome da imagem Docker (obrigatório)")
	tag := flag.String("tag", "latest", "Tag da imagem")
	port := flag.String("port", "", "Mapeamento de porta (ex: 5432:5432)")
	name := flag.String("name", "", "Nome para o contêiner (obrigatório)")

	var envVars stringslice
	flag.Var(&envVars, "e", "Define uma variável de ambiente (pode ser usado múltiplas vezes)")
	flag.Var(&envVars, "env", "Alias para -e")

	var volumes stringslice
	flag.Var(&volumes, "v", "Mapeia um volume (host:container). Pode ser usado múltiplas vezes.")
	flag.Var(&volumes, "volume", "Alias para -v")

	flag.Parse()

	if *image == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "Erro: As flags --image e --name são obrigatórias.")
		os.Exit(1)
	}

	imageWithTag := fmt.Sprintf("%s:%s", *image, *tag)

	// Monta a lista de argumentos para o comando 'docker run'
	args := []string{"run", "-d", "--name", *name}

	if *port != "" {
		args = append(args, "-p", *port)
	}

	for _, envVar := range envVars {
		args = append(args, "-e", envVar)
	}

	for _, volume := range volumes {
		args = append(args, "-v", volume)
	}

	args = append(args, imageWithTag)

	// Log para depuração (vai para stderr)
	fmt.Fprintf(os.Stderr, "Debug: Executando comando: docker %s\n", strings.Join(args, " "))

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erro ao executar 'docker run': %v\n%s", err, string(output))
		os.Exit(1)
	}

	fmt.Printf("Contêiner '%s' iniciado com sucesso. ID: %s", *name, strings.TrimSpace(string(output)))
}
