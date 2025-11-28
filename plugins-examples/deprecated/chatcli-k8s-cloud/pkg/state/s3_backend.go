package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
)

// S3Backend implementa Backend usando S3 + DynamoDB
type S3Backend struct {
	bucketName    string
	region        string
	lockTableName string
	prefix        string // prefixo para organizar estados

	s3Client     *s3.Client
	dynamoClient *dynamodb.Client
	config       BackendConfig

	initialized bool
}

// NewS3Backend cria um novo backend S3
func NewS3Backend(bucketName, region, lockTableName string) *S3Backend {
	if lockTableName == "" {
		lockTableName = "k8s-cloud-state-locks"
	}

	return &S3Backend{
		bucketName:    bucketName,
		region:        region,
		lockTableName: lockTableName,
		prefix:        "clusters/",
	}
}

// Initialize garante que bucket e tabela existem
func (b *S3Backend) Initialize() error {
	if b.initialized {
		return nil
	}

	logger.Progress("Inicializando state backend S3...")

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(b.region))
	if err != nil {
		return fmt.Errorf("falha ao carregar config AWS: %w", err)
	}

	b.s3Client = s3.NewFromConfig(cfg)
	b.dynamoClient = dynamodb.NewFromConfig(cfg)

	if err := b.ensureBucket(ctx); err != nil {
		return fmt.Errorf("falha ao configurar bucket: %w", err)
	}

	if err := b.ensureLockTable(ctx); err != nil {
		return fmt.Errorf("falha ao configurar lock table: %w", err)
	}

	b.initialized = true
	logger.Success("State backend inicializado!")

	return nil
}

// ensureBucket garante que o bucket existe
func (b *S3Backend) ensureBucket(ctx context.Context) error {
	_, err := b.s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(b.bucketName),
	})

	if err == nil {
		logger.Infof("   ✓ Bucket S3 '%s' já existe", b.bucketName)
		return b.validateBucketConfig(ctx)
	}

	logger.Infof("   📦 Criando bucket S3 '%s'...", b.bucketName)

	createInput := &s3.CreateBucketInput{
		Bucket: aws.String(b.bucketName),
	}

	if b.region != "us-east-1" {
		createInput.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(b.region),
		}
	}

	if _, err := b.s3Client.CreateBucket(ctx, createInput); err != nil {
		return fmt.Errorf("falha ao criar bucket: %w", err)
	}

	logger.Info("      ✓ Bucket criado")

	waiter := s3.NewBucketExistsWaiter(b.s3Client)
	if err := waiter.Wait(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(b.bucketName),
	}, 2*time.Minute); err != nil {
		return fmt.Errorf("timeout aguardando bucket: %w", err)
	}

	if err := b.configureBucket(ctx); err != nil {
		return err
	}

	logger.Info("   ✅ Bucket configurado!")
	return nil
}

// configureBucket aplica configurações de segurança
func (b *S3Backend) configureBucket(ctx context.Context) error {
	// 1. Bloquear acesso público
	logger.Info("      🔒 Bloqueando acesso público...")
	_, err := b.s3Client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(b.bucketName),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("falha ao bloquear acesso público: %w", err)
	}
	logger.Info("      ✓ Acesso público bloqueado")

	// 2. Habilitar versionamento
	logger.Info("      📚 Habilitando versionamento...")
	_, err = b.s3Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(b.bucketName),
		VersioningConfiguration: &types.VersioningConfiguration{
			Status: types.BucketVersioningStatusEnabled,
		},
	})
	if err != nil {
		return fmt.Errorf("falha ao habilitar versionamento: %w", err)
	}
	logger.Info("      ✓ Versionamento habilitado")

	// 3. Configurar criptografia
	logger.Info("      🔐 Configurando criptografia...")
	_, err = b.s3Client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(b.bucketName),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm: types.ServerSideEncryptionAes256,
					},
					BucketKeyEnabled: aws.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("falha ao configurar criptografia: %w", err)
	}
	logger.Info("      ✓ Criptografia configurada (AES256)")

	// 4. Lifecycle policies
	logger.Info("      ♻️  Configurando lifecycle policies...")
	if err := b.configureLifecycle(ctx); err != nil {
		logger.Warningf("      ⚠️  Falha ao configurar lifecycle: %v", err)
	} else {
		logger.Info("      ✓ Lifecycle configurado")
	}

	// 5. Tags
	logger.Info("      🏷️  Aplicando tags...")
	if err := b.applyBucketTags(ctx); err != nil {
		logger.Warningf("      ⚠️  Falha ao aplicar tags: %v", err)
	} else {
		logger.Info("      ✓ Tags aplicadas")
	}

	return nil
}

// configureLifecycle configura políticas de lifecycle - CORRIGIDO
func (b *S3Backend) configureLifecycle(ctx context.Context) error {
	rules := []types.LifecycleRule{
		// Regra 1: Expirar versões antigas após 30 dias
		{
			ID:     aws.String("expire-old-versions"),
			Status: types.ExpirationStatusEnabled,
			NoncurrentVersionExpiration: &types.NoncurrentVersionExpiration{
				NoncurrentDays: aws.Int32(30),
			},
			// AWS SDK v2: Prefix simples (sem wrapper)
			Prefix: aws.String(b.prefix),
		},
		// Regra 2: Limpar uploads incompletos
		{
			ID:     aws.String("cleanup-incomplete-uploads"),
			Status: types.ExpirationStatusEnabled,
			AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{
				DaysAfterInitiation: aws.Int32(7),
			},
			// Prefix vazio = aplicar a todo o bucket
			Prefix: aws.String(""),
		},
	}

	_, err := b.s3Client.PutBucketLifecycleConfiguration(ctx,
		&s3.PutBucketLifecycleConfigurationInput{
			Bucket: aws.String(b.bucketName),
			LifecycleConfiguration: &types.BucketLifecycleConfiguration{
				Rules: rules,
			},
		})

	return err
}

// applyBucketTags aplica tags ao bucket
func (b *S3Backend) applyBucketTags(ctx context.Context) error {
	tags := []types.Tag{
		{
			Key:   aws.String("ManagedBy"),
			Value: aws.String("chatcli-k8s-cloud"),
		},
		{
			Key:   aws.String("Purpose"),
			Value: aws.String("kubernetes-state"),
		},
		{
			Key:   aws.String("CreatedAt"),
			Value: aws.String(time.Now().Format(time.RFC3339)),
		},
	}

	_, err := b.s3Client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(b.bucketName),
		Tagging: &types.Tagging{
			TagSet: tags,
		},
	})

	return err
}

// validateBucketConfig valida configurações
func (b *S3Backend) validateBucketConfig(ctx context.Context) error {
	warnings := []string{}

	versioning, err := b.s3Client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{
		Bucket: aws.String(b.bucketName),
	})
	if err == nil {
		if versioning.Status != types.BucketVersioningStatusEnabled {
			warnings = append(warnings, "Versionamento não está habilitado")
		}
	}

	_, err = b.s3Client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{
		Bucket: aws.String(b.bucketName),
	})
	if err != nil {
		warnings = append(warnings, "Criptografia não configurada")
	}

	publicAccess, err := b.s3Client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{
		Bucket: aws.String(b.bucketName),
	})
	if err != nil || !*publicAccess.PublicAccessBlockConfiguration.BlockPublicAcls {
		warnings = append(warnings, "Bucket pode estar exposto publicamente")
	}

	if len(warnings) > 0 {
		logger.Warning("   ⚠️  Avisos de configuração do bucket:")
		for _, w := range warnings {
			logger.Warningf("      • %s", w)
		}
	}

	return nil
}

// ensureLockTable garante que a tabela DynamoDB existe - CORRIGIDO
func (b *S3Backend) ensureLockTable(ctx context.Context) error {
	_, err := b.dynamoClient.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(b.lockTableName),
	})

	if err == nil {
		logger.Infof("   ✓ Tabela DynamoDB '%s' já existe", b.lockTableName)
		return nil
	}

	logger.Infof("   🔐 Criando tabela de locks '%s'...", b.lockTableName)

	// CORREÇÃO: DynamoDB usa SSE automática diferente de S3
	createOutput, err := b.dynamoClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(b.lockTableName),
		AttributeDefinitions: []dynamodbtypes.AttributeDefinition{
			{
				AttributeName: aws.String("LockID"),
				AttributeType: dynamodbtypes.ScalarAttributeTypeS,
			},
		},
		KeySchema: []dynamodbtypes.KeySchemaElement{
			{
				AttributeName: aws.String("LockID"),
				KeyType:       dynamodbtypes.KeyTypeHash,
			},
		},
		BillingMode: dynamodbtypes.BillingModePayPerRequest,

		// CORREÇÃO: Para DynamoDB, use KMS ou omita (usa AWS-owned key por padrão)
		SSESpecification: &dynamodbtypes.SSESpecification{
			Enabled: aws.Bool(true),
			// REMOVER: SSEType não pode ser AES256 em DynamoDB
			// Quando Enabled=true sem SSEType, usa AWS-owned key (grátis e automático)
		},

		Tags: []dynamodbtypes.Tag{
			{
				Key:   aws.String("ManagedBy"),
				Value: aws.String("chatcli-k8s-cloud"),
			},
			{
				Key:   aws.String("Purpose"),
				Value: aws.String("state-locking"),
			},
		},
	})

	if err != nil {
		return fmt.Errorf("falha ao criar tabela: %w", err)
	}

	logger.Info("      ✓ Tabela criada, aguardando ficar ativa...")

	// Aguardar tabela
	waiter := dynamodb.NewTableExistsWaiter(b.dynamoClient)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(b.lockTableName),
	}, 2*time.Minute); err != nil {
		return fmt.Errorf("timeout aguardando tabela: %w", err)
	}

	// Habilitar point-in-time recovery DEPOIS da criação
	logger.Info("      ⏳ Habilitando point-in-time recovery...")
	_, err = b.dynamoClient.UpdateContinuousBackups(ctx, &dynamodb.UpdateContinuousBackupsInput{
		TableName: createOutput.TableDescription.TableName,
		PointInTimeRecoverySpecification: &dynamodbtypes.PointInTimeRecoverySpecification{
			PointInTimeRecoveryEnabled: aws.Bool(true),
		},
	})
	if err != nil {
		logger.Warningf("      ⚠️  Não foi possível habilitar point-in-time recovery: %v", err)
	} else {
		logger.Info("      ✓ Point-in-time recovery habilitado")
	}

	logger.Info("   ✅ Tabela de locks configurada!")
	return nil
}

// Save salva o estado de um cluster
func (b *S3Backend) Save(clusterName string, state interface{}) error {
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("falha ao serializar estado: %w", err)
	}

	key := b.stateKey(clusterName)
	ctx := context.Background()

	_, err = b.s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(b.bucketName),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(data),
		ServerSideEncryption: types.ServerSideEncryptionAes256,
		ContentType:          aws.String("application/json"),
		Metadata: map[string]string{
			"cluster":   clusterName,
			"timestamp": time.Now().Format(time.RFC3339),
		},
	})

	if err != nil {
		return fmt.Errorf("falha ao salvar estado: %w", err)
	}

	logger.Debugf("Estado salvo: s3://%s/%s", b.bucketName, key)
	return nil
}

// Load carrega o estado de um cluster
func (b *S3Backend) Load(clusterName string, state interface{}) error {
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			return err
		}
	}

	key := b.stateKey(clusterName)
	ctx := context.Background()

	result, err := b.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		return fmt.Errorf("falha ao carregar estado: %w", err)
	}
	defer result.Body.Close()

	if err := json.NewDecoder(result.Body).Decode(state); err != nil {
		return fmt.Errorf("falha ao deserializar estado: %w", err)
	}

	logger.Debugf("Estado carregado: s3://%s/%s", b.bucketName, key)
	return nil
}

// Delete remove o estado de um cluster
func (b *S3Backend) Delete(clusterName string) error {
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			return err
		}
	}

	key := b.stateKey(clusterName)
	ctx := context.Background()

	_, err := b.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		return fmt.Errorf("falha ao deletar estado: %w", err)
	}

	logger.Debugf("Estado removido: s3://%s/%s", b.bucketName, key)
	return nil
}

// List lista todos os clusters com estado
func (b *S3Backend) List() ([]string, error) {
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			return nil, err
		}
	}

	ctx := context.Background()

	result, err := b.s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucketName),
		Prefix: aws.String(b.prefix),
	})

	if err != nil {
		return nil, fmt.Errorf("falha ao listar estados: %w", err)
	}

	var clusters []string
	for _, obj := range result.Contents {
		key := *obj.Key
		name := filepath.Base(key)
		name = name[:len(name)-5] // remover .json
		clusters = append(clusters, name)
	}

	return clusters, nil
}

// Exists verifica se um cluster tem estado
func (b *S3Backend) Exists(clusterName string) (bool, error) {
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			return false, err
		}
	}

	key := b.stateKey(clusterName)
	ctx := context.Background()

	_, err := b.s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		return false, nil
	}

	return true, nil
}

// Lock adquire lock para operações concorrentes
func (b *S3Backend) Lock(clusterName string) error {
	if !b.initialized {
		if err := b.Initialize(); err != nil {
			return err
		}
	}

	ctx := context.Background()
	lockID := b.lockID(clusterName)

	hostname, _ := os.Hostname()
	lockInfo := LockInfo{
		ID:        lockID,
		Cluster:   clusterName,
		Operation: "cluster-operation",
		Owner:     hostname,
		CreatedAt: time.Now(),
	}

	lockData, _ := json.Marshal(lockInfo)

	_, err := b.dynamoClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(b.lockTableName),
		Item: map[string]dynamodbtypes.AttributeValue{
			"LockID": &dynamodbtypes.AttributeValueMemberS{
				Value: lockID,
			},
			"Info": &dynamodbtypes.AttributeValueMemberS{
				Value: string(lockData),
			},
			"CreatedAt": &dynamodbtypes.AttributeValueMemberN{
				Value: fmt.Sprintf("%d", time.Now().Unix()),
			},
		},
		ConditionExpression: aws.String("attribute_not_exists(LockID)"),
	})

	if err != nil {
		return fmt.Errorf("falha ao adquirir lock: %w", err)
	}

	logger.Debugf("Lock adquirido: %s", lockID)
	return nil
}

// Unlock libera lock
func (b *S3Backend) Unlock(clusterName string) error {
	if !b.initialized {
		return nil
	}

	ctx := context.Background()
	lockID := b.lockID(clusterName)

	_, err := b.dynamoClient.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(b.lockTableName),
		Key: map[string]dynamodbtypes.AttributeValue{
			"LockID": &dynamodbtypes.AttributeValueMemberS{
				Value: lockID,
			},
		},
	})

	if err != nil {
		logger.Warningf("Falha ao liberar lock: %v", err)
		return err
	}

	logger.Debugf("Lock liberado: %s", lockID)
	return nil
}

// GetInfo retorna informações do backend
func (b *S3Backend) GetInfo() BackendInfo {
	return BackendInfo{
		Type:              "s3",
		Location:          b.bucketName,
		Region:            b.region,
		Encrypted:         true,
		VersioningEnabled: true,
		LockingEnabled:    true,
		Metadata: map[string]string{
			"lockTable": b.lockTableName,
			"prefix":    b.prefix,
		},
	}
}

// Helper methods
func (b *S3Backend) stateKey(clusterName string) string {
	return fmt.Sprintf("%s%s.json", b.prefix, clusterName)
}

func (b *S3Backend) lockID(clusterName string) string {
	return fmt.Sprintf("cluster-%s", clusterName)
}
