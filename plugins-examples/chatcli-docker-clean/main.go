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
	ID     string
	Name   string
	Image  string
	Status string
	Size   string
}

type Image struct {
	ID         string
	Repository string
	Tag        string
	Size       string
	FullName   string // Repository:Tag completo para filtro
}

type Volume struct {
	Name   string
	Driver string
	Size   string
}

type Network struct {
	ID     string
	Name   string
	Driver string
}

func logf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
	_ = os.Stderr.Sync() //#nosec G104 -- example plugin / dev tool — best-effort cleanup
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
		fatalf("Uso: @docker-clean <containers|images|all|volumes|networks|system> [opções]")
	}

	subcommand := args[0]
	subcommandArgs := args[1:]

	switch subcommand {
	case "containers":
		cleanContainers(subcommandArgs)
	case "images":
		cleanImages(subcommandArgs)
	case "volumes":
		cleanVolumes(subcommandArgs)
	case "networks":
		cleanNetworks(subcommandArgs)
	case "all":
		cleanAll(subcommandArgs)
	case "system":
		cleanSystem(subcommandArgs)
	default:
		fatalf("Subcomando desconhecido: %s", subcommand)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@docker-clean",
		Description: "Gerencia, remove containers, imagens, volumes e redes Docker (suporta operações em lote)",
		Usage: `@docker-clean <comando> [opções]
    
    Exemplos:
      # Múltiplos filtros
      @docker-clean images --filter nginx,redis,postgres
      @docker-clean containers --filter web,api,worker
      
      # IDs específicos
      @docker-clean images --ids abc123,def456,ghi789
      @docker-clean containers --ids container1,container2
      
      # Combinado com dry-run
      @docker-clean images --filter nginx,redis --dry-run`,
		Version: "2.2.0",
	}
	jsonMeta, _ := json.Marshal(meta)
	fmt.Println(string(jsonMeta))
}

func cleanContainers(args []string) {
	cleanCmd := flag.NewFlagSet("containers", flag.ExitOnError)
	all := cleanCmd.Bool("all", false, "Remove todos os containers (incluindo em execução)")
	filter := cleanCmd.String("filter", "", "Filtra containers (aceita múltiplos separados por vírgula)")
	ids := cleanCmd.String("ids", "", "IDs específicos para remover (separados por vírgula)")
	dryRun := cleanCmd.Bool("dry-run", false, "Apenas lista containers sem remover")

	if err := cleanCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	var containers []Container

	// Se IDs específicos foram fornecidos
	if *ids != "" {
		logf("🔍 Buscando containers específicos...\n")
		specificIDs := splitFilters(*ids)
		for _, id := range specificIDs {
			output, err := runCommand("docker", 10*time.Second, "ps", "-a", "--filter", fmt.Sprintf("id=%s", id), "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}")
			if err == nil && strings.TrimSpace(output) != "" {
				lines := strings.Split(strings.TrimSpace(output), "\n")
				for _, line := range lines {
					if line == "" {
						continue
					}
					parts := strings.Split(line, "|")
					if len(parts) >= 5 {
						containers = append(containers, Container{
							ID:     parts[0],
							Name:   parts[1],
							Image:  parts[2],
							Status: parts[3],
							Size:   parts[4],
						})
					}
				}
			} else {
				// Tentar buscar por nome também
				output, err = runCommand("docker", 10*time.Second, "ps", "-a", "--filter", fmt.Sprintf("name=%s", id), "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}")
				if err == nil && strings.TrimSpace(output) != "" {
					lines := strings.Split(strings.TrimSpace(output), "\n")
					for _, line := range lines {
						if line == "" {
							continue
						}
						parts := strings.Split(line, "|")
						if len(parts) >= 5 {
							containers = append(containers, Container{
								ID:     parts[0],
								Name:   parts[1],
								Image:  parts[2],
								Status: parts[3],
								Size:   parts[4],
							})
						}
					}
				} else {
					logf("⚠️  Container '%s' não encontrado\n", id)
				}
			}
		}
	} else {
		// Lógica existente de listagem
		logf("🔍 Listando containers...\n")
		listArgs := []string{"ps", "-a", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}"}
		if !*all {
			listArgs = []string{"ps", "-a", "-f", "status=exited", "-f", "status=created", "--format", "{{.ID}}|{{.Names}}|{{.Image}}|{{.Status}}|{{.Size}}"}
		}
		output, err := runCommand("docker", 30*time.Second, listArgs...)
		if err != nil {
			fatalf("Falha ao listar containers: %v", err)
		}
		containers = parseContainerList(output, *filter)
	}

	if len(containers) == 0 {
		fmt.Println("\n✅ Nenhum container encontrado para remover.")
		return
	}

	logf("📦 Encontrados %d containers:\n\n", len(containers))

	// Mostrar lista para a IA
	fmt.Println("CONTAINERS ENCONTRADOS:")
	for i, c := range containers {
		fmt.Printf("%d. %s (ID: %s, Imagem: %s, Status: %s, Tamanho: %s)\n",
			i+1, c.Name, c.ID[:12], c.Image, c.Status, c.Size)
	}
	fmt.Println()

	if *dryRun {
		fmt.Printf("ℹ️  Modo dry-run: %d container(s) seriam removidos.\n", len(containers))
		return
	}

	// Remover containers
	logf("🗑️  Removendo %d container(s)...\n", len(containers))
	removed := 0
	failed := 0

	for _, c := range containers {
		removeArgs := []string{"rm", c.ID}
		if *all || strings.Contains(c.Status, "Up") {
			removeArgs = []string{"rm", "-f", c.ID}
		}

		logf("⏳ Removendo %s (%s)...", c.Name, c.ID[:12])
		_, err := runCommand("docker", 30*time.Second, removeArgs...)
		if err != nil {
			logf(" ❌ FALHOU\n")
			failed++
		} else {
			logf(" ✅ OK\n")
			removed++
		}
	}

	fmt.Printf("\n✅ Concluído: %d removidos, %d falhas\n", removed, failed)
}

func cleanImages(args []string) {
	cleanCmd := flag.NewFlagSet("images", flag.ExitOnError)
	dangling := cleanCmd.Bool("dangling", false, "Remove apenas imagens 'dangling' (sem tag)")
	filter := cleanCmd.String("filter", "", "Filtra imagens por nome (aceita múltiplos separados por vírgula)")
	ids := cleanCmd.String("ids", "", "IDs específicos para remover (separados por vírgula)")
	dryRun := cleanCmd.Bool("dry-run", false, "Apenas lista imagens sem remover")
	unused := cleanCmd.Bool("unused", false, "Remove todas as imagens não utilizadas")

	if err := cleanCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	var images []Image

	// Se IDs específicos foram fornecidos
	if *ids != "" {
		logf("🔍 Buscando imagens específicas...\n")
		specificIDs := splitFilters(*ids)
		for _, id := range specificIDs {
			// Busca informações da imagem
			output, err := runCommand("docker", 10*time.Second, "images", "--format", "{{.ID}}|{{.Repository}}|{{.Tag}}|{{.Size}}", "--filter", fmt.Sprintf("id=%s", id))
			if err == nil && strings.TrimSpace(output) != "" {
				lines := strings.Split(strings.TrimSpace(output), "\n")
				for _, line := range lines {
					if line == "" {
						continue
					}
					parts := strings.Split(line, "|")
					if len(parts) >= 4 {
						repo := parts[1]
						tag := parts[2]
						images = append(images, Image{
							ID:         parts[0],
							Repository: repo,
							Tag:        tag,
							Size:       parts[3],
							FullName:   fmt.Sprintf("%s:%s", repo, tag),
						})
					}
				}
			} else {
				logf("⚠️  Imagem com ID '%s' não encontrada\n", id)
			}
		}
	} else {
		// Lógica existente de listagem
		logf("🔍 Listando imagens...\n")
		listArgs := []string{"images", "--format", "{{.ID}}|{{.Repository}}|{{.Tag}}|{{.Size}}"}
		if *dangling {
			listArgs = append(listArgs, "-f", "dangling=true")
		}
		output, err := runCommand("docker", 30*time.Second, listArgs...)
		if err != nil {
			fatalf("Falha ao listar imagens: %v", err)
		}
		images = parseImageList(output, *filter)
	}

	if len(images) == 0 {
		fmt.Println("\n✅ Nenhuma imagem encontrada para remover.")
		return
	}

	logf("🖼️  Encontradas %d imagens:\n\n", len(images))

	// Mostrar lista para a IA
	fmt.Println("IMAGENS ENCONTRADAS:")
	for i, img := range images {
		displayName := img.FullName
		if img.Repository == "<none>" {
			displayName = img.ID[:12] + " (sem tag)"
		}
		fmt.Printf("%d. %s (ID: %s, Tamanho: %s)\n",
			i+1, displayName, img.ID[:12], img.Size)
	}
	fmt.Println()

	if *dryRun {
		fmt.Printf("ℹ️  Modo dry-run: %d imagem(s) seriam removidas.\n", len(images))
		return
	}

	// Remover imagens
	logf("🗑️  Removendo %d imagem(s)...\n", len(images))
	removed := 0
	failed := 0

	for _, img := range images {
		displayName := img.FullName
		removeID := img.ID

		if img.Repository == "<none>" {
			displayName = img.ID[:12]
		}

		logf("⏳ Removendo %s...", displayName)

		removeArgs := []string{"rmi", removeID}
		if *unused {
			removeArgs = []string{"rmi", "-f", removeID}
		}

		_, err := runCommand("docker", 30*time.Second, removeArgs...)
		if err != nil {
			logf(" ❌ FALHOU\n")
			failed++
		} else {
			logf(" ✅ OK\n")
			removed++
		}
	}

	fmt.Printf("\n✅ Concluído: %d removidas, %d falhas\n", removed, failed)
}

func cleanVolumes(args []string) {
	cleanCmd := flag.NewFlagSet("volumes", flag.ExitOnError)
	filter := cleanCmd.String("filter", "", "Filtra volumes por nome (aceita múltiplos separados por vírgula)")
	names := cleanCmd.String("names", "", "Nomes específicos de volumes (separados por vírgula)")
	dryRun := cleanCmd.Bool("dry-run", false, "Apenas lista volumes sem remover")
	all := cleanCmd.Bool("all", false, "Lista todos os volumes (não apenas os não utilizados)")

	if err := cleanCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	var volumes []Volume

	if *names != "" {
		// Volumes específicos
		logf("🔍 Buscando volumes específicos...\n")
		specificNames := splitFilters(*names)
		for _, name := range specificNames {
			output, err := runCommand("docker", 10*time.Second, "volume", "inspect", "--format", "{{.Name}}|{{.Driver}}", name)
			if err == nil && strings.TrimSpace(output) != "" {
				parts := strings.Split(strings.TrimSpace(output), "|")
				if len(parts) >= 2 {
					volumes = append(volumes, Volume{
						Name:   parts[0],
						Driver: parts[1],
					})
				}
			} else {
				logf("⚠️  Volume '%s' não encontrado\n", name)
			}
		}
	} else {
		logf("🔍 Listando volumes...\n")
		listArgs := []string{"volume", "ls", "--format", "{{.Name}}|{{.Driver}}"}
		if !*all {
			listArgs = append(listArgs, "-f", "dangling=true")
		}

		output, err := runCommand("docker", 30*time.Second, listArgs...)
		if err != nil {
			fatalf("Falha ao listar volumes: %v", err)
		}

		volumeLines := strings.Split(strings.TrimSpace(output), "\n")
		filters := splitFilters(*filter)

		for _, line := range volumeLines {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				vol := Volume{
					Name:   parts[0],
					Driver: parts[1],
				}
				if len(filters) == 0 || matchesAnyFilter(vol.Name, filters) {
					volumes = append(volumes, vol)
				}
			}
		}
	}

	if len(volumes) == 0 {
		if *all {
			fmt.Println("\n✅ Nenhum volume encontrado.")
		} else {
			fmt.Println("\n✅ Nenhum volume não utilizado encontrado.")
		}
		return
	}

	logf("💾 Encontrados %d volumes:\n\n", len(volumes))

	fmt.Println("VOLUMES ENCONTRADOS:")
	for i, v := range volumes {
		fmt.Printf("%d. %s (Driver: %s)\n", i+1, v.Name, v.Driver)
	}
	fmt.Println()

	if *dryRun {
		fmt.Printf("ℹ️  Modo dry-run: %d volume(s) seriam removidos.\n", len(volumes))
		return
	}

	logf("🗑️  Removendo volumes...\n")
	removed := 0
	failed := 0

	// Se nomes específicos foram fornecidos, remove individualmente
	if *names != "" {
		for _, vol := range volumes {
			logf("⏳ Removendo %s...", vol.Name)
			_, err := runCommand("docker", 30*time.Second, "volume", "rm", vol.Name)
			if err != nil {
				logf(" ❌ FALHOU\n")
				failed++
			} else {
				logf(" ✅ OK\n")
				removed++
			}
		}
	} else {
		// Usa prune para volumes não utilizados
		_, err := runCommand("docker", 60*time.Second, "volume", "prune", "-f")
		if err != nil {
			fatalf("Falha ao remover volumes: %v", err)
		}
		removed = len(volumes)
	}

	fmt.Printf("\n✅ Concluído: %d volume(s) removidos", removed)
	if failed > 0 {
		fmt.Printf(", %d falhas", failed)
	}
	fmt.Println()
}

func cleanNetworks(args []string) {
	cleanCmd := flag.NewFlagSet("networks", flag.ExitOnError)
	dryRun := cleanCmd.Bool("dry-run", false, "Apenas lista redes sem remover")

	if err := cleanCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🔍 Listando redes não utilizadas...\n")

	output, err := runCommand("docker", 30*time.Second, "network", "ls", "--format", "{{.ID}}|{{.Name}}|{{.Driver}}", "--filter", "type=custom")
	if err != nil {
		fatalf("Falha ao listar redes: %v", err)
	}

	networkLines := strings.Split(strings.TrimSpace(output), "\n")
	var networks []Network

	for _, line := range networkLines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 3 {
			networks = append(networks, Network{
				ID:     parts[0],
				Name:   parts[1],
				Driver: parts[2],
			})
		}
	}

	if len(networks) == 0 {
		fmt.Println("\n✅ Nenhuma rede não utilizada encontrada.")
		return
	}

	logf("🌐 Encontradas %d redes:\n\n", len(networks))

	fmt.Println("REDES ENCONTRADAS:")
	for i, n := range networks {
		fmt.Printf("%d. %s (ID: %s, Driver: %s)\n", i+1, n.Name, n.ID[:12], n.Driver)
	}
	fmt.Println()

	if *dryRun {
		fmt.Printf("ℹ️  Modo dry-run: até %d rede(s) seriam removidas.\n", len(networks))
		return
	}

	logf("🗑️  Removendo redes...\n")
	_, err = runCommand("docker", 60*time.Second, "network", "prune", "-f")
	if err != nil {
		fatalf("Falha ao remover redes: %v", err)
	}

	fmt.Println("\n✅ Redes não utilizadas removidas com sucesso.")
}

func cleanAll(args []string) {
	cleanCmd := flag.NewFlagSet("all", flag.ExitOnError)
	dryRun := cleanCmd.Bool("dry-run", false, "Apenas lista recursos sem remover")
	includeRunning := cleanCmd.Bool("include-running", false, "Inclui containers em execução")

	if err := cleanCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🧹 Limpeza completa iniciada...\n\n")

	if *dryRun {
		fmt.Println("ℹ️  Modo dry-run ativado. Use comandos específicos para ver detalhes:")
		fmt.Println("   • @docker-clean containers --dry-run")
		fmt.Println("   • @docker-clean images --dry-run")
		fmt.Println("   • @docker-clean volumes --dry-run")
		fmt.Println("   • @docker-clean networks --dry-run")
		return
	}

	logf("⚠️  Iniciando limpeza completa do Docker...\n\n")

	removed := 0
	failed := 0

	// 1. Remover containers
	logf("1️⃣ Removendo containers...\n")
	containerListArgs := []string{"ps", "-a", "-q"}
	if !*includeRunning {
		containerListArgs = []string{"ps", "-a", "-q", "-f", "status=exited", "-f", "status=created"}
	}

	containerOutput, err := runCommand("docker", 30*time.Second, containerListArgs...)
	if err == nil && strings.TrimSpace(containerOutput) != "" {
		containerIDs := strings.Split(strings.TrimSpace(containerOutput), "\n")
		for _, id := range containerIDs {
			removeArgs := []string{"rm", id}
			if *includeRunning {
				removeArgs = []string{"rm", "-f", id}
			}

			_, err := runCommand("docker", 30*time.Second, removeArgs...)
			if err != nil {
				logf("   ❌ Falha: %s\n", id[:12])
				failed++
			} else {
				logf("   ✅ Removido: %s\n", id[:12])
				removed++
			}
		}
	}

	// 2. Remover imagens
	logf("\n2️⃣ Removendo imagens não utilizadas...\n")
	_, _ = runCommand("docker", 120*time.Second, "image", "prune", "-a", "-f")

	// 3. Remover volumes
	logf("\n3️⃣ Removendo volumes não utilizados...\n")
	_, _ = runCommand("docker", 60*time.Second, "volume", "prune", "-f")

	// 4. Remover redes
	logf("\n4️⃣ Removendo redes não utilizadas...\n")
	_, _ = runCommand("docker", 60*time.Second, "network", "prune", "-f")

	// 5. Espaço em disco
	logf("\n📊 Uso de disco após limpeza:\n")
	dfOutput, _ := runCommand("docker", 30*time.Second, "system", "df")
	fmt.Println(dfOutput)

	fmt.Printf("\n✅ Limpeza completa finalizada. Containers removidos: %d, Falhas: %d\n", removed, failed)
}

func cleanSystem(args []string) {
	cleanCmd := flag.NewFlagSet("system", flag.ExitOnError)
	all := cleanCmd.Bool("all", false, "Remove todas as imagens")
	volumes := cleanCmd.Bool("volumes", false, "Inclui volumes")
	dryRun := cleanCmd.Bool("dry-run", false, "Apenas mostra o que seria removido")

	if err := cleanCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	if *dryRun {
		output, _ := runCommand("docker", 30*time.Second, "system", "df")
		fmt.Println("USO ATUAL DO DISCO:")
		fmt.Println(output)
		return
	}

	cmdArgs := []string{"system", "prune", "-f"}
	if *all {
		cmdArgs = append(cmdArgs, "-a")
	}
	if *volumes {
		cmdArgs = append(cmdArgs, "--volumes")
	}

	logf("🧹 Executando limpeza do sistema Docker...\n")
	output, err := runCommand("docker", 180*time.Second, cmdArgs...)
	if err != nil {
		fatalf("Falha na limpeza: %v", err)
	}

	fmt.Println(output)
	fmt.Println("\n✅ Limpeza do sistema concluída.")
}

func parseContainerList(output, filter string) []Container {
	var containers []Container
	lines := strings.Split(strings.TrimSpace(output), "\n")
	filters := splitFilters(filter)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 5 {
			c := Container{
				ID:     parts[0],
				Name:   parts[1],
				Image:  parts[2],
				Status: parts[3],
				Size:   parts[4],
			}

			if len(filters) == 0 ||
				matchesAnyFilter(c.Name, filters) ||
				matchesAnyFilter(c.ID, filters) ||
				matchesAnyFilter(c.Image, filters) {
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
		if len(parts) >= 4 {
			repo := parts[1]
			tag := parts[2]
			fullName := fmt.Sprintf("%s:%s", repo, tag)

			img := Image{
				ID:         parts[0],
				Repository: repo,
				Tag:        tag,
				Size:       parts[3],
				FullName:   fullName,
			}

			// Verifica múltiplos filtros
			if len(filters) == 0 ||
				matchesAnyFilter(fullName, filters) ||
				matchesAnyFilter(repo, filters) ||
				matchesAnyFilter(img.ID, filters) {
				images = append(images, img)
			}
		}
	}

	return images
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

	cmd := exec.CommandContext(ctx, name, args...) //#nosec G204 -- example plugin / dev tool — subprocess invocation is the entire purpose
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), fmt.Errorf("comando expirou após %v", timeout)
	}

	return out.String(), err
}

// splitFilters divide filtros separados por vírgula
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

// matchesAnyFilter verifica se o item corresponde a qualquer filtro
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
