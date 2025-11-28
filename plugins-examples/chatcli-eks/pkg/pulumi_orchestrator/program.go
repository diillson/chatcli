package pulumi_orchestrator

import (
	"fmt"
	"strings"

	"github.com/diillson/chatcli/plugins-examples/chatcli-eks/pkg/config"
	awsProvider "github.com/pulumi/pulumi-aws/sdk/v6/go/aws"
	aws_ec2 "github.com/pulumi/pulumi-aws/sdk/v6/go/aws/ec2"
	aws_eks "github.com/pulumi/pulumi-aws/sdk/v6/go/aws/eks"
	aws_iam "github.com/pulumi/pulumi-aws/sdk/v6/go/aws/iam"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	helm_v3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	yamlz "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/yaml"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"gopkg.in/yaml.v3"
)

func DefineEKSInfrastructure(cfg *config.EKSConfig) pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		var vpcId pulumi.StringInput
		var publicSubnets pulumi.StringArrayInput
		var privateSubnets pulumi.StringArrayInput

		azResult, err := awsProvider.GetAvailabilityZones(ctx, &awsProvider.GetAvailabilityZonesArgs{State: pulumi.StringRef("available")}, nil)
		if err != nil {
			return err
		}
		azs := azResult.Names
		azCount := 3
		if len(azs) < 3 {
			azCount = len(azs)
		}

		if cfg.VpcID != "" {
			vpcId = pulumi.String(cfg.VpcID)
			privateSubnets = toStringArray(cfg.PrivateSubnetIDs)
			publicSubnets = toStringArray(cfg.PublicSubnetIDs)
		} else {
			vpc, err := aws_ec2.NewVpc(ctx, fmt.Sprintf("%s-vpc", cfg.ClusterName), &aws_ec2.VpcArgs{
				CidrBlock:          pulumi.String("10.0.0.0/16"),
				EnableDnsHostnames: pulumi.Bool(true),
				EnableDnsSupport:   pulumi.Bool(true),
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-vpc", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			igw, err := aws_ec2.NewInternetGateway(ctx, fmt.Sprintf("%s-igw", cfg.ClusterName), &aws_ec2.InternetGatewayArgs{
				VpcId: vpc.ID(),
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-igw", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			publicRouteTable, err := aws_ec2.NewRouteTable(ctx, fmt.Sprintf("%s-public-rt", cfg.ClusterName), &aws_ec2.RouteTableArgs{
				VpcId: vpc.ID(),
				Routes: aws_ec2.RouteTableRouteArray{
					&aws_ec2.RouteTableRouteArgs{
						CidrBlock: pulumi.String("0.0.0.0/0"),
						GatewayId: igw.ID(),
					},
				},
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-public-rt", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			var pubSubnetIds pulumi.StringArray
			var privSubnetIds pulumi.StringArray

			for i := 0; i < azCount; i++ {
				az := azs[i]
				pubCidr := fmt.Sprintf("10.0.%d.0/24", i)
				privCidr := fmt.Sprintf("10.0.%d.0/24", i+100)

				pubSubnet, err := aws_ec2.NewSubnet(ctx, fmt.Sprintf("%s-public-%d", cfg.ClusterName, i), &aws_ec2.SubnetArgs{
					VpcId:               vpc.ID(),
					CidrBlock:           pulumi.String(pubCidr),
					AvailabilityZone:    pulumi.String(az),
					MapPublicIpOnLaunch: pulumi.Bool(true),
					Tags: pulumi.StringMap{
						"Name": pulumi.String(fmt.Sprintf("%s-public-%d", cfg.ClusterName, i)),
						"kubernetes.io/cluster/" + cfg.ClusterName: pulumi.String("shared"),
						"kubernetes.io/role/elb":                   pulumi.String("1"),
					},
				})
				if err != nil {
					return err
				}

				_, err = aws_ec2.NewRouteTableAssociation(ctx, fmt.Sprintf("%s-public-rt-assoc-%d", cfg.ClusterName, i), &aws_ec2.RouteTableAssociationArgs{
					RouteTableId: publicRouteTable.ID(),
					SubnetId:     pubSubnet.ID(),
				})
				if err != nil {
					return err
				}

				eip, err := aws_ec2.NewEip(ctx, fmt.Sprintf("%s-nat-eip-%d", cfg.ClusterName, i), &aws_ec2.EipArgs{
					Domain: pulumi.String("vpc"),
					Tags: pulumi.StringMap{
						"Name": pulumi.String(fmt.Sprintf("%s-nat-eip-%d", cfg.ClusterName, i)),
					},
				})
				if err != nil {
					return err
				}

				nat, err := aws_ec2.NewNatGateway(ctx, fmt.Sprintf("%s-nat-%d", cfg.ClusterName, i), &aws_ec2.NatGatewayArgs{
					AllocationId: eip.ID(),
					SubnetId:     pubSubnet.ID(),
					Tags: pulumi.StringMap{
						"Name": pulumi.String(fmt.Sprintf("%s-nat-%d", cfg.ClusterName, i)),
					},
				})
				if err != nil {
					return err
				}

				privRouteTable, err := aws_ec2.NewRouteTable(ctx, fmt.Sprintf("%s-private-rt-%d", cfg.ClusterName, i), &aws_ec2.RouteTableArgs{
					VpcId: vpc.ID(),
					Routes: aws_ec2.RouteTableRouteArray{
						&aws_ec2.RouteTableRouteArgs{
							CidrBlock:    pulumi.String("0.0.0.0/0"),
							NatGatewayId: nat.ID(),
						},
					},
					Tags: pulumi.StringMap{
						"Name": pulumi.String(fmt.Sprintf("%s-private-rt-%d", cfg.ClusterName, i)),
					},
				})
				if err != nil {
					return err
				}

				pubSubnetIds = append(pubSubnetIds, pubSubnet.ID())

				privSubnet, err := aws_ec2.NewSubnet(ctx, fmt.Sprintf("%s-private-%d", cfg.ClusterName, i), &aws_ec2.SubnetArgs{
					VpcId:               vpc.ID(),
					CidrBlock:           pulumi.String(privCidr),
					AvailabilityZone:    pulumi.String(az),
					MapPublicIpOnLaunch: pulumi.Bool(false),
					Tags: pulumi.StringMap{
						"Name": pulumi.String(fmt.Sprintf("%s-private-%d", cfg.ClusterName, i)),
						"kubernetes.io/cluster/" + cfg.ClusterName: pulumi.String("shared"),
						"kubernetes.io/role/internal-elb":          pulumi.String("1"),
					},
				})
				if err != nil {
					return err
				}

				_, err = aws_ec2.NewRouteTableAssociation(ctx, fmt.Sprintf("%s-private-rt-assoc-%d", cfg.ClusterName, i), &aws_ec2.RouteTableAssociationArgs{
					RouteTableId: privRouteTable.ID(),
					SubnetId:     privSubnet.ID(),
				})
				if err != nil {
					return err
				}

				privSubnetIds = append(privSubnetIds, privSubnet.ID())
			}

			vpcId = vpc.ID()
			publicSubnets = pulumi.StringArray(pubSubnetIds)
			privateSubnets = pulumi.StringArray(privSubnetIds)
		}

		clusterRole, err := aws_iam.NewRole(ctx, "eks-cluster-role", &aws_iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
                          "Version": "2012-10-17",
                          "Statement": [{
                                "Effect": "Allow",
                                "Principal": { "Service": "eks.amazonaws.com" },
                                "Action": "sts:AssumeRole"
                          }]
                        }`),
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-cluster-role", cfg.ClusterName)),
			},
		})
		if err != nil {
			return err
		}

		_, err = aws_iam.NewRolePolicyAttachment(ctx, "cluster-policy-attachment", &aws_iam.RolePolicyAttachmentArgs{
			PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"),
			Role:      clusterRole.Name,
		})
		if err != nil {
			return err
		}

		clusterSg, err := aws_ec2.NewSecurityGroup(ctx, "eks-cluster-sg", &aws_ec2.SecurityGroupArgs{
			VpcId:       vpcId,
			Description: pulumi.String("EKS cluster security group"),
			Egress: aws_ec2.SecurityGroupEgressArray{
				&aws_ec2.SecurityGroupEgressArgs{
					Protocol:   pulumi.String("-1"),
					FromPort:   pulumi.Int(0),
					ToPort:     pulumi.Int(0),
					CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
				},
			},
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-cluster-sg", cfg.ClusterName)),
			},
		})
		if err != nil {
			return err
		}

		allSubnets := pulumi.All(publicSubnets, privateSubnets).ApplyT(func(args []interface{}) pulumi.StringArray {
			pub := args[0].([]string)
			priv := args[1].([]string)
			var combined pulumi.StringArray
			for _, s := range pub {
				combined = append(combined, pulumi.String(s))
			}
			for _, s := range priv {
				combined = append(combined, pulumi.String(s))
			}
			return combined
		}).(pulumi.StringArrayOutput)

		cluster, err := aws_eks.NewCluster(ctx, cfg.ClusterName, &aws_eks.ClusterArgs{
			Name:    pulumi.String(cfg.ClusterName),
			RoleArn: clusterRole.Arn,
			Version: pulumi.String(cfg.Version),
			VpcConfig: &aws_eks.ClusterVpcConfigArgs{
				SubnetIds:            allSubnets,
				SecurityGroupIds:     pulumi.StringArray{clusterSg.ID()},
				EndpointPublicAccess: pulumi.Bool(true),
			},
			Tags: pulumi.StringMap{
				"Name": pulumi.String(cfg.ClusterName),
			},
		})
		if err != nil {
			return err
		}

		nodeRole, err := aws_iam.NewRole(ctx, "eks-node-role", &aws_iam.RoleArgs{
			AssumeRolePolicy: pulumi.String(`{
                          "Version": "2012-10-17",
                          "Statement": [{
                                "Effect": "Allow",
                                "Principal": { "Service": "ec2.amazonaws.com" },
                                "Action": "sts:AssumeRole"
                          }]
                        }`),
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-node-role", cfg.ClusterName)),
			},
		})
		if err != nil {
			return err
		}

		nodePolicies := []string{
			"arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
			"arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
			"arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
			"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
		}

		var nodePolicyAttachments []pulumi.Resource
		for i, policyArn := range nodePolicies {
			att, err := aws_iam.NewRolePolicyAttachment(ctx, fmt.Sprintf("node-policy-%d", i), &aws_iam.RolePolicyAttachmentArgs{
				PolicyArn: pulumi.String(policyArn),
				Role:      nodeRole.Name,
			})
			if err != nil {
				return err
			}
			nodePolicyAttachments = append(nodePolicyAttachments, att)
		}

		vpcCniAddon, err := aws_eks.NewAddon(ctx, "vpc-cni-addon", &aws_eks.AddonArgs{
			ClusterName:              cluster.Name,
			AddonName:                pulumi.String("vpc-cni"),
			ResolveConflictsOnCreate: pulumi.String("OVERWRITE"),
			ResolveConflictsOnUpdate: pulumi.String("OVERWRITE"),
		}, pulumi.DependsOn([]pulumi.Resource{cluster}))
		if err != nil {
			return err
		}

		kubeProxyAddon, err := aws_eks.NewAddon(ctx, "kube-proxy-addon", &aws_eks.AddonArgs{
			ClusterName:              cluster.Name,
			AddonName:                pulumi.String("kube-proxy"),
			ResolveConflictsOnCreate: pulumi.String("OVERWRITE"),
			ResolveConflictsOnUpdate: pulumi.String("OVERWRITE"),
		}, pulumi.DependsOn([]pulumi.Resource{cluster}))
		if err != nil {
			return err
		}

		capacityType := "ON_DEMAND"
		if cfg.UseSpot {
			capacityType = "SPOT"
		}

		nodeGroup, err := aws_eks.NewNodeGroup(ctx, "main-node-group", &aws_eks.NodeGroupArgs{
			ClusterName:   cluster.Name,
			NodeGroupName: pulumi.String(fmt.Sprintf("%s-main-ng", cfg.ClusterName)),
			NodeRoleArn:   nodeRole.Arn,
			SubnetIds:     privateSubnets,
			CapacityType:  pulumi.String(capacityType),
			InstanceTypes: pulumi.StringArray{pulumi.String(cfg.NodeType)},
			ScalingConfig: &aws_eks.NodeGroupScalingConfigArgs{
				DesiredSize: pulumi.Int(cfg.MinNodes),
				MinSize:     pulumi.Int(cfg.MinNodes),
				MaxSize:     pulumi.Int(cfg.MaxNodes),
			},
			UpdateConfig: &aws_eks.NodeGroupUpdateConfigArgs{
				MaxUnavailable: pulumi.Int(1),
			},
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-main-ng", cfg.ClusterName)),
				"kubernetes.io/cluster/" + cfg.ClusterName:     pulumi.String("owned"),
				"k8s.io/cluster-autoscaler/enabled":            pulumi.String("true"),
				"k8s.io/cluster-autoscaler/" + cfg.ClusterName: pulumi.String("owned"),
			},
		}, pulumi.DependsOn(append([]pulumi.Resource{cluster, vpcCniAddon, kubeProxyAddon}, nodePolicyAttachments...)))
		if err != nil {
			return err
		}

		_, err = aws_eks.NewAddon(ctx, "coredns-addon", &aws_eks.AddonArgs{
			ClusterName:              cluster.Name,
			AddonName:                pulumi.String("coredns"),
			ResolveConflictsOnCreate: pulumi.String("OVERWRITE"),
			ResolveConflictsOnUpdate: pulumi.String("OVERWRITE"),
		}, pulumi.DependsOn([]pulumi.Resource{cluster, nodeGroup, vpcCniAddon, kubeProxyAddon}))
		if err != nil {
			return err
		}

		oidcUrl := cluster.Identities.Index(pulumi.Int(0)).Oidcs().Index(pulumi.Int(0)).Issuer().Elem()

		oidcProvider, err := aws_iam.NewOpenIdConnectProvider(ctx, "oidc-provider", &aws_iam.OpenIdConnectProviderArgs{
			Url: oidcUrl,
			ClientIdLists: pulumi.StringArray{
				pulumi.String("sts.amazonaws.com"),
			},
			ThumbprintLists: pulumi.StringArray{
				pulumi.String("9e99a48a9960b14926bb7f3b02e22da2b0ab7280"),
			},
		}, pulumi.DependsOn([]pulumi.Resource{cluster}))
		if err != nil {
			return err
		}

		oidcProviderArn := oidcProvider.Arn
		oidcProviderUrl := cluster.Identities.Index(pulumi.Int(0)).Oidcs().Index(pulumi.Int(0)).Issuer().Elem().
			ApplyT(func(url string) string {
				return strings.TrimPrefix(url, "https://")
			}).(pulumi.StringOutput)

		lbControllerPolicyDoc := pulumi.String(`{
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Action": ["iam:CreateServiceLinkedRole"],
          "Resource": "*",
          "Condition": {
            "StringEquals": {
              "iam:AWSServiceName": "elasticloadbalancing.amazonaws.com"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": [
            "ec2:DescribeAccountAttributes",
            "ec2:DescribeAddresses",
            "ec2:DescribeAvailabilityZones",
            "ec2:DescribeInternetGateways",
            "ec2:DescribeVpcs",
            "ec2:DescribeVpcPeeringConnections",
            "ec2:DescribeSubnets",
            "ec2:DescribeSecurityGroups",
            "ec2:DescribeInstances",
            "ec2:DescribeNetworkInterfaces",
            "ec2:DescribeTags",
            "ec2:GetCoipPoolUsage",
            "ec2:DescribeCoipPools",
            "elasticloadbalancing:DescribeLoadBalancers",
            "elasticloadbalancing:DescribeLoadBalancerAttributes",
            "elasticloadbalancing:DescribeListeners",
            "elasticloadbalancing:DescribeListenerCertificates",
            "elasticloadbalancing:DescribeSSLPolicies",
            "elasticloadbalancing:DescribeRules",
            "elasticloadbalancing:DescribeTargetGroups",
            "elasticloadbalancing:DescribeTargetGroupAttributes",
            "elasticloadbalancing:DescribeTargetHealth",
            "elasticloadbalancing:DescribeTags"
          ],
          "Resource": "*"
        },
        {
          "Effect": "Allow",
          "Action": [
            "cognito-idp:DescribeUserPoolClient",
            "acm:ListCertificates",
            "acm:DescribeCertificate",
            "iam:ListServerCertificates",
            "iam:GetServerCertificate",
            "waf-regional:GetWebACL",
            "waf-regional:GetWebACLForResource",
            "waf-regional:AssociateWebACL",
            "waf-regional:DisassociateWebACL",
            "wafv2:GetWebACL",
            "wafv2:GetWebACLForResource",
            "wafv2:AssociateWebACL",
            "wafv2:DisassociateWebACL",
            "shield:GetSubscriptionState",
            "shield:DescribeProtection",
            "shield:CreateProtection",
            "shield:DeleteProtection"
          ],
          "Resource": "*"
        },
        {
          "Effect": "Allow",
          "Action": [
            "ec2:AuthorizeSecurityGroupIngress",
            "ec2:RevokeSecurityGroupIngress",
            "ec2:CreateSecurityGroup"
          ],
          "Resource": "*"
        },
        {
          "Effect": "Allow",
          "Action": ["ec2:CreateTags"],
          "Resource": "arn:aws:ec2:*:*:security-group/*",
          "Condition": {
            "StringEquals": {
              "ec2:CreateAction": "CreateSecurityGroup"
            },
            "Null": {
              "aws:RequestTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": ["ec2:CreateTags", "ec2:DeleteTags"],
          "Resource": "arn:aws:ec2:*:*:security-group/*",
          "Condition": {
            "Null": {
              "aws:RequestTag/elbv2.k8s.aws/cluster": "true",
              "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": [
            "ec2:AuthorizeSecurityGroupIngress",
            "ec2:RevokeSecurityGroupIngress",
            "ec2:DeleteSecurityGroup"
          ],
          "Resource": "*",
          "Condition": {
            "Null": {
              "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:CreateLoadBalancer",
            "elasticloadbalancing:CreateTargetGroup"
          ],
          "Resource": "*",
          "Condition": {
            "Null": {
              "aws:RequestTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:CreateListener",
            "elasticloadbalancing:DeleteListener",
            "elasticloadbalancing:CreateRule",
            "elasticloadbalancing:DeleteRule"
          ],
          "Resource": "*"
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:AddTags",
            "elasticloadbalancing:RemoveTags"
          ],
          "Resource": [
            "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*",
            "arn:aws:elasticloadbalancing:*:*:loadbalancer/net/*/*",
            "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/*/*"
          ],
          "Condition": {
            "Null": {
              "aws:RequestTag/elbv2.k8s.aws/cluster": "true",
              "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:AddTags",
            "elasticloadbalancing:RemoveTags"
          ],
          "Resource": [
            "arn:aws:elasticloadbalancing:*:*:listener/net/*/*/*",
            "arn:aws:elasticloadbalancing:*:*:listener/app/*/*/*",
            "arn:aws:elasticloadbalancing:*:*:listener-rule/net/*/*/*",
            "arn:aws:elasticloadbalancing:*:*:listener-rule/app/*/*/*"
          ]
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:ModifyLoadBalancerAttributes",
            "elasticloadbalancing:SetIpAddressType",
            "elasticloadbalancing:SetSecurityGroups",
            "elasticloadbalancing:SetSubnets",
            "elasticloadbalancing:DeleteLoadBalancer",
            "elasticloadbalancing:ModifyTargetGroup",
            "elasticloadbalancing:ModifyTargetGroupAttributes",
            "elasticloadbalancing:DeleteTargetGroup"
          ],
          "Resource": "*",
          "Condition": {
            "Null": {
              "aws:ResourceTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": ["elasticloadbalancing:AddTags"],
          "Resource": [
            "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*",
            "arn:aws:elasticloadbalancing:*:*:loadbalancer/net/*/*",
            "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/*/*"
          ],
          "Condition": {
            "StringEquals": {
              "elasticloadbalancing:CreateAction": [
                "CreateTargetGroup",
                "CreateLoadBalancer"
              ]
            },
            "Null": {
              "aws:RequestTag/elbv2.k8s.aws/cluster": "false"
            }
          }
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:RegisterTargets",
            "elasticloadbalancing:DeregisterTargets"
          ],
          "Resource": "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*"
        },
        {
          "Effect": "Allow",
          "Action": [
            "elasticloadbalancing:SetWebAcl",
            "elasticloadbalancing:ModifyListener",
            "elasticloadbalancing:AddListenerCertificates",
            "elasticloadbalancing:RemoveListenerCertificates",
            "elasticloadbalancing:ModifyRule"
          ],
          "Resource": "*"
        }
      ]
    }`)

		lbControllerPolicy, err := aws_iam.NewPolicy(ctx, "aws-lb-controller-policy", &aws_iam.PolicyArgs{
			Policy: lbControllerPolicyDoc,
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-lb-controller-policy", cfg.ClusterName)),
			},
		})
		if err != nil {
			return err
		}

		lbControllerRole, err := aws_iam.NewRole(ctx, "aws-lb-controller-role", &aws_iam.RoleArgs{
			AssumeRolePolicy: pulumi.All(oidcProviderArn, oidcProviderUrl).ApplyT(func(args []interface{}) string {
				providerArn := args[0].(string)
				providerUrl := args[1].(string)
				return fmt.Sprintf(`{
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Principal": {
            "Federated": "%s"
          },
          "Action": "sts:AssumeRoleWithWebIdentity",
          "Condition": {
            "StringEquals": {
              "%s:sub": "system:serviceaccount:kube-system:aws-load-balancer-controller",
              "%s:aud": "sts.amazonaws.com"
            }
          }
        }
      ]
    }`, providerArn, providerUrl, providerUrl)
			}).(pulumi.StringOutput),
			Tags: pulumi.StringMap{
				"Name": pulumi.String(fmt.Sprintf("%s-lb-controller-role", cfg.ClusterName)),
			},
		})
		if err != nil {
			return err
		}

		_, err = aws_iam.NewRolePolicyAttachment(ctx, "lb-controller-policy-attachment", &aws_iam.RolePolicyAttachmentArgs{
			PolicyArn: lbControllerPolicy.Arn,
			Role:      lbControllerRole.Name,
		})
		if err != nil {
			return err
		}

		certManagerPolicyDoc := pulumi.String(`{
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Action": "route53:GetChange",
          "Resource": "arn:aws:route53:::change/*"
        },
        {
          "Effect": "Allow",
          "Action": [
            "route53:ChangeResourceRecordSets",
            "route53:ListResourceRecordSets"
          ],
          "Resource": "arn:aws:route53:::hostedzone/*"
        },
        {
          "Effect": "Allow",
          "Action": "route53:ListHostedZonesByName",
          "Resource": "*"
        }
      ]
    }`)

		var certManagerRole *aws_iam.Role
		if cfg.WithCertManager {
			certManagerPolicy, err := aws_iam.NewPolicy(ctx, "cert-manager-policy", &aws_iam.PolicyArgs{
				Policy: certManagerPolicyDoc,
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-cert-manager-policy", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			certManagerRole, err = aws_iam.NewRole(ctx, "cert-manager-role", &aws_iam.RoleArgs{
				AssumeRolePolicy: pulumi.All(oidcProviderArn, oidcProviderUrl).ApplyT(func(args []interface{}) string {
					providerArn := args[0].(string)
					providerUrl := args[1].(string)
					return fmt.Sprintf(`{
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Principal": {
            "Federated": "%s"
          },
          "Action": "sts:AssumeRoleWithWebIdentity",
          "Condition": {
            "StringEquals": {
              "%s:sub": "system:serviceaccount:cert-manager:cert-manager",
              "%s:aud": "sts.amazonaws.com"
            }
          }
        }
      ]
    }`, providerArn, providerUrl, providerUrl)
				}).(pulumi.StringOutput),
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-cert-manager-role", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			_, err = aws_iam.NewRolePolicyAttachment(ctx, "cert-manager-policy-attachment", &aws_iam.RolePolicyAttachmentArgs{
				PolicyArn: certManagerPolicy.Arn,
				Role:      certManagerRole.Name,
			})
			if err != nil {
				return err
			}
		}

		// External DNS Policy (Route53)
		externalDNSPolicyDoc := pulumi.String(`{
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Action": [
            "route53:ChangeResourceRecordSets"
          ],
          "Resource": [
            "arn:aws:route53:::hostedzone/*"
          ]
        },
        {
          "Effect": "Allow",
          "Action": [
            "route53:ListHostedZones",
            "route53:ListResourceRecordSets",
            "route53:ListTagsForResource"
          ],
          "Resource": [
            "*"
          ]
        }
      ]
    }`)

		var externalDNSRole *aws_iam.Role
		if cfg.WithExternalDNS {
			externalDNSPolicy, err := aws_iam.NewPolicy(ctx, "external-dns-policy", &aws_iam.PolicyArgs{
				Policy: externalDNSPolicyDoc,
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-external-dns-policy", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			externalDNSRole, err = aws_iam.NewRole(ctx, "external-dns-role", &aws_iam.RoleArgs{
				AssumeRolePolicy: pulumi.All(oidcProviderArn, oidcProviderUrl).ApplyT(func(args []interface{}) string {
					providerArn := args[0].(string)
					providerUrl := args[1].(string)
					return fmt.Sprintf(`{
      "Version": "2012-10-17",
      "Statement": [
        {
          "Effect": "Allow",
          "Principal": {
            "Federated": "%s"
          },
          "Action": "sts:AssumeRoleWithWebIdentity",
          "Condition": {
            "StringEquals": {
              "%s:sub": "system:serviceaccount:kube-system:external-dns",
              "%s:aud": "sts.amazonaws.com"
            }
          }
        }
      ]
    }`, providerArn, providerUrl, providerUrl)
				}).(pulumi.StringOutput),
				Tags: pulumi.StringMap{
					"Name": pulumi.String(fmt.Sprintf("%s-external-dns-role", cfg.ClusterName)),
				},
			})
			if err != nil {
				return err
			}

			_, err = aws_iam.NewRolePolicyAttachment(ctx, "external-dns-policy-attachment", &aws_iam.RolePolicyAttachmentArgs{
				PolicyArn: externalDNSPolicy.Arn,
				Role:      externalDNSRole.Name,
			})
			if err != nil {
				return err
			}
		}

		if len(cfg.ExtraIngressRules) > 0 {
			for i, rule := range cfg.ExtraIngressRules {
				parts := strings.Split(rule, ":")
				if len(parts) == 2 {
					var portInt int
					fmt.Sscanf(parts[0], "%d", &portInt)
					cidr := strings.TrimSpace(parts[1])

					_, err := aws_ec2.NewSecurityGroupRule(ctx, fmt.Sprintf("extra-ingress-rule-%d", i), &aws_ec2.SecurityGroupRuleArgs{
						Type:            pulumi.String("ingress"),
						FromPort:        pulumi.Int(portInt),
						ToPort:          pulumi.Int(portInt),
						Protocol:        pulumi.String("tcp"),
						CidrBlocks:      pulumi.StringArray{pulumi.String(cidr)},
						SecurityGroupId: clusterSg.ID(),
					})
					if err != nil {
						return err
					}
				}
			}
		}

		kubeconfigYAML := pulumi.All(cluster.Name, cluster.Endpoint, cluster.CertificateAuthorities.Index(pulumi.Int(0)).Data().Elem()).
			ApplyT(func(args []interface{}) string {
				clusterName := args[0].(string)
				endpoint := args[1].(string)
				ca := args[2].(string)

				kubecfg := map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Config",
					"clusters": []map[string]interface{}{
						{
							"name": clusterName,
							"cluster": map[string]interface{}{
								"certificate-authority-data": ca,
								"server":                     endpoint,
							},
						},
					},
					"contexts": []map[string]interface{}{
						{
							"name": clusterName,
							"context": map[string]interface{}{
								"cluster": clusterName,
								"user":    clusterName,
							},
						},
					},
					"current-context": clusterName,
					"users": []map[string]interface{}{
						{
							"name": clusterName,
							"user": map[string]interface{}{
								"exec": map[string]interface{}{
									"apiVersion": "client.authentication.k8s.io/v1beta1",
									"command":    "aws",
									"args": []string{
										"eks",
										"get-token",
										"--cluster-name",
										clusterName,
									},
								},
							},
						},
					},
				}

				yamlBytes, _ := yaml.Marshal(kubecfg)
				return string(yamlBytes)
			}).(pulumi.StringOutput)

		k8sProvider, err := k8s.NewProvider(ctx, "k8s-prov", &k8s.ProviderArgs{
			Kubeconfig:                  kubeconfigYAML,
			EnableServerSideApply:       pulumi.Bool(true),
			SuppressDeprecationWarnings: pulumi.Bool(true),
		}, pulumi.DependsOn([]pulumi.Resource{nodeGroup}))
		if err != nil {
			return err
		}

		var awsLBController *helm_v3.Release

		if cfg.WithLBController {
			awsRegion := cfg.AWSRegion

			awsLBController, err = helm_v3.NewRelease(ctx, "aws-lb-controller", &helm_v3.ReleaseArgs{
				Chart:     pulumi.String("aws-load-balancer-controller"),
				Version:   pulumi.String("1.7.1"),
				Namespace: pulumi.String("kube-system"),
				RepositoryOpts: &helm_v3.RepositoryOptsArgs{
					Repo: pulumi.String("https://aws.github.io/eks-charts"),
				},
				Values: pulumi.Map{
					"clusterName": cluster.Name,
					"region":      pulumi.String(awsRegion),
					"vpcId":       vpcId,
					"serviceAccount": pulumi.Map{
						"create": pulumi.Bool(true),
						"name":   pulumi.String("aws-load-balancer-controller"),
						"annotations": pulumi.Map{
							"eks.amazonaws.com/role-arn": lbControllerRole.Arn,
						},
					},
					"replicaCount": pulumi.Int(2),
					"enableShield": pulumi.Bool(false),
					"enableWaf":    pulumi.Bool(false),
					"enableWafv2":  pulumi.Bool(false),
				},
				Timeout:         pulumi.Int(600),
				SkipAwait:       pulumi.Bool(false),
				CreateNamespace: pulumi.Bool(false),
			}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{lbControllerRole}))
			if err != nil {
				return err
			}
		}

		var certManagerRelease *helm_v3.Release
		if cfg.WithCertManager {
			if cfg.CertManagerEmail == "" {
				return fmt.Errorf("INTERNAL ERROR: cert-manager-email não foi validado no main.go")
			}
			if cfg.BaseDomain == "" {
				return fmt.Errorf("INTERNAL ERROR: base-domain não foi validado no main.go")
			}
			if cfg.ACMEConfig == nil {
				return fmt.Errorf("INTERNAL ERROR: ACMEConfig não foi inicializado")
			}

			certManagerDeps := []pulumi.Resource{}
			if awsLBController != nil {
				certManagerDeps = append(certManagerDeps, awsLBController)
			}

			// CERT-MANAGER COM WEBHOOK HABILITADO
			certManagerRelease, err = helm_v3.NewRelease(ctx, "cert-manager", &helm_v3.ReleaseArgs{
				Chart:     pulumi.String("cert-manager"),
				Version:   pulumi.String("v1.14.2"),
				Namespace: pulumi.String("cert-manager"),
				RepositoryOpts: &helm_v3.RepositoryOptsArgs{
					Repo: pulumi.String("https://charts.jetstack.io"),
				},
				Values: pulumi.Map{
					"installCRDs": pulumi.Bool(true),
					"serviceAccount": pulumi.Map{
						"create": pulumi.Bool(true),
						"name":   pulumi.String("cert-manager"),
						"annotations": pulumi.Map{
							"eks.amazonaws.com/role-arn": certManagerRole.Arn,
						},
					},
					"global": pulumi.Map{
						"leaderElection": pulumi.Map{
							"namespace": pulumi.String("cert-manager"),
						},
					},
					// ✅ Aumentar timeout para challenges DNS
					"webhook": pulumi.Map{
						"timeoutSeconds": pulumi.Int(30),
					},
				},
				Timeout:         pulumi.Int(600),
				SkipAwait:       pulumi.Bool(false),
				CreateNamespace: pulumi.Bool(true),
			}, pulumi.Provider(k8sProvider), pulumi.DependsOn(certManagerDeps))
			if err != nil {
				return err
			}

			// REFLECTOR (espera Cert-Manager estar pronto)
			reflectorRelease, err := helm_v3.NewRelease(ctx, "reflector", &helm_v3.ReleaseArgs{
				Chart:     pulumi.String("reflector"),
				Version:   pulumi.String("9.1.41"),
				Namespace: pulumi.String("kube-system"),
				RepositoryOpts: &helm_v3.RepositoryOptsArgs{
					Repo: pulumi.String("https://emberstack.github.io/helm-charts"),
				},
				Timeout:         pulumi.Int(300),
				SkipAwait:       pulumi.Bool(false),
				CreateNamespace: pulumi.Bool(false),
			}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{certManagerRelease}))
			if err != nil {
				return err
			}

			acmeServerURL := cfg.ACMEConfig.GetServerURL()
			if acmeServerURL == "" {
				return fmt.Errorf("INTERNAL ERROR: URL do servidor ACME está vazio")
			}

			issuerName := cfg.ACMEConfig.GetIssuerName()

			clusterIssuerYaml := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %s-prod
  labels:
    acme-provider: %s
    environment: %s
spec:
  acme:
    server: %s
    email: %s
    privateKeySecretRef:
      name: %s-prod-key
    solvers:
      - selector:
          dnsZones:
            - "%s"
        dns01:
          route53:
            region: %s
`,
				issuerName,                 // Nome: letsencrypt-prod ou google-trust-prod
				cfg.ACMEProvider,           // Label para rastreamento
				cfg.ACMEConfig.Environment, // Label ambiente
				acmeServerURL,              // URL dinâmica
				cfg.CertManagerEmail,
				issuerName, // Secret key name
				cfg.BaseDomain,
				cfg.AWSRegion,
			)

			issuerResource, err := yamlz.NewConfigGroup(ctx,
				fmt.Sprintf("%s-cluster-issuer", issuerName),
				&yamlz.ConfigGroupArgs{
					YAML: []string{clusterIssuerYaml},
				},
				pulumi.Provider(k8sProvider),
				pulumi.DependsOn([]pulumi.Resource{certManagerRelease}),
			)
			if err != nil {
				return err
			}

			// WILDCARD CERTIFICATE (com anotações corretas)
			wildcardCertYaml := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wildcard-tls-cert
  namespace: cert-manager
  labels:
    acme-provider: %s
spec:
  secretName: wildcard-tls
  issuerRef:
    name: %s-prod
    kind: ClusterIssuer
  dnsNames:
    - "*.%s"
    - "*.dev.%s"
    - "*.app.%s"
    - "*.tools.%s"
    - "%s"
  secretTemplate:
    annotations:
      reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
      reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"
      reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: ""
  usages:
    - digital signature
    - key encipherment
`,
				cfg.ACMEProvider, // Label para identificação
				issuerName,       // Referência ao issuer
				cfg.BaseDomain, cfg.BaseDomain, cfg.BaseDomain, cfg.BaseDomain, cfg.BaseDomain,
			)

			certResource, err := yamlz.NewConfigGroup(ctx, "wildcard-certificate", &yamlz.ConfigGroupArgs{
				YAML: []string{wildcardCertYaml},
			}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{issuerResource, reflectorRelease}))
			if err != nil {
				return err
			}

			// NGINX INGRESS (se habilitado)
			var nginxRelease *helm_v3.Release
			if cfg.WithNginx {
				nginxDeps := []pulumi.Resource{certResource}
				if awsLBController != nil {
					nginxDeps = append(nginxDeps, awsLBController)
				}

				nginxRelease, err = helm_v3.NewRelease(ctx, "nginx-ingress", &helm_v3.ReleaseArgs{
					Chart:     pulumi.String("ingress-nginx"),
					Version:   pulumi.String("4.10.0"),
					Namespace: pulumi.String("ingress-nginx"),
					RepositoryOpts: &helm_v3.RepositoryOptsArgs{
						Repo: pulumi.String("https://kubernetes.github.io/ingress-nginx"),
					},
					Values: pulumi.Map{
						"controller": pulumi.Map{
							"service": pulumi.Map{
								"type": pulumi.String("LoadBalancer"),
								"annotations": pulumi.Map{
									"service.beta.kubernetes.io/aws-load-balancer-type":                              pulumi.String("external"),
									"service.beta.kubernetes.io/aws-load-balancer-nlb-target-type":                   pulumi.String("ip"),
									"service.beta.kubernetes.io/aws-load-balancer-scheme":                            pulumi.String("internet-facing"),
									"service.beta.kubernetes.io/aws-load-balancer-ssl-ports":                         pulumi.String("443"),
									"service.beta.kubernetes.io/aws-load-balancer-cross-zone-load-balancing-enabled": pulumi.String("true"),
								},
								"externalTrafficPolicy": pulumi.String("Local"), // Preserva IP do cliente
							},
							"config": pulumi.Map{
								"ssl-protocols":              pulumi.String("TLSv1.2 TLSv1.3"),
								"ssl-ciphers":                pulumi.String("ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384"),
								"ssl-prefer-server-ciphers":  pulumi.String("true"),
								"enable-real-ip":             pulumi.String("true"),
								"use-forwarded-headers":      pulumi.String("true"),
								"compute-full-forwarded-for": pulumi.String("true"),
								"use-proxy-protocol":         pulumi.String("false"),
								"hsts":                       pulumi.String("true"),
								"hsts-max-age":               pulumi.String("31536000"),
								"hsts-include-subdomains":    pulumi.String("true"),
								"ssl-redirect":               pulumi.String("true"),
							},
							"resources": pulumi.Map{
								"requests": pulumi.Map{
									"cpu":    pulumi.String("100m"),
									"memory": pulumi.String("128Mi"),
								},
								"limits": pulumi.Map{
									"cpu":    pulumi.String("500m"),
									"memory": pulumi.String("512Mi"),
								},
							},
							"replicaCount": pulumi.Int(2),
							"affinity": pulumi.Map{
								"podAntiAffinity": pulumi.Map{
									"preferredDuringSchedulingIgnoredDuringExecution": pulumi.Array{
										pulumi.Map{
											"weight": pulumi.Int(100),
											"podAffinityTerm": pulumi.Map{
												"labelSelector": pulumi.Map{
													"matchExpressions": pulumi.Array{
														pulumi.Map{
															"key":      pulumi.String("app.kubernetes.io/name"),
															"operator": pulumi.String("In"),
															"values":   pulumi.StringArray{pulumi.String("ingress-nginx")},
														},
													},
												},
												"topologyKey": pulumi.String("kubernetes.io/hostname"),
											},
										},
									},
								},
							},
							"admissionWebhooks": pulumi.Map{
								"enabled": pulumi.Bool(false),
							},
						},
					},
					Timeout:         pulumi.Int(600),
					SkipAwait:       pulumi.Bool(false),
					CreateNamespace: pulumi.Bool(true),
				}, pulumi.Provider(k8sProvider), pulumi.DependsOn(nginxDeps))
				if err != nil {
					return err
				}
			}

			// EXTERNAL DNS (se habilitado)
			if cfg.WithExternalDNS {

				externalDNSDeps := []pulumi.Resource{}
				if nginxRelease != nil {
					externalDNSDeps = append(externalDNSDeps, nginxRelease)
				}

				_, err = helm_v3.NewRelease(ctx, "external-dns", &helm_v3.ReleaseArgs{
					Chart:     pulumi.String("external-dns"),
					Version:   pulumi.String("1.14.3"),
					Namespace: pulumi.String("kube-system"),
					RepositoryOpts: &helm_v3.RepositoryOptsArgs{
						Repo: pulumi.String("https://kubernetes-sigs.github.io/external-dns"),
					},
					Values: pulumi.Map{
						"provider": pulumi.String("aws"),
						"sources": pulumi.StringArray{
							pulumi.String("ingress"),
							pulumi.String("service"),
						},
						"domainFilters": pulumi.StringArray{
							pulumi.String(cfg.BaseDomain),
						},
						"policy":     pulumi.String("upsert-only"),
						"registry":   pulumi.String("txt"),
						"txtOwnerId": pulumi.String(cfg.ClusterName),
						"txtPrefix":  pulumi.String("external-dns-"),
						"serviceAccount": pulumi.Map{
							"create": pulumi.Bool(true),
							"name":   pulumi.String("external-dns"),
							"annotations": pulumi.Map{
								"eks.amazonaws.com/role-arn": externalDNSRole.Arn,
							},
						},
						"logLevel": pulumi.String("info"),
						"interval": pulumi.String("1m"),
						"extraArgs": pulumi.StringArray{
							pulumi.String("--aws-zone-type=public"),
							pulumi.String("--aws-prefer-cname"),
						},
					},
					Timeout:         pulumi.Int(300),
					SkipAwait:       pulumi.Bool(false),
					CreateNamespace: pulumi.Bool(false),
				}, pulumi.Provider(k8sProvider), pulumi.DependsOn(externalDNSDeps))
				if err != nil {
					return err
				}
			}

			// ARGOCD (aguarda certificado estar replicado)
			if cfg.WithArgoCD {
				if cfg.ArgocdDomain == "" {
					return fmt.Errorf("--argocd-domain é obrigatório quando usar --with-argocd com TLS")
				}

				// Aguarda secret replicado no namespace argocd
				argoDeps := []pulumi.Resource{certResource, reflectorRelease}
				if nginxRelease != nil {
					argoDeps = append(argoDeps, nginxRelease)
				}

				// Configuração ArgoCD production-ready
				argoValues := pulumi.Map{
					"global": pulumi.Map{
						"domain": pulumi.String(cfg.ArgocdDomain),
					},
					"configs": pulumi.Map{
						"params": pulumi.Map{
							"server.insecure": pulumi.String("true"),
						},
					},
					"certificate": pulumi.Map{
						"enabled": pulumi.Bool(false),
					},
					"server": pulumi.Map{
						"ingress": pulumi.Map{
							"enabled":          pulumi.Bool(true),
							"ingressClassName": pulumi.String("nginx"),
							"annotations": pulumi.Map{
								"nginx.ingress.kubernetes.io/backend-protocol":   pulumi.String("HTTP"),
								"nginx.ingress.kubernetes.io/force-ssl-redirect": pulumi.String("true"),
								"nginx.ingress.kubernetes.io/ssl-passthrough":    pulumi.String("false"),
								"nginx.ingress.kubernetes.io/websocket-services": pulumi.String("argocd-server"),
								"nginx.ingress.kubernetes.io/proxy-read-timeout": pulumi.String("600"),
								"nginx.ingress.kubernetes.io/proxy-send-timeout": pulumi.String("600"),
								"external-dns.alpha.kubernetes.io/hostname":      pulumi.String(cfg.ArgocdDomain),
							},
							"hosts":    pulumi.Array{pulumi.String(cfg.ArgocdDomain)},
							"paths":    pulumi.Array{pulumi.String("/")},
							"pathType": pulumi.String("Prefix"),
							"extraTls": pulumi.Array{
								pulumi.Map{
									"hosts":      pulumi.Array{pulumi.String(cfg.ArgocdDomain)},
									"secretName": pulumi.String("wildcard-tls"),
								},
							},
						},
					},
				}

				// Ingress com TLS configurado corretamente
				//if cfg.WithNginx {
				//	ingressAnnotations := pulumi.Map{
				//		"nginx.ingress.kubernetes.io/backend-protocol":   pulumi.String("HTTP"),
				//		"nginx.ingress.kubernetes.io/force-ssl-redirect": pulumi.String("true"),
				//		"nginx.ingress.kubernetes.io/ssl-passthrough":    pulumi.String("false"),
				//		"nginx.ingress.kubernetes.io/websocket-services": pulumi.String("argocd-server"),
				//		"nginx.ingress.kubernetes.io/proxy-read-timeout": pulumi.String("600"),
				//		"nginx.ingress.kubernetes.io/proxy-send-timeout": pulumi.String("600"),
				//	}
				//
				//	if cfg.WithExternalDNS {
				//		ingressAnnotations["external-dns.alpha.kubernetes.io/hostname"] = pulumi.String(cfg.ArgocdDomain)
				//	}
				//
				//	serverMap := argoValues["server"].(pulumi.Map)
				//	serverMap["ingress"] = pulumi.Map{
				//		"enabled":          pulumi.Bool(true),
				//		"ingressClassName": pulumi.String("nginx"),
				//		"annotations":      ingressAnnotations,
				//		"hosts": pulumi.Array{
				//			pulumi.String(cfg.ArgocdDomain),
				//		},
				//		"paths":    pulumi.Array{pulumi.String("/")},
				//		"pathType": pulumi.String("Prefix"),
				//		"https":    pulumi.Bool(true), // HTTP interno, TLS no Nginx
				//		"tls": pulumi.Array{
				//			pulumi.Map{
				//				"secretName": pulumi.String("wildcard-tls"),
				//				"hosts": pulumi.Array{
				//					pulumi.String(cfg.ArgocdDomain),
				//				},
				//			},
				//		},
				//	}
				//}

				// Deploy ArgoCD
				argoRelease, err := helm_v3.NewRelease(ctx, "argocd", &helm_v3.ReleaseArgs{
					Name:      pulumi.String("argocd"),
					Chart:     pulumi.String("argo-cd"),
					Version:   pulumi.String("6.7.3"),
					Namespace: pulumi.String("argocd"),
					RepositoryOpts: &helm_v3.RepositoryOptsArgs{
						Repo: pulumi.String("https://argoproj.github.io/argo-helm"),
					},
					Values:          argoValues,
					Timeout:         pulumi.Int(900),
					SkipAwait:       pulumi.Bool(false),
					CreateNamespace: pulumi.Bool(true),
				}, pulumi.Provider(k8sProvider), pulumi.DependsOn(argoDeps))
				if err != nil {
					return err
				}

				// Exports do ArgoCD
				ctx.Export("argocdUrl", pulumi.Sprintf("https://%s", cfg.ArgocdDomain))
				ctx.Export("argocdUsername", pulumi.String("admin"))
				ctx.Export("argocdPasswordCmd", pulumi.String(
					"kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath=\"{.data.password}\" | base64 -d",
				))

				// Export para validação do certificado
				ctx.Export("validateCertificate", pulumi.String(
					fmt.Sprintf("kubectl get certificate -n cert-manager wildcard-tls-cert && kubectl get secret -n argocd wildcard-tls"),
				))

				_ = argoRelease // Evita warning de variável não usada
			}
		}

		if cfg.WithIstio {
			istioBase, err := helm_v3.NewRelease(ctx, "istio-base", &helm_v3.ReleaseArgs{
				Chart:     pulumi.String("base"),
				Version:   pulumi.String("1.21.0"),
				Namespace: pulumi.String("istio-system"),
				RepositoryOpts: &helm_v3.RepositoryOptsArgs{
					Repo: pulumi.String("https://istio-release.storage.googleapis.com/charts"),
				},
				Timeout:         pulumi.Int(600),
				SkipAwait:       pulumi.Bool(true),
				CreateNamespace: pulumi.Bool(true),
			}, pulumi.Provider(k8sProvider))
			if err != nil {
				return err
			}

			_, err = helm_v3.NewRelease(ctx, "istiod", &helm_v3.ReleaseArgs{
				Chart:     pulumi.String("istiod"),
				Version:   pulumi.String("1.21.0"),
				Namespace: pulumi.String("istio-system"),
				RepositoryOpts: &helm_v3.RepositoryOptsArgs{
					Repo: pulumi.String("https://istio-release.storage.googleapis.com/charts"),
				},
				Timeout:         pulumi.Int(600),
				SkipAwait:       pulumi.Bool(true),
				CreateNamespace: pulumi.Bool(false),
			}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{istioBase}))
			if err != nil {
				return err
			}
		}

		ctx.Export("clusterName", cluster.Name)
		ctx.Export("clusterEndpoint", cluster.Endpoint)
		ctx.Export("kubeconfig", pulumi.ToSecret(kubeconfigYAML))
		ctx.Export("nodeGroupStatus", nodeGroup.Status)
		ctx.Export("vpcId", vpcId)
		ctx.Export("oidcProviderArn", oidcProvider.Arn)
		ctx.Export("lbControllerRoleArn", lbControllerRole.Arn)

		if cfg.WithCertManager && certManagerRole != nil {
			ctx.Export("certManagerRoleArn", certManagerRole.Arn)
		}

		if cfg.ArgocdDomain != "" {
			ctx.Export("argocdUrl", pulumi.Sprintf("https://%s", cfg.ArgocdDomain))
		}

		return nil
	}
}

func toStringArray(arr []string) pulumi.StringArray {
	var res pulumi.StringArray
	for _, s := range arr {
		res = append(res, pulumi.String(s))
	}
	return res
}
