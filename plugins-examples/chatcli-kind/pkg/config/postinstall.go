package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

// ApplyIngressNodeConfiguration verifica se workers estão prontos para ingress
func ApplyIngressNodeConfiguration(clusterName string, isHA bool, needsIngress bool) error {
	if !isHA || !needsIngress {
		return nil
	}

	utils.Logf("   🔧 Verifying ingress-ready workers for HA deployment...\n")

	// Aguardar workers estarem Ready
	maxRetries := 60
	for i := 0; i < maxRetries; i++ {
		output, err := utils.RunCommand("kubectl", 10*time.Second,
			"get", "nodes", "-l", "!node-role.kubernetes.io/control-plane", "--no-headers")

		if err == nil && strings.TrimSpace(output) != "" {
			lines := strings.Split(strings.TrimSpace(output), "\n")
			allReady := true
			for _, line := range lines {
				if !strings.Contains(line, " Ready ") {
					allReady = false
					break
				}
			}
			if allReady {
				break
			}
		}

		if i > 0 && i%10 == 0 {
			utils.Logf("   ⏳ Waiting for workers... (%d seconds)\n", i*2)
		}
		time.Sleep(2 * time.Second)
	}

	// Verificar workers com label ingress-ready
	output, err := utils.RunCommand("kubectl", 10*time.Second,
		"get", "nodes", "-l", "ingress-ready=true,!node-role.kubernetes.io/control-plane",
		"--no-headers")

	if err != nil {
		return fmt.Errorf("failed to list ingress-ready workers: %w", err)
	}

	workerLines := strings.Split(strings.TrimSpace(output), "\n")
	ingressWorkerCount := 0
	utils.Logf("   📊 Ingress-ready workers:\n")
	for _, line := range workerLines {
		if strings.TrimSpace(line) != "" {
			ingressWorkerCount++
			fields := strings.Fields(line)
			if len(fields) > 0 {
				utils.Logf("      • %s\n", fields[0])
			}
		}
	}

	if ingressWorkerCount == 0 {
		return fmt.Errorf("no ingress-ready workers found")
	}

	utils.Logf("   ✅ Found %d ingress-ready worker(s) - ready for HA ingress deployment\n", ingressWorkerCount)
	return nil
}
