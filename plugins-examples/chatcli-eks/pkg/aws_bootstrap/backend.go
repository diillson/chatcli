package aws_bootstrap

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbTypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3Types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type AWSBackendClients struct {
	S3Client       *s3.Client
	DynamoDBClient *dynamodb.Client
	Config         aws.Config
}

func NewAWSClients(ctx context.Context, region string) (*AWSBackendClients, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return &AWSBackendClients{
		S3Client:       s3.NewFromConfig(cfg),
		DynamoDBClient: dynamodb.NewFromConfig(cfg),
		Config:         cfg,
	}, nil
}

func (c *AWSBackendClients) EnsureS3Backend(ctx context.Context, bucketName, region string) error {
	fmt.Fprintf(os.Stderr, "🪣 Verificando bucket S3: %s\n", bucketName)
	_, err := c.S3Client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucketName)})
	if err == nil {
		fmt.Fprintf(os.Stderr, "   -> Bucket existe.\n")
		return nil
	}

	fmt.Fprintf(os.Stderr, "   -> Criando bucket...\n")
	createInput := &s3.CreateBucketInput{Bucket: aws.String(bucketName)}
	if region != "" && region != "us-east-1" {
		createInput.CreateBucketConfiguration = &s3Types.CreateBucketConfiguration{
			LocationConstraint: s3Types.BucketLocationConstraint(region),
		}
	}
	if _, err := c.S3Client.CreateBucket(ctx, createInput); err != nil {
		// Se falhar por já existir de outra conta ou conflito, propaga o erro
		return fmt.Errorf("failed to create bucket %s: %w", bucketName, err)
	}

	// Configurações de segurança (block public access e versionamento)
	_, _ = c.S3Client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucketName),
		PublicAccessBlockConfiguration: &s3Types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	_, _ = c.S3Client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(bucketName),
		VersioningConfiguration: &s3Types.VersioningConfiguration{
			Status: s3Types.BucketVersioningStatusEnabled,
		},
	})
	fmt.Fprintf(os.Stderr, "   -> ✅ Bucket criado.\n")
	return nil
}

func (c *AWSBackendClients) EnsureDynamoDBLockTable(ctx context.Context, tableName string) error {
	fmt.Fprintf(os.Stderr, "🔐 Verificando tabela de lock: %s\n", tableName)
	_, err := c.DynamoDBClient.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(tableName)})
	if err == nil {
		fmt.Fprintf(os.Stderr, "   -> Tabela existe.\n")
		return nil
	}

	fmt.Fprintf(os.Stderr, "   -> Criando tabela...\n")
	_, err = c.DynamoDBClient.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableName),
		AttributeDefinitions: []dynamodbTypes.AttributeDefinition{
			{AttributeName: aws.String("LockID"), AttributeType: dynamodbTypes.ScalarAttributeTypeS},
		},
		KeySchema: []dynamodbTypes.KeySchemaElement{
			{AttributeName: aws.String("LockID"), KeyType: dynamodbTypes.KeyTypeHash},
		},
		BillingMode: dynamodbTypes.BillingModePayPerRequest,
	})
	if err != nil {
		return fmt.Errorf("failed to create dynamodb table %s: %w", tableName, err)
	}
	fmt.Fprintf(os.Stderr, "   -> ✅ Tabela criada.\n")
	return nil
}
