package installer

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

func InstallNginxIngress() error {
	utils.Logf("   📦 Installing Nginx Ingress Controller (production-ready configuration)...\n")

	topology, err := detectClusterTopology()
	if err != nil {
		return fmt.Errorf("failed to detect cluster topology: %w", err)
	}

	utils.Logf("   📊 Cluster Topology: %d control-plane(s), %d worker(s)\n",
		topology.ControlPlaneCount, topology.WorkerCount)

	if topology.IsHA {
		utils.Logf("   🔧 HA mode detected - configuring for high availability\n")
	}

	// Aplicar manifesto oficial
	manifestURL := "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml"

	utils.Logf("   📥 Applying Nginx Ingress manifest...\n")
	output, err := utils.RunCommand("kubectl", 3*time.Minute, "apply", "-f", manifestURL)
	if err != nil {
		return fmt.Errorf("failed to apply manifest: %s", output)
	}
	utils.Logf("   ✓ Manifest applied\n")

	// Aguardar Service ser criado
	utils.Logf("   ⏳ Waiting for service to be created (5s)...\n")
	time.Sleep(5 * time.Second)

	// ✅ PATCH CRÍTICO: Forçar NodePort 30080/30443
	utils.Logf("   🔧 Patching service to use fixed NodePorts (30080/30443)...\n")

	servicePatch := `{
                "spec": {
                        "ports": [
                                {
                                        "name": "http",
                                        "port": 80,
                                        "protocol": "TCP",
                                        "targetPort": "http",
                                        "nodePort": 30080
                                },
                                {
                                        "name": "https",
                                        "port": 443,
                                        "protocol": "TCP",
                                        "targetPort": "https",
                                        "nodePort": 30443
                                }
                        ]
                }
        }`

	patchArgs := []string{
		"patch", "service", "ingress-nginx-controller",
		"-n", "ingress-nginx",
		"--type", "strategic",
		"--patch", servicePatch,
	}

	output, err = utils.RunCommand("kubectl", 30*time.Second, patchArgs...)
	if err != nil {
		return fmt.Errorf("failed to patch service NodePorts: %s", output)
	}
	utils.Logf("   ✓ Service patched with NodePort 30080/30443\n")

	// Verificar NodePorts
	svcOutput, err := utils.RunCommand("kubectl", 30*time.Second,
		"get", "svc", "-n", "ingress-nginx", "ingress-nginx-controller",
		"-o", "jsonpath={.spec.ports[*].nodePort}")
	if err == nil {
		if strings.Contains(svcOutput, "30080") && strings.Contains(svcOutput, "30443") {
			utils.Logf("   ✅ NodePorts correctly set: %s\n", svcOutput)
		} else {
			utils.Logf("   ⚠️  Unexpected NodePorts: %s\n", svcOutput)
		}
	}

	// Aguardar pods
	utils.Logf("   ⏳ Waiting for initial pod to be ready (up to 5 minutes)...\n")
	waitArgs := []string{
		"wait", "--namespace", "ingress-nginx",
		"--for=condition=ready", "pod",
		"--selector=app.kubernetes.io/component=controller",
		"--timeout=5m",
	}
	if output, err := utils.RunCommand("kubectl", 6*time.Minute, waitArgs...); err != nil {
		return fmt.Errorf("timeout waiting for pods: %s", output)
	}
	utils.Logf("   ✓ Initial Nginx Ingress Controller pod ready\n")

	// ✅ PRODUCTION CONFIG: HA scheduling + PodDisruptionBudget
	if topology.IsHA {
		utils.Logf("   🔧 Applying production HA configuration...\n")
		if err := applyProductionHAConfig(topology.WorkerCount); err != nil {
			utils.Logf("   ⚠️  Warning: HA configuration failed: %v\n", err)
		} else {
			utils.Logf("   ✓ Production HA configuration applied\n")
		}
	}

	// Teste de conectividade
	utils.Logf("   🧪 Testing connectivity...\n")
	testOutput, err := utils.RunCommand("curl", 10*time.Second,
		"-s", "-o", "/dev/null", "-w", "%{http_code}", "http://localhost")
	if err == nil {
		statusCode := strings.TrimSpace(testOutput)
		if statusCode == "404" || statusCode == "200" {
			utils.Logf("   ✅ Nginx is responding! (HTTP %s)\n", statusCode)
		} else {
			utils.Logf("   ⚠️  Unexpected response: HTTP %s\n", statusCode)
		}
	} else {
		utils.Logf("   ⚠️  Could not connect to localhost:80\n")
	}

	utils.Logf("\n   ✅ Nginx Ingress Controller ready!\n")
	if topology.IsHA {
		utils.Logf("   💡 Production HA: Multiple replicas distributed across workers\n")
	}
	utils.Logf("   💡 Access: http://localhost\n\n")

	return nil
}

func applyProductionHAConfig(workerCount int) error {
	// Calcular número de réplicas: mínimo 2, máximo 3 ou número de workers
	replicas := 3
	if workerCount < 3 {
		replicas = workerCount
	}
	if replicas < 2 {
		replicas = 2
	}

	minAvailable := 1
	if replicas >= 3 {
		minAvailable = 2
	}

	utils.Logf("   📊 Configuring %d replicas with minAvailable=%d\n", replicas, minAvailable)

	// ✅ PATCH 1: Deployment com múltiplas réplicas + anti-affinity + resource limits
	deploymentPatch := fmt.Sprintf(`{
          "spec": {
            "replicas": %d,
            "template": {
              "spec": {
                "nodeSelector": {
                  "ingress-ready": "true"
                },
                "tolerations": [
                  {
                    "key": "node-role.kubernetes.io/control-plane",
                    "operator": "Equal",
                    "effect": "NoSchedule"
                  }
                ],
                "affinity": {
                  "podAntiAffinity": {
                    "preferredDuringSchedulingIgnoredDuringExecution": [
                      {
                        "weight": 100,
                        "podAffinityTerm": {
                          "labelSelector": {
                            "matchExpressions": [
                              {
                                "key": "app.kubernetes.io/name",
                                "operator": "In",
                                "values": ["ingress-nginx"]
                              }
                            ]
                          },
                          "topologyKey": "kubernetes.io/hostname"
                        }
                      }
                    ]
                  }
                }
              }
            }
          }
        }`, replicas)

	patchArgs := []string{
		"patch", "deployment", "ingress-nginx-controller",
		"-n", "ingress-nginx",
		"--type", "strategic",
		"--patch", deploymentPatch,
	}

	output, err := utils.RunCommand("kubectl", 30*time.Second, patchArgs...)
	if err != nil {
		return fmt.Errorf("deployment patch failed: %s", output)
	}
	utils.Logf("   ✓ Deployment configured with %d replicas and anti-affinity\n", replicas)

	// ✅ PATCH 2: PodDisruptionBudget para garantir disponibilidade mínima
	utils.Logf("   🛡️  Creating PodDisruptionBudget...\n")

	pdbYAML := fmt.Sprintf(`apiVersion: policy/v1
    kind: PodDisruptionBudget
    metadata:
      name: ingress-nginx-controller
      namespace: ingress-nginx
    spec:
      minAvailable: %d
      selector:
        matchLabels:
          app.kubernetes.io/name: ingress-nginx
          app.kubernetes.io/component: controller
    `, minAvailable)

	pdbFile, err := utils.CreateTempFile("nginx-pdb-*.yaml", pdbYAML)
	if err != nil {
		return fmt.Errorf("failed to create PDB file: %w", err)
	}
	defer utils.RemoveFile(pdbFile)

	output, err = utils.RunCommand("kubectl", 30*time.Second, "apply", "-f", pdbFile)
	if err != nil {
		return fmt.Errorf("failed to apply PDB: %s", output)
	}
	utils.Logf("   ✓ PodDisruptionBudget created (minAvailable: %d)\n", minAvailable)

	// Aguardar pods serem distribuídos
	utils.Logf("   ⏳ Waiting for pods to be distributed across workers (20s)...\n")
	time.Sleep(20 * time.Second)

	// Verificar distribuição dos pods
	podDistOutput, err := utils.RunCommand("kubectl", 30*time.Second,
		"get", "pods", "-n", "ingress-nginx",
		"-l", "app.kubernetes.io/component=controller",
		"-o", "custom-columns=POD:metadata.name,NODE:spec.nodeName,STATUS:status.phase",
		"--no-headers")

	if err == nil && strings.TrimSpace(podDistOutput) != "" {
		utils.Logf("   📊 Nginx pod distribution:\n")
		lines := strings.Split(strings.TrimSpace(podDistOutput), "\n")
		nodeCount := make(map[string]int)
		runningCount := 0

		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				podName := fields[0]
				nodeName := fields[1]
				status := fields[2]
				nodeCount[nodeName]++
				if status == "Running" {
					runningCount++
				}
				utils.Logf("      • %s → %s (%s)\n", podName, nodeName, status)
			}
		}

		utils.Logf("   📈 Summary: %d/%d pods running across %d nodes\n",
			runningCount, replicas, len(nodeCount))

		if len(nodeCount) >= 2 {
			utils.Logf("   ✅ High availability achieved (pods distributed)\n")
		} else {
			utils.Logf("   ⚠️  Warning: All pods on same node (may need more time to redistribute)\n")
		}
	}

	return nil
}

func detectClusterTopology() (*ClusterTopology, error) {
	topology := &ClusterTopology{}

	output, err := utils.RunCommand("kubectl", 30*time.Second,
		"get", "nodes", "-l", "node-role.kubernetes.io/control-plane", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get control-plane nodes: %w", err)
	}
	topology.ControlPlaneCount = countLines(output)

	output, err = utils.RunCommand("kubectl", 30*time.Second,
		"get", "nodes", "-l", "!node-role.kubernetes.io/control-plane", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get worker nodes: %w", err)
	}
	topology.WorkerCount = countLines(output)

	topology.IsHA = topology.ControlPlaneCount >= 3

	return topology, nil
}

type ClusterTopology struct {
	ControlPlaneCount int
	WorkerCount       int
	IsHA              bool
}

func countLines(output string) int {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func RemoveNginxIngress() error {
	utils.Logf("   🗑️  Removing Nginx Ingress...\n")

	// Remover PDB primeiro
	utils.RunCommand("kubectl", 30*time.Second, "delete", "pdb",
		"-n", "ingress-nginx", "ingress-nginx-controller", "--ignore-not-found=true")

	// Remover manifesto
	manifestURL := "https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml"
	output, err := utils.RunCommand("kubectl", 2*time.Minute, "delete", "-f", manifestURL, "--ignore-not-found=true")
	if err != nil {
		return fmt.Errorf("failed to delete: %s", output)
	}
	utils.Logf("   ✅ Nginx Ingress removed successfully\n")
	return nil
}

func IsNginxIngressInstalled() bool {
	_, err := utils.RunCommand("kubectl", utils.ShortTimeout,
		"get", "deployment", "-n", "ingress-nginx", "ingress-nginx-controller")
	return err == nil
}
