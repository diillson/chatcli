package validator

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

func EnsureDependencies(deps ...string) error {
	for _, dep := range deps {
		if _, err := exec.LookPath(dep); err != nil {
			return fmt.Errorf("required dependency '%s' not found in PATH", dep)
		}
	}
	return nil
}

// ✅ REFATORAÇÃO: Receber expectedControlPlanes e expectedWorkers como parâmetros
func WaitForClusterReady(clusterName string, expectedControlPlanes, expectedWorkers int) error {
	utils.Logf("   ⏳ Waiting for cluster to be ready...\n")

	expectedNodes := expectedControlPlanes + expectedWorkers
	isHA := expectedControlPlanes >= 3

	if isHA {
		utils.Logf("   📊 HA cluster: %d control-plane(s) + %d worker(s) = %d nodes expected\n",
			expectedControlPlanes, expectedWorkers, expectedNodes)
		utils.Logf("   ℹ️  Extended wait time for HA cluster stabilization\n")
	} else {
		utils.Logf("   📊 Single control-plane cluster: %d control-plane + %d worker(s) = %d nodes expected\n",
			expectedControlPlanes, expectedWorkers, expectedNodes)
	}

	// ✅ ETAPA 1: Aguardar API Server responder
	utils.Logf("   - Waiting for API server to respond...\n")
	maxAPIRetries := 60 // 2 minutos
	if isHA {
		maxAPIRetries = 120 // 4 minutos para HA
	}

	apiReady := false
	for i := 0; i < maxAPIRetries; i++ {
		output, err := utils.RunCommand("kubectl", 5*time.Second, "cluster-info")
		if err == nil && strings.Contains(output, "running") {
			apiReady = true
			break
		}

		if i > 0 && i%15 == 0 {
			utils.Logf("   ⏳ Still waiting for API server... (%d seconds)\n", i*2)
		}
		time.Sleep(2 * time.Second)
	}

	if !apiReady {
		return fmt.Errorf("timeout waiting for API server to respond")
	}
	utils.Logf("   ✓ API server is responding\n")

	// ✅ ETAPA 2: Aguardar todos os nodes serem registrados
	utils.Logf("   - Waiting for all %d nodes to be registered...\n", expectedNodes)

	maxNodeRetries := 60
	if isHA {
		maxNodeRetries = 90 // 3 minutos para HA
	}

	nodesRegistered := false
	for i := 0; i < maxNodeRetries; i++ {
		output, err := utils.RunCommand("kubectl", 5*time.Second, "get", "nodes", "--no-headers")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(output), "\n")
			nodeCount := 0
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					nodeCount++
				}
			}

			if nodeCount >= expectedNodes {
				nodesRegistered = true
				utils.Logf("   ✓ All %d nodes registered\n", nodeCount)
				break
			}

			if i > 0 && i%10 == 0 {
				utils.Logf("   ⏳ Nodes registered: %d/%d (%d seconds)\n", nodeCount, expectedNodes, i*2)
			}
		}

		time.Sleep(2 * time.Second)
	}

	if !nodesRegistered {
		utils.Logf("   ⚠️  Warning: Not all nodes registered in expected time\n")
		// Não retornar erro aqui, continuar para verificar se os que existem estão Ready
	}

	// ✅ ETAPA 3: Aguardar nodes ficarem Ready
	utils.Logf("   - Waiting for all nodes to be Ready...\n")

	maxReadyRetries := 90 // 3 minutos
	if isHA {
		maxReadyRetries = 150 // 5 minutos para HA
	}

	allReady := false
	for i := 0; i < maxReadyRetries; i++ {
		output, err := utils.RunCommand("kubectl", 10*time.Second, "get", "nodes", "--no-headers")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(output), "\n")
			ready := true
			notReadyNodes := []string{}
			readyCount := 0

			for _, line := range lines {
				if line != "" {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						nodeName := fields[0]
						status := fields[1]
						if strings.Contains(status, "Ready") {
							readyCount++
						} else {
							ready = false
							notReadyNodes = append(notReadyNodes, nodeName)
						}
					}
				}
			}

			// Verificar se temos pelo menos os nodes esperados e todos estão Ready
			if ready && readyCount >= expectedNodes {
				allReady = true
				utils.Logf("   ✓ All %d nodes are Ready\n", readyCount)
				break
			}

			if i > 0 && i%15 == 0 {
				if len(notReadyNodes) > 0 {
					utils.Logf("   ⏳ Waiting for nodes (%d/%d ready): %s (%d seconds)\n",
						readyCount, expectedNodes, strings.Join(notReadyNodes, ", "), i*2)
				} else {
					utils.Logf("   ⏳ Waiting for nodes to be Ready... (%d/%d, %d seconds)\n",
						readyCount, expectedNodes, i*2)
				}
			}
		}

		time.Sleep(2 * time.Second)
	}

	if !allReady {
		utils.Logf("   ⚠️  Warning: Not all nodes became Ready in expected time\n")
		if output, err := utils.RunCommand("kubectl", 10*time.Second, "get", "nodes"); err == nil {
			utils.Logf("   Current node status:\n%s\n", output)
		}
		return fmt.Errorf("timeout waiting for nodes to be ready")
	}

	// ✅ ETAPA 4: Aguardar pods do sistema ficarem prontos
	utils.Logf("   - Waiting for system pods to be ready...\n")

	systemNamespaces := []string{"kube-system"}

	for _, ns := range systemNamespaces {
		maxPodRetries := 60
		if isHA {
			maxPodRetries = 90
		}

		podsReady := false
		for i := 0; i < maxPodRetries; i++ {
			output, err := utils.RunCommand("kubectl", 10*time.Second,
				"get", "pods", "-n", ns, "--no-headers")

			if err == nil && output != "" {
				lines := strings.Split(strings.TrimSpace(output), "\n")
				allPodsReady := true
				notReadyPods := []string{}

				for _, line := range lines {
					if line != "" {
						fields := strings.Fields(line)
						if len(fields) >= 3 {
							podName := fields[0]
							status := fields[2]
							if !strings.Contains(status, "Running") &&
								!strings.Contains(status, "Completed") &&
								!strings.Contains(status, "Succeeded") {
								allPodsReady = false
								notReadyPods = append(notReadyPods, podName)
							}
						}
					}
				}

				if allPodsReady {
					podsReady = true
					break
				}

				if i > 0 && i%15 == 0 && len(notReadyPods) > 0 {
					utils.Logf("   ⏳ Waiting for pods in %s: %s\n", ns, strings.Join(notReadyPods, ", "))
				}
			}

			time.Sleep(2 * time.Second)
		}

		if !podsReady {
			utils.Logf("   ⚠️  Warning: Not all pods in %s became ready\n", ns)
		} else {
			utils.Logf("   ✓ All system pods in %s are ready\n", ns)
		}
	}

	// Tempo adicional para estabilização em HA
	if isHA {
		utils.Logf("   ⏳ Allowing extra time for HA cluster stabilization (10s)...\n")
		time.Sleep(10 * time.Second)
	}

	utils.Logf("   ✅ Cluster is ready!\n")
	return nil
}
