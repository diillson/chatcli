package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/config"
	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/installer"
	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/validador"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

type FlagDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"` // "string", "int", "bool"
	Default     string `json:"default,omitempty"`
}

type SubcommandDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Flags       []FlagDefinition `json:"flags"`
}

type ExtendedMetadata struct {
	Subcommands []SubcommandDefinition `json:"subcommands"`
}

const (
	Version            = "3.0.0"
	DefaultClusterName = "kind"
)

func main() {
	metadataFlag := flag.Bool("metadata", false, "Display plugin metadata in JSON format")
	schemaFlag := flag.Bool("schema", false, "Display plugin schema in JSON format") // <-- NOVA FLAG
	flag.Parse()

	if *metadataFlag {
		printMetadata()
		return
	}
	if *schemaFlag {
		printSchema()
		return
	}

	if err := validator.EnsureDependencies("docker", "curl", "kind", "kubectl"); err != nil {
		utils.Fatalf("Dependency check failed: %v", err)
	}

	args := flag.Args()
	if len(args) == 0 {
		utils.Fatalf("Usage: @kind <create|delete|list|export|import> [options]")
	}

	subcommand := args[0]
	subcommandArgs := args[1:]

	switch subcommand {
	case "create":
		handleCreate(subcommandArgs)
	case "delete":
		handleDelete(subcommandArgs)
	case "update":
		handleUpdate(subcommandArgs)
	case "remove":
		handleRemove(subcommandArgs)
	case "list":
		handleList()
	case "export":
		handleExport(subcommandArgs)
	case "import":
		handleImport(subcommandArgs)
	default:
		utils.Fatalf("Unknown subcommand: %s. Use: create, delete, list, export, import", subcommand)
	}
}

func printMetadata() {
	meta := Metadata{
		Name:        "@kind",
		Description: "Production-ready Kubernetes cluster manager using Kind. Supports multi-node, HA control plane, Istio, Nginx, MetalLB, Cert-Manager, Cilium, and more.",
		Usage:       "@kind <create|delete|list|export|import> [options]",
		Version:     Version,
	}
	jsonMeta, _ := json.MarshalIndent(meta, "", "  ")
	fmt.Println(string(jsonMeta))
}

func printSchema() {
	schema := ExtendedMetadata{
		Subcommands: []SubcommandDefinition{
			{
				Name:        "create",
				Description: "Cria um novo cluster Kind com componentes adicionais.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster."},
					{Name: "--k8s-version", Type: "string", Description: "Versão do Kubernetes a ser usada (ex: 1.30.0)."},
					{Name: "--control-plane-nodes", Type: "int", Default: "1", Description: "Número de nós de control-plane (1 para normal, 3 para alta disponibilidade)."},
					{Name: "--worker-nodes", Type: "int", Default: "0", Description: "Número de nós de trabalho (workers)."},
					{Name: "--disable-default-cni", Type: "bool", Description: "Desabilita o CNI padrão do Kind, necessário para instalar CNIs customizados como o Cilium."},
					{Name: "--pod-subnet", Type: "string", Default: "10.244.0.0/16", Description: "CIDR da rede de Pods."},
					{Name: "--service-subnet", Type: "string", Default: "10.96.0.0/12", Description: "CIDR da rede de Serviços."},
					{Name: "--with-istio", Type: "bool", Description: "Instala o service mesh Istio no cluster."},
					{Name: "--istio-profile", Type: "string", Default: "demo", Description: "Perfil de instalação do Istio (ex: demo, default, minimal)."},
					{Name: "--with-nginx-ingress", Type: "bool", Description: "Instala o Nginx Ingress Controller."},
					{Name: "--with-metallb", Type: "bool", Description: "Instala o MetalLB para serviços do tipo LoadBalancer."},
					{Name: "--metallb-address-pool", Type: "string", Description: "Faixa de IPs para o MetalLB (ex: 172.18.255.200-172.18.255.250). Obrigatório se --with-metallb for usado."},
					{Name: "--skip-metallb-warning", Type: "bool", Description: "Pula o aviso interativo do MetalLB no macOS. Essencial para automação."},
					{Name: "--with-cert-manager", Type: "bool", Description: "Instala o Cert-Manager para gerenciamento de certificados TLS."},
					{Name: "--with-cilium", Type: "bool", Description: "Instala o CNI Cilium. Requer --disable-default-cni."},
					{Name: "--cilium-hubble", Type: "bool", Description: "Habilita a UI de observabilidade Hubble para o Cilium."},
				},
			},
			{
				Name:        "delete",
				Description: "Deleta um cluster Kind existente.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster a ser deletado."},
				},
			},
			{
				Name:        "list",
				Description: "Lista todos os clusters Kind existentes na máquina.",
				Flags:       []FlagDefinition{},
			},
			{
				Name:        "export",
				Description: "Exporta o kubeconfig de um cluster.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster do qual exportar o kubeconfig."},
					{Name: "--output", Type: "string", Description: "Caminho do arquivo para salvar o kubeconfig. Se omitido, imprime na saída padrão."},
				},
			},
			{
				Name:        "update",
				Description: "Instala componentes adicionais em um cluster Kind já existente. Não pode adicionar nós ou mudar configurações de base.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster a ser atualizado."},
					{Name: "--install-istio", Type: "bool", Description: "Instala o service mesh Istio no cluster."},
					{Name: "--istio-version", Type: "string", Default: "1.22.1", Description: "Versão do Istio a ser instalada."},
					{Name: "--istio-profile", Type: "string", Default: "demo", Description: "Perfil de instalação do Istio."},
					{Name: "--install-nginx-ingress", Type: "bool", Description: "Instala o Nginx Ingress Controller."},
					{Name: "--install-cert-manager", Type: "bool", Description: "Instala o Cert-Manager."},
					{Name: "--cert-manager-version", Type: "string", Default: "v1.13.0", Description: "Versão do Cert-Manager."},
				},
			},
			{
				Name:        "remove",
				Description: "Remove componentes de um cluster Kind existente.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster alvo."},
					{Name: "--istio", Type: "bool", Description: "Remove o Istio do cluster."},
					{Name: "--nginx-ingress", Type: "bool", Description: "Remove o Nginx Ingress Controller."},
					{Name: "--cert-manager", Type: "bool", Description: "Remove o Cert-Manager."},
					{Name: "--metallb", Type: "bool", Description: "Remove o MetalLB."},
					{Name: "--cilium", Type: "bool", Description: "Remove o Cilium CNI."},
					{Name: "--yes", Type: "bool", Description: "Confirma a remoção sem prompt interativo (útil para automação)."},
				},
			},
		},
	}
	jsonSchema, _ := json.MarshalIndent(schema, "", "  ")
	fmt.Println(string(jsonSchema))
}

func handleCreate(args []string) {
	createCmd := flag.NewFlagSet("create", flag.ExitOnError)

	// Flags de configuração do Cluster
	clusterName := createCmd.String("name", DefaultClusterName, "Cluster name")
	k8sVersion := createCmd.String("k8s-version", "", "Kubernetes version (e.g., 1.30.0)")
	controlPlaneNodes := createCmd.Int("control-plane-nodes", 1, "Number of control plane nodes (1 or 3 for HA)")
	workerNodes := createCmd.Int("worker-nodes", 0, "Number of worker nodes")

	// Flags de Rede
	disableDefaultCNI := createCmd.Bool("disable-default-cni", false, "Disable default CNI (for custom CNI installation)")
	podSubnet := createCmd.String("pod-subnet", "10.244.0.0/16", "Pod network CIDR")
	serviceSubnet := createCmd.String("service-subnet", "10.96.0.0/12", "Service network CIDR")
	dnsDomain := createCmd.String("dns-domain", "cluster.local", "Kubernetes DNS domain")
	apiServerPort := createCmd.Int("api-server-port", 6443, "API server port")

	// Flags de Componentes Adicionais
	withIstio := createCmd.Bool("with-istio", false, "Install Istio service mesh")
	istioVersion := createCmd.String("istio-version", "1.22.1", "Istio version")
	istioProfile := createCmd.String("istio-profile", "demo", "Istio installation profile")

	withNginxIngress := createCmd.Bool("with-nginx-ingress", false, "Install Nginx Ingress Controller")
	// As flags de porta do Nginx foram removidas, pois a lógica agora é automática

	withMetalLB := createCmd.Bool("with-metallb", false, "Install MetalLB load balancer")
	metalLBAddressPool := createCmd.String("metallb-address-pool", "", "MetalLB IP address pool (e.g., 172.18.255.200-172.18.255.250)")
	skipMetalLBWarning := createCmd.Bool("skip-metallb-warning", false, "Skip MetalLB macOS warning (for automation)")

	withCertManager := createCmd.Bool("with-cert-manager", false, "Install Cert-Manager")
	certManagerVersion := createCmd.String("cert-manager-version", "v1.13.0", "Cert-Manager version")

	withCilium := createCmd.Bool("with-cilium", false, "Install Cilium CNI (requires --disable-default-cni)")
	ciliumVersion := createCmd.String("cilium-version", "1.14.5", "Cilium version")
	ciliumHubble := createCmd.Bool("cilium-hubble", false, "Enable Hubble observability")
	ciliumKubeProxyReplacement := createCmd.Bool("cilium-kube-proxy-replacement", false, "Enable kube-proxy replacement")

	// Flags Avançadas
	featureGates := createCmd.String("feature-gates", "", "Kubernetes feature gates (comma-separated, e.g., 'GracefulNodeShutdown=true')")
	runtimeConfig := createCmd.String("runtime-config", "", "API runtime config (comma-separated)")
	registryMirrors := createCmd.String("registry-mirrors", "", "Container registry mirrors (comma-separated)")
	insecureRegistries := createCmd.String("insecure-registries", "", "Insecure registries (comma-separated)")

	// Flags de Comportamento
	exportLogs := createCmd.Bool("export-logs", false, "Export cluster logs on failure")
	retainOnFailure := createCmd.Bool("retain-on-failure", false, "Retain cluster on creation failure")

	if err := createCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments: %v", err)
	}

	// Validações
	if *controlPlaneNodes != 1 && *controlPlaneNodes != 3 {
		utils.Fatalf("Control plane nodes must be 1 or 3 (for HA)")
	}
	if *withMetalLB && *metalLBAddressPool == "" {
		utils.Fatalf("MetalLB requires --metallb-address-pool")
	}
	if *withCilium && !*disableDefaultCNI {
		utils.Fatalf("Cilium requires --disable-default-cni")
	}

	isMacOS := runtime.GOOS == "darwin"
	networking := config.CustomNetworking(*podSubnet, *serviceSubnet, *dnsDomain)

	// Monta a struct de configuração com todas as informações necessárias
	clusterConfig := &config.ClusterConfig{
		Name:               *clusterName,
		KubernetesVersion:  *k8sVersion,
		ControlPlaneNodes:  *controlPlaneNodes,
		WorkerNodes:        *workerNodes,
		DisableDefaultCNI:  *disableDefaultCNI,
		Networking:         networking,
		APIServerPort:      *apiServerPort,
		FeatureGates:       parseCommaSeparated(*featureGates),
		RuntimeConfig:      parseCommaSeparated(*runtimeConfig),
		RegistryMirrors:    parseCommaSeparated(*registryMirrors),
		InsecureRegistries: parseCommaSeparated(*insecureRegistries),
		IsMacOS:            isMacOS,
		WithNginxIngress:   *withNginxIngress, // Passa a informação para o gerador de config
		WithIstio:          *withIstio,        // Passa a informação para o gerador de config
	}

	utils.Logf("🚀 Creating Kind cluster '%s' with %d control plane node(s) and %d worker node(s)...\n",
		*clusterName, *controlPlaneNodes, *workerNodes)

	if isMacOS {
		utils.Logf("🍎 macOS detected - applying optimizations\n")
	}
	if *disableDefaultCNI {
		utils.Logf("🔌 Default CNI disabled - custom CNI will be required\n")
	}

	// Gera o arquivo de configuração do Kind com base na lógica dinâmica
	configPath, err := config.GenerateKindConfig(clusterConfig)
	if err != nil {
		utils.Fatalf("Failed to generate Kind config: %v", err)
	}
	defer os.Remove(configPath)

	// Cria o cluster
	cmdArgs := []string{"create", "cluster", "--name", *clusterName, "--config", configPath}
	if *retainOnFailure {
		cmdArgs = append(cmdArgs, "--retain")
	}
	if *k8sVersion != "" {
		imageTag := fmt.Sprintf("kindest/node:v%s", *k8sVersion)
		cmdArgs = append(cmdArgs, "--image", imageTag)
	}

	output, err := utils.RunCommand("kind", utils.DefaultTimeout, cmdArgs...)
	if err != nil {
		if *exportLogs {
			utils.Logf("📋 Exporting cluster logs...\n")
			logsDir := fmt.Sprintf("/tmp/%s-logs", *clusterName)
			utils.RunCommand("kind", utils.DefaultTimeout, "export", "logs", logsDir, "--name", *clusterName)
			utils.Logf("📁 Logs exported to: %s\n", logsDir)
		}
		utils.Fatalf("Failed to create cluster:\n%s", output)
	}
	utils.Logf("✅ Cluster Kind created successfully\n")
	utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	utils.Logf("🔍 Validating cluster health...\n")
	utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	if err := validator.WaitForClusterReady(*clusterName, *controlPlaneNodes, *workerNodes); err != nil {
		utils.Logf("⚠️  Warning: Cluster validation issues: %v\n", err)
		utils.Logf("💡 The cluster may still work, try checking manually:\n")
		utils.Logf("   kubectl get nodes\n")
		utils.Logf("   kubectl get pods -A\n\n")
	} else {
		utils.Logf("\n✅ Cluster health check passed!\n\n")
	}

	isHA := *controlPlaneNodes >= 3
	needsIngress := *withNginxIngress || *withIstio
	if isHA && needsIngress && *workerNodes > 0 {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🔧 Configuring HA ingress worker node...\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

		if err := config.ApplyIngressNodeConfiguration(*clusterName, isHA, needsIngress); err != nil {
			utils.Logf("⚠️  Warning: Failed to configure ingress node: %v\n", err)
			utils.Logf("💡 You can apply manually later:\n")
			utils.Logf("   kubectl label node <worker-name> node-role.kubernetes.io/ingress=true\n")
			utils.Logf("   kubectl taint node <worker-name> node-role.kubernetes.io/ingress=true:NoSchedule\n\n")
		} else {
			utils.Logf("\n✅ Ingress node configuration completed!\n\n")
		}
	}

	// Instalação dos componentes adicionais
	if *withCilium {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🐝 Installing Cilium CNI...\n")
		ciliumOpts := installer.CiliumOptions{
			Version:              *ciliumVersion,
			EnableHubble:         *ciliumHubble,
			KubeProxyReplacement: *ciliumKubeProxyReplacement,
		}
		if err := installer.InstallCilium(ciliumOpts); err != nil {
			utils.Fatalf("Failed to install Cilium: %v", err)
		}
		utils.Logf("✅ Cilium installed\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *withMetalLB {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("⚖️  Installing MetalLB...\n")
		if err := installer.InstallMetalLB(*metalLBAddressPool, *skipMetalLBWarning); err != nil {
			utils.Fatalf("Failed to install MetalLB: %v", err)
		}
		utils.Logf("✅ MetalLB installed\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *withCertManager {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🔐 Installing Cert-Manager...\n")
		if err := installer.InstallCertManager(*certManagerVersion); err != nil {
			utils.Fatalf("Failed to install Cert-Manager: %v", err)
		}
		utils.Logf("✅ Cert-Manager installed\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *withNginxIngress {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🌐 Installing Nginx Ingress Controller...\n")
		// Chamada simplificada, sem parâmetros de porta
		if err := installer.InstallNginxIngress(); err != nil {
			utils.Fatalf("Failed to install Nginx Ingress: %v", err)
		}
		utils.Logf("✅ Nginx Ingress installed\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *withIstio {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("✨ Installing Istio...\n")
		if err := installer.InstallIstio(*clusterName, *istioVersion, *istioProfile, isMacOS, *withNginxIngress); err != nil {
			utils.Fatalf("Failed to install Istio: %v", err)
		}
		utils.Logf("✅ Istio installed\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	printClusterInfo(*clusterName, *controlPlaneNodes, *workerNodes, *withIstio, *withNginxIngress, *withMetalLB, *withCertManager, *withCilium)
}

func handleDelete(args []string) {
	deleteCmd := flag.NewFlagSet("delete", flag.ExitOnError)
	clusterName := deleteCmd.String("name", DefaultClusterName, "Cluster name to delete")
	if err := deleteCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments: %v", err)
	}

	utils.Logf("🔥 Deleting cluster '%s'...\n", *clusterName)
	output, err := utils.RunCommand("kind", utils.DefaultTimeout, "delete", "cluster", "--name", *clusterName)
	if err != nil {
		utils.Fatalf("Failed to delete cluster:\n%s", output)
	}

	fmt.Printf("✅ Cluster '%s' deleted successfully\n", *clusterName)
}

func handleList() {
	utils.Logf("📋 Listing Kind clusters...\n")
	output, err := utils.RunCommand("kind", utils.ShortTimeout, "get", "clusters")
	if err != nil {
		utils.Fatalf("Failed to list clusters:\n%s", output)
	}
	if strings.TrimSpace(output) == "" {
		utils.Logf("No clusters found\n")
	} else {
		fmt.Print(output)
	}
}

func handleExport(args []string) {
	exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
	clusterName := exportCmd.String("name", DefaultClusterName, "Cluster name")
	outputPath := exportCmd.String("output", "", "Output path for kubeconfig (default: stdout)")
	if err := exportCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments: %v", err)
	}

	utils.Logf("📤 Exporting kubeconfig for cluster '%s'...\n", *clusterName)

	cmdArgs := []string{"export", "kubeconfig", "--name", *clusterName}
	if *outputPath != "" {
		cmdArgs = append(cmdArgs, "--kubeconfig", *outputPath)
	}

	output, err := utils.RunCommand("kind", utils.ShortTimeout, cmdArgs...)
	if err != nil {
		utils.Fatalf("Failed to export kubeconfig:\n%s", output)
	}

	if *outputPath != "" {
		fmt.Printf("✅ Kubeconfig exported to: %s\n", *outputPath)
	} else {
		fmt.Print(output)
	}
}

func handleImport(args []string) {
	importCmd := flag.NewFlagSet("import", flag.ExitOnError)
	clusterName := importCmd.String("name", DefaultClusterName, "Cluster name")
	imagePath := importCmd.String("image", "", "Docker image tarball path")
	if err := importCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments: %v", err)
	}

	if *imagePath == "" {
		utils.Fatalf("--image is required")
	}

	utils.Logf("📥 Importing image to cluster '%s'...\n", *clusterName)
	output, err := utils.RunCommand("kind", utils.ExtendedTimeout, "load", "image-archive", *imagePath, "--name", *clusterName)
	if err != nil {
		utils.Fatalf("Failed to import image:\n%s", output)
	}

	fmt.Printf("✅ Image imported successfully\n")
}

func handleUpdate(args []string) {
	updateCmd := flag.NewFlagSet("update", flag.ExitOnError)
	clusterName := updateCmd.String("name", DefaultClusterName, "Nome do cluster a ser atualizado")

	installIstio := updateCmd.Bool("install-istio", false, "Instala o Istio no cluster existente")
	istioVersion := updateCmd.String("istio-version", "1.22.1", "Versão do Istio a ser instalada")
	istioProfile := updateCmd.String("istio-profile", "demo", "Perfil de instalação do Istio")

	installNginx := updateCmd.Bool("install-nginx-ingress", false, "Instala o Nginx Ingress Controller")

	installCertManager := updateCmd.Bool("install-cert-manager", false, "Instala o Cert-Manager")
	certManagerVersion := updateCmd.String("cert-manager-version", "v1.13.0", "Versão do Cert-Manager")

	if err := updateCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments for update: %v", err)
	}

	// Verificar se o cluster existe
	utils.Logf("🔍 Verifying cluster '%s' exists...\n", *clusterName)
	output, err := utils.RunCommand("kind", utils.ShortTimeout, "get", "clusters")
	if err != nil || !strings.Contains(output, *clusterName) {
		utils.Fatalf("Cluster '%s' not found. Cannot perform update.", *clusterName)
	}
	utils.Logf("✅ Cluster found. Proceeding with updates.\n")

	isMacOS := runtime.GOOS == "darwin"

	// Lógica de instalação dos componentes
	if *installNginx {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🌐 Installing Nginx Ingress Controller on existing cluster...\n")
		// AVISO: A instalação do Nginx em um cluster existente pode não funcionar
		// se as portas 80/443 não foram mapeadas na criação do cluster.
		utils.Logf("   ⚠️  Warning: This will only work if ports 80/443 were mapped during cluster creation.\n")
		if err := installer.InstallNginxIngress(); err != nil {
			utils.Fatalf("Failed to install Nginx Ingress: %v", err)
		}
		utils.Logf("✅ Nginx Ingress installed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *installCertManager {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🔐 Installing Cert-Manager on existing cluster...\n")
		if err := installer.InstallCertManager(*certManagerVersion); err != nil {
			utils.Fatalf("Failed to install Cert-Manager: %v", err)
		}
		utils.Logf("✅ Cert-Manager installed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *installIstio {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("✨ Installing Istio on existing cluster...\n")

		// LÓGICA CORRIGIDA: Verifica se o Nginx está sendo instalado AGORA
		// ou se ele JÁ EXISTE no cluster.
		nginxIsPresent := *installNginx || installer.IsNginxIngressInstalled()
		if nginxIsPresent {
			utils.Logf("   ℹ️  Nginx Ingress detected. Istio will be configured to avoid port conflicts on macOS.\n")
		}
		if err := installer.InstallIstio(*clusterName, *istioVersion, *istioProfile, isMacOS, nginxIsPresent); err != nil {
			utils.Fatalf("Failed to install Istio: %v", err)
		}
		utils.Logf("✅ Istio installed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	utils.Logf("\n🎉 Update process completed for cluster '%s'.\n", *clusterName)
}

func handleRemove(args []string) {
	removeCmd := flag.NewFlagSet("remove", flag.ExitOnError)
	clusterName := removeCmd.String("name", DefaultClusterName, "Nome do cluster do qual remover componentes")

	removeIstio := removeCmd.Bool("istio", false, "Remove o Istio do cluster")
	removeNginx := removeCmd.Bool("nginx-ingress", false, "Remove o Nginx Ingress Controller")
	removeCertManager := removeCmd.Bool("cert-manager", false, "Remove o Cert-Manager")
	removeMetalLB := removeCmd.Bool("metallb", false, "Remove o MetalLB")
	removeCilium := removeCmd.Bool("cilium", false, "Remove o Cilium CNI")
	// Adicionamos uma flag de confirmação para automação
	confirm := removeCmd.Bool("yes", false, "Confirma a remoção sem prompt interativo")

	if err := removeCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments for remove: %v", err)
	}

	// Verificar se o cluster existe
	utils.Logf("🔍 Verifying cluster '%s' exists...\n", *clusterName)
	output, err := utils.RunCommand("kind", utils.ShortTimeout, "get", "clusters")
	if err != nil || !strings.Contains(output, *clusterName) {
		utils.Fatalf("Cluster '%s' not found. Cannot perform removal.", *clusterName)
	}
	utils.Logf("✅ Cluster found. Proceeding with removal.\n")

	// Lógica de remoção dos componentes
	if *removeIstio {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Istio from cluster...\n")
		if err := installer.RemoveIstio(*confirm); err != nil {
			utils.Fatalf("Failed to remove Istio: %v", err)
		}
		utils.Logf("✅ Istio removed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *removeNginx {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Nginx Ingress Controller...\n")
		if err := installer.RemoveNginxIngress(); err != nil {
			utils.Fatalf("Failed to remove Nginx Ingress: %v", err)
		}
		utils.Logf("✅ Nginx Ingress removed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *removeCertManager {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Cert-Manager...\n")
		if err := installer.RemoveCertManager(); err != nil {
			utils.Fatalf("Failed to remove Cert-Manager: %v", err)
		}
		utils.Logf("✅ Cert-Manager removed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *removeMetalLB {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing MetalLB...\n")
		if err := installer.RemoveMetalLB(); err != nil {
			utils.Fatalf("Failed to remove MetalLB: %v", err)
		}
		utils.Logf("✅ MetalLB removed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *removeCilium {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Cilium CNI...\n")
		if err := installer.RemoveCilium(*confirm); err != nil {
			utils.Fatalf("Failed to remove Cilium: %v", err)
		}
		utils.Logf("✅ Cilium removed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	utils.Logf("\n🎉 Removal process completed for cluster '%s'.\n", *clusterName)
}

func parseCommaSeparated(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func printClusterInfo(name string, controlPlane, workers int, istio, nginx, metallb, certManager, cilium bool) {
	fmt.Printf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	fmt.Printf("✅ Cluster '%s' is ready!\n\n", name)
	fmt.Printf("📊 Cluster Configuration:\n")
	fmt.Printf("   • Control Plane Nodes: %d\n", controlPlane)
	fmt.Printf("   • Worker Nodes: %d\n", workers)
	fmt.Printf("   • Total Nodes: %d\n\n", controlPlane+workers)

	if istio || nginx || metallb || certManager || cilium {
		fmt.Printf("🔧 Installed Components:\n")
		if cilium {
			fmt.Printf("   ✓ Cilium CNI\n")
		}
		if istio {
			fmt.Printf("   ✓ Istio Service Mesh\n")
		}
		if nginx {
			fmt.Printf("   ✓ Nginx Ingress Controller\n")
		}
		if metallb {
			fmt.Printf("   ✓ MetalLB Load Balancer\n")
		}
		if certManager {
			fmt.Printf("   ✓ Cert-Manager\n")
		}
		fmt.Println()
	}

	fmt.Printf("💡 Useful Commands:\n")
	fmt.Printf("   kubectl config use-context kind-%s\n", name)
	fmt.Printf("   kubectl cluster-info\n")
	fmt.Printf("   kubectl get nodes -o wide\n")
	fmt.Printf("   kubectl get pods -A\n")

	if cilium {
		fmt.Printf("\n🐝 Cilium Commands:\n")
		fmt.Printf("   cilium status\n")
		fmt.Printf("   cilium connectivity test\n")
		fmt.Printf("   kubectl -n kube-system exec -it ds/cilium -- cilium status --verbose\n")
	}

	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}
