package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ClusterConfig struct {
	Name               string
	KubernetesVersion  string
	ControlPlaneNodes  int
	WorkerNodes        int
	DisableDefaultCNI  bool
	Networking         NetworkingConfig
	APIServerPort      int
	FeatureGates       []string
	RuntimeConfig      []string
	RegistryMirrors    []string
	InsecureRegistries []string
	IsMacOS            bool
	WithNginxIngress   bool
	WithIstio          bool

	// --- Novas Configurações de Registry e Certificados ---
	PrivateRegistryURL string // Ex: my-registry.corp.com:5000
	RegistryCAPath     string // Caminho no host para o CA do registry
	CustomCAPath       string // Caminho no host para um CA genérico (ex: proxy corporativo)
	InsecureSkipVerify bool   // Se true, ignora erros x509 no registry
}

func GenerateKindConfig(cfg *ClusterConfig) (string, error) {
	tempFile, err := os.CreateTemp("", fmt.Sprintf("kind-config-%s-*.yaml", cfg.Name))
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer func(tempFile *os.File) {
		err := tempFile.Close()
		if err != nil {
			fmt.Printf("failed to close temp file: %v\n", err)
		}
	}(tempFile)

	var builder strings.Builder

	builder.WriteString("kind: Cluster\n")
	builder.WriteString("apiVersion: kind.x-k8s.io/v1alpha4\n")

	// --- Networking ---
	hasNetworking := cfg.DisableDefaultCNI || cfg.APIServerPort != 6443 || cfg.Networking.DNSDomain != "cluster.local"
	if hasNetworking {
		builder.WriteString("networking:\n")
		if cfg.DisableDefaultCNI {
			builder.WriteString("  disableDefaultCNI: true\n")
			builder.WriteString(fmt.Sprintf("  podSubnet: \"%s\"\n", cfg.Networking.PodSubnet))
			builder.WriteString(fmt.Sprintf("  serviceSubnet: \"%s\"\n", cfg.Networking.ServiceSubnet))
		}
		if cfg.APIServerPort != 6443 {
			builder.WriteString(fmt.Sprintf("  apiServerPort: %d\n", cfg.APIServerPort))
		}
		if cfg.Networking.DNSDomain != "cluster.local" {
			builder.WriteString(fmt.Sprintf("  dnsDomain: \"%s\"\n", cfg.Networking.DNSDomain))
		}
	}

	// --- FeatureGates ---
	if len(cfg.FeatureGates) > 0 {
		builder.WriteString("featureGates:\n")
		for _, gate := range cfg.FeatureGates {
			parts := strings.SplitN(gate, "=", 2)
			if len(parts) == 2 {
				builder.WriteString(fmt.Sprintf("  %s: %s\n", parts[0], parts[1]))
			}
		}
	}

	// --- RuntimeConfig ---
	if len(cfg.RuntimeConfig) > 0 {
		builder.WriteString("runtimeConfig:\n")
		for _, rc := range cfg.RuntimeConfig {
			parts := strings.SplitN(rc, "=", 2)
			if len(parts) == 2 {
				builder.WriteString(fmt.Sprintf("  %s: %s\n", parts[0], parts[1]))
			}
		}
	}

	// --- Containerd Config Patches (Registry Privado & Certificados) ---
	hasContainerdPatches := cfg.PrivateRegistryURL != "" || len(cfg.RegistryMirrors) > 0 || len(cfg.InsecureRegistries) > 0

	if hasContainerdPatches {
		builder.WriteString("containerdConfigPatches:\n")
		builder.WriteString("- |\n")
		builder.WriteString("  [plugins.\"io.containerd.grpc.v1.cri\".registry]\n")

		// Configuração específica para um Registry Privado (com TLS/CA)
		if cfg.PrivateRegistryURL != "" {
			builder.WriteString(fmt.Sprintf("    [plugins.\"io.containerd.grpc.v1.cri\".registry.configs.\"%s\".tls]\n", cfg.PrivateRegistryURL))

			if cfg.RegistryCAPath != "" {
				// O arquivo será montado em /etc/containerd/certs.d/ dentro do nó
				filename := filepath.Base(cfg.RegistryCAPath)
				containerPath := fmt.Sprintf("/etc/containerd/certs.d/%s", filename)
				builder.WriteString(fmt.Sprintf("      ca_file = \"%s\"\n", containerPath))
			}

			if cfg.InsecureSkipVerify {
				builder.WriteString("      insecure_skip_verify = true\n")
			}
		}

		// Configuração de Mirrors genéricos
		if len(cfg.RegistryMirrors) > 0 {
			builder.WriteString("    [plugins.\"io.containerd.grpc.v1.cri\".registry.mirrors]\n")
			for _, mirror := range cfg.RegistryMirrors {
				builder.WriteString(fmt.Sprintf("      [plugins.\"io.containerd.grpc.v1.cri\".registry.mirrors.\"%s\"]\n", mirror))
				builder.WriteString(fmt.Sprintf("        endpoint = [\"%s\"]\n", mirror))
			}
		}

		// Configuração de Registries Inseguros genéricos
		if len(cfg.InsecureRegistries) > 0 {
			builder.WriteString("    [plugins.\"io.containerd.grpc.v1.cri\".registry.configs]\n")
			for _, registry := range cfg.InsecureRegistries {
				builder.WriteString(fmt.Sprintf("      [plugins.\"io.containerd.grpc.v1.cri\".registry.configs.\"%s\".tls]\n", registry))
				builder.WriteString("        insecure_skip_verify = true\n")
			}
		}
	}

	// ============================================================
	// SEÇÃO DE NODES - PRODUCTION-READY ARCHITECTURE
	// ============================================================
	builder.WriteString("nodes:\n")

	isHA := cfg.ControlPlaneNodes >= 3
	needsIngress := cfg.WithNginxIngress || cfg.WithIstio

	// Função auxiliar para injetar montagens de certificados nos nós
	// Isso mapeia arquivos do host para dentro do container do nó Kind
	addExtraMounts := func() {
		if cfg.RegistryCAPath != "" || cfg.CustomCAPath != "" {
			builder.WriteString("  extraMounts:\n")
		}

		// Montar CA do Registry Privado (para uso do containerd)
		if cfg.RegistryCAPath != "" {
			filename := filepath.Base(cfg.RegistryCAPath)
			// Kind monta volumes como readOnly por padrão se não especificado, mas vamos ser explícitos
			builder.WriteString(fmt.Sprintf("  - hostPath: %s\n", cfg.RegistryCAPath))
			builder.WriteString(fmt.Sprintf("    containerPath: /etc/containerd/certs.d/%s\n", filename))
			builder.WriteString("    readOnly: true\n")
		}

		// Montar CA Customizado (para confiança do SO, ex: proxy corporativo)
		// Montamos em um diretório padrão de certificados do Linux
		if cfg.CustomCAPath != "" {
			filename := filepath.Base(cfg.CustomCAPath)
			builder.WriteString(fmt.Sprintf("  - hostPath: %s\n", cfg.CustomCAPath))
			builder.WriteString(fmt.Sprintf("    containerPath: /usr/local/share/ca-certificates/%s\n", filename))
			builder.WriteString("    readOnly: true\n")
		}
	}

	// --- CONTROL PLANES ---
	for i := 0; i < cfg.ControlPlaneNodes; i++ {
		builder.WriteString("- role: control-plane\n")

		// Injeta mounts de certificados em todos os control planes
		addExtraMounts()

		if i == 0 {
			// Configuração específica do primeiro control plane (InitConfiguration)
			builder.WriteString("  kubeadmConfigPatches:\n")
			builder.WriteString("  - |\n")
			builder.WriteString("    kind: InitConfiguration\n")
			builder.WriteString("    nodeRegistration:\n")
			builder.WriteString("      kubeletExtraArgs:\n")
			builder.WriteString("        node-labels: \"ingress-ready=true\"\n")

			// Mapeamento de portas apenas em cluster simples (não-HA)
			if !isHA && needsIngress {
				builder.WriteString("  extraPortMappings:\n")

				if cfg.WithNginxIngress {
					if cfg.WithIstio {
						// Se ambos Nginx e Istio, separar portas
						builder.WriteString("  # Nginx Ingress Controller\n")
						builder.WriteString("  - containerPort: 30080\n    hostPort: 80\n    protocol: TCP\n")
						builder.WriteString("  - containerPort: 30443\n    hostPort: 443\n    protocol: TCP\n")
						builder.WriteString("  # Istio Gateway\n")
						builder.WriteString("  - containerPort: 30180\n    hostPort: 8080\n    protocol: TCP\n")
						builder.WriteString("  - containerPort: 30543\n    hostPort: 8443\n    protocol: TCP\n")
					} else {
						// Apenas Nginx
						builder.WriteString("  # Nginx Ingress Controller\n")
						builder.WriteString("  - containerPort: 30080\n    hostPort: 80\n    protocol: TCP\n")
						builder.WriteString("  - containerPort: 30443\n    hostPort: 443\n    protocol: TCP\n")
					}
				} else if cfg.WithIstio {
					// Apenas Istio
					builder.WriteString("  # Istio Gateway\n")
					builder.WriteString("  - containerPort: 30080\n    hostPort: 80\n    protocol: TCP\n")
					builder.WriteString("  - containerPort: 30443\n    hostPort: 443\n    protocol: TCP\n")
				}

				if cfg.WithIstio {
					builder.WriteString("  - containerPort: 30021\n    hostPort: 15021\n    protocol: TCP\n")
				}
			}
		} else {
			// Configuração dos demais control planes (JoinConfiguration)
			builder.WriteString("  kubeadmConfigPatches:\n")
			builder.WriteString("  - |\n")
			builder.WriteString("    kind: JoinConfiguration\n")
			builder.WriteString("    nodeRegistration:\n")
			builder.WriteString("      kubeletExtraArgs:\n")
			builder.WriteString("        node-labels: \"ingress-ready=true\"\n")
		}
	}

	// --- WORKERS ---
	if cfg.WorkerNodes > 0 {
		// HA com ingress: APENAS 1 worker com portas (evita conflito Docker)
		// Todos os workers têm label ingress-ready
		// Múltiplas réplicas distribuídas via anti-affinity no Kubernetes depois

		if isHA && needsIngress {
			// Worker 1: COM portas mapeadas (ponto de entrada do host)
			builder.WriteString("- role: worker\n")

			// Injeta mounts de certificados
			addExtraMounts()

			builder.WriteString("  kubeadmConfigPatches:\n")
			builder.WriteString("  - |\n")
			builder.WriteString("    kind: JoinConfiguration\n")
			builder.WriteString("    nodeRegistration:\n")
			builder.WriteString("      kubeletExtraArgs:\n")
			builder.WriteString("        node-labels: \"ingress-ready=true,ingress-port-mapped=true\"\n")

			builder.WriteString("  extraPortMappings:\n")

			if cfg.WithNginxIngress {
				if cfg.WithIstio {
					builder.WriteString("  # Nginx Ingress Controller\n")
					builder.WriteString("  - containerPort: 30080\n    hostPort: 80\n    protocol: TCP\n")
					builder.WriteString("  - containerPort: 30443\n    hostPort: 443\n    protocol: TCP\n")
					builder.WriteString("  # Istio Gateway\n")
					builder.WriteString("  - containerPort: 30180\n    hostPort: 8080\n    protocol: TCP\n")
					builder.WriteString("  - containerPort: 30543\n    hostPort: 8443\n    protocol: TCP\n")
				} else {
					builder.WriteString("  # Nginx Ingress Controller\n")
					builder.WriteString("  - containerPort: 30080\n    hostPort: 80\n    protocol: TCP\n")
					builder.WriteString("  - containerPort: 30443\n    hostPort: 443\n    protocol: TCP\n")
				}
			} else if cfg.WithIstio {
				builder.WriteString("  # Istio Gateway\n")
				builder.WriteString("  - containerPort: 30080\n    hostPort: 80\n    protocol: TCP\n")
				builder.WriteString("  - containerPort: 30443\n    hostPort: 443\n    protocol: TCP\n")
			}

			if cfg.WithIstio {
				builder.WriteString("  - containerPort: 30021\n    hostPort: 15021\n    protocol: TCP\n")
			}

			// Workers 2+: SEM portas mapeadas (apenas label para receber pods)
			for i := 1; i < cfg.WorkerNodes; i++ {
				builder.WriteString("- role: worker\n")
				addExtraMounts() // Certificados em todos os workers
				builder.WriteString("  kubeadmConfigPatches:\n")
				builder.WriteString("  - |\n")
				builder.WriteString("    kind: JoinConfiguration\n")
				builder.WriteString("    nodeRegistration:\n")
				builder.WriteString("      kubeletExtraArgs:\n")
				builder.WriteString("        node-labels: \"ingress-ready=true\"\n")
			}
		} else {
			// Cluster simples ou HA sem ingress
			for i := 0; i < cfg.WorkerNodes; i++ {
				builder.WriteString("- role: worker\n")
				addExtraMounts() // Certificados em todos os workers
			}
		}
	}

	if _, err := tempFile.WriteString(builder.String()); err != nil {
		return "", fmt.Errorf("failed to write config: %w", err)
	}

	return tempFile.Name(), nil
}
