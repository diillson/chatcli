package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
}

// fatalf escreve uma mensagem de erro para stderr e encerra o programa com status 1.
func fatalf(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, "❌ Erro: "+format+"\n", v...)
	os.Exit(1)
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
		Version:     "2.1.1",
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

	output, err := runCommand("kind", cmdArgs...)
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
		logf("\n-------------------------------------\n")
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
		logf("-------------------------------------\n")
	}

	// Instalar Istio se solicitado
	if *withIstio {
		logf("\n-------------------------------------\n")
		logf("✨ Iniciando instalação do Istio...\n")
		if err := installIstio(*clusterName, *istioVersion, *istioProfile, isMacOS); err != nil {
			fatalf("Falha ao instalar o Istio: %v", err)
		}
		logf("✅ Istio instalado e configurado com sucesso!\n")
		logf("-------------------------------------\n")
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

	maxRetries := 30
	for i := 0; i < maxRetries; i++ {
		output, err := runCommandWithTimeout("kubectl", 10*time.Second, "get", "nodes")
		if err == nil && strings.Contains(output, "Ready") {
			logf("   ✓ Nodes prontos\n")
			return nil
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
				// Nginx usa portas alternativas quando Istio está presente
				config.WriteString("  - containerPort: 80\n")
				config.WriteString("    hostPort: 8080\n")
				config.WriteString("    protocol: TCP\n")
				config.WriteString("  - containerPort: 443\n")
				config.WriteString("    hostPort: 8443\n")
				config.WriteString("    protocol: TCP\n")
			} else {
				// Nginx usa portas padrão quando está sozinho
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
				// macOS usa NodePort mapeado
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
				// Linux pode usar portas diretas
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
	logf("   - Aplicando manifesto do Nginx Ingress Controller...\n")

	manifestURL := "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml"

	if output, err := runCommand("kubectl", "apply", "-f", manifestURL); err != nil {
		return fmt.Errorf("falha ao aplicar manifesto: %s", output)
	}

	logf("   - Aguardando namespace ingress-nginx ser criado...\n")
	time.Sleep(5 * time.Second)

	// Verificar se o namespace existe
	maxRetries := 30
	namespaceReady := false
	for i := 0; i < maxRetries; i++ {
		output, err := runCommandWithTimeout("kubectl", 10*time.Second, "get", "namespace", "ingress-nginx")
		if err == nil && strings.Contains(output, "ingress-nginx") {
			namespaceReady = true
			logf("   ✓ Namespace ingress-nginx criado\n")
			break
		}
		time.Sleep(2 * time.Second)
	}

	if !namespaceReady {
		return fmt.Errorf("timeout aguardando namespace ingress-nginx")
	}

	// Aguardar deployment ser criado
	logf("   - Aguardando deployment do Nginx Ingress Controller...\n")
	time.Sleep(5 * time.Second)

	// Verificar se há pods sendo criados
	deploymentReady := false
	for i := 0; i < maxRetries; i++ {
		output, err := runCommandWithTimeout("kubectl", 10*time.Second, "get", "pods", "-n", "ingress-nginx")
		if err == nil && strings.Contains(output, "ingress-nginx-controller") {
			deploymentReady = true
			logf("   ✓ Deployment do Nginx Ingress encontrado\n")
			break
		}
		time.Sleep(2 * time.Second)
	}

	if !deploymentReady {
		// Listar o que existe no namespace para debug
		if output, err := runCommand("kubectl", "get", "all", "-n", "ingress-nginx"); err == nil {
			logf("   Debug - recursos em ingress-nginx:\n%s\n", output)
		}
		return fmt.Errorf("deployment do Nginx Ingress não foi criado")
	}

	// Aguardar pods ficarem prontos com polling manual
	logf("   - Aguardando pods do Nginx Ingress ficarem prontos...\n")

	podsReady := false
	for i := 0; i < 60; i++ { // 60 tentativas = 2 minutos
		output, err := runCommandWithTimeout("kubectl", 10*time.Second, "get", "pods", "-n", "ingress-nginx", "-o", "json")
		if err == nil {
			// Verificar se há pods Running
			if strings.Contains(output, `"phase":"Running"`) && strings.Contains(output, `"ready":true`) {
				podsReady = true
				logf("   ✓ Pods do Nginx Ingress estão prontos\n")
				break
			}
		}

		// Mostrar progresso a cada 10 segundos
		if i%5 == 0 && i > 0 {
			logf("   ⏳ Ainda aguardando... (%d segundos)\n", i*2)
			// Mostrar status dos pods
			if statusOutput, err := runCommandWithTimeout("kubectl", 5*time.Second, "get", "pods", "-n", "ingress-nginx"); err == nil {
				logf("   Status atual:\n%s\n", statusOutput)
			}
		}

		time.Sleep(2 * time.Second)
	}

	if !podsReady {
		// Mostrar logs para debug
		logf("   ⚠️  Coletando informações de debug...\n")
		if output, err := runCommand("kubectl", "get", "pods", "-n", "ingress-nginx", "-o", "wide"); err == nil {
			logf("   Pods:\n%s\n", output)
		}
		if output, err := runCommand("kubectl", "describe", "pods", "-n", "ingress-nginx"); err == nil {
			logf("   Descrição dos pods:\n%s\n", output)
		}
		return fmt.Errorf("timeout aguardando pods do Nginx Ingress ficarem prontos")
	}

	// Verificar serviço
	logf("   - Verificando serviço do Nginx Ingress...\n")
	if output, err := runCommand("kubectl", "get", "svc", "-n", "ingress-nginx"); err != nil {
		logf("   ⚠️  Aviso: não foi possível verificar serviço: %s\n", output)
	} else {
		logf("   ✓ Serviço:\n%s\n", output)
	}

	return nil
}

func ensureIstioctl(version string) (string, error) {
	// Primeiro, verificar se istioctl já está instalado
	if path, err := exec.LookPath("istioctl"); err == nil {
		logf("   ✓ istioctl encontrado em: %s\n", path)
		// Verificar versão
		if output, err := runCommand(path, "version", "--remote=false"); err == nil {
			logf("   ✓ Versão instalada: %s", output)
		}
		return path, nil
	}

	logf("   - istioctl não encontrado, instalando versão %s...\n", version)

	// Determinar diretório de instalação
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

	// Criar diretório temporário para download
	tempDir, err := os.MkdirTemp("", "istioctl-install-*")
	if err != nil {
		return "", fmt.Errorf("falha ao criar diretório temporário: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Download do Istio
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	istioURL := fmt.Sprintf("https://github.com/istio/istio/releases/download/%s/istio-%s-%s-%s.tar.gz",
		version, version, goos, goarch)
	tarPath := filepath.Join(tempDir, "istio.tar.gz")

	logf("   - Baixando Istio de: %s\n", istioURL)
	if output, err := runCommand("curl", "-L", "-o", tarPath, istioURL); err != nil {
		return "", fmt.Errorf("falha ao baixar Istio: %s", output)
	}

	// Extrair arquivo
	logf("   - Extraindo istioctl...\n")
	var tarArgs []string
	if runtime.GOOS == "darwin" {
		tarArgs = []string{"--no-xattrs", "-xzf", tarPath, "-C", tempDir}
	} else {
		tarArgs = []string{"-xzf", tarPath, "-C", tempDir}
	}

	if output, err := runCommand("tar", tarArgs...); err != nil {
		return "", fmt.Errorf("falha ao extrair Istio: %s", output)
	}

	// Copiar istioctl para o diretório de instalação
	istioctlSource := filepath.Join(tempDir, fmt.Sprintf("istio-%s", version), "bin", "istioctl")
	istioctlDest := filepath.Join(installDir, "istioctl")

	logf("   - Instalando istioctl em: %s\n", istioctlDest)

	// Ler o arquivo fonte
	data, err := os.ReadFile(istioctlSource)
	if err != nil {
		return "", fmt.Errorf("falha ao ler istioctl: %w", err)
	}

	// Escrever no destino com permissões corretas
	if err := os.WriteFile(istioctlDest, data, 0755); err != nil {
		return "", fmt.Errorf("falha ao instalar istioctl: %w", err)
	}

	logf("   ✅ istioctl instalado com sucesso!\n")

	// Verificar se está no PATH
	if _, err := exec.LookPath("istioctl"); err != nil {
		logf("   ⚠️  AVISO: %s não está no PATH.\n", installDir)
		logf("   Adicione ao seu ~/.bashrc ou ~/.zshrc:\n")
		logf("   export PATH=\"%s:$PATH\"\n", installDir)
		logf("   Ou execute: export PATH=\"%s:$PATH\"\n\n", installDir)
	}

	return istioctlDest, nil
}

func installIstio(clusterName, istioVersion, istioProfile string, isMacOS bool) error {
	// Garantir que istioctl está disponível
	istioctlPath, err := ensureIstioctl(istioVersion)
	if err != nil {
		return fmt.Errorf("falha ao garantir istioctl: %w", err)
	}

	logf("   - Instalando o painel de controle do Istio (perfil '%s')...\n", istioProfile)

	installArgs := []string{"install", "--set", "profile=" + istioProfile}

	// No macOS, configurar Istio Gateway para usar NodePort
	if isMacOS {
		logf("   - Configurando Istio Gateway para NodePort (otimizado para macOS)...\n")
		installArgs = append(installArgs,
			// Configuração para Istio 1.22+
			"--set", "components.ingressGateways[0].name=istio-ingressgateway",
			"--set", "components.ingressGateways[0].enabled=true",
			"--set", "components.ingressGateways[0].k8s.service.type=NodePort",
			// Porta HTTP
			"--set", "components.ingressGateways[0].k8s.service.ports[0].name=http2",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].port=80",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].targetPort=8080",
			"--set", "components.ingressGateways[0].k8s.service.ports[0].nodePort=30080",
			// Porta HTTPS
			"--set", "components.ingressGateways[0].k8s.service.ports[1].name=https",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].port=443",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].targetPort=8443",
			"--set", "components.ingressGateways[0].k8s.service.ports[1].nodePort=30443",
			// Porta de status
			"--set", "components.ingressGateways[0].k8s.service.ports[2].name=status-port",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].port=15021",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].targetPort=15021",
			"--set", "components.ingressGateways[0].k8s.service.ports[2].nodePort=30021",
		)
	}

	installArgs = append(installArgs, "-y")

	logf("   - Executando: %s %s\n", istioctlPath, strings.Join(installArgs, " "))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, istioctlPath, installArgs...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("falha na instalação do Istio: %s", out.String())
	}

	logf("   - Saída da instalação:\n%s\n", out.String())

	// Aguardar os pods do Istio ficarem prontos usando polling manual
	logf("   - Aguardando pods do Istio ficarem prontos...\n")

	// Aguardar istiod
	logf("   - Aguardando istiod...\n")
	istiodReady := false
	for i := 0; i < 60; i++ { // 60 tentativas = 2 minutos
		output, err := runCommandWithTimeout("kubectl", 10*time.Second, "get", "pods", "-n", "istio-system", "-l", "app=istiod", "-o", "json")
		if err == nil && strings.Contains(output, `"phase":"Running"`) && strings.Contains(output, `"ready":true`) {
			istiodReady = true
			logf("   ✓ istiod está pronto\n")
			break
		}
		if i%5 == 0 && i > 0 {
			logf("   ⏳ Aguardando istiod... (%d segundos)\n", i*2)
		}
		time.Sleep(2 * time.Second)
	}

	if !istiodReady {
		logf("   ⚠️  Aviso: istiod pode não estar completamente pronto\n")
	}

	// Aguardar ingress gateway
	logf("   - Aguardando Istio Ingress Gateway...\n")
	gwReady := false
	for i := 0; i < 60; i++ {
		output, err := runCommandWithTimeout("kubectl", 10*time.Second, "get", "pods", "-n", "istio-system", "-l", "app=istio-ingressgateway", "-o", "json")
		if err == nil && strings.Contains(output, `"phase":"Running"`) && strings.Contains(output, `"ready":true`) {
			gwReady = true
			logf("   ✓ Istio Ingress Gateway está pronto\n")
			break
		}
		if i%5 == 0 && i > 0 {
			logf("   ⏳ Aguardando gateway... (%d segundos)\n", i*2)
		}
		time.Sleep(2 * time.Second)
	}

	if !gwReady {
		logf("   ⚠️  Aviso: Istio Gateway pode não estar completamente pronto\n")
	}

	// Verificar status geral
	time.Sleep(5 * time.Second)
	logf("   - Verificando status dos componentes do Istio...\n")
	if output, err := runCommand("kubectl", "get", "pods", "-n", "istio-system"); err != nil {
		logf("   ⚠️  Aviso: não foi possível verificar pods: %s\n", output)
	} else {
		logf("   Pods do Istio:\n%s\n", output)
	}

	// Habilitar injeção de sidecar
	logf("   - Habilitando injeção de sidecar no namespace 'default'...\n")
	if output, err := runCommand("kubectl", "label", "namespace", "default", "istio-injection=enabled", "--overwrite"); err != nil {
		return fmt.Errorf("falha ao habilitar injeção de sidecar: %s", output)
	}

	// Verificar versão do Istio instalado
	logf("   - Verificando versão do Istio...\n")
	if output, err := runCommand(istioctlPath, "version"); err != nil {
		logf("   ⚠️  Aviso: não foi possível verificar versão: %s\n", output)
	} else {
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

	logf("   - Baixando Kind de: %s\n", downloadURL)
	logf("   - Instalando em: %s\n", installPath)

	if output, err := runCommand("curl", "-Lo", installPath, downloadURL); err != nil {
		return fmt.Errorf("falha no download do Kind: %s", output)
	}

	logf("   - Definindo permissão de execução...\n")
	if output, err := runCommand("chmod", "+x", installPath); err != nil {
		return fmt.Errorf("falha ao definir permissão de execução: %s", output)
	}

	logf("✅ Kind instalado com sucesso!\n")
	logf("   Aviso: O diretório de instalação ('%s') pode não estar no seu PATH.\n", installDir)
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
					return fmt.Errorf("kind foi instalado, mas não está no PATH. Por favor, adicione o diretório de instalação ao seu PATH")
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
	output, err := runCommand("kind", cmdArgs...)
	if err != nil {
		fatalf("Falha ao deletar o cluster Kind:\n%s", output)
	}

	fmt.Printf("✅ Cluster Kind '%s' deletado com sucesso!\n", *clusterName)
}

func listClusters() {
	logf("📋 Listando clusters Kind existentes...\n")
	output, err := runCommand("kind", "get", "clusters")
	if err != nil {
		fatalf("Falha ao listar clusters:\n%s", output)
	}
	if strings.TrimSpace(output) == "" {
		logf("Nenhum cluster Kind encontrado.\n")
	} else {
		fmt.Print(output)
	}
}

func runCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func runCommandWithTimeout(name string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}
