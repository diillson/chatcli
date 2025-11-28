package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/internal/logger"
)

// IAMManager gerencia roles e policies IAM
type IAMManager struct {
	iamClient   *iam.Client
	clusterName string
}

// NewIAMManager cria um novo gerenciador IAM
func NewIAMManager(iamClient *iam.Client, clusterName string) *IAMManager {
	return &IAMManager{
		iamClient:   iamClient,
		clusterName: clusterName,
	}
}

// IAMResources recursos IAM criados
type IAMResources struct {
	ClusterRole     RoleResource `json:"clusterRole"`
	NodeRole        RoleResource `json:"nodeRole"`
	OIDCProvider    string       `json:"oidcProvider,omitempty"`
	InstanceProfile string       `json:"instanceProfile"`
}

// RoleResource representa um IAM role
type RoleResource struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

// CreateIAMResources cria todos os recursos IAM necessários
func (im *IAMManager) CreateIAMResources(ctx context.Context) (*IAMResources, error) {
	logger.Info("🔐 Criando recursos IAM...")

	resources := &IAMResources{}

	// 1. Criar Cluster Role
	clusterRole, err := im.createClusterRole(ctx)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar cluster role: %w", err)
	}
	resources.ClusterRole = *clusterRole
	logger.Successf("   ✓ Cluster role criada: %s", clusterRole.Name)

	// 2. Criar Node Role
	nodeRole, err := im.createNodeRole(ctx)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar node role: %w", err)
	}
	resources.NodeRole = *nodeRole
	logger.Successf("   ✓ Node role criada: %s", nodeRole.Name)

	// 3. Criar Instance Profile
	instanceProfile, err := im.createInstanceProfile(ctx, nodeRole.Name)
	if err != nil {
		return nil, fmt.Errorf("falha ao criar instance profile: %w", err)
	}
	resources.InstanceProfile = instanceProfile
	logger.Successf("   ✓ Instance profile criado: %s", instanceProfile)

	logger.Success("✅ Recursos IAM criados!")
	return resources, nil
}

// createClusterRole cria a role para o EKS cluster
func (im *IAMManager) createClusterRole(ctx context.Context) (*RoleResource, error) {
	roleName := fmt.Sprintf("%s-cluster-role", im.clusterName)

	// Trust policy para EKS
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]string{
					"Service": "eks.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}

	trustPolicyJSON, _ := json.Marshal(trustPolicy)

	// Criar role
	result, err := im.iamClient.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(string(trustPolicyJSON)),
		Description:              aws.String("EKS cluster role"),
		Tags: []types.Tag{
			{Key: aws.String("ManagedBy"), Value: aws.String("chatcli-k8s-cloud")},
			{Key: aws.String("Cluster"), Value: aws.String(im.clusterName)},
		},
	})

	if err != nil {
		// Se role já existe, obter ARN
		if isRoleExistsError(err) {
			getResult, err := im.iamClient.GetRole(ctx, &iam.GetRoleInput{
				RoleName: aws.String(roleName),
			})
			if err != nil {
				return nil, err
			}
			return &RoleResource{
				Name: roleName,
				ARN:  *getResult.Role.Arn,
			}, nil
		}
		return nil, err
	}

	// Anexar managed policy
	managedPolicyARN := "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
	_, err = im.iamClient.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  aws.String(roleName),
		PolicyArn: aws.String(managedPolicyARN),
	})

	if err != nil {
		return nil, err
	}

	// Aguardar role propagar
	time.Sleep(10 * time.Second)

	return &RoleResource{
		Name: roleName,
		ARN:  *result.Role.Arn,
	}, nil
}

// createNodeRole cria a role para os worker nodes
func (im *IAMManager) createNodeRole(ctx context.Context) (*RoleResource, error) {
	roleName := fmt.Sprintf("%s-node-role", im.clusterName)

	// Trust policy para EC2
	trustPolicy := map[string]interface{}{
		"Version": "2012-10-17",
		"Statement": []map[string]interface{}{
			{
				"Effect": "Allow",
				"Principal": map[string]string{
					"Service": "ec2.amazonaws.com",
				},
				"Action": "sts:AssumeRole",
			},
		},
	}

	trustPolicyJSON, _ := json.Marshal(trustPolicy)

	// Criar role
	result, err := im.iamClient.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(string(trustPolicyJSON)),
		Description:              aws.String("EKS node role"),
		Tags: []types.Tag{
			{Key: aws.String("ManagedBy"), Value: aws.String("chatcli-k8s-cloud")},
			{Key: aws.String("Cluster"), Value: aws.String(im.clusterName)},
		},
	})

	if err != nil {
		if isRoleExistsError(err) {
			getResult, err := im.iamClient.GetRole(ctx, &iam.GetRoleInput{
				RoleName: aws.String(roleName),
			})
			if err != nil {
				return nil, err
			}
			return &RoleResource{
				Name: roleName,
				ARN:  *getResult.Role.Arn,
			}, nil
		}
		return nil, err
	}

	// Anexar managed policies necessárias
	managedPolicies := []string{
		"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
		"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
		"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
	}

	for _, policyARN := range managedPolicies {
		_, err = im.iamClient.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: aws.String(policyARN),
		})
		if err != nil {
			return nil, err
		}
	}

	// Aguardar role propagar
	time.Sleep(10 * time.Second)

	return &RoleResource{
		Name: roleName,
		ARN:  *result.Role.Arn,
	}, nil
}

// createInstanceProfile cria instance profile para nodes
func (im *IAMManager) createInstanceProfile(ctx context.Context, roleName string) (string, error) {
	profileName := fmt.Sprintf("%s-node-instance-profile", im.clusterName)

	// Criar instance profile
	result, err := im.iamClient.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String(profileName),
		Tags: []types.Tag{
			{Key: aws.String("ManagedBy"), Value: aws.String("chatcli-k8s-cloud")},
			{Key: aws.String("Cluster"), Value: aws.String(im.clusterName)},
		},
	})

	if err != nil {
		if isInstanceProfileExistsError(err) {
			return profileName, nil
		}
		return "", err
	}

	// Adicionar role ao instance profile
	_, err = im.iamClient.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String(profileName),
		RoleName:            aws.String(roleName),
	})

	if err != nil {
		return "", err
	}

	// Aguardar propagar
	time.Sleep(10 * time.Second)

	return *result.InstanceProfile.Arn, nil
}

// DeleteIAMResources remove recursos IAM
func (im *IAMManager) DeleteIAMResources(ctx context.Context, resources *IAMResources) error {
	logger.Info("🗑️  Removendo recursos IAM...")

	// 1. Remover instance profile
	if resources.InstanceProfile != "" {
		profileName := fmt.Sprintf("%s-node-instance-profile", im.clusterName)

		// Remover role do instance profile
		_, _ = im.iamClient.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
			InstanceProfileName: aws.String(profileName),
			RoleName:            aws.String(resources.NodeRole.Name),
		})

		// Deletar instance profile
		_, _ = im.iamClient.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{
			InstanceProfileName: aws.String(profileName),
		})
	}

	// 2. Remover node role
	if err := im.deleteRole(ctx, resources.NodeRole.Name); err != nil {
		logger.Warningf("   ⚠️  Erro ao remover node role: %v", err)
	}

	// 3. Remover cluster role
	if err := im.deleteRole(ctx, resources.ClusterRole.Name); err != nil {
		logger.Warningf("   ⚠️  Erro ao remover cluster role: %v", err)
	}

	logger.Success("✅ Recursos IAM removidos!")
	return nil
}

// deleteRole remove uma role e suas policies
func (im *IAMManager) deleteRole(ctx context.Context, roleName string) error {
	// Listar policies anexadas
	listResult, err := im.iamClient.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: aws.String(roleName),
	})

	if err != nil {
		return err
	}

	// Desanexar todas as policies
	for _, policy := range listResult.AttachedPolicies {
		_, _ = im.iamClient.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{
			RoleName:  aws.String(roleName),
			PolicyArn: policy.PolicyArn,
		})
	}

	// Deletar role
	_, err = im.iamClient.DeleteRole(ctx, &iam.DeleteRoleInput{
		RoleName: aws.String(roleName),
	})

	return err
}

// Helper functions
func isRoleExistsError(err error) bool {
	return err != nil && (err.Error() == "EntityAlreadyExists" ||
		containsString(err.Error(), "already exists"))
}

func isInstanceProfileExistsError(err error) bool {
	return err != nil && (err.Error() == "EntityAlreadyExists" ||
		containsString(err.Error(), "already exists"))
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr)))
}
