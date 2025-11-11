package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

const defaultIstioVersion = "1.22.1"

// logf escreve mensagens de progresso e log para stderr, mantendo stdout limpo para o resultado.
func logf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format, v...)
	os.Stderr.Sync() // Força flush imediato
}

// fatalf escreve uma mensagem de erro para stderr e encerra o programa com status 1.
func fatalf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ Erro: "+format+"\n", v...)
	os.Exit(1)
}

// keepAlive envia sinais periódicos de atividade
func keepAlive(ctx context.Context, interval time.Duration, message string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logf("   ⏳ %s (ainda processando...)\n", message)
		}
	}
}

func main() {
	metadataFlag := flag.Bool("metadata", false, "Exibe os metadados do plugin em formato JSON")
	flag.Parse()

	if *metadataFlag {
		printMetadata()
		return
	}

	if err := ensureDependencies("docker", "curl", "kind"); err != nil {
		fatalf("Erro de dependência: %v", err)
	}

	args := flag.Args()
	if len(args) == 0 {
		fatalf("Uso: @kind <create|delete|list> [opções]")
	}

	subcommand := args[0]
	subcommandArgs := args[1:]

	switch subcommand {
	case "create":
		createCluster(subcommandArgs)
	case "delete":
		deleteCluster(subcommandArgs)
	case "list":
		listClusters()
	default:
		fatalf("Subcomando desconhecido: %s. Use create, delete, ou list.", subcommand)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@kind",
		Description: "Gerencia clusters Kubernetes locais com o Kind. Otimizado para macOS. Suporta Istio e Nginx Ingress.",
		Usage:       "@kind <create|delete|list> [--name <nome>] [--k8s-version <ver>] [--with-istio] [--istio-version <ver>] [--istio-profile <perfil>] [--with-nginx-ingress]",
		Version:     "2.3.0",
	}
	jsonMeta, _ := json.Marshal(meta)
	fmt.Println(string(jsonMeta))
}

func createCluster(args []string) {
	createCmd := flag.NewFlagSet("create", flag.ExitOnError)
	clusterName := createCmd.String("name", "kind", "Nome do cluster Kind")
	k8sVersion := createCmd.String("k8s-version", "", "Versão do Kubernetes a ser usada (ex: 1.28.0)")
	withIstio := createCmd.Bool("with-istio", false, "Instala o Istio no cluster após a criação")
	istioVersion := createCmd.String("istio-version", defaultIstioVersion, "Versão do Istio a ser instalada")
	istioProfile := createCmd.String("istio-profile", "demo", "Perfil de instalação do Istio (ex: demo, default)")
	withNginxIngress := createCmd.Bool("with-nginx-ingress", false, "Instala o Nginx Ingress Controller (recomendado para macOS)")
	if err := createCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	isMacOS := runtime.GOOS == "darwin"

	// Validar combinação de flags
	if *withIstio && *withNginxIngress {
		logf("⚠️  AVISO: Istio e Nginx Ingress solicitados juntos.\n")
		logf("   Configurando Nginx nas portas 8080/8443 para evitar conflitos.\n")
		logf("   Istio usará as portas 80/443 (padrão).\n\n")
	}

	// No macOS, avisar sobre otimizações
	if isMacOS {
		logf("🍎 macOS detectado! Aplicando otimizações para Docker Desktop...\n")
		logf("   ✓ Mapeamento de portas otimizado para localhost\n")
		logf("   ✓ Configuração otimizada para Ingress Controller\n")
		if *withIstio {
			logf("   ✓ Istio será configurado com NodePort (compatível com macOS)\n")
		}
		logf("\n")
	}

	var configPath string
	var err error

	// Criar configuração otimizada se necessário
	if isMacOS || *withIstio || *withNginxIngress {
		configPath, err = createKindConfig(*clusterName, isMacOS, *withIstio, *withNginxIngress)
		if err != nil {
			fatalf("Falha ao criar configuração do Kind: %v", err)
		}
		defer os.Remove(configPath)
	}

	cmdArgs := []string{"create", "cluster", "--name", *clusterName}

	if configPath != "" {
		cmdArgs = append(cmdArgs, "--config", configPath)
	}

	if *k8sVersion != "" {
		imageTag := fmt.Sprintf("kindest/node:v%s", *k8sVersion)
		cmdArgs = append(cmdArgs, "--image", imageTag)
		logf("🚀 Subindo um novo cluster Kind ('%s') com Kubernetes v%s...\n", *clusterName, *k8sVersion)
	} else {
		logf("🚀 Subindo um novo cluster Kind ('%s') com a versão padrão do Kubernetes...\n", *clusterName)
	}

	output, err := runCommand("kind", 5*time.Minute, cmdArgs...)
	if err != nil {
		fatalf("Falha ao criar o cluster Kind:\n%s", output)
	}
	logf("✅ Cluster Kind criado com sucesso!\n")

	// Aguardar cluster ficar pronto
	logf("⏳ Aguardando cluster ficar completamente pronto...\n")
	if err := waitForClusterReady(*clusterName); err != nil {
		logf("⚠️  Aviso: %v\n", err)
	}
	time.Sleep(5 * time.Second)

	// Instalar Nginx Ingress se solicitado
	if *withNginxIngress {
		logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		logf("🌐 Instalando Nginx Ingress Controller...\n")
		nginxPort := 80
		nginxPortTLS := 443
		if *withIstio {
			nginxPort = 8080
			nginxPortTLS = 8443
		}
		if err := installNginxIngress(nginxPort, nginxPortTLS); err != nil {
			fatalf("Falha ao instalar Nginx Ingress: %v", err)
		}
		logf("✅ Nginx Ingress instalado com sucesso!\n")
		logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	// Instalar Istio se solicitado
	if *withIstio {
		logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		logf("✨ Iniciando instalação do Istio...\n")
		if err := installIstio(*clusterName, *istioVersion, *istioProfile, isMacOS); err != nil {
			fatalf("Falha ao instalar o Istio: %v", err)
		}
		logf("✅ Istio instalado e configurado com sucesso!\n")
		logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	// Mensagem final
	fmt.Printf("\n✅ Cluster '%s' criado com sucesso!\n\n", *clusterName)

	if isMacOS {
		fmt.Println("🍎 Acesso no macOS:")
		if *withNginxIngress && !*withIstio {
			fmt.Println("   • Nginx Ingress: http://localhost e https://localhost")
			fmt.Println("   • Crie recursos Ingress para rotear tráfego")
		}
		if *withNginxIngress && *withIstio {
			fmt.Println("   • Nginx Ingress: http://localhost:8080 e https://localhost:8443")
			fmt.Println("   • Istio Gateway: http://localhost:80 e https://localhost:443")
			fmt.Println("   • Use Ingress para Nginx ou Gateway/VirtualService para Istio")
		}
		if *withIstio && !*withNginxIngress {
			fmt.Println("   • Istio Gateway: http://localhost:80 e https://localhost:443")
			fmt.Println("   • Use Gateway e VirtualService para rotear tráfego")
		}
		if !*withNginxIngress && !*withIstio {
			fmt.Println("   💡 Dica: Use --with-nginx-ingress ou --with-istio para acesso fácil via localhost")
		}
		fmt.Println()
	}

	fmt.Println("💡 Comandos úteis:")
	fmt.Printf("   kubectl config use-context kind-%s\n", *clusterName)
	fmt.Println("   kubectl cluster-info")
	fmt.Println("   kubectl get nodes")
	if *withIstio {
		fmt.Println("   kubectl get pods -n istio-system")
		fmt.Println("   istioctl version")
	}
	if *withNginxIngress {
		fmt.Println("   kubectl get pods -n ingress-nginx")
	}
}

func waitForClusterReady(clusterName string) error {
	logf("   - Aguardando nodes ficarem prontos...\n")

	maxRetries := 60 // 2 minutos
	for i := 0; i < maxRetries; i++ {
		output, err := runCommandWithTimeout("kubectl", 30*time.Second, "get", "nodes", "--no-headers")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(output), "\n")
			allReady := true
			for _, line := range lines {
				if line != "" && !strings.Contains(line, " Ready ") {
					allReady = false
					break
				}
			}
			if allReady && len(lines) > 0 {
				logf("   ✓ Nodes prontos\n")
				return nil
			}
		}

		if i > 0 && i%10 == 0 {
			logf("   ⏳ Ainda aguardando nodes... (%d segundos)\n", i*2)
		}
		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout aguardando nodes ficarem prontos")
}

func createKindConfig(clusterName string, isMacOS, withIstio, withNginxIngress bool) (string, error) {
	tempFile, err := os.CreateTemp("", "kind-config-*.yaml")
	if err != nil {
		return "", err
	}
	defer tempFile.Close()

	var config strings.Builder

	config.WriteString("kind: Cluster\n")
	config.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")
	config.WriteString("nodes:\n")
	config.WriteString("- role: control-plane\n")

	// Adicionar configurações para macOS ou quando Ingress é necessário
	if isMacOS || withNginxIngress || withIstio {
		config.WriteString("  kubeadmConfigPatches:\n")
		config.WriteString("  - |\n")
		config.WriteString("    kind: InitConfiguration\n")
		config.WriteString("    nodeRegistration:\n")
		config.WriteString("      kubeletExtraArgs:\n")
		config.WriteString("        node-labels: \"ingress-ready=true\"\n")

		config.WriteString("  extraPortMappings:\n")

		// Configurar portas para Nginx (evitar conflito com Istio)
		if withNginxIngress {
			if withIstio {
				config.WriteString("  - containerPort: 80\n")
				config.WriteString("    hostPort: 8080\n")
				config.WriteString("    protocol: TCP\n")
				config.WriteString("  - containerPort: 443\n")
				config.WriteString("    hostPort: 8443\n")
				config.WriteString("    protocol: TCP\n")
			} else {
				config.WriteString("  - containerPort: 80\n")
				config.WriteString("    hostPort: 80\n")
				config.WriteString("    protocol: TCP\n")
				config.WriteString("  - containerPort: 443\n")
				config.WriteString("    hostPort: 443\n")
				config.WriteString("    protocol: TCP\n")
			}
		}

		// Configurar portas para Istio
		if withIstio {
			if isMacOS {
				config.WriteString("  - containerPort: 30080\n")
				config.WriteString("    hostPort: 80\n")
				config.WriteString("    protocol: TCP\n")
				config.WriteString("  - containerPort: 30443\n")
				config.WriteString("    hostPort: 443\n")
				config.WriteString("    protocol: TCP\n")
				config.WriteString("  - containerPort: 30021\n")
				config.WriteString("    hostPort: 15021\n")
				config.WriteString("    protocol: TCP\n")
			} else {
				if !withNginxIngress {
					config.WriteString("  - containerPort: 80\n")
					config.WriteString("    hostPort: 80\n")
					config.WriteString("    protocol: TCP\n")
					config.WriteString("  - containerPort: 443\n")
					config.WriteString("    hostPort: 443\n")
					config.WriteString("    protocol: TCP\n")
				}
			}
		}
	}

	if _, err := tempFile.WriteString(config.String()); err != nil {
		return "", err
	}

	return tempFile.Name(), nil
}

func installNginxIngress(httpPort, httpsPort int) error {
	logf("   📦 Aplicando manifesto do Nginx Ingress Controller...\n")

	manifestURL := "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml"

	if output, err := runCommand("kubectl", 2*time.Minute, "apply", "-f", manifestURL); err != nil {
		return fmt.Errorf("falha ao aplicar manifesto: %s", output)
	}

	logf("   ⏳ Aguardando recursos serem criados...\n")
	time.Sleep(10 * time.Second)

	logf("   ⏳ Aguardando deployment do Nginx Ingress Controller (pode levar até 3 minutos)...\n")

	deploymentReady := waitForResource(
		"deployment",
		"ingress-nginx",
		"ingress-nginx-controller",
		3*time.Minute,
		5*time.Second,
	)

	if !deploymentReady {
		logf("   ⚠️  Coletando informações de debug...\n")
		if output, _ := runCommandWithTimeout("kubectl", 30*time.Second, "get", "all", "-n", "ingress-nginx"); output != "" {
			logf("   Debug - recursos em ingress-nginx:\n%s\n", output)
		}
		return fmt.Errorf("timeout aguardando deployment do Nginx Ingress ser criado")
	}

	logf("   ⏳ Aguardando pods ficarem prontos (pode levar até 3 minutos)...\n")

	podsReady := waitForPodsReady("ingress-nginx", "app.kubernetes.io/component=controller", 3*time.Minute, 5*time.Second)

	if !podsReady {
		logf("   ⚠️  Coletando informações de debug...\n")
		if output, _ := runCommandWithTimeout("kubectl", 30*time.Second, "get", "pods", "-n", "ingress-nginx", "-o", "wide"); output != "" {
			logf("   Pods:\n%s\n", output)
		}
		return fmt.Errorf("timeout aguardando pods do Nginx Ingress ficarem prontos")
	}

	logf("   ✓ Nginx Ingress Controller está pronto!\n")

	if output, err := runCommandWithTimeout("kubectl", 30*time.Second, "get", "svc", "-n", "ingress-nginx", "ingress-nginx-controller"); err == nil {
		logf("   ✓ Serviço configurado:\n%s\n", output)
	}

	return nil
}

func waitForResource(resourceType, namespace, name string, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	iterations := 0
	lastLog := time.Now()

	for time.Now().Before(deadline) {
		iterations++
		output, err := runCommandWithTimeout("kubectl", 30*time.Second, "get", resourceType, "-n", namespace, name, "--ignore-not-found")

		if err == nil && strings.Contains(output, name) {
			logf("   ✓ %s/%s criado\n", resourceType, name)
			return true
		}

		// Mostrar progresso a cada 15 segundos
		if time.Since(lastLog) >= 15*time.Second {
			remaining := time.Until(deadline)
			logf("   ⏳ Aguardando %s/%s... (%.0f segundos restantes)\n", resourceType, name, remaining.Seconds())
			lastLog = time.Now()
		}

		time.Sleep(interval)
	}

	return false
}

func waitForPodsReady(namespace, labelSelector string, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	iterations := 0
	lastStatus := ""
	lastLog := time.Now()

	for time.Now().Before(deadline) {
		iterations++

		output, err := runCommandWithTimeout("kubectl", 30*time.Second, "get", "pods", "-n", namespace, "-l", labelSelector, "-o", "jsonpath={range .items[*]}{.metadata.name}{'|'}{.status.phase}{'|'}{range .status.conditions[?(@.type=='Ready')]}{.status}{end}{'\\n'}{end}")

		if err == nil && output != "" {
			lines := strings.Split(strings.TrimSpace(output), "\n")
			allReady := true
			statusSummary := ""

			for _, line := range lines {
				if line == "" {
					continue
				}
				parts := strings.Split(line, "|")
				if len(parts) >= 3 {
					podName := parts[0]
					phase := parts[1]
					ready := parts[2]

					statusSummary += fmt.Sprintf("%s: %s/%s ", podName, phase, ready)

					if phase != "Running" || ready != "True" {
						allReady = false
					}
				}
			}

			if statusSummary != lastStatus && time.Since(lastLog) >= 15*time.Second {
				logf("   📊 Status: %s\n", statusSummary)
				lastStatus = statusSummary
				lastLog = time.Now()
			}

			if allReady && len(lines) > 0 {
				logf("   ✓ Todos os pods estão prontos!\n")
				return true
			}
		}

		// Mostrar progresso a cada 15 segundos
		if time.Since(lastLog) >= 15*time.Second {
			remaining := time.Until(deadline)
			logf("   ⏳ Aguardando pods... (%.0f segundos restantes)\n", remaining.Seconds())
			lastLog = time.Now()
		}

		time.Sleep(interval)
	}

	return false
}

func ensureIstioctl(version string) (string, error) {
	if path, err := exec.LookPath("istioctl"); err == nil {
		logf("   ✓ istioctl encontrado em: %s\n", path)
		if output, err := runCommandWithTimeout(path, 30*time.Second, "version", "--remote=false"); err == nil {
			logf("   ✓ Versão instalada: %s", output)
		}
		return path, nil
	}

	logf("   📥 istioctl não encontrado, instalando versão %s...\n", version)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("não foi possível encontrar o diretório home: %w", err)
	}

	installDir := filepath.Join(homeDir, ".local", "bin")
	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		if err := os.MkdirAll(installDir, 0755); err != nil {
			return "", fmt.Errorf("não foi possível criar diretório %s: %w", installDir, err)
		}
	}

	tempDir, err := os.MkdirTemp("", "istioctl-install-*")
	if err != nil {
		return "", fmt.Errorf("falha ao criar diretório temporário: %w", err)
	}
	defer os.RemoveAll(tempDir)

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	istioURL := fmt.Sprintf("https://github.com/istio/istio/releases/download/%s/istio-%s-%s-%s.tar.gz",
		version, version, goos, goarch)
	tarPath := filepath.Join(tempDir, "istio.tar.gz")

	logf("   📥 Baixando Istio de: %s\n", istioURL)
	if output, err := runCommand("curl", 5*time.Minute, "-L", "-o", tarPath, istioURL); err != nil {
		return "", fmt.Errorf("falha ao baixar Istio: %s", output)
	}

	logf("   📦 Extraindo istioctl...\n")
	var tarArgs []string
	if runtime.GOOS == "darwin" {
		tarArgs = []string{"--no-xattrs", "-xzf", tarPath, "-C", tempDir}
	} else {
		tarArgs = []string{"-xzf", tarPath, "-C", tempDir}
	}

	if output, err := runCommand("tar", 2*time.Minute, tarArgs...); err != nil {
		return "", fmt.Errorf("falha ao extrair Istio: %s", output)
	}

	istioctlSource := filepath.Join(tempDir, fmt.Sprintf("istio-%s", version), "bin", "istioctl")
	istioctlDest := filepath.Join(installDir, "istioctl")

	logf("   📥 Instalando istioctl em: %s\n", istioctlDest)

	data, err := os.ReadFile(istioctlSource)
	if err != nil {
		return "", fmt.Errorf("falha ao ler istioctl: %w", err)
	}

	if err := os.WriteFile(istioctlDest, data, 0755); err != nil {
		return "", fmt.Errorf("falha ao instalar istioctl: %w", err)
	}

	logf("   ✅ istioctl instalado com sucesso!\n")

	if _, err := exec.LookPath("istioctl"); err != nil {
		logf("   ⚠️  AVISO: %s não está no PATH.\n", installDir)
		logf("   Adicione ao seu ~/.bashrc ou ~/.zshrc:\n")
		logf("   export PATH=\"%s:$PATH\"\n", installDir)
		logf("   Ou execute: export PATH=\"%s:$PATH\"\n\n", installDir)
	}

	return istioctlDest, nil
}

func installIstio(clusterName, istioVersion, istioProfile string, isMacOS bool) error {
	istioctlPath, err := ensureIstioctl(istioVersion)
	if err != nil {
		return fmt.Errorf("falha ao garantir istioctl: %w", err)
	}

	logf("   🔧 Instalando o painel de controle do Istio (perfil '%s')...\n", istioProfile)

	installArgs := []string{"install", "--set", "profile=" + istioProfile}

	if isMacOS {
		logf("   🍎 Configurando Istio Gateway para NodePort (otimizado para macOS)...\n")
		installArgs = append(installArgs,
			"--set", "components.ingressGateways[0].name=istio-ingressgateway",
			"--set", "components.ingressGateways[0].enabled=true",
			"--set", "components.ingressGateways[0].k8s.service.type=NodePort",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].name=http2",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].port=80",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].targetPort=8080",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].nodePort=30080",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].name=https",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].port=443",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].targetPort=8443",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].nodePort=30443",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].name=status-port",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].port=15021",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].targetPort=15021",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].nodePort=30021",
		)
	}

	installArgs = append(installArgs, "-y")

	logf("   ⏳ Executando instalação do Istio (pode levar até 5 minutos)...\n")
	logf("   💡 Mantenha a paciência, o processo está em andamento...\n")

	// Executar instalação com feedback de progresso
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Iniciar keep-alive para mostrar que está processando
	keepAliveCtx, keepAliveCancel := context.WithCancel(ctx)
	defer keepAliveCancel()
	go keepAlive(keepAliveCtx, 15*time.Second, "Instalando Istio")

	cmd := exec.CommandContext(ctx, istioctlPath, installArgs...)

	// Capturar stdout e stderr em tempo real
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	// Combinar outputs
	multiReader := io.MultiReader(stdout, stderr)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("falha ao iniciar instalação do Istio: %w", err)
	}

	// Ler output em tempo real
	buf := make([]byte, 1024)
	var output strings.Builder
	lastLog := time.Now()

	for {
		n, err := multiReader.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			output.WriteString(chunk)

			// Log progressivo a cada 10 segundos ou quando há linha completa
			if time.Since(lastLog) >= 10*time.Second || strings.Contains(chunk, "\n") {
				lines := strings.Split(strings.TrimSpace(chunk), "\n")
				for _, line := range lines {
					if line != "" {
						logf("      %s\n", line)
					}
				}
				lastLog = time.Now()
			}
		}
		if err != nil {
			break
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout na instalação do Istio após 5 minutos")
		}
		return fmt.Errorf("falha na instalação do Istio: %s", output.String())
	}

	keepAliveCancel() // Parar keep-alive
	logf("   ✓ Instalação concluída\n")

	// Aguardar componentes ficarem prontos
	logf("   ⏳ Aguardando componentes do Istio ficarem prontos (pode levar até 3 minutos)...\n")

	logf("   ⏳ Aguardando istiod...\n")
	istiodReady := waitForPodsReady("istio-system", "app=istiod", 3*time.Minute, 5*time.Second)

	if !istiodReady {
		logf("   ⚠️  Aviso: istiod pode não estar completamente pronto, mas continuando...\n")
	}

	logf("   ⏳ Aguardando Istio Ingress Gateway...\n")
	gwReady := waitForPodsReady("istio-system", "app=istio-ingressgateway", 3*time.Minute, 5*time.Second)

	if !gwReady {
		logf("   ⚠️  Aviso: Istio Gateway pode não estar completamente pronto, mas continuando...\n")
	}

	time.Sleep(5 * time.Second)
	logf("   📊 Verificando status dos componentes do Istio...\n")
	if output, err := runCommandWithTimeout("kubectl", 30*time.Second, "get", "pods", "-n", "istio-system"); err == nil {
		logf("   Pods do Istio:\n%s\n", output)
	}

	logf("   🔧 Habilitando injeção de sidecar no namespace 'default'...\n")
	if output, err := runCommandWithTimeout("kubectl", 30*time.Second, "label", "namespace", "default", "istio-injection=enabled", "--overwrite"); err != nil {
		return fmt.Errorf("falha ao habilitar injeção de sidecar: %s", output)
	}
	logf("   ✓ Injeção de sidecar habilitada\n")

	logf("   📋 Verificando versão do Istio...\n")
	if output, err := runCommandWithTimeout(istioctlPath, 30*time.Second, "version"); err == nil {
		logf("   Versão do Istio:\n%s\n", output)
	}

	return nil
}

func installKind() error {
	logf("⚠️  O comando 'kind' não foi encontrado. Tentando instalar automaticamente...\n")

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	kindVersion := "v0.23.0"
	downloadURL := fmt.Sprintf("https://kind.sigs.k8s.io/dl/%s/kind-%s-%s", kindVersion, goos, goarch)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("não foi possível encontrar o diretório home do usuário")
	}

	installDir := filepath.Join(homeDir, ".local", "bin")
	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		if err := os.MkdirAll(installDir, 0755); err != nil {
			return fmt.Errorf("não foi possível criar o diretório de instalação %s: %w", installDir, err)
		}
	}
	installPath := filepath.Join(installDir, "kind")

	logf("   📥 Baixando Kind de: %s\n", downloadURL)
	logf("   📥 Instalando em: %s\n", installPath)

	if output, err := runCommand("curl", 2*time.Minute, "-Lo", installPath, downloadURL); err != nil {
		return fmt.Errorf("falha no download do Kind: %s", output)
	}

	logf("   🔧 Definindo permissão de execução...\n")
	if output, err := runCommandWithTimeout("chmod", 10*time.Second, "+x", installPath); err != nil {
		return fmt.Errorf("falha ao definir permissão de execução: %s", output)
	}

	logf("✅ Kind instalado com sucesso!\n")
	logf("   ⚠️  Aviso: O diretório de instalação ('%s') pode não estar no seu PATH.\n", installDir)
	logf("   Você pode precisar reiniciar seu terminal ou adicionar a seguinte linha ao seu ~/.bashrc ou ~/.zshrc:\n")
	logf("   export PATH=\"%s:$PATH\"\n", installDir)

	return nil
}

func ensureDependencies(deps ...string) error {
	for _, dep := range deps {
		_, err := exec.LookPath(dep)
		if err != nil {
			if dep == "kind" {
				if err := installKind(); err != nil {
					return fmt.Errorf("falha ao instalar o Kind: %w", err)
				}
				if _, err := exec.LookPath("kind"); err != nil {
					return fmt.Errorf("kind foi instalado, mas não está no PATH")
				}
			} else {
				return fmt.Errorf("dependência necessária '%s' não encontrada no PATH", dep)
			}
		}
	}
	return nil
}

func deleteCluster(args []string) {
	deleteCmd := flag.NewFlagSet("delete", flag.ExitOnError)
	clusterName := deleteCmd.String("name", "kind", "Nome do cluster a ser deletado")
	if err := deleteCmd.Parse(args); err != nil {
		fatalf("Erro ao analisar argumentos: %v", err)
	}

	logf("🔥 Deletando o cluster Kind '%s'...\n", *clusterName)
	cmdArgs := []string{"delete", "cluster", "--name", *clusterName}
	output, err := runCommand("kind", 5*time.Minute, cmdArgs...)
	if err != nil {
		fatalf("Falha ao deletar o cluster Kind:\n%s", output)
	}

	fmt.Printf("✅ Cluster Kind '%s' deletado com sucesso!\n", *clusterName)
}

func listClusters() {
	logf("📋 Listando clusters Kind existentes...\n")
	output, err := runCommand("kind", 30*time.Second, "get", "clusters")
	if err != nil {
		fatalf("Falha ao listar clusters:\n%s", output)
	}
	if strings.TrimSpace(output) == "" {
		logf("Nenhum cluster Kind encontrado.\n")
	} else {
		fmt.Print(output)
	}
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

func runCommandWithTimeout(name string, timeout time.Duration, args ...string) (string, error) {
	return runCommand(name, timeout, args...)
}
