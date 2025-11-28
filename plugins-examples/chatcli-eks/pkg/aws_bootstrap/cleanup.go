package aws_bootstrap

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// CleanupOptions define o que será deletado
type CleanupOptions struct {
	BucketName       string
	LockTableName    string
	KMSKeyAlias      string
	DryRun           bool // Simula sem deletar
	ForceEmptyBucket bool // Esvazia bucket antes de deletar
}

// CleanupResult armazena o resultado da operação
type CleanupResult struct {
	BucketDeleted        bool
	BucketObjectsDeleted int
	LockTableDeleted     bool
	KMSKeyScheduled      bool
	Errors               []error
}

// FullCleanup remove todos os recursos auxiliares
func (c *AWSBackendClients) FullCleanup(ctx context.Context, opts CleanupOptions) (*CleanupResult, error) {
	result := &CleanupResult{}

	if opts.DryRun {
		fmt.Fprintln(os.Stderr, "🔍 MODO DRY-RUN: Apenas simulando deleções...")
		fmt.Fprintln(os.Stderr, "")
	}

	// 1. Deletar Bucket S3 (com esvaziamento)
	if opts.BucketName != "" {
		fmt.Fprintf(os.Stderr, "🪣 Processando Bucket S3: %s\n", opts.BucketName)

		if err := c.cleanupS3Bucket(ctx, opts.BucketName, opts.ForceEmptyBucket, opts.DryRun); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("bucket S3: %w", err))
		} else {
			result.BucketDeleted = true
		}
	}

	// 2. Deletar Tabela DynamoDB
	if opts.LockTableName != "" {
		fmt.Fprintf(os.Stderr, "🔐 Processando Tabela DynamoDB: %s\n", opts.LockTableName)

		if err := c.cleanupDynamoDBTable(ctx, opts.LockTableName, opts.DryRun); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("dynamodb: %w", err))
		} else {
			result.LockTableDeleted = true
		}
	}

	// 3. Deletar Chave KMS
	if opts.KMSKeyAlias != "" {
		fmt.Fprintf(os.Stderr, "🔑 Processando Chave KMS: %s\n", opts.KMSKeyAlias)

		if err := c.cleanupKMSKey(ctx, opts.KMSKeyAlias, opts.DryRun); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("kms: %w", err))
		} else {
			result.KMSKeyScheduled = true
		}
	}

	return result, nil
}

// cleanupS3Bucket remove bucket com estratégia de esvaziamento
func (c *AWSBackendClients) cleanupS3Bucket(ctx context.Context, bucketName string, forceEmpty, dryRun bool) error {
	// Verificar se bucket existe
	_, err := c.S3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucketName),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "   ⚠️  Bucket não encontrado (pode já ter sido deletado)\n")
		return nil
	}

	if forceEmpty {
		fmt.Fprintf(os.Stderr, "   🗑️  Esvaziando bucket...\n")

		// 1. Deletar objetos normais
		if err := c.emptyBucketObjects(ctx, bucketName, dryRun); err != nil {
			return fmt.Errorf("erro ao esvaziar objetos: %w", err)
		}

		// 2. Deletar versões antigas (se versionamento habilitado)
		if err := c.emptyBucketVersions(ctx, bucketName, dryRun); err != nil {
			return fmt.Errorf("erro ao esvaziar versões: %w", err)
		}
	}

	// Deletar bucket
	if !dryRun {
		_, err = c.S3Client.DeleteBucket(ctx, &s3.DeleteBucketInput{
			Bucket: aws.String(bucketName),
		})
		if err != nil {
			return fmt.Errorf("falha ao deletar bucket: %w", err)
		}
		fmt.Fprintf(os.Stderr, "   ✅ Bucket deletado\n")
	} else {
		fmt.Fprintf(os.Stderr, "   [DRY-RUN] Bucket seria deletado\n")
	}

	return nil
}

// emptyBucketObjects remove todos os objetos do bucket
func (c *AWSBackendClients) emptyBucketObjects(ctx context.Context, bucketName string, dryRun bool) error {
	paginator := s3.NewListObjectsV2Paginator(c.S3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
	})

	totalDeleted := 0

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return err
		}

		if len(page.Contents) == 0 {
			continue
		}

		// Preparar batch de deleção
		var objectsToDelete []s3Types.ObjectIdentifier
		for _, obj := range page.Contents {
			objectsToDelete = append(objectsToDelete, s3Types.ObjectIdentifier{
				Key: obj.Key,
			})
		}

		if !dryRun {
			_, err = c.S3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucketName),
				Delete: &s3Types.Delete{
					Objects: objectsToDelete,
					Quiet:   aws.Bool(true),
				},
			})
			if err != nil {
				return err
			}
		}

		totalDeleted += len(objectsToDelete)
		fmt.Fprintf(os.Stderr, "      - %d objetos deletados\n", totalDeleted)
	}

	if totalDeleted == 0 {
		fmt.Fprintf(os.Stderr, "      - Bucket já estava vazio\n")
	}

	return nil
}

// emptyBucketVersions remove versões antigas de objetos
func (c *AWSBackendClients) emptyBucketVersions(ctx context.Context, bucketName string, dryRun bool) error {
	paginator := s3.NewListObjectVersionsPaginator(c.S3Client, &s3.ListObjectVersionsInput{
		Bucket: aws.String(bucketName),
	})

	totalDeleted := 0

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			// Se bucket não tem versionamento, ignora erro
			return nil
		}

		var objectsToDelete []s3Types.ObjectIdentifier

		// Versões
		for _, version := range page.Versions {
			objectsToDelete = append(objectsToDelete, s3Types.ObjectIdentifier{
				Key:       version.Key,
				VersionId: version.VersionId,
			})
		}

		// Delete markers
		for _, marker := range page.DeleteMarkers {
			objectsToDelete = append(objectsToDelete, s3Types.ObjectIdentifier{
				Key:       marker.Key,
				VersionId: marker.VersionId,
			})
		}

		if len(objectsToDelete) == 0 {
			continue
		}

		if !dryRun {
			_, err = c.S3Client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucketName),
				Delete: &s3Types.Delete{
					Objects: objectsToDelete,
					Quiet:   aws.Bool(true),
				},
			})
			if err != nil {
				return err
			}
		}

		totalDeleted += len(objectsToDelete)
		fmt.Fprintf(os.Stderr, "      - %d versões deletadas\n", totalDeleted)
	}

	return nil
}

// cleanupDynamoDBTable deleta tabela de lock
func (c *AWSBackendClients) cleanupDynamoDBTable(ctx context.Context, tableName string, dryRun bool) error {
	// Verificar se tabela existe
	_, err := c.DynamoDBClient.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(tableName),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "   ⚠️  Tabela não encontrada (pode já ter sido deletada)\n")
		return nil
	}

	if !dryRun {
		_, err = c.DynamoDBClient.DeleteTable(ctx, &dynamodb.DeleteTableInput{
			TableName: aws.String(tableName),
		})
		if err != nil {
			return fmt.Errorf("falha ao deletar tabela: %w", err)
		}
		fmt.Fprintf(os.Stderr, "   ✅ Tabela deletada\n")
	} else {
		fmt.Fprintf(os.Stderr, "   [DRY-RUN] Tabela seria deletada\n")
	}

	return nil
}

// cleanupKMSKey agenda deleção da chave KMS (mínimo 7 dias AWS)
func (c *AWSBackendClients) cleanupKMSKey(ctx context.Context, keyAlias string, dryRun bool) error {
	kmsClient := kms.NewFromConfig(c.Config)

	// Resolver alias para KeyID
	fullAlias := keyAlias
	if !contains(keyAlias, "alias/") {
		fullAlias = fmt.Sprintf("alias/%s", keyAlias)
	}

	aliasOutput, err := kmsClient.ListAliases(ctx, &kms.ListAliasesInput{})
	if err != nil {
		return err
	}

	var keyID string
	for _, alias := range aliasOutput.Aliases {
		if alias.AliasName != nil && *alias.AliasName == fullAlias {
			if alias.TargetKeyId != nil {
				keyID = *alias.TargetKeyId
			}
			break
		}
	}

	if keyID == "" {
		fmt.Fprintf(os.Stderr, "   ⚠️  Chave não encontrada (pode já ter sido deletada)\n")
		return nil
	}

	if !dryRun {
		// AWS exige mínimo de 7 dias para deleção de chaves KMS
		_, err = kmsClient.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
			KeyId:               aws.String(keyID),
			PendingWindowInDays: aws.Int32(7), // Mínimo permitido pela AWS
		})
		if err != nil {
			return fmt.Errorf("falha ao agendar deleção: %w", err)
		}

		fmt.Fprintf(os.Stderr, "   ⚠️  Chave agendada para deleção em 7 dias (mínimo AWS)\n")
		fmt.Fprintf(os.Stderr, "      KeyID: %s\n", keyID)
		fmt.Fprintf(os.Stderr, "      💡 Para cancelar: aws kms cancel-key-deletion --key-id %s\n", keyID)
	} else {
		fmt.Fprintf(os.Stderr, "   [DRY-RUN] Chave seria agendada para deleção\n")
	}

	return nil
}

// GetCleanupEstimate calcula o que será deletado (preview)
func (c *AWSBackendClients) GetCleanupEstimate(ctx context.Context, opts CleanupOptions) (string, error) {
	estimate := "📊 PREVIEW DE DELEÇÃO:\n\n"

	// Bucket S3
	if opts.BucketName != "" {
		objectCount := 0
		versionCount := 0

		// Contar objetos
		paginator := s3.NewListObjectsV2Paginator(c.S3Client, &s3.ListObjectsV2Input{
			Bucket: aws.String(opts.BucketName),
		})
		for paginator.HasMorePages() {
			page, _ := paginator.NextPage(ctx)
			objectCount += len(page.Contents)
		}

		// Contar versões
		versionPaginator := s3.NewListObjectVersionsPaginator(c.S3Client, &s3.ListObjectVersionsInput{
			Bucket: aws.String(opts.BucketName),
		})
		for versionPaginator.HasMorePages() {
			page, _ := versionPaginator.NextPage(ctx)
			versionCount += len(page.Versions) + len(page.DeleteMarkers)
		}

		estimate += fmt.Sprintf("🪣 Bucket S3: %s\n", opts.BucketName)
		estimate += fmt.Sprintf("   - Objetos: %d\n", objectCount)
		estimate += fmt.Sprintf("   - Versões: %d\n", versionCount)
		estimate += fmt.Sprintf("   - Total a deletar: %d itens\n\n", objectCount+versionCount)
	}

	// DynamoDB
	if opts.LockTableName != "" {
		tableInfo, err := c.DynamoDBClient.DescribeTable(ctx, &dynamodb.DescribeTableInput{
			TableName: aws.String(opts.LockTableName),
		})
		if err == nil {
			estimate += fmt.Sprintf("🔐 Tabela DynamoDB: %s\n", opts.LockTableName)
			estimate += fmt.Sprintf("   - Status: %s\n", tableInfo.Table.TableStatus)
			estimate += fmt.Sprintf("   - Itens: %d (aprox.)\n\n", *tableInfo.Table.ItemCount)
		}
	}

	// KMS
	if opts.KMSKeyAlias != "" {
		estimate += fmt.Sprintf("🔑 Chave KMS: %s\n", opts.KMSKeyAlias)
		estimate += "   - Será agendada para deleção em 7 dias\n"
		estimate += "   - Custo atual: ~$1/mês\n\n"
	}

	estimate += "💰 Economia estimada após deleção: ~$1-5/mês\n"

	return estimate, nil
}
