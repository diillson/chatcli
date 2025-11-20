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
	Version            = "3.2.0"
	DefaultClusterName = "kind"
)

func main() {
	metadataFlag := flag.Bool("metadata", false, "Display plugin metadata in JSON format")
	schemaFlag := flag.Bool("schema", false, "Display plugin schema in JSON format")
	flag.Parse()

	if *metadataFlag {
		printMetadata()
		return
	}
	if *schemaFlag {
		printSchema()
		return
	}

	// Validação de dependências essenciais
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
		Description: "Production-ready Kubernetes cluster manager using Kind. Supports multi-node, HA, Istio, Nginx, MetalLB, and private registries.",
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
				Description: "Cria um novo cluster Kind com componentes adicionais e suporte enterprise.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster."},
					{Name: "--k8s-version", Type: "string", Description: "Versão do Kubernetes (ex: 1.30.0)."},
					{Name: "--control-plane-nodes", Type: "int", Default: "1", Description: "Número de nós de control-plane (1 ou 3 para HA)."},
					{Name: "--worker-nodes", Type: "int", Default: "0", Description: "Número de nós de trabalho (workers)."},

					// --- Flags de Registry Privado e Certificados ---
					{Name: "--private-registry", Type: "string", Description: "URL do registry privado (ex: registry.corp.com:5000)."},
					{Name: "--registry-ca", Type: "string", Description: "Caminho local para o arquivo CA do registry privado."},
					{Name: "--custom-ca", Type: "string", Description: "Caminho local para um CA customizado (ex: proxy corporativo) a ser confiado pelos nós."},
					{Name: "--insecure-registry", Type: "bool", Description: "Ignora verificação TLS para o registry privado."},
					// -------------------------------------------------------

					{Name: "--disable-default-cni", Type: "bool", Description: "Desabilita o CNI padrão (necessário para Cilium)."},
					{Name: "--pod-subnet", Type: "string", Default: "10.244.0.0/16", Description: "CIDR da rede de Pods."},
					{Name: "--service-subnet", Type: "string", Default: "10.96.0.0/12", Description: "CIDR da rede de Serviços."},

					// --- Istio ---
					{Name: "--with-istio", Type: "bool", Description: "Instala Istio service mesh."},
					{Name: "--istio-profile", Type: "string", Default: "demo", Description: "Perfil de instalação do Istio."},
					{Name: "--istio-hub", Type: "string", Description: "Registry/Hub customizado para imagens do Istio (ex: meu-registry.com/istio)."},

					{Name: "--with-nginx-ingress", Type: "bool", Description: "Instala Nginx Ingress Controller."},
					{Name: "--nginx-certgen-image", Type: "string", Description: "Imagem customizada para o Job de cert-gen do Nginx (kube-webhook-certgen) em registry privado."},

					{Name: "--with-metallb", Type: "bool", Description: "Instala MetalLB."},
					{Name: "--metallb-address-pool", Type: "string", Description: "Range de IPs para MetalLB."},
					{Name: "--skip-metallb-warning", Type: "bool", Description: "Pula aviso do MetalLB no macOS."},
					{Name: "--with-cert-manager", Type: "bool", Description: "Instala Cert-Manager."},
					{Name: "--with-cilium", Type: "bool", Description: "Instala Cilium CNI."},
					{Name: "--cilium-hubble", Type: "bool", Description: "Habilita Hubble UI."},
				},
			},
			{
				Name:        "delete",
				Description: "Deleta um cluster Kind existente.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster."},
				},
			},
			{
				Name:        "list",
				Description: "Lista todos os clusters Kind.",
				Flags:       []FlagDefinition{},
			},
			{
				Name:        "export",
				Description: "Exporta o kubeconfig de um cluster.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster."},
					{Name: "--output", Type: "string", Description: "Arquivo de saída."},
				},
			},
			{
				Name:        "update",
				Description: "Instala componentes em um cluster existente.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster."},
					{Name: "--install-istio", Type: "bool", Description: "Instala Istio."},
					{Name: "--istio-hub", Type: "string", Description: "Registry customizado para imagens do Istio."},
					{Name: "--install-nginx-ingress", Type: "bool", Description: "Instala Nginx Ingress."},
					{Name: "--install-cert-manager", Type: "bool", Description: "Instala Cert-Manager."},
				},
			},
			{
				Name:        "remove",
				Description: "Remove componentes de um cluster.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Default: "kind", Description: "Nome do cluster."},
					{Name: "--istio", Type: "bool", Description: "Remove Istio."},
					{Name: "--nginx-ingress", Type: "bool", Description: "Remove Nginx Ingress."},
					{Name: "--yes", Type: "bool", Description: "Confirma remoção sem prompt."},
				},
			},
		},
	}
	jsonSchema, _ := json.MarshalIndent(schema, "", "  ")
	fmt.Println(string(jsonSchema))
}

func handleCreate(args []string) {
	createCmd := flag.NewFlagSet("create", flag.ExitOnError)

	// Configuração Básica
	clusterName := createCmd.String("name", DefaultClusterName, "Cluster name")
	k8sVersion := createCmd.String("k8s-version", "", "Kubernetes version")
	controlPlaneNodes := createCmd.Int("control-plane-nodes", 1, "Number of control plane nodes")
	workerNodes := createCmd.Int("worker-nodes", 0, "Number of worker nodes")

	// --- Flags de Registry Privado e Certificados ---
	privateRegistry := createCmd.String("private-registry", "", "Private registry URL (e.g., registry.corp.com:5000)")
	registryCA := createCmd.String("registry-ca", "", "Path to private registry CA certificate file")
	customCA := createCmd.String("custom-ca", "", "Path to custom CA certificate file (e.g., corporate proxy)")
	insecureRegistry := createCmd.Bool("insecure-registry", false, "Skip TLS verification for private registry")
	// -----------------------------------------------

	// Rede
	disableDefaultCNI := createCmd.Bool("disable-default-cni", false, "Disable default CNI")
	podSubnet := createCmd.String("pod-subnet", "10.244.0.0/16", "Pod network CIDR")
	serviceSubnet := createCmd.String("service-subnet", "10.96.0.0/12", "Service network CIDR")
	dnsDomain := createCmd.String("dns-domain", "cluster.local", "DNS domain")
	apiServerPort := createCmd.Int("api-server-port", 6443, "API server port")

	// Componentes
	withIstio := createCmd.Bool("with-istio", false, "Install Istio")
	istioVersion := createCmd.String("istio-version", "1.22.1", "Istio version")
	istioProfile := createCmd.String("istio-profile", "demo", "Istio profile")
	istioHub := createCmd.String("istio-hub", "", "Custom image hub for Istio (e.g., my-registry.com/istio)")

	withNginxIngress := createCmd.Bool("with-nginx-ingress", false, "Install Nginx Ingress")
	nginxCertGenImage := createCmd.String("nginx-certgen-image", "",
		"Custom image for Nginx cert-gen job (e.g., registry.corp.com/ingress-nginx/kube-webhook-certgen:v1.3.0)")

	withMetalLB := createCmd.Bool("with-metallb", false, "Install MetalLB")
	metalLBAddressPool := createCmd.String("metallb-address-pool", "", "MetalLB IP pool")
	skipMetalLBWarning := createCmd.Bool("skip-metallb-warning", false, "Skip MetalLB warning")

	withCertManager := createCmd.Bool("with-cert-manager", false, "Install Cert-Manager")
	certManagerVersion := createCmd.String("cert-manager-version", "v1.13.0", "Cert-Manager version")

	withCilium := createCmd.Bool("with-cilium", false, "Install Cilium")
	ciliumVersion := createCmd.String("cilium-version", "1.14.5", "Cilium version")
	ciliumHubble := createCmd.Bool("cilium-hubble", false, "Enable Hubble")
	ciliumKubeProxyReplacement := createCmd.Bool("cilium-kube-proxy-replacement", false, "Enable kube-proxy replacement")

	// Avançado
	featureGates := createCmd.String("feature-gates", "", "Feature gates")
	runtimeConfig := createCmd.String("runtime-config", "", "Runtime config")
	registryMirrors := createCmd.String("registry-mirrors", "", "Registry mirrors")
	insecureRegistries := createCmd.String("insecure-registries", "", "Insecure registries")

	// Comportamento
	exportLogs := createCmd.Bool("export-logs", false, "Export logs on failure")
	retainOnFailure := createCmd.Bool("retain-on-failure", false, "Retain cluster on failure")

	if err := createCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments: %v", err)
	}

	// Validações
	if *controlPlaneNodes != 1 && *controlPlaneNodes != 3 {
		utils.Fatalf("Control plane nodes must be 1 or 3")
	}
	if *withMetalLB && *metalLBAddressPool == "" {
		utils.Fatalf("MetalLB requires --metallb-address-pool")
	}
	if *withCilium && !*disableDefaultCNI {
		utils.Fatalf("Cilium requires --disable-default-cni")
	}

	// Validação de arquivos de certificado
	if *registryCA != "" {
		if _, err := os.Stat(*registryCA); os.IsNotExist(err) {
			utils.Fatalf("Registry CA file not found: %s", *registryCA)
		}
	}
	if *customCA != "" {
		if _, err := os.Stat(*customCA); os.IsNotExist(err) {
			utils.Fatalf("Custom CA file not found: %s", *customCA)
		}
	}

	isMacOS := runtime.GOOS == "darwin"
	networking := config.CustomNetworking(*podSubnet, *serviceSubnet, *dnsDomain)

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
		WithNginxIngress:   *withNginxIngress,
		WithIstio:          *withIstio,

		// --- Passando as novas configurações para o gerador ---
		PrivateRegistryURL: *privateRegistry,
		RegistryCAPath:     *registryCA,
		CustomCAPath:       *customCA,
		InsecureSkipVerify: *insecureRegistry,
		// ------------------------------------------------------
	}

	utils.Logf("🚀 Creating Kind cluster '%s'...\n", *clusterName)

	if *privateRegistry != "" {
		utils.Logf("🔐 Configuring private registry: %s\n", *privateRegistry)
		if *registryCA != "" {
			utils.Logf("   📄 Using custom CA for registry\n")
		}
		if *insecureRegistry {
			utils.Logf("   ⚠️  Using insecure registry connection (TLS skip verify)\n")
		}
	}
	if *customCA != "" {
		utils.Logf("🔐 Injecting custom CA bundle into nodes\n")
	}

	// Gera o config
	configPath, err := config.GenerateKindConfig(clusterConfig)
	if err != nil {
		utils.Fatalf("Failed to generate Kind config: %v", err)
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			utils.Logf("Failed to remove temporary config file: %v", err)
		}
	}(configPath)

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
			logsDir := fmt.Sprintf("/tmp/%s-logs", *clusterName)
			_, err := utils.RunCommand("kind", utils.DefaultTimeout, "export", "logs", logsDir, "--name", *clusterName)
			if err != nil {
				return
			}
			utils.Logf("📁 Logs exported to: %s\n", logsDir)
		}
		utils.Fatalf("Failed to create cluster:\n%s", output)
	}

	utils.Logf("✅ Cluster created successfully\n")

	// Validação do cluster
	if err := validator.WaitForClusterReady(*clusterName, *controlPlaneNodes, *workerNodes); err != nil {
		utils.Logf("⚠️  Warning: Cluster validation issues: %v\n", err)
	} else {
		utils.Logf("✅ Cluster health check passed!\n")
	}

	// Configuração de nós HA
	isHA := *controlPlaneNodes >= 3
	needsIngress := *withNginxIngress && *workerNodes > 0
	if isHA && needsIngress {
		err := config.ApplyIngressNodeConfiguration(*clusterName, isHA, needsIngress)
		if err != nil {
			return
		}
	}

	// Instalação de componentes
	if *withCilium {
		err := installer.InstallCilium(installer.CiliumOptions{
			Version:              *ciliumVersion,
			EnableHubble:         *ciliumHubble,
			KubeProxyReplacement: *ciliumKubeProxyReplacement,
		})
		if err != nil {
			return
		}
	}

	if *withMetalLB {
		err := installer.InstallMetalLB(*metalLBAddressPool, *skipMetalLBWarning)
		if err != nil {
			return
		}
	}

	if *withCertManager {
		err := installer.InstallCertManager(*certManagerVersion)
		if err != nil {
			return
		}
	}

	if *withNginxIngress {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🌐 Installing Nginx Ingress Controller...\n")

		opts := installer.NginxOptions{
			PrivateRegistry: *privateRegistry,
			CertGenImage:    *nginxCertGenImage,
		}

		if err := installer.InstallNginxIngress(opts); err != nil {
			utils.Fatalf("Failed to install Nginx Ingress: %v", err)
		}
		utils.Logf("✅ Nginx Ingress installed\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	if *withIstio {
		nginxIsPresent := *withNginxIngress

		opts := installer.InstallIstioOptions{
			ClusterName: *clusterName,
			Version:     *istioVersion,
			Profile:     *istioProfile,
			IsMacOS:     isMacOS,
			WithNginx:   nginxIsPresent,
			ImageHub:    *istioHub,
		}
		err := installer.InstallIstio(opts)
		if err != nil {
			return
		}
	}

	printClusterInfo(*clusterName, *controlPlaneNodes, *workerNodes, *withIstio, *withNginxIngress, *withMetalLB, *withCertManager, *withCilium)
}

func handleDelete(args []string) {
	deleteCmd := flag.NewFlagSet("delete", flag.ExitOnError)
	clusterName := deleteCmd.String("name", DefaultClusterName, "Cluster name")
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
	fmt.Print(output)
}

func handleExport(args []string) {
	exportCmd := flag.NewFlagSet("export", flag.ExitOnError)
	clusterName := exportCmd.String("name", DefaultClusterName, "Cluster name")
	outputPath := exportCmd.String("output", "", "Output path")
	if err := exportCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments: %v", err)
	}

	utils.Logf("📤 Exporting kubeconfig for '%s'...\n", *clusterName)
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

	utils.Logf("📥 Importing image to '%s'...\n", *clusterName)
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
	istioHub := updateCmd.String("istio-hub", "", "Registry customizado para imagens do Istio")

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
		utils.Logf("   ⚠️  Warning: This will only work if ports 80/443 were mapped during cluster creation.\n")
		opts := installer.NginxOptions{
			PrivateRegistry: "",
			CertGenImage:    "",
		}

		if err := installer.InstallNginxIngress(opts); err != nil {
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

		nginxIsPresent := *installNginx || installer.IsNginxIngressInstalled()
		if nginxIsPresent {
			utils.Logf("   ℹ️  Nginx Ingress detected. Istio will be configured to avoid port conflicts on macOS.\n")
		}

		opts := installer.InstallIstioOptions{
			ClusterName: *clusterName,
			Version:     *istioVersion,
			Profile:     *istioProfile,
			IsMacOS:     isMacOS,
			WithNginx:   nginxIsPresent,
			ImageHub:    *istioHub,
		}
		if err := installer.InstallIstio(opts); err != nil {
			utils.Fatalf("Failed to install Istio: %v", err)
		}
		utils.Logf("✅ Istio installed successfully!\n")
		utils.Logf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	}

	utils.Logf("\n🎉 Update process completed for cluster '%s'.\n", *clusterName)
}

func handleRemove(args []string) {
	removeCmd := flag.NewFlagSet("remove", flag.ExitOnError)
	clusterName := removeCmd.String("name", DefaultClusterName, "Nome do cluster")

	removeIstio := removeCmd.Bool("istio", false, "Remove o Istio")
	removeNginx := removeCmd.Bool("nginx-ingress", false, "Remove o Nginx Ingress")
	removeCertManager := removeCmd.Bool("cert-manager", false, "Remove o Cert-Manager")
	removeMetalLB := removeCmd.Bool("metallb", false, "Remove o MetalLB")
	removeCilium := removeCmd.Bool("cilium", false, "Remove o Cilium")
	confirm := removeCmd.Bool("yes", false, "Confirma a remoção sem prompt")

	if err := removeCmd.Parse(args); err != nil {
		utils.Fatalf("Failed to parse arguments for remove: %v", err)
	}

	utils.Logf("🔍 Verifying cluster '%s' exists...\n", *clusterName)
	output, err := utils.RunCommand("kind", utils.ShortTimeout, "get", "clusters")
	if err != nil || !strings.Contains(output, *clusterName) {
		utils.Fatalf("Cluster '%s' not found.", *clusterName)
	}

	if *removeIstio {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Istio...\n")
		if err := installer.RemoveIstio(*confirm); err != nil {
			utils.Fatalf("Failed to remove Istio: %v", err)
		}
		utils.Logf("✅ Istio removed successfully!\n")
	}

	if *removeNginx {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Nginx Ingress...\n")
		if err := installer.RemoveNginxIngress(); err != nil {
			utils.Fatalf("Failed to remove Nginx Ingress: %v", err)
		}
		utils.Logf("✅ Nginx Ingress removed successfully!\n")
	}

	if *removeCertManager {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Cert-Manager...\n")
		if err := installer.RemoveCertManager(); err != nil {
			utils.Fatalf("Failed to remove Cert-Manager: %v", err)
		}
		utils.Logf("✅ Cert-Manager removed successfully!\n")
	}

	if *removeMetalLB {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing MetalLB...\n")
		if err := installer.RemoveMetalLB(); err != nil {
			utils.Fatalf("Failed to remove MetalLB: %v", err)
		}
		utils.Logf("✅ MetalLB removed successfully!\n")
	}

	if *removeCilium {
		utils.Logf("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
		utils.Logf("🗑️  Removing Cilium CNI...\n")
		if err := installer.RemoveCilium(*confirm); err != nil {
			utils.Fatalf("Failed to remove Cilium: %v", err)
		}
		utils.Logf("✅ Cilium removed successfully!\n")
	}

	utils.Logf("\n🎉 Removal process completed.\n")
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
	fmt.Printf("📊 Topology: %d CP + %d Workers\n\n", controlPlane, workers)
	if istio || nginx || metallb || certManager || cilium {
		fmt.Printf("🔧 Installed Components:\n")
		if cilium {
			fmt.Printf("   ✓ Cilium CNI\n")
		}
		if istio {
			fmt.Printf("   ✓ Istio Service Mesh\n")
		}
		if nginx {
			fmt.Printf("   ✓ Nginx Ingress\n")
		}
		if metallb {
			fmt.Printf("   ✓ MetalLB\n")
		}
		if certManager {
			fmt.Printf("   ✓ Cert-Manager\n")
		}
		fmt.Println()
	}
	fmt.Printf("💡 Use 'kubectl config use-context kind-%s' to start.\n", name)
	fmt.Printf("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}
