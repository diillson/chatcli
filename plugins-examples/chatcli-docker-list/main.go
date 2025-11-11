package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

type Container struct {
	ID      string
	Name    string
	Image   string
	Status  string
	Size    string
	Ports   string
	Created string
}

type Image struct {
	ID         string
	Repository string
	Tag        string
	Size       string
	Created    string
	FullName   string
}

type Volume struct {
	Name       string
	Driver     string
	Mountpoint string
	Scope      string
}

type Network struct {
	ID     string
	Name   string
	Driver string
	Scope  string
}

func logf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
	os.Stderr.Sync()
}

func fatalf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ ERRO: "+format+"\n", v...)
	os.Exit(1)
}

func main() {
	metadataFlag := flag.Bool("metadata", false, "Exibe os metadados do plugin em formato JSON")
	flag.Parse()

	if *metadataFlag {
		printMetadata()
		return
	}

	if err := ensureDependencies("docker"); err != nil {
		fatalf("Dependência não encontrada: %v", err)
	}

	args := flag.Args()
	if len(args) == 0 {
		fatalf("Uso: @docker-list <containers|images|volumes|networks|all> [opções]")
	}

	subcommand := args[0]
	subcommandArgs := args[1:]

	switch subcommand {
	case "containers":
		listContainers(subcommandArgs)
	case "images":
		listImages(subcommandArgs)
	case "volumes":
		listVolumes(subcommandArgs)
	case "networks":
		listNetworks(subcommandArgs)
	case "all":
		listAll(subcommandArgs)
	default:
		fatalf("Subcomando desconhecido: %s", subcommand)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@docker-list",
		Description: "Lista containers, imagens, volumes e redes Docker com filtros avançados",
		Usage: `@docker-list <comando> [opções]
        
        Exemplos:
          # Listar todos os containers
          @docker-list containers
          
          # Listar containers em execução
          @docker-list containers --running
          
          # Filtrar por nome/imagem
          @docker-list containers --filter nginx,redis
          
          # Listar imagens com filtro
          @docker-list images --filter postgres
          
          # Listar volumes
          @docker-list volumes
          
          # Listar redes
          @docker-list networks
          
          # Listar tudo
          @docker-list all`,
		Version: "1.0.0",
	}
	jsonMeta, _ := json.Marshal(meta)
	fmt.Println(string(jsonMeta))
}

func listContainers(args []string) {
	listCmd := flag.NewFlagSet("containers", flag.ExitOnError)
	running := listCmd.Bool("running", false, "Lista apenas containers em execução")
	stopped := listCmd.Bool("stopped", false, "Lista apenas containers parados")
	filter := listCmd.String("filter", "", "Filtra por nome/imagem (múltiplos separados por vírgula)")
	verbose := listCmd.Bool("verbose", false, "Exibe informações detalhadas")

	if err := listCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🔍 Listando containers...\n")

	// Montar argumentos de listagem
	var listArgs []string

	if *running {
		listArgs = []string{"ps", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}|{{.Ports}}|{{.CreatedAt}}"}
	} else if *stopped {
		listArgs = []string{"ps", "-a", "-f", "status=exited", "-f", "status=created", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}|{{.Ports}}|{{.CreatedAt}}"}
	} else {
		// Padrão: lista todos
		listArgs = []string{"ps", "-a", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}|{{.Ports}}|{{.CreatedAt}}"}
	}

	output, err := runCommand("docker", 30*time.Second, listArgs...)
	if err != nil {
		fatalf("Falha ao listar containers: %v", err)
	}

	containers := parseContainerList(output, *filter)

	if len(containers) == 0 {
		fmt.Println("\n📦 Nenhum container encontrado.")
		return
	}

	fmt.Printf("\n📦 CONTAINERS ENCONTRADOS: %d\n", len(containers))
	fmt.Println(strings.Repeat("=", 80))

	for i, c := range containers {
		statusIcon := "⏹️"
		if strings.Contains(c.Status, "Up") {
			statusIcon = "✅"
		} else if strings.Contains(c.Status, "Exited") {
			statusIcon = "❌"
		}

		fmt.Printf("\n%d. %s %s\n", i+1, statusIcon, c.Name)
		fmt.Printf("   ID:      %s\n", c.ID[:12])
		fmt.Printf("   Imagem:  %s\n", c.Image)
		fmt.Printf("   Status:  %s\n", c.Status)
		fmt.Printf("   Tamanho: %s\n", c.Size)

		if *verbose {
			if c.Ports != "" {
				fmt.Printf("   Portas:  %s\n", c.Ports)
			}
			fmt.Printf("   Criado:  %s\n", c.Created)
		}
	}

	fmt.Println()
}

func listImages(args []string) {
	listCmd := flag.NewFlagSet("images", flag.ExitOnError)
	filter := listCmd.String("filter", "", "Filtra por nome/repositório (múltiplos separados por vírgula)")
	dangling := listCmd.Bool("dangling", false, "Lista apenas imagens 'dangling' (sem tag)")
	verbose := listCmd.Bool("verbose", false, "Exibe informações detalhadas")

	if err := listCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🔍 Listando imagens...\n")

	listArgs := []string{"images", "--format", "{{.ID}}|{{.Repository}}|{{.Tag}}|{{.Size}}|{{.CreatedAt}}"}

	if *dangling {
		listArgs = append(listArgs, "-f", "dangling=true")
	}

	output, err := runCommand("docker", 30*time.Second, listArgs...)
	if err != nil {
		fatalf("Falha ao listar imagens: %v", err)
	}

	images := parseImageList(output, *filter)

	if len(images) == 0 {
		fmt.Println("\n🖼️  Nenhuma imagem encontrada.")
		return
	}

	fmt.Printf("\n🖼️  IMAGENS ENCONTRADAS: %d\n", len(images))
	fmt.Println(strings.Repeat("=", 80))

	for i, img := range images {
		displayName := img.FullName
		icon := "📦"

		if img.Repository == "<none>" {
			displayName = img.ID[:12] + " (sem tag)"
			icon = "⚠️"
		}

		fmt.Printf("\n%d. %s %s\n", i+1, icon, displayName)
		fmt.Printf("   ID:      %s\n", img.ID[:12])
		fmt.Printf("   Tamanho: %s\n", img.Size)

		if *verbose {
			fmt.Printf("   Criado:  %s\n", img.Created)
		}
	}

	fmt.Println()
}

func listVolumes(args []string) {
	listCmd := flag.NewFlagSet("volumes", flag.ExitOnError)
	filter := listCmd.String("filter", "", "Filtra por nome (múltiplos separados por vírgula)")
	dangling := listCmd.Bool("dangling", false, "Lista apenas volumes não utilizados")
	verbose := listCmd.Bool("verbose", false, "Exibe informações detalhadas")

	if err := listCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🔍 Listando volumes...\n")

	listArgs := []string{"volume", "ls", "--format", "{{.Name}}|{{.Driver}}|{{.Mountpoint}}|{{.Scope}}"}

	if *dangling {
		listArgs = append(listArgs, "-f", "dangling=true")
	}

	output, err := runCommand("docker", 30*time.Second, listArgs...)
	if err != nil {
		fatalf("Falha ao listar volumes: %v", err)
	}

	volumes := parseVolumeList(output, *filter)

	if len(volumes) == 0 {
		fmt.Println("\n💾 Nenhum volume encontrado.")
		return
	}

	fmt.Printf("\n💾 VOLUMES ENCONTRADOS: %d\n", len(volumes))
	fmt.Println(strings.Repeat("=", 80))

	for i, v := range volumes {
		fmt.Printf("\n%d. 📁 %s\n", i+1, v.Name)
		fmt.Printf("   Driver: %s\n", v.Driver)

		if *verbose {
			fmt.Printf("   Mount:  %s\n", v.Mountpoint)
			fmt.Printf("   Escopo: %s\n", v.Scope)
		}
	}

	fmt.Println()
}

func listNetworks(args []string) {
	listCmd := flag.NewFlagSet("networks", flag.ExitOnError)
	filter := listCmd.String("filter", "", "Filtra por nome (múltiplos separados por vírgula)")
	verbose := listCmd.Bool("verbose", false, "Exibe informações detalhadas")

	if err := listCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🔍 Listando redes...\n")

	output, err := runCommand("docker", 30*time.Second, "network", "ls", "--format", "{{.ID}}|{{.Name}}|{{.Driver}}|{{.Scope}}")
	if err != nil {
		fatalf("Falha ao listar redes: %v", err)
	}

	networks := parseNetworkList(output, *filter)

	if len(networks) == 0 {
		fmt.Println("\n🌐 Nenhuma rede encontrada.")
		return
	}

	fmt.Printf("\n🌐 REDES ENCONTRADAS: %d\n", len(networks))
	fmt.Println(strings.Repeat("=", 80))

	for i, n := range networks {
		icon := "🔗"
		if n.Name == "bridge" || n.Name == "host" || n.Name == "none" {
			icon = "🔧"
		}

		fmt.Printf("\n%d. %s %s\n", i+1, icon, n.Name)
		fmt.Printf("   ID:     %s\n", n.ID[:12])
		fmt.Printf("   Driver: %s\n", n.Driver)

		if *verbose {
			fmt.Printf("   Escopo: %s\n", n.Scope)
		}
	}

	fmt.Println()
}

func listAll(args []string) {
	listCmd := flag.NewFlagSet("all", flag.ExitOnError)
	verbose := listCmd.Bool("verbose", false, "Exibe informações detalhadas")

	if err := listCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	fmt.Println("\n🐳 RESUMO COMPLETO DO DOCKER")
	fmt.Println(strings.Repeat("=", 80))

	// Containers
	containerOutput, _ := runCommand("docker", 30*time.Second, "ps", "-a", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}|{{.Ports}}|{{.CreatedAt}}")
	containers := parseContainerList(containerOutput, "")

	running := 0
	stopped := 0
	for _, c := range containers {
		if strings.Contains(c.Status, "Up") {
			running++
		} else {
			stopped++
		}
	}

	fmt.Printf("\n📦 CONTAINERS: %d total\n", len(containers))
	fmt.Printf("   ✅ Em execução: %d\n", running)
	fmt.Printf("   ⏹️  Parados: %d\n", stopped)

	// Imagens
	imageOutput, _ := runCommand("docker", 30*time.Second, "images", "--format", "{{.ID}}|{{.Repository}}|{{.Tag}}|{{.Size}}|{{.CreatedAt}}")
	images := parseImageList(imageOutput, "")

	danglingImages, _ := runCommand("docker", 30*time.Second, "images", "-f", "dangling=true", "-q")
	danglingCount := len(strings.Split(strings.TrimSpace(danglingImages), "\n"))
	if danglingImages == "" {
		danglingCount = 0
	}

	fmt.Printf("\n🖼️  IMAGENS: %d total\n", len(images))
	if danglingCount > 0 {
		fmt.Printf("   ⚠️  Sem tag: %d\n", danglingCount)
	}

	// Volumes
	volumeOutput, _ := runCommand("docker", 30*time.Second, "volume", "ls", "--format", "{{.Name}}|{{.Driver}}|{{.Mountpoint}}|{{.Scope}}")
	volumes := parseVolumeList(volumeOutput, "")
	fmt.Printf("\n💾 VOLUMES: %d\n", len(volumes))

	// Redes
	networkOutput, _ := runCommand("docker", 30*time.Second, "network", "ls", "--format", "{{.ID}}|{{.Name}}|{{.Driver}}|{{.Scope}}")
	networks := parseNetworkList(networkOutput, "")
	fmt.Printf("\n🌐 REDES: %d\n", len(networks))

	// Uso de disco
	if *verbose {
		fmt.Println("\n📊 USO DE DISCO:")
		dfOutput, _ := runCommand("docker", 30*time.Second, "system", "df")
		fmt.Println(dfOutput)
	}

	fmt.Println()
}

// Funções auxiliares de parsing

func parseContainerList(output, filter string) []Container {
	var containers []Container
	lines := strings.Split(strings.TrimSpace(output), "\n")
	filters := splitFilters(filter)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 7 {
			c := Container{
				ID:      parts[0],
				Name:    parts[1],
				Image:   parts[2],
				Status:  parts[3],
				Size:    parts[4],
				Ports:   parts[5],
				Created: parts[6],
			}

			if len(filters) == 0 || matchesAnyFilter(c.Name, filters) || matchesAnyFilter(c.Image, filters) {
				containers = append(containers, c)
			}
		}
	}

	return containers
}

func parseImageList(output, filter string) []Image {
	var images []Image
	lines := strings.Split(strings.TrimSpace(output), "\n")
	filters := splitFilters(filter)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 5 {
			repo := parts[1]
			tag := parts[2]
			fullName := fmt.Sprintf("%s:%s", repo, tag)

			img := Image{
				ID:         parts[0],
				Repository: repo,
				Tag:        tag,
				Size:       parts[3],
				Created:    parts[4],
				FullName:   fullName,
			}

			if len(filters) == 0 || matchesAnyFilter(fullName, filters) || matchesAnyFilter(repo, filters) {
				images = append(images, img)
			}
		}
	}

	return images
}

func parseVolumeList(output, filter string) []Volume {
	var volumes []Volume
	lines := strings.Split(strings.TrimSpace(output), "\n")
	filters := splitFilters(filter)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 4 {
			v := Volume{
				Name:       parts[0],
				Driver:     parts[1],
				Mountpoint: parts[2],
				Scope:      parts[3],
			}

			if len(filters) == 0 || matchesAnyFilter(v.Name, filters) {
				volumes = append(volumes, v)
			}
		}
	}

	return volumes
}

func parseNetworkList(output, filter string) []Network {
	var networks []Network
	lines := strings.Split(strings.TrimSpace(output), "\n")
	filters := splitFilters(filter)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 4 {
			n := Network{
				ID:     parts[0],
				Name:   parts[1],
				Driver: parts[2],
				Scope:  parts[3],
			}

			if len(filters) == 0 || matchesAnyFilter(n.Name, filters) {
				networks = append(networks, n)
			}
		}
	}

	return networks
}

func ensureDependencies(deps ...string) error {
	for _, dep := range deps {
		_, err := exec.LookPath(dep)
		if err != nil {
			return fmt.Errorf("dependência '%s' não encontrada", dep)
		}
	}
	return nil
}

func runCommand(name string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("comando expirou após %v", timeout)
	}

	return out.String(), err
}

func splitFilters(filter string) []string {
	if filter == "" {
		return nil
	}
	filters := strings.Split(filter, ",")
	result := make([]string, 0, len(filters))
	for _, f := range filters {
		trimmed := strings.TrimSpace(f)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func matchesAnyFilter(item string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	itemLower := strings.ToLower(item)
	for _, filter := range filters {
		if strings.Contains(itemLower, strings.ToLower(filter)) {
			return true
		}
	}
	return false
}
