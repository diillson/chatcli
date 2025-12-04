package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-eks/pkg/aws_bootstrap"
	"github.com/diillson/chatcli/plugins-examples/chatcli-eks/pkg/config"
	"github.com/diillson/chatcli/plugins-examples/chatcli-eks/pkg/pulumi_orchestrator"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

type FlagDefinition struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Type            string   `json:"type"`
	Default         string   `json:"default,omitempty"`
	RequiredWhen    []string `json:"required_when,omitempty"`
	ValidationError string   `json:"validation_error,omitempty"`
}

type SubcommandDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Flags       []FlagDefinition `json:"flags"`
	Examples    []string         `json:"examples"`
}

type ExtendedMetadata struct {
	Subcommands []SubcommandDefinition `json:"subcommands"`
}

func main() {
	metadataFlag := flag.Bool("metadata", false, "Display plugin metadata")
	schemaFlag := flag.Bool("schema", false, "Display plugin schema")
	flag.Parse()

	if *metadataFlag {
		printMetadata()
		return
	}
	if *schemaFlag {
		printSchema()
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Uso: @eks <create|delete|cleanup> [opções]")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		handleCreate(args[1:])
	case "delete":
		handleDelete(args[1:])
	case "cleanup":
		handleCleanup(args[1:])
	case "kms-info":
		handleKMSInfo(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "❌ Erro: Subcomando desconhecido: %s\n\n", args[0])
		fmt.Fprintln(os.Stderr, "Comandos disponíveis:")
		fmt.Fprintln(os.Stderr, "  create     - Cria ou atualiza cluster EKS")
		fmt.Fprintln(os.Stderr, "  delete     - Destrói cluster EKS")
		fmt.Fprintln(os.Stderr, "  cleanup    - Remove recursos auxiliares (S3, DynamoDB, KMS)")
		fmt.Fprintln(os.Stderr, "  kms-info   - Exibe informações de chave KMS")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Use '@eks <comando> --help' para ver opções específicas")
		fmt.Fprintln(os.Stderr, "Use '@eks --schema' para ver documentação completa em JSON")
		os.Exit(1)
	}
}

func handleKMSInfo(args []string) {
	infoCmd := flag.NewFlagSet("kms-info", flag.ExitOnError)
	clusterName := infoCmd.String("cluster-name", "", "Nome do cluster")
	kmsKeyID := infoCmd.String("kms-key-id", "", "Alias ou ARN da chave")
	awsRegion := infoCmd.String("region", "us-east-1", "Região AWS")

	infoCmd.Parse(args)

	if *clusterName == "" && *kmsKeyID == "" {
		fmt.Fprintln(os.Stderr, "❌ Especifique --cluster-name OU --kms-key-id")
		os.Exit(1)
	}

	awsClients, err := aws_bootstrap.NewAWSClients(context.Background(), *awsRegion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Erro AWS: %v\n", err)
		os.Exit(1)
	}

	// Inferir KMS do cluster se não especificado
	if *kmsKeyID == "" {
		*kmsKeyID = fmt.Sprintf("alias/pulumi-secrets-%s", *clusterName)
	}

	info, err := awsClients.GetKMSKeyInfo(context.Background(), *kmsKeyID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Erro: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "🔑 INFORMAÇÕES DA CHAVE KMS:")
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 60))
	fmt.Fprintf(os.Stderr, "Alias:        %s\n", info.Alias)
	fmt.Fprintf(os.Stderr, "KeyID:        %s\n", info.KeyID)
	fmt.Fprintf(os.Stderr, "ARN:          %s\n", info.ARN)
	fmt.Fprintf(os.Stderr, "Estado:       %s\n", info.State)
	fmt.Fprintf(os.Stderr, "Criado em:    %s\n", info.CreationDate.Format(time.RFC3339))
	fmt.Fprintf(os.Stderr, "Gerenciado:   %s\n", info.KeyManager)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Tags:")
	for k, v := range info.Tags {
		fmt.Fprintf(os.Stderr, "  - %s: %s\n", k, v)
	}
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 60))
}

// getCurrentPulumiBackend tenta descobrir o backend atual do Pulumi CLI e se há token salvo.
// Retorna:
//   - current: URL do backend atual (ex: https://api.pulumi.com, s3://..., file://...)
//   - hasToken: true se for Pulumi Cloud e houver token disponível (no arquivo ou env var)
func getCurrentPulumiBackend() (current string, hasToken bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	credPath := filepath.Join(home, ".pulumi", "credentials.json")
	data, err := os.ReadFile(credPath)
	if err != nil {
		return "", false
	}

	var creds struct {
		Current      string            `json:"current"`
		AccessTokens map[string]string `json:"accessTokens"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", false
	}

	cur := strings.TrimSpace(creds.Current)
	if cur == "" {
		return "", false
	}

	// Se o backend atual for Pulumi Cloud (http/https), verifique se há token salvo
	if strings.HasPrefix(cur, "http://") || strings.HasPrefix(cur, "https://") {
		// 1) Token via env (caso exista)
		if os.Getenv("PULUMI_ACCESS_TOKEN") != "" {
			return cur, true
		}
		// 2) Token no arquivo credentials.json
		if creds.AccessTokens != nil {
			if tok, ok := creds.AccessTokens[cur]; ok && tok != "" {
				return cur, true
			}
			// Tentar equivalência sem barra final
			curNoSlash := strings.TrimRight(cur, "/")
			for k, v := range creds.AccessTokens {
				if strings.TrimRight(k, "/") == curNoSlash && v != "" {
					return cur, true
				}
			}
		}
		// Sem token
		return cur, false
	}

	// Para outros backends (s3://, file://, azblob://, gs://, etc) não exigimos token
	return cur, true
}

func handleCreate(args []string) {
	createCmd := flag.NewFlagSet("create", flag.ExitOnError)

	stateBucketName := createCmd.String("state-bucket-name", "", "Nome do bucket S3 para o estado (Obrigatório se não usar Pulumi Cloud).")
	clusterName := createCmd.String("name", "prod-eks", "Identificador único do Cluster (Stack Name).")
	awsRegion := createCmd.String("region", "us-east-1", "Região da AWS.")
	k8sVersion := createCmd.String("k8s-version", "1.31", "Versão do Kubernetes.")

	nodeType := createCmd.String("node-type", "t3.medium", "Tipo da instância para os nós.")
	minNodes := createCmd.Int("min-nodes", 2, "Número mínimo de nós.")
	maxNodes := createCmd.Int("max-nodes", 5, "Número máximo de nós.")
	useSpot := createCmd.Bool("use-spot", false, "Usar instâncias Spot para economizar custos.")

	vpcID := createCmd.String("vpc-id", "", "ID de uma VPC existente. Se vazio, cria uma nova.")
	privSubnets := createCmd.String("private-subnets", "", "Lista de IDs de subnets privadas (separadas por vírgula).")
	pubSubnets := createCmd.String("public-subnets", "", "Lista de IDs de subnets públicas (separadas por vírgula).")
	extraRules := createCmd.String("extra-ingress-rules", "", "Regras extras de SG (ex: '8080:0.0.0.0/0').")

	withLB := createCmd.Bool("with-lb-controller", true, "Instalar AWS Load Balancer Controller.")
	withNginx := createCmd.Bool("with-nginx", false, "Instalar Nginx Ingress Controller.")
	withArgo := createCmd.Bool("with-argocd", false, "Instalar ArgoCD.")
	withIstio := createCmd.Bool("with-istio", false, "Instalar Istio Service Mesh.")
	withIstioObservability := createCmd.Bool("with-istio-observability", false, "Instalar pilha de observabilidade do Istio (Kiali, Prometheus, Grafana). Requer --with-istio.")
	withIstioTracing := createCmd.Bool("with-istio-tracing", false, "Instalar Jaeger para tracing distribuído com Istio. Requer --with-istio-observability.")

	withCertManager := createCmd.Bool("with-cert-manager", false, "Instalar Cert-Manager.")
	argocdDomain := createCmd.String("argocd-domain", "", "Domínio para ArgoCD (ex: argocd.example.com).")
	certManagerEmail := createCmd.String("cert-manager-email", "", "Email para Let's Encrypt (obrigatório se usar cert-manager).")
	acmeServer := createCmd.String("acme-server", "production", "Servidor ACME: 'production' ou 'staging' (aplica-se ao provedor escolhido).")
	acmeEabKid := createCmd.String("acme-eab-kid", "", "EAB KeyID (obrigatório para --acme-provider=google)")
	acmeEabHmac := createCmd.String("acme-eab-hmac", "", "EAB HMAC key (obrigatório para --acme-provider=google)")
	acmeEabAlg := createCmd.String("acme-eab-key-alg", "HS256", "Algoritmo do EAB: HS256|HS384|HS512")
	acmeEabSecretName := createCmd.String("acme-eab-secret-name", "acme-eab-secret", "Nome do Secret para armazenar o HMAC")

	baseDomain := createCmd.String("base-domain", "", "Domínio base para certificado wildcard.")
	withExternalDNS := createCmd.Bool("with-external-dns", false, "Instalar External DNS (automação Route53).")

	refreshState := createCmd.Bool("refresh", true, "Validar estado real dos recursos antes de aplicar (recomendado).")
	acmeProvider := createCmd.String("acme-provider", "letsencrypt", "Provedor ACME: 'letsencrypt' ou 'google' (Google Trust Services).")

	secretsProvider := createCmd.String("secrets-provider", "awskms", "Provider de criptografia para secrets pulumi: 'passphrase' (local), 'awskms' (recomendado).")
	kmsKeyID := createCmd.String("kms-key-id", "", "ID da chave KMS para criptografia (obrigatório se secrets-provider=awskms). Formato: 'arn:aws:kms:REGION:ACCOUNT:key/KEY-ID' ou 'alias/key-alias'.")
	configPassphrase := createCmd.String("config-passphrase", "", "Passphrase para criptografia local de secrets (obrigatório se secrets-provider=passphrase). Alternativamente use PULUMI_CONFIG_PASSPHRASE env var.")

	kmsAction := createCmd.String("kms-action", "reuse", "Ação se KMS já existe: 'reuse' (padrão), 'fail', 'rotate'")

	if err := createCmd.Parse(args); err != nil {
		os.Exit(1)
	}

	// Escolha do backend
	var backendURL string
	if *stateBucketName != "" {
		// S3 + Dynamo lock (explícito)
		fmt.Fprintln(os.Stderr, "\n--- Fase de Bootstrap do Backend de Estado (S3) ---")
		awsClients, err := aws_bootstrap.NewAWSClients(context.Background(), *awsRegion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Erro ao inicializar clientes AWS: %v\n", err)
			os.Exit(1)
		}

		if err := awsClients.EnsureS3Backend(context.Background(), *stateBucketName, *awsRegion); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Erro S3: %v\n", err)
			os.Exit(1)
		}

		backendURL = fmt.Sprintf("s3://%s?region=%s", *stateBucketName, *awsRegion)
		fmt.Fprintln(os.Stderr, "   Backend S3 configurado:", backendURL)
		fmt.Fprintln(os.Stderr, "----------------------------------------------------\n")
	} else {
		// Sem S3: tente reutilizar o backend do 'pulumi login'
		current, hasToken := getCurrentPulumiBackend()
		switch {
		case current == "":
			// Sem login/credenciais: fallback local file://
			home, _ := os.UserHomeDir()
			localDir := filepath.Join(home, ".chatcli", "pulumi", *clusterName)
			_ = os.MkdirAll(localDir, 0755)
			backendURL = "file://" + localDir
			fmt.Fprintln(os.Stderr, "📦 Backend: local (file) [fallback automático]")
			fmt.Fprintln(os.Stderr, "    ", backendURL)

		case strings.HasPrefix(current, "http://") || strings.HasPrefix(current, "https://"):
			// Pulumi Cloud via CLI; só use se houver token salvo
			if hasToken {
				// Não setamos PULUMI_BACKEND_URL; a Automation API reutiliza o login do CLI
				fmt.Fprintln(os.Stderr, "☁️  Backend: Pulumi Cloud (via 'pulumi login')")
			} else {
				// Sem token armazenado → evitar erro não-interativo, usa local
				home, _ := os.UserHomeDir()
				localDir := filepath.Join(home, ".chatcli", "pulumi", *clusterName)
				_ = os.MkdirAll(localDir, 0755)
				backendURL = "file://" + localDir
				fmt.Fprintln(os.Stderr, "⚠️  Sem token do Pulumi Cloud disponível; usando backend local (file) para evitar falhas não interativas")
				fmt.Fprintln(os.Stderr, "    ", backendURL)
			}

		default:
			// Outros backends já logados via CLI (s3://, file://, azblob://, etc)
			backendURL = current
			fmt.Fprintln(os.Stderr, "📦 Backend (via 'pulumi login'):", backendURL)
		}
	}

	if *acmeEabKid == "" {
		*acmeEabKid = strings.TrimSpace(os.Getenv("ACME_EAB_KID"))
	}
	if *acmeEabHmac == "" {
		*acmeEabHmac = strings.TrimSpace(os.Getenv("ACME_EAB_HMAC"))
	}

	if *withNginx && !*withExternalDNS {
		fmt.Fprintln(os.Stderr, "💡 DICA: Use --with-external-dns para automação de DNS no Route53")
	}

	// ==================== DICA DE EXPOSIÇÃO ARGO ====================
	if *withArgo && *argocdDomain != "" && *baseDomain == "" {
		fmt.Fprintln(os.Stderr, "💡 DICA: ArgoCD será exposto em:", *argocdDomain)
		fmt.Fprintln(os.Stderr, "   Se quiser TLS automático via Cert-Manager, adicione:")
		fmt.Fprintln(os.Stderr, "   --with-cert-manager --base-domain=<seu-dominio> --cert-manager-email=<seu-email>")
		fmt.Fprintln(os.Stderr, "")
	}

	// ==================== VALIDAÇÕES DE CERT-MANAGER ====================
	if *withCertManager && *certManagerEmail == "" {
		fmt.Fprintln(os.Stderr, "❌ ERRO: --cert-manager-email é obrigatório quando usar --with-cert-manager")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Por que é necessário?")
		fmt.Fprintln(os.Stderr, "   - Provedores ACME (Let's Encrypt, Google) exigem email para:")
		fmt.Fprintln(os.Stderr, "     • Notificações de expiração de certificado")
		fmt.Fprintln(os.Stderr, "     • Avisos de segurança críticos")
		fmt.Fprintln(os.Stderr, "     • Recuperação de conta ACME")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Exemplo de uso:")
		fmt.Fprintln(os.Stderr, "   @eks create \\")
		fmt.Fprintln(os.Stderr, "     --with-cert-manager \\")
		fmt.Fprintln(os.Stderr, "     --cert-manager-email=admin@example.com \\")
		fmt.Fprintln(os.Stderr, "     --base-domain=example.com")
		os.Exit(1)
	}

	if *withCertManager && *baseDomain == "" {
		fmt.Fprintln(os.Stderr, "❌ ERRO: --base-domain é obrigatório quando usar --with-cert-manager")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Por que é necessário?")
		fmt.Fprintln(os.Stderr, "   - Cert-Manager usa o domínio base para:")
		fmt.Fprintln(os.Stderr, "     • Criar certificado wildcard (*.example.com)")
		fmt.Fprintln(os.Stderr, "     • Validação DNS-01 via Route53")
		fmt.Fprintln(os.Stderr, "     • Configurar ClusterIssuer com DNS challenge")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Exemplo de uso:")
		fmt.Fprintln(os.Stderr, "   @eks create \\")
		fmt.Fprintln(os.Stderr, "     --with-cert-manager \\")
		fmt.Fprintln(os.Stderr, "     --cert-manager-email=admin@example.com \\")
		fmt.Fprintln(os.Stderr, "     --base-domain=example.com")
		os.Exit(1)
	}

	// Validação de ACME Provider
	validProviders := map[string]bool{
		"letsencrypt": true,
		"google":      true,
	}
	if !validProviders[*acmeProvider] {
		fmt.Fprintf(os.Stderr, "❌ ERRO: --acme-provider inválido: '%s'\n", *acmeProvider)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Valores aceitos:")
		fmt.Fprintln(os.Stderr, "   • letsencrypt  → Let's Encrypt (padrão)")
		fmt.Fprintln(os.Stderr, "   • google       → Google Trust Services")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Exemplo:")
		fmt.Fprintln(os.Stderr, "   @eks create --with-cert-manager --acme-provider=google ...")
		os.Exit(1)
	}

	if *withCertManager && *acmeProvider == "google" {
		if *acmeEabKid == "" || *acmeEabHmac == "" {
			fmt.Fprintln(os.Stderr, "❌ ERRO: Google Trust Services exige EAB: --acme-eab-kid e --acme-eab-hmac")
			fmt.Fprintln(os.Stderr, "   Dica: export ACME_EAB_KID=... && export ACME_EAB_HMAC=...")
			os.Exit(1)
		}
	}

	// Validação de Environment
	validEnvironments := map[string]bool{
		"production": true,
		"staging":    true,
	}
	if !validEnvironments[*acmeServer] {
		fmt.Fprintf(os.Stderr, "❌ ERRO: --acme-server inválido: '%s'\n", *acmeServer)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Valores aceitos:")
		fmt.Fprintln(os.Stderr, "   • production  → Certificados válidos (confiáveis em browsers)")
		fmt.Fprintln(os.Stderr, "   • staging     → Certificados de teste (NÃO confiáveis)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Use staging apenas para testes/desenvolvimento!")
		os.Exit(1)
	}

	// ==================== VALIDAÇÕES DE EXTERNAL DNS ====================
	if *withExternalDNS && *baseDomain == "" {
		fmt.Fprintln(os.Stderr, "❌ ERRO: --base-domain é obrigatório quando usar --with-external-dns")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Por que é necessário?")
		fmt.Fprintln(os.Stderr, "   - External DNS usa o domínio base para:")
		fmt.Fprintln(os.Stderr, "     • Filtrar zona Route53 correta")
		fmt.Fprintln(os.Stderr, "     • Criar registros DNS automaticamente")
		fmt.Fprintln(os.Stderr, "     • Evitar modificações em zonas não relacionadas")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Exemplo:")
		fmt.Fprintln(os.Stderr, "   @eks create --with-external-dns --base-domain=example.com ...")
		os.Exit(1)
	}

	// ==================== VALIDAÇÕES Stack Observability Istiod ====================
	if *withIstioObservability && *withIstio == false || *withIstioObservability == true && *withIstioTracing == true && *withIstio == false {
		fmt.Fprintln(os.Stderr, "❌ ERRO: --with-istio é obrigatório quando usar --with-istio-observability e --with-istio-tracing")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Por que é necessário?")
		fmt.Fprintln(os.Stderr, "   - A stacks de observebilidade Istio exigem o uso do Istio.:")
		fmt.Fprintln(os.Stderr, "     • Kiali depende do Istio para visualização de malha de serviços")
		fmt.Fprintln(os.Stderr, "     • Prometheus coleta métricas específicas do Istio")
		fmt.Fprintln(os.Stderr, "     • Grafana usa dashboards pré-configurados para Istio")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Exemplo:")
		fmt.Fprintln(os.Stderr, "   @eks create --with-istio --with-istio-observability=true ...")
		os.Exit(1)
	}

	// ==================== AVISOS E DICAS ====================
	if *withCertManager && *acmeServer == "staging" {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "⚠️  AVISO: Usando ambiente STAGING")
		fmt.Fprintln(os.Stderr, "   - Certificados NÃO serão confiáveis em navegadores")
		fmt.Fprintln(os.Stderr, "   - Use apenas para testes e desenvolvimento")
		fmt.Fprintln(os.Stderr, "   - Para produção, remova a flag --acme-server=staging")
		fmt.Fprintln(os.Stderr, "")
	}

	if *withNginx && !*withExternalDNS {
		fmt.Fprintln(os.Stderr, "💡 DICA: Nginx Ingress detectado sem External DNS")
		fmt.Fprintln(os.Stderr, "   Considere adicionar --with-external-dns para automação de DNS no Route53")
		fmt.Fprintln(os.Stderr, "")
	}

	// ==================== VALIDAÇÕES DE SECRETS PROVIDER ====================
	validSecretsProviders := map[string]bool{
		"passphrase": true,
		"awskms":     true,
	}
	if !validSecretsProviders[*secretsProvider] {
		fmt.Fprintf(os.Stderr, "❌ ERRO: --secrets-provider inválido: '%s'\n", *secretsProvider)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Valores aceitos:")
		fmt.Fprintln(os.Stderr, "   • passphrase      → Criptografia local (precisa passphrase)")
		fmt.Fprintln(os.Stderr, "   • awskms          → AWS KMS (recomendado, sem passphrase)")
		os.Exit(1)
	}

	if *secretsProvider == "awskms" && *kmsKeyID == "" {
		fmt.Fprintln(os.Stderr, "⚠️ AVISO: --kms-key-id não informado, será criado um novo.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Exemplos de uso:")
		fmt.Fprintln(os.Stderr, "   • Usando ARN completo:")
		fmt.Fprintln(os.Stderr, "     --kms-key-id=arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012")
		fmt.Fprintln(os.Stderr, "   • Usando alias (mais simples):")
		fmt.Fprintln(os.Stderr, "     --kms-key-id=alias/pulumi-secrets")
	}

	validKMSActions := map[string]bool{
		"reuse":  true,
		"fail":   true,
		"rotate": true,
	}
	if !validKMSActions[*kmsAction] {
		fmt.Fprintf(os.Stderr, "❌ ERRO: --kms-action inválido: '%s'\n", *kmsAction)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "   Valores aceitos: reuse | fail | rotate")
		os.Exit(1)
	}

	if *secretsProvider == "awskms" && *kmsKeyID == "" {
		fmt.Fprintln(os.Stderr, "🔑 Nenhuma chave KMS fornecida, criando automaticamente...")

		awsClients, err := aws_bootstrap.NewAWSClients(context.Background(), *awsRegion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Erro ao inicializar AWS: %v\n", err)
			os.Exit(1)
		}

		keyAlias := fmt.Sprintf("pulumi-secrets-%s", *clusterName)
		keyDescription := fmt.Sprintf("Pulumi secrets encryption for cluster %s", *clusterName)

		keyID, err := awsClients.EnsureKMSKeyWithAction(
			context.Background(),
			keyAlias,
			keyDescription,
			aws_bootstrap.KMSAction(*kmsAction),
		)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Erro ao criar chave KMS: %v\n", err)
			os.Exit(1)
		}

		finalAlias := fmt.Sprintf("alias/%s", keyAlias)
		if *kmsAction == "rotate" {
			info, err := awsClients.GetKMSKeyInfo(context.Background(), keyID)
			if err == nil && info.Alias != "" {
				finalAlias = info.Alias
			}
		}

		*kmsKeyID = finalAlias
		os.Setenv("PULUMI_CONFIG_PASSPHRASE", finalAlias)

		fmt.Fprintf(os.Stderr, "   ✅ Chave KMS configurada:\n")
		fmt.Fprintf(os.Stderr, "      Alias: %s\n", finalAlias)
		fmt.Fprintf(os.Stderr, "      KeyID: %s\n", keyID)
		fmt.Fprintln(os.Stderr, "")
	}

	if *secretsProvider == "awskms" && *kmsKeyID != "" {
		os.Setenv("PULUMI_CONFIG_PASSPHRASE", *kmsKeyID)
	}

	if *secretsProvider == "passphrase" {
		finalPassphrase := *configPassphrase
		if finalPassphrase == "" {
			finalPassphrase = os.Getenv("PULUMI_CONFIG_PASSPHRASE")
		}
		if finalPassphrase == "" {
			fmt.Fprintln(os.Stderr, "❌ ERRO: Passphrase não definida")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "   Quando usar --secrets-provider=passphrase, defina a senha via:")
			fmt.Fprintln(os.Stderr, "   --config-passphrase='sua-senha-segura'")
			fmt.Fprintln(os.Stderr, "   ou export PULUMI_CONFIG_PASSPHRASE='sua-senha-segura'")
			os.Exit(1)
		}
		os.Setenv("PULUMI_CONFIG_PASSPHRASE", finalPassphrase)
	}

	if *secretsProvider == "awskms" {
		fmt.Fprintln(os.Stderr, "🔐 Secrets Provider: AWS KMS")
		fmt.Fprintf(os.Stderr, "   Chave KMS: %s\n", *kmsKeyID)
		fmt.Fprintln(os.Stderr, "   💰 Custo estimado: ~$1/mês (chave) + $0.03 por 10k operações")
		fmt.Fprintln(os.Stderr, "")
	}

	if *withCertManager {
		providerNames := map[string]string{"letsencrypt": "Let's Encrypt", "google": "Google Trust Services"}
		envNames := map[string]string{"production": "Production", "staging": "Staging"}

		fmt.Fprintln(os.Stderr, "🔐 CONFIGURAÇÃO DE CERTIFICADOS TLS:")
		fmt.Fprintf(os.Stderr, "   Provider:       %s\n", providerNames[*acmeProvider])
		fmt.Fprintf(os.Stderr, "   Ambiente:       %s\n", envNames[*acmeServer])
		fmt.Fprintf(os.Stderr, "   Email ACME:     %s\n", *certManagerEmail)
		fmt.Fprintf(os.Stderr, "   Domínio Base:   %s\n", *baseDomain)
		fmt.Fprintf(os.Stderr, "   Certificado cobre:    *.%s, *.dev.%s, *.app.%s, *.tools.%s, %s\n", *baseDomain, *baseDomain, *baseDomain, *baseDomain, *baseDomain)
		fmt.Fprintln(os.Stderr, "")
	}

	splitFn := func(s string) []string {
		if s == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		var clean []string
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				clean = append(clean, trimmed)
			}
		}
		return clean
	}

	acmeConfig := &config.ACMEConfig{
		Provider:    config.ACMEProvider(*acmeProvider),
		Environment: *acmeServer,
	}
	if *acmeProvider == "google" {
		acmeConfig.ExternalAccountBinding = &config.ACMEExternalAccountBinding{
			KeyID:        *acmeEabKid,
			HMACKey:      *acmeEabHmac,
			KeyAlgorithm: *acmeEabAlg,
			SecretName:   *acmeEabSecretName,
		}
	}

	cfg := &config.EKSConfig{
		ClusterName:            *clusterName,
		AWSRegion:              *awsRegion,
		Version:                *k8sVersion,
		NodeType:               *nodeType,
		MinNodes:               *minNodes,
		MaxNodes:               *maxNodes,
		UseSpot:                *useSpot,
		VpcID:                  *vpcID,
		PrivateSubnetIDs:       splitFn(*privSubnets),
		PublicSubnetIDs:        splitFn(*pubSubnets),
		ExtraIngressRules:      splitFn(*extraRules),
		WithLBController:       *withLB,
		WithNginx:              *withNginx,
		WithArgoCD:             *withArgo,
		WithIstio:              *withIstio,
		WithIstioObservability: *withIstioObservability,
		WithIstioTracing:       *withIstioTracing,
		WithCertManager:        *withCertManager,
		ArgocdDomain:           *argocdDomain,
		CertManagerEmail:       *certManagerEmail,
		AcmeServer:             *acmeServer,
		ACMEProvider:           *acmeProvider,
		ACMEConfig:             acmeConfig,
		BaseDomain:             *baseDomain,
		WithExternalDNS:        *withExternalDNS,
		RefreshState:           *refreshState,
		SecretsProvider:        *secretsProvider,
		KMSKeyID:               *kmsKeyID,
		ConfigPassphrase:       *configPassphrase,
	}

	if err := pulumi_orchestrator.CreateOrUpdateEKS(context.Background(), backendURL, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Falha na operação: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "\n🎉 Operação finalizada com sucesso.")
}

func handleDelete(args []string) {
	deleteCmd := flag.NewFlagSet("delete", flag.ExitOnError)
	stateBucketName := deleteCmd.String("state-bucket-name", "", "Bucket S3 do estado (deve ser o mesmo usado na criação).")
	clusterName := deleteCmd.String("name", "", "Nome do cluster usado na criação (Ex: prod-eks). NÃO use o ID da AWS.")
	awsRegion := deleteCmd.String("region", "us-east-1", "Região da AWS.")
	refreshState := deleteCmd.Bool("refresh", true, "Validar estado real antes de destruir.")

	secretsProvider := deleteCmd.String("secrets-provider", "awskms", "Provider de criptografia: 'passphrase' ou 'awskms'.")
	kmsKeyID := deleteCmd.String("kms-key-id", "", "ID da chave KMS (se usar awskms).")
	configPassphrase := deleteCmd.String("config-passphrase", "", "Passphrase (se usar passphrase).")

	deleteCmd.Parse(args)

	if *secretsProvider == "awskms" {
		if *kmsKeyID == "" {
			fmt.Fprintln(os.Stderr, "❌ ERRO: Você precisa informar --kms-key-id no delete")
			os.Exit(1)
		}
		os.Setenv("PULUMI_CONFIG_PASSPHRASE", *kmsKeyID)
	}

	if *secretsProvider == "passphrase" {
		finalPassphrase := *configPassphrase
		if finalPassphrase == "" {
			finalPassphrase = os.Getenv("PULUMI_CONFIG_PASSPHRASE")
		}
		if finalPassphrase == "" {
			fmt.Fprintln(os.Stderr, "❌ ERRO: Passphrase necessária para deletar")
			os.Exit(1)
		}
		os.Setenv("PULUMI_CONFIG_PASSPHRASE", finalPassphrase)
	}

	if *clusterName == "" {
		fmt.Fprintln(os.Stderr, "❌ Erro: O flag --name é obrigatório.")
		fmt.Fprintln(os.Stderr, "   Use o mesmo nome que você definiu ao criar o cluster.")
		deleteCmd.Usage()
		os.Exit(1)
	}

	var backendURL string
	if *stateBucketName != "" {
		// Se S3 foi usado na criação, monte a URL com lock Dynamo
		backendURL = fmt.Sprintf("s3://%s?region=%s", *stateBucketName, *awsRegion)
		fmt.Fprintln(os.Stderr, "🧹 Usando backend S3:", backendURL)
	} else {
		// Tente usar o backend do 'pulumi login'; se não houver, caia para local
		current, hasToken := getCurrentPulumiBackend()
		switch {
		case current == "":
			home, _ := os.UserHomeDir()
			localDir := filepath.Join(home, ".chatcli", "pulumi", *clusterName)
			_ = os.MkdirAll(localDir, 0755)
			backendURL = "file://" + localDir
			fmt.Fprintln(os.Stderr, "🧹 Backend local (file) [fallback]:", backendURL)

		case strings.HasPrefix(current, "http://") || strings.HasPrefix(current, "https://"):
			if hasToken {
				// Deixe vazio para reutilizar o login do CLI
				fmt.Fprintln(os.Stderr, "🧹 Backend: Pulumi Cloud (via 'pulumi login')")
			} else {
				home, _ := os.UserHomeDir()
				localDir := filepath.Join(home, ".chatcli", "pulumi", *clusterName)
				_ = os.MkdirAll(localDir, 0755)
				backendURL = "file://" + localDir
				fmt.Fprintln(os.Stderr, "⚠️  Sem token do Pulumi Cloud; usando backend local (file):", backendURL)
			}

		default:
			backendURL = current
			fmt.Fprintln(os.Stderr, "🧹 Backend (via 'pulumi login'):", backendURL)
		}
	}

	fmt.Fprintf(os.Stderr, "🗑️  Iniciando destruição da stack: %s\n", *clusterName)

	if err := pulumi_orchestrator.DestroyEKS(
		context.Background(),
		backendURL,
		*clusterName,
		*awsRegion,
		*refreshState,
		*secretsProvider,
		*kmsKeyID,
		*configPassphrase,
	); err != nil {
		fmt.Fprintf(os.Stderr, "❌ Falha na destruição: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "\n🎉 Cluster destruído com sucesso.")
}

func handleCleanup(args []string) {
	cleanupCmd := flag.NewFlagSet("cleanup", flag.ExitOnError)

	stateBucketName := cleanupCmd.String("state-bucket-name", "", "Bucket S3 a deletar")
	kmsKeyAlias := cleanupCmd.String("kms-key-alias", "", "Alias da chave KMS a deletar")
	clusterName := cleanupCmd.String("cluster-name", "", "Nome do cluster (para inferir recursos)")
	awsRegion := cleanupCmd.String("region", "us-east-1", "Região AWS")
	dryRun := cleanupCmd.Bool("dry-run", false, "Simula sem deletar")
	forceEmpty := cleanupCmd.Bool("force-empty-bucket", true, "Esvazia bucket antes de deletar")
	preview := cleanupCmd.Bool("preview", false, "Mostra o que será deletado sem executar")
	autoApprove := cleanupCmd.Bool("auto-approve", false, "Pula confirmação (use com cuidado em pipelines)")

	cleanupCmd.Parse(args)

	// Inferir nomes se cluster-name foi passado
	if *clusterName != "" {
		if *stateBucketName == "" {
			*stateBucketName = *clusterName + "-state"
		}
		if *kmsKeyAlias == "" {
			*kmsKeyAlias = "pulumi-secrets-" + *clusterName
		}
	}

	if *stateBucketName == "" && *kmsKeyAlias == "" {
		fmt.Fprintln(os.Stderr, "❌ Erro: Especifique pelo menos um recurso para deletar")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Uso:")
		fmt.Fprintln(os.Stderr, "  @eks cleanup --cluster-name=prod-eks --auto-approve")
		fmt.Fprintln(os.Stderr, "  @eks cleanup --state-bucket-name=my-bucket --kms-key-alias=my-key --auto-approve")
		os.Exit(1)
	}

	awsClients, err := aws_bootstrap.NewAWSClients(context.Background(), *awsRegion)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Erro ao conectar AWS: %v\n", err)
		os.Exit(1)
	}

	opts := aws_bootstrap.CleanupOptions{
		BucketName:       *stateBucketName,
		KMSKeyAlias:      *kmsKeyAlias,
		DryRun:           *dryRun,
		ForceEmptyBucket: *forceEmpty,
	}

	if *preview {
		estimate, err := awsClients.GetCleanupEstimate(context.Background(), opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ Erro ao calcular preview: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, estimate)
		fmt.Fprintln(os.Stderr, "💡 Execute com --auto-approve para deletar os recursos")
		return
	}

	if !*dryRun && !*autoApprove {
		fmt.Fprintln(os.Stderr, "❌ ERRO: Flag --auto-approve é obrigatória para executar deleções reais")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "⚠️  AVISO DE SEGURANÇA:")
		fmt.Fprintln(os.Stderr, "   Esta operação é IRREVERSÍVEL e deletará:")
		if *stateBucketName != "" {
			fmt.Fprintf(os.Stderr, "   - Bucket S3: %s\n", *stateBucketName)
		}
		if *kmsKeyAlias != "" {
			fmt.Fprintf(os.Stderr, "   - Chave KMS: %s\n", *kmsKeyAlias)
		}
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Opções seguras:")
		fmt.Fprintln(os.Stderr, "  1. Preview:  @eks cleanup --cluster-name=prod-eks --preview")
		fmt.Fprintln(os.Stderr, "  2. Dry-run:  @eks cleanup --cluster-name=prod-eks --dry-run")
		fmt.Fprintln(os.Stderr, "  3. Executar: @eks cleanup --cluster-name=prod-eks --auto-approve")
		os.Exit(1)
	}

	if *autoApprove && !*dryRun {
		fmt.Fprintln(os.Stderr, "⚠️  MODO AUTO-APPROVE ATIVADO")
		fmt.Fprintln(os.Stderr, "   Recursos serão deletados SEM confirmação adicional")
		fmt.Fprintln(os.Stderr, "")
	}

	fmt.Fprintln(os.Stderr, "🗑️  Iniciando cleanup de recursos...")
	fmt.Fprintln(os.Stderr, "")

	fmt.Fprintln(os.Stderr, "📋 Recursos a processar:")
	if *stateBucketName != "" {
		fmt.Fprintf(os.Stderr, "   - Bucket S3: %s\n", *stateBucketName)
	}
	if *kmsKeyAlias != "" {
		fmt.Fprintf(os.Stderr, "   - Chave KMS: %s\n", *kmsKeyAlias)
	}
	fmt.Fprintln(os.Stderr, "")

	result, err := awsClients.FullCleanup(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Erro durante cleanup: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "\n"+strings.Repeat("=", 60))
	fmt.Fprintln(os.Stderr, "📋 RELATÓRIO DE CLEANUP")
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 60))

	if result.BucketDeleted {
		fmt.Fprintf(os.Stderr, "✅ Bucket S3 deletado: %s (%d objetos removidos)\n", *stateBucketName, result.BucketObjectsDeleted)
	}
	if result.KMSKeyScheduled {
		fmt.Fprintf(os.Stderr, "⏳ Chave KMS agendada para deleção: %s (efetivo em 7 dias)\n", *kmsKeyAlias)
	}

	if len(result.Errors) > 0 {
		fmt.Fprintln(os.Stderr, "\n⚠️  Avisos/Erros parciais:")
		for _, err := range result.Errors {
			fmt.Fprintf(os.Stderr, "   - %v\n", err)
		}
		fmt.Fprintln(os.Stderr, "\n⚠️  Cleanup concluído COM avisos (exit code 2)")
		os.Exit(2)
	}

	fmt.Fprintln(os.Stderr, "\n🎉 Cleanup concluído com sucesso!")
	fmt.Fprintln(os.Stderr, "💰 Economia estimada: ~$1-5/mês")
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 60))
}

func printMetadata() {
	meta := Metadata{
		Name:        "@eks",
		Description: "Platform Engineering CLI: Cria clusters EKS completos com VPC, Spot Instances, ArgoCD, Istio e Nginx e gestão avançada de KMS",
		Usage:       "@eks <create|delete|kms-info> [options]",
		Version:     "3.6.3",
	}
	_ = json.NewEncoder(os.Stdout).Encode(meta)
}

func printSchema() {
	schema := ExtendedMetadata{
		Subcommands: []SubcommandDefinition{
			{
				Name:        "create",
				Description: "Cria ou atualiza a plataforma EKS.",
				Examples: []string{
					"# Let's Encrypt Production (padrão)\n@eks create --with-cert-manager --base-domain=example.com --cert-manager-email=admin@example.com",

					"# Google Trust Services Production\n@eks create --with-cert-manager --base-domain=example.com --cert-manager-email=admin@example.com --acme-provider=google",

					"# Testes com Staging (qualquer provider)\n@eks create --with-cert-manager --base-domain=example.com --cert-manager-email=admin@example.com --acme-provider=google --acme-server=staging",

					"# Google Trust (Production) com EAB via flags\n@eks create --with-cert-manager --base-domain=example.com --cert-manager-email=admin@example.com --acme-provider=google --acme-eab-kid=SEU_KID --acme-eab-hmac=SEU_HMAC",

					"# Google Trust (Production) com EAB via env\nexport ACME_EAB_KID=SEU_KID\nexport ACME_EAB_HMAC=SEU_HMAC\n@eks create --with-cert-manager --base-domain=example.com --cert-manager-email=admin@example.com --acme-provider=google",
				},
				Flags: []FlagDefinition{
					// ===== CLUSTER =====
					{Name: "--name", Type: "string", Default: "prod-eks", Description: "Identificador do Cluster (Stack Name)."},
					{Name: "--region", Type: "string", Default: "us-east-1", Description: "Região AWS."},
					{Name: "--k8s-version", Type: "string", Default: "1.31", Description: "Versão do Kubernetes."},

					// ===== BACKEND DE ESTADO =====
					{Name: "--state-bucket-name", Type: "string", Description: "Bucket S3 para estado do Pulumi."},

					// ===== SECRETS ENCRYPTION =====
					{Name: "--secrets-provider", Type: "string", Default: "awskms", Description: "Provider de criptografia para pulumi var PULUMI_CONFIG_PASSPHRASE: 'passphrase' (local), 'awskms' (AWS recomendado), Não pode ser alterado posteriormente."},
					{Name: "--kms-key-id", Type: "string", Description: "ID da chave KMS (formato: 'alias/key-name' ou ARN completo). Cria automaticamente se omitido com awskms."},
					{Name: "--config-passphrase", Type: "string", Description: "Passphrase para secrets (obrigatório se secrets-provider=passphrase). Alternativamente use env var PULUMI_CONFIG_PASSPHRASE."},
					{
						Name:        "--kms-action",
						Type:        "string",
						Default:     "reuse",
						Description: "Ação se chave KMS já existe: 'reuse' (reutiliza, padrão), 'fail' (aborta se existe), 'rotate' (cria nova com timestamp).",
					},

					// ===== NODES =====
					{Name: "--node-type", Type: "string", Default: "t3.medium", Description: "Tipo da instância EC2 dos nós."},
					{Name: "--min-nodes", Type: "int", Default: "2", Description: "Número mínimo de nós no cluster."},
					{Name: "--max-nodes", Type: "int", Default: "5", Description: "Número máximo de nós no cluster."},
					{Name: "--use-spot", Type: "bool", Default: "false", Description: "Usar instâncias Spot (economia de ~70%)."},

					// ===== REDE =====
					{Name: "--vpc-id", Type: "string", Description: "ID da VPC existente (cria nova se vazio)."},
					{Name: "--private-subnets", Type: "string", Description: "IDs das subnets privadas (separadas por vírgula)."},
					{Name: "--public-subnets", Type: "string", Description: "IDs das subnets públicas (separadas por vírgula)."},
					{Name: "--extra-ingress-rules", Type: "string", Description: "Regras extras de Security Group (ex: '8080:0.0.0.0/0,9090:10.0.0.0/16')."},

					// ===== COMPONENTES CORE =====
					{Name: "--with-lb-controller", Type: "bool", Default: "true", Description: "Instalar AWS Load Balancer Controller."},
					{Name: "--with-nginx", Type: "bool", Default: "false", Description: "Instalar Nginx Ingress Controller."},

					// ===== TLS / CERTIFICADOS =====
					{Name: "--with-cert-manager", Type: "bool", Default: "false", Description: "Instalar Cert-Manager (gestão automática de certificados)."},
					{Name: "--cert-manager-email", Type: "string", Description: "Email para Let's Encrypt (obrigatório se usar --with-cert-manager)."},
					{Name: "--acme-server", Type: "string", Default: "production", Description: "Servidor ACME: 'production' ou 'staging' (aplica-se ao provedor escolhido)."},
					{
						Name:        "--acme-provider",
						Type:        "string",
						Default:     "letsencrypt",
						Description: "Provedor ACME para certificados TLS: 'letsencrypt' (Let's Encrypt) ou 'google' (Google Trust Services). Google tem rate limits mais generosos.",
					},
					{
						Name:        "--acme-eab-kid",
						Type:        "string",
						Description: "EAB KeyID (obrigatório para --acme-provider=google). Pode vir de env ACME_EAB_KID.",
						RequiredWhen: []string{
							"--with-cert-manager",
							"--acme-provider=google",
						},
						ValidationError: "Google Trust Services exige EAB: informe --acme-eab-kid (ou ACME_EAB_KID).",
					},
					{
						Name:        "--acme-eab-hmac",
						Type:        "string",
						Description: "EAB HMAC key (obrigatório para --acme-provider=google). Pode vir de env ACME_EAB_HMAC.",
						RequiredWhen: []string{
							"--with-cert-manager",
							"--acme-provider=google",
						},
						ValidationError: "Google Trust Services exige EAB: informe --acme-eab-hmac (ou ACME_EAB_HMAC).",
					},
					{
						Name:        "--acme-eab-key-alg",
						Type:        "string",
						Default:     "HS256",
						Description: "Algoritmo EAB: HS256|HS384|HS512 (padrão: HS256).",
					},
					{
						Name:        "--acme-eab-secret-name",
						Type:        "string",
						Default:     "acme-eab-secret",
						Description: "Nome do Secret no namespace cert-manager para armazenar o HMAC.",
					},
					{
						Name:        "--base-domain",
						Type:        "string",
						Default:     "",
						Description: "Domínio base para certificado wildcard (ex: example.com cria *.example.com).",
						RequiredWhen: []string{
							"--with-cert-manager",
							"--with-external-dns",
						},
						ValidationError: "Obrigatório quando usar Cert-Manager ou External DNS",
					},
					{Name: "--with-external-dns", Type: "bool", Default: "false", Description: "Instalar External DNS (cria registros Route53 automaticamente)."},

					// ===== APLICAÇÕES =====
					{Name: "--with-argocd", Type: "bool", Default: "false", Description: "Instalar ArgoCD (GitOps CD)."},
					{Name: "--argocd-domain", Type: "string", Description: "Domínio completo do ArgoCD (ex: argo.example.com). Requer --with-nginx e --with-cert-manager."},

					// ===== SERVICE MESH =====
					{Name: "--with-istio", Type: "bool", Default: "false", Description: "Instalar Istio Service Mesh."},
					{Name: "--with-istio-observability", Type: "bool", Default: "false", Description: "Instalar pilha de observabilidade do Istio (Kiali, Prometheus, Grafana). Requer --with-istio."},
					{Name: "--with-istio-tracing", Type: "bool", Default: "false", Description: "Instalar Jaeger para tracing distribuído com Istio. Requer --with-istio-observability."},

					// ===== OUTROS =====
					{Name: "--refresh", Type: "bool", Default: "true", Description: "Valida estado real dos recursos antes de aplicar mudanças (recomendado). Use --refresh=false para pular validação."},
				},
			},
			{
				Name:        "delete",
				Description: "Destrói o cluster e recursos associados.",
				Flags: []FlagDefinition{
					{Name: "--name", Type: "string", Description: "Nome do cluster (mesmo usado em create)."},
					{Name: "--region", Type: "string", Default: "us-east-1", Description: "Região AWS."},
					{Name: "--state-bucket-name", Type: "string", Description: "Bucket S3 do estado (mesmo usado em create)."},
					{Name: "--refresh", Type: "bool", Default: "true", Description: "Valida estado real antes de destruir."},
				},
			},
			{
				Name:        "cleanup",
				Description: "Remove recursos auxiliares (bucket S3, DynamoDB, KMS) para custo zero",
				Examples: []string{
					"# Preview (seguro, sem confirmação necessária)",
					"@eks cleanup --cluster-name=prod-eks --preview",
					"",
					"# Dry-run (simulação)",
					"@eks cleanup --cluster-name=prod-eks --dry-run",
					"",
					"# Execução real (REQUER --auto-approve)",
					"@eks cleanup --cluster-name=prod-eks --auto-approve",
					"",
					"# Pipeline CI/CD (com logs estruturados)",
					"@eks cleanup \\",
					"  --cluster-name=prod-eks \\",
					"  --region=us-east-1 \\",
					"  --auto-approve",
					"",
					"# Cleanup seletivo",
					"@eks cleanup \\",
					"  --state-bucket-name=my-bucket \\",
					"  --kms-key-alias=my-key \\",
					"  --auto-approve",
				},
				Flags: []FlagDefinition{
					{Name: "--cluster-name", Type: "string", Description: "Nome do cluster (infere automaticamente nomes dos recursos)"},
					{Name: "--state-bucket-name", Type: "string", Description: "Bucket S3 a deletar"},
					{Name: "--kms-key-alias", Type: "string", Description: "Alias da chave KMS a deletar"},
					{Name: "--region", Type: "string", Default: "us-east-1", Description: "Região AWS"},
					{Name: "--preview", Type: "bool", Default: "false", Description: "Mostra o que será deletado sem executar (não requer confirmação)"},
					{Name: "--dry-run", Type: "bool", Default: "false", Description: "Simula operação sem deletar (não requer confirmação)"},
					{Name: "--force-empty-bucket", Type: "bool", Default: "true", Description: "Esvazia bucket antes de deletar"},
					{
						Name:            "--auto-approve",
						Type:            "bool",
						Default:         "false",
						Description:     "Confirmação de deleção IRREVERSIVEL, (OBRIGATÓRIO para executar deleções reais). Use com cuidado!",
						RequiredWhen:    []string{"!--dry-run", "!--preview"},
						ValidationError: "Flag --auto-approve é obrigatória para deleções reais (proteção contra deleções acidentais)",
					},
				},
			},
			{
				Name:        "kms-info",
				Description: "Exibe informações detalhadas de uma chave KMS",
				Examples: []string{
					"# Buscar por nome do cluster (infere alias automaticamente)",
					"@eks kms-info --cluster-name=prod-eks",
					"",
					"# Buscar por alias específico",
					"@eks kms-info --kms-key-id=alias/pulumi-secrets-prod-eks",
					"",
					"# Buscar por KeyID direto",
					"@eks kms-info --kms-key-id=12345678-1234-1234-1234-123456789012",
					"",
					"# Buscar por ARN",
					"@eks kms-info --kms-key-id=arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012",
					"",
					"# Especificar região diferente",
					"@eks kms-info --cluster-name=prod-eks --region=us-west-2",
					"",
					"# Exemplo de saída:",
					"# 🔑 INFORMAÇÕES DA CHAVE KMS:",
					"# ============================================================",
					"# Alias:        alias/pulumi-secrets-prod-eks",
					"# KeyID:        12345678-1234-1234-1234-123456789012",
					"# ARN:          arn:aws:kms:us-east-1:123456789012:key/12345...",
					"# Estado:       Enabled",
					"# Criado em:    2025-01-19T14:30:00Z",
					"# Gerenciado:   CUSTOMER",
					"#",
					"# Tags:",
					"#   - ManagedBy: chatcli-eks",
					"#   - Purpose: pulumi-secrets",
					"#   - CreatedAt: 2025-01-19T14:30:00Z",
					"# ============================================================",
				},
				Flags: []FlagDefinition{
					{
						Name:        "--cluster-name",
						Type:        "string",
						Description: "Nome do cluster (infere alias: alias/pulumi-secrets-{cluster-name})",
					},
					{
						Name:        "--kms-key-id",
						Type:        "string",
						Description: "Identificador da chave: alias (alias/name), KeyID (UUID), ou ARN completo",
					},
					{
						Name:        "--region",
						Type:        "string",
						Default:     "us-east-1",
						Description: "Região AWS onde a chave está localizada",
					},
				},
			},
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(schema)
}
