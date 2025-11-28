package aws_bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmsTypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type KMSAction string

type KMSKeyInfo struct {
	Alias        string
	KeyID        string
	ARN          string
	State        string
	CreationDate time.Time
	KeyManager   string
	Tags         map[string]string
}

const (
	KMSActionReuse  KMSAction = "reuse"
	KMSActionFail   KMSAction = "fail"
	KMSActionRotate KMSAction = "rotate"
)

//func (c *AWSBackendClients) EnsureKMSKey(ctx context.Context, aliasName, description string) (string, error) {
//	kmsClient := kms.NewFromConfig(c.Config) // ✅ USAR c.Config (não c.cfg)
//
//	// Verificar se alias já existe
//	fullAlias := fmt.Sprintf("alias/%s", aliasName)
//	fmt.Fprintf(os.Stderr, "🔑 Verificando chave KMS: %s\n", fullAlias)
//
//	listAliasesOutput, err := kmsClient.ListAliases(ctx, &kms.ListAliasesInput{})
//	if err == nil {
//		for _, alias := range listAliasesOutput.Aliases {
//			if alias.AliasName != nil && *alias.AliasName == fullAlias {
//				if alias.TargetKeyId != nil {
//					fmt.Fprintf(os.Stderr, "   -> ✅ Chave existente: %s\n", *alias.TargetKeyId)
//					return *alias.TargetKeyId, nil
//				}
//			}
//		}
//	}
//
//	// Criar nova chave
//	fmt.Fprintf(os.Stderr, "   -> Criando nova chave KMS...\n")
//	createKeyOutput, err := kmsClient.CreateKey(ctx, &kms.CreateKeyInput{
//		Description: aws.String(description),
//		KeyUsage:    kmsTypes.KeyUsageTypeEncryptDecrypt,
//		Origin:      kmsTypes.OriginTypeAwsKms,
//		Tags: []kmsTypes.Tag{
//			{
//				TagKey:   aws.String("ManagedBy"),
//				TagValue: aws.String("chatcli-eks"),
//			},
//			{
//				TagKey:   aws.String("Purpose"),
//				TagValue: aws.String("pulumi-secrets"),
//			},
//		},
//	})
//	if err != nil {
//		return "", fmt.Errorf("falha ao criar chave KMS: %w", err)
//	}
//
//	keyID := *createKeyOutput.KeyMetadata.KeyId
//	fmt.Fprintf(os.Stderr, "   -> Chave criada: %s\n", keyID)
//
//	// Criar alias
//	fmt.Fprintf(os.Stderr, "   -> Criando alias: %s\n", fullAlias)
//	_, err = kmsClient.CreateAlias(ctx, &kms.CreateAliasInput{
//		AliasName:   aws.String(fullAlias),
//		TargetKeyId: aws.String(keyID),
//	})
//	if err != nil {
//		// Se alias já existe, não é erro fatal
//		if IsAliasAlreadyExistsError(err) {
//			fmt.Fprintf(os.Stderr, "   -> ⚠️  Alias já existe (provavelmente criado por outro processo)\n")
//			return keyID, nil
//		}
//		return "", fmt.Errorf("falha ao criar alias KMS: %w", err)
//	}
//
//	fmt.Fprintf(os.Stderr, "   -> ✅ Chave KMS configurada: %s\n", fullAlias)
//	return keyID, nil
//}

func (c *AWSBackendClients) EnsureKMSKeyWithAction(
	ctx context.Context,
	aliasName,
	description string,
	action KMSAction,
) (string, error) {
	kmsClient := kms.NewFromConfig(c.Config)

	// Verificar se alias base existe
	fullAlias := fmt.Sprintf("alias/%s", aliasName)
	fmt.Fprintf(os.Stderr, "🔑 Verificando chave KMS: %s\n", fullAlias)

	existingKeyID, exists := c.checkKMSAliasExists(ctx, kmsClient, fullAlias)

	// Aplicar estratégia
	switch action {
	case KMSActionFail:
		if exists {
			return "", fmt.Errorf(
				"❌ Chave KMS já existe: %s (KeyID: %s)\n"+
					"   Use --kms-action=reuse para reutilizar, rotate para rotacionar e criar nova, ou --kms-key-id para usar uma personalizada",
				fullAlias, existingKeyID,
			)
		}

	case KMSActionRotate:
		if exists {
			// Criar alias com timestamp
			timestamp := time.Now().Format("20060102-150405")
			newAlias := fmt.Sprintf("%s-%s", aliasName, timestamp)
			fullAlias = fmt.Sprintf("alias/%s", newAlias)

			fmt.Fprintf(os.Stderr, "   ⚠️  Alias existente detectado, criando nova chave rotacionada\n")
			fmt.Fprintf(os.Stderr, "   🔄 Novo alias: %s\n", fullAlias)

			// Limpar flag de existência para forçar criação
			exists = false
		}

	case KMSActionReuse:
		if exists {
			fmt.Fprintf(os.Stderr, "   -> ✅ Reutilizando chave existente: %s\n", existingKeyID)
			return existingKeyID, nil
		}
	}

	// Criar nova chave (se necessário)
	if !exists {
		return c.createKMSKeyWithAlias(ctx, kmsClient, fullAlias, description)
	}

	return existingKeyID, nil
}

// Helper: Verificar se alias existe
func (c *AWSBackendClients) checkKMSAliasExists(
	ctx context.Context,
	kmsClient *kms.Client,
	fullAlias string,
) (string, bool) {
	listAliasesOutput, err := kmsClient.ListAliases(ctx, &kms.ListAliasesInput{})
	if err != nil {
		return "", false
	}

	for _, alias := range listAliasesOutput.Aliases {
		if alias.AliasName != nil && *alias.AliasName == fullAlias {
			if alias.TargetKeyId != nil {
				return *alias.TargetKeyId, true
			}
		}
	}

	return "", false
}

// Helper: Criar chave + alias
func (c *AWSBackendClients) createKMSKeyWithAlias(
	ctx context.Context,
	kmsClient *kms.Client,
	fullAlias,
	description string,
) (string, error) {
	fmt.Fprintf(os.Stderr, "   -> Criando nova chave KMS...\n")

	createKeyOutput, err := kmsClient.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String(description),
		KeyUsage:    kmsTypes.KeyUsageTypeEncryptDecrypt,
		Origin:      kmsTypes.OriginTypeAwsKms,
		Tags: []kmsTypes.Tag{
			{
				TagKey:   aws.String("ManagedBy"),
				TagValue: aws.String("chatcli-eks"),
			},
			{
				TagKey:   aws.String("Purpose"),
				TagValue: aws.String("pulumi-secrets"),
			},
			{
				TagKey:   aws.String("CreatedAt"),
				TagValue: aws.String(time.Now().Format(time.RFC3339)),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("falha ao criar chave KMS: %w", err)
	}

	keyID := *createKeyOutput.KeyMetadata.KeyId
	fmt.Fprintf(os.Stderr, "   -> Chave criada: %s\n", keyID)

	// Criar alias
	fmt.Fprintf(os.Stderr, "   -> Criando alias: %s\n", fullAlias)
	_, err = kmsClient.CreateAlias(ctx, &kms.CreateAliasInput{
		AliasName:   aws.String(fullAlias),
		TargetKeyId: aws.String(keyID),
	})
	if err != nil {
		if IsAliasAlreadyExistsError(err) {
			fmt.Fprintf(os.Stderr, "   -> ⚠️  Alias já existe (race condition detectada)\n")
			return keyID, nil
		}
		return "", fmt.Errorf("falha ao criar alias KMS: %w", err)
	}

	fmt.Fprintf(os.Stderr, "   -> ✅ Chave KMS configurada: %s\n", fullAlias)
	return keyID, nil
}

// Helper para detectar erro de alias duplicado
func IsAliasAlreadyExistsError(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "AlreadyExistsException") ||
		contains(err.Error(), "already exists")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func (c *AWSBackendClients) GetKMSKeyInfo(ctx context.Context, keyIdentifier string) (*KMSKeyInfo, error) {
	kmsClient := kms.NewFromConfig(c.Config)

	// Normalizar identificador
	var keyID string
	var aliasName string

	if strings.HasPrefix(keyIdentifier, "alias/") {
		// É um alias, precisa resolver para KeyID
		aliasName = keyIdentifier
		listAliasesOutput, err := kmsClient.ListAliases(ctx, &kms.ListAliasesInput{})
		if err != nil {
			return nil, fmt.Errorf("falha ao listar aliases: %w", err)
		}

		found := false
		for _, alias := range listAliasesOutput.Aliases {
			if alias.AliasName != nil && *alias.AliasName == aliasName {
				if alias.TargetKeyId != nil {
					keyID = *alias.TargetKeyId
					found = true
					break
				}
			}
		}

		if !found {
			return nil, fmt.Errorf("alias não encontrado: %s", aliasName)
		}
	} else if strings.HasPrefix(keyIdentifier, "arn:") {
		// É um ARN
		keyID = keyIdentifier
	} else {
		// Assume que é KeyID direto
		keyID = keyIdentifier
	}

	// Buscar metadados da chave
	describeKeyOutput, err := kmsClient.DescribeKey(ctx, &kms.DescribeKeyInput{
		KeyId: aws.String(keyID),
	})
	if err != nil {
		return nil, fmt.Errorf("falha ao descrever chave: %w", err)
	}

	metadata := describeKeyOutput.KeyMetadata

	// Buscar tags
	tagsOutput, err := kmsClient.ListResourceTags(ctx, &kms.ListResourceTagsInput{
		KeyId: aws.String(keyID),
	})
	if err != nil {
		return nil, fmt.Errorf("falha ao buscar tags: %w", err)
	}

	tags := make(map[string]string)
	for _, tag := range tagsOutput.Tags {
		if tag.TagKey != nil && tag.TagValue != nil {
			tags[*tag.TagKey] = *tag.TagValue
		}
	}

	// Se veio de alias, buscar o nome do alias
	if aliasName == "" && metadata.KeyId != nil {
		// Tentar encontrar alias associado
		listAliasesOutput, err := kmsClient.ListAliases(ctx, &kms.ListAliasesInput{
			KeyId: metadata.KeyId,
		})
		if err == nil && len(listAliasesOutput.Aliases) > 0 {
			// Pegar primeiro alias não-AWS
			for _, alias := range listAliasesOutput.Aliases {
				if alias.AliasName != nil && !strings.HasPrefix(*alias.AliasName, "alias/aws/") {
					aliasName = *alias.AliasName
					break
				}
			}
		}
	}

	info := &KMSKeyInfo{
		Alias:        aliasName,
		KeyID:        *metadata.KeyId,
		ARN:          *metadata.Arn,
		State:        string(metadata.KeyState),
		CreationDate: *metadata.CreationDate,
		KeyManager:   string(metadata.KeyManager),
		Tags:         tags,
	}

	return info, nil
}
