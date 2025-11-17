package pulumi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
)

// StateBackendConfig configuração do backend S3
type StateBackendConfig struct {
	S3Bucket      string
	S3KeyPrefix   string
	DynamoDBTable string
	Region        string
	AutoCreate    bool
	AccountID     string
}

// generateRandomSuffix gera sufixo aleatório de 8 caracteres
func generateRandomSuffix() string {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback para timestamp se random falhar
		return fmt.Sprintf("%x", time.Now().Unix()%0xFFFFFFFF)
	}
	return hex.EncodeToString(bytes)
}

// GenerateBackendURL gera URL do backend automaticamente com nome único
func GenerateBackendURL(ctx context.Context, region, clusterName string) (string, error) {
	// Carregar config AWS para obter Account ID
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("erro ao carregar config AWS: %w", err)
	}

	// Obter Account ID
	stsClient := sts.NewFromConfig(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("erro ao obter Account ID: %w", err)
	}

	accountID := *identity.Account

	// Gerar nome de bucket GLOBALMENTE ÚNICO
	// Formato: k8s-cloud-{account-id}-{region}-{random}
	// Exemplo: k8s-cloud-123456789012-us-east-1-a1b2c3d4
	randomSuffix := generateRandomSuffix()
	bucketName := fmt.Sprintf("k8s-cloud-%s-%s-%s", accountID, region, randomSuffix)

	// Validar se nome é válido (S3 bucket naming rules)
	if len(bucketName) > 63 {
		return "", fmt.Errorf("nome do bucket muito longo: %s", bucketName)
	}

	s3Client := s3.NewFromConfig(awsCfg)

	// Verificar se bucket já existe (improvável mas possível)
	_, err = s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})

	if err == nil {
		// Bucket existe! Tentar outro nome
		logger.Warningf("⚠️  Bucket '%s' já existe, gerando novo nome...", bucketName)
		randomSuffix = generateRandomSuffix()
		bucketName = fmt.Sprintf("k8s-cloud-%s-%s-%s", accountID, region, randomSuffix)
	}

	// URL completa com path do cluster
	backendURL := fmt.Sprintf("s3://%s/clusters/%s", bucketName, clusterName)

	logger.Infof("📦 Backend auto-gerado: %s", backendURL)
	logger.Debugf("   • Account ID: %s", accountID)
	logger.Debugf("   • Região: %s", region)
	logger.Debugf("   • Sufixo: %s", randomSuffix)

	return backendURL, nil
}

// GenerateBackendURLWithName gera backend URL a partir de um nome de bucket fornecido
func GenerateBackendURLWithName(ctx context.Context, bucketName, region, clusterName string) (string, error) {
	// ✅ CORREÇÃO: Extrair apenas o nome do bucket se vier com path
	// Se vier: "bucket/path" -> usar apenas "bucket"
	// Se vier: "bucket" -> usar como está
	if strings.Contains(bucketName, "/") {
		parts := strings.Split(bucketName, "/")
		bucketName = parts[0]
		logger.Debugf("Extraído nome do bucket: %s", bucketName)
	}

	// Validar nome do bucket
	if !isValidBucketName(bucketName) {
		return "", fmt.Errorf("nome de bucket inválido: %s (deve ter 3-63 chars, lowercase, numbers, hyphens)", bucketName)
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return "", fmt.Errorf("erro ao carregar config AWS: %w", err)
	}

	s3Client := s3.NewFromConfig(awsCfg)

	// Verificar se bucket está disponível
	_, err = s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})

	if err == nil {
		// Bucket já existe, pode ser nosso ou de outra conta
		logger.Infof("📦 Bucket '%s' já existe, será reutilizado", bucketName)
	} else {
		// Bucket não existe ou não temos acesso
		logger.Infof("📦 Bucket '%s' será criado", bucketName)
	}

	backendURL := fmt.Sprintf("s3://%s/clusters/%s", bucketName, clusterName)
	return backendURL, nil
}

// isValidBucketName valida nome de bucket S3
func isValidBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	// Bucket names devem ser lowercase, numbers e hyphens
	// Não pode começar/terminar com hyphen
	// Não pode ter dois hyphens consecutivos
	if name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	if strings.Contains(name, "--") {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// ParseBackendURL parseia URL s3://bucket/path ou apenas nome de bucket
func ParseBackendURL(backendURL, region, clusterName string) (*StateBackendConfig, error) {
	cfg := &StateBackendConfig{
		Region:     region,
		AutoCreate: true,
	}

	// Caso 1: URL completa s3://bucket/path
	if strings.HasPrefix(backendURL, "s3://") {
		parts := strings.TrimPrefix(backendURL, "s3://")
		segments := strings.SplitN(parts, "/", 2)

		if len(segments) == 0 || segments[0] == "" {
			return nil, fmt.Errorf("bucket name não pode ser vazio")
		}

		cfg.S3Bucket = segments[0]
		if len(segments) == 2 {
			cfg.S3KeyPrefix = segments[1]
		} else {
			cfg.S3KeyPrefix = fmt.Sprintf("clusters/%s", clusterName)
		}
	} else {
		// Caso 2: Apenas nome do bucket
		if !isValidBucketName(backendURL) {
			return nil, fmt.Errorf("nome de bucket inválido: %s", backendURL)
		}
		cfg.S3Bucket = backendURL
		cfg.S3KeyPrefix = fmt.Sprintf("clusters/%s", clusterName)
	}

	// Nome da tabela DynamoDB
	cfg.DynamoDBTable = fmt.Sprintf("%s-locks", cfg.S3Bucket)

	return cfg, nil
}

// EnsureBackend garante que bucket S3 e tabela DynamoDB existem com guardrails
func EnsureBackend(ctx context.Context, cfg *StateBackendConfig) error {
	logger.Infof("🔍 Verificando backend: s3://%s/%s", cfg.S3Bucket, cfg.S3KeyPrefix)

	// Carregar configuração AWS
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return fmt.Errorf("erro ao carregar config AWS: %w", err)
	}

	// Obter Account ID para tags
	stsClient := sts.NewFromConfig(awsCfg)
	identity, err := stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("erro ao obter Account ID: %w", err)
	}
	cfg.AccountID = *identity.Account

	// Verificar/criar bucket S3
	if err := ensureS3BucketPro(ctx, awsCfg, cfg); err != nil {
		return err
	}

	// Verificar/criar tabela DynamoDB
	if err := ensureDynamoDBTablePro(ctx, awsCfg, cfg); err != nil {
		return err
	}

	logger.Success("✅ Backend configurado e pronto")
	return nil
}

func ensureS3BucketPro(ctx context.Context, awsCfg aws.Config, cfg *StateBackendConfig) error {
	s3Client := s3.NewFromConfig(awsCfg)

	// Verificar se bucket existe e temos acesso
	_, err := s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(cfg.S3Bucket),
	})

	if err == nil {
		logger.Infof("✅ Bucket S3 '%s' já existe e acessível", cfg.S3Bucket)
		// Verificar/atualizar configurações de segurança
		return ensureBucketSecuritySettings(ctx, s3Client, cfg)
	}

	// Bucket não existe ou não temos acesso
	if !cfg.AutoCreate {
		return fmt.Errorf("bucket S3 '%s' não existe/não acessível e auto-create está desabilitado", cfg.S3Bucket)
	}

	// Tentar criar bucket
	logger.Progressf("📦 Criando bucket S3 '%s' com guardrails produtivos...", cfg.S3Bucket)

	createInput := &s3.CreateBucketInput{
		Bucket: aws.String(cfg.S3Bucket),
	}

	// LocationConstraint necessário para regiões != us-east-1
	if cfg.Region != "us-east-1" {
		createInput.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(cfg.Region),
		}
	}

	_, err = s3Client.CreateBucket(ctx, createInput)
	if err != nil {
		// Verificar se erro é porque bucket existe em outra conta
		if strings.Contains(err.Error(), "BucketAlreadyExists") {
			return fmt.Errorf("❌ Bucket '%s' já existe em outra conta AWS (nomes são globalmente únicos). Use --state-bucket com outro nome", cfg.S3Bucket)
		}
		if strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") {
			// Bucket existe mas HeadBucket falhou (race condition?)
			logger.Warning("⚠️  Bucket existe mas não foi detectado anteriormente, continuando...")
			return ensureBucketSecuritySettings(ctx, s3Client, cfg)
		}
		return fmt.Errorf("erro ao criar bucket S3: %w", err)
	}

	logger.Success("✅ Bucket S3 criado")

	// Aguardar bucket estar pronto
	time.Sleep(2 * time.Second)

	// Aplicar guardrails
	if err := ensureBucketSecuritySettings(ctx, s3Client, cfg); err != nil {
		logger.Warningf("⚠️  Alguns guardrails não foram aplicados: %v", err)
	}

	return nil
}

func ensureBucketSecuritySettings(ctx context.Context, s3Client *s3.Client, cfg *StateBackendConfig) error {
	logger.Progress("🔒 Aplicando guardrails de segurança ao bucket...")

	// 1. Versionamento
	_, err := s3Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(cfg.S3Bucket),
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: s3types.BucketVersioningStatusEnabled,
		},
	})
	if err != nil {
		return fmt.Errorf("erro ao habilitar versionamento: %w", err)
	}
	logger.Info("  ✓ Versionamento habilitado")

	// 2. Encriptação
	_, err = s3Client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(cfg.S3Bucket),
		ServerSideEncryptionConfiguration: &s3types.ServerSideEncryptionConfiguration{
			Rules: []s3types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &s3types.ServerSideEncryptionByDefault{
						SSEAlgorithm: s3types.ServerSideEncryptionAes256,
					},
					BucketKeyEnabled: aws.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("erro ao habilitar encriptação: %w", err)
	}
	logger.Info("  ✓ Encriptação AES-256 habilitada")

	// 3. Bloquear acesso público
	_, err = s3Client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(cfg.S3Bucket),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("erro ao bloquear acesso público: %w", err)
	}
	logger.Info("  ✓ Acesso público bloqueado")

	// 4. Lifecycle policy (CORRIGIDO)
	_, err = s3Client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(cfg.S3Bucket),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{
				{
					ID:     aws.String("archive-old-versions"),
					Status: s3types.ExpirationStatusEnabled,
					Prefix: aws.String(""),
					NoncurrentVersionTransitions: []s3types.NoncurrentVersionTransition{
						{
							NoncurrentDays: aws.Int32(30),
							StorageClass:   s3types.TransitionStorageClassGlacierIr,
						},
					},
					NoncurrentVersionExpiration: &s3types.NoncurrentVersionExpiration{
						NoncurrentDays: aws.Int32(90),
					},
				},
			},
		},
	})
	if err != nil {
		logger.Warningf("  ⚠️  Não foi possível configurar lifecycle: %v", err)
	} else {
		logger.Info("  ✓ Lifecycle policy configurada (30d → Glacier, 90d → Delete)")
	}

	// 5. Tags
	_, err = s3Client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(cfg.S3Bucket),
		Tagging: &s3types.Tagging{
			TagSet: []s3types.Tag{
				{Key: aws.String("ManagedBy"), Value: aws.String("chatcli-k8s-cloud")},
				{Key: aws.String("Purpose"), Value: aws.String("pulumi-state-backend")},
				{Key: aws.String("Environment"), Value: aws.String("production")},
				{Key: aws.String("CostCenter"), Value: aws.String("infrastructure")},
				{Key: aws.String("AccountID"), Value: aws.String(cfg.AccountID)},
				{Key: aws.String("CreatedAt"), Value: aws.String(time.Now().Format(time.RFC3339))},
			},
		},
	})
	if err != nil {
		logger.Warningf("  ⚠️  Não foi possível aplicar tags: %v", err)
	} else {
		logger.Info("  ✓ Tags padronizadas aplicadas")
	}

	logger.Success("🔒 Guardrails de segurança aplicados com sucesso!")
	return nil
}

func ensureDynamoDBTablePro(ctx context.Context, awsCfg aws.Config, cfg *StateBackendConfig) error {
	dynamoClient := dynamodb.NewFromConfig(awsCfg)

	// Verificar se tabela existe
	describeOutput, err := dynamoClient.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(cfg.DynamoDBTable),
	})

	if err == nil {
		logger.Infof("✅ Tabela DynamoDB '%s' já existe", cfg.DynamoDBTable)

		if describeOutput.Table.SSEDescription == nil ||
			describeOutput.Table.SSEDescription.Status != types.SSEStatusEnabled {
			logger.Warning("  ⚠️  Encryption at rest não está habilitada")
		}

		return nil
	}

	if !cfg.AutoCreate {
		return fmt.Errorf("tabela DynamoDB '%s' não existe e auto-create está desabilitado", cfg.DynamoDBTable)
	}

	logger.Progressf("🔐 Criando tabela DynamoDB '%s' com guardrails...", cfg.DynamoDBTable)

	// CORREÇÃO: Remover SSESpecification ou usar apenas Enabled=true
	// DynamoDB usa encriptação padrão AWS-owned key quando habilitado
	_, err = dynamoClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(cfg.DynamoDBTable),
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String("LockID"),
				KeyType:       types.KeyTypeHash,
			},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String("LockID"),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		BillingMode: types.BillingModePayPerRequest,
		// ✅ CORREÇÃO: Usar SSESpecification corretamente
		SSESpecification: &types.SSESpecification{
			Enabled: aws.Bool(true),
			// ❌ REMOVER SSEType - deixa AWS usar a chave padrão
		},
		Tags: []types.Tag{
			{Key: aws.String("ManagedBy"), Value: aws.String("chatcli-k8s-cloud")},
			{Key: aws.String("Purpose"), Value: aws.String("pulumi-state-locks")},
			{Key: aws.String("Environment"), Value: aws.String("production")},
			{Key: aws.String("AccountID"), Value: aws.String(cfg.AccountID)},
			{Key: aws.String("CreatedAt"), Value: aws.String(time.Now().Format(time.RFC3339))},
		},
	})

	if err != nil {
		return fmt.Errorf("erro ao criar tabela DynamoDB: %w", err)
	}

	logger.Success("✅ Tabela DynamoDB criada")

	logger.Progress("⏳ Aguardando tabela ficar ativa...")
	waiter := dynamodb.NewTableExistsWaiter(dynamoClient)
	err = waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(cfg.DynamoDBTable),
	}, 2*time.Minute)

	if err != nil {
		return fmt.Errorf("timeout aguardando tabela: %w", err)
	}

	// Point-in-Time Recovery
	_, err = dynamoClient.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName: aws.String(cfg.DynamoDBTable),
		PointInTimeRecoverySpecification: &types.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: aws.Bool(true),
		},
	})
	if err != nil {
		logger.Warningf("⚠️  Não foi possível habilitar Point-in-Time Recovery: %v", err)
	} else {
		logger.Info("  ✓ Point-in-Time Recovery habilitado")
	}

	logger.Success("🔐 Tabela DynamoDB configurada com guardrails!")
	logger.Info("  ✓ Encriptação habilitada (AWS-owned key)")
	return nil
}
