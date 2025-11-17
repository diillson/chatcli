package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

func InstallIstio(clusterName, istioVersion, istioProfile string, isMacOS, withNginx bool) error {
	istioctlPath, err := ensureIstioctl(istioVersion)
	if err != nil {
		return fmt.Errorf("failed to ensure istioctl: %w", err)
	}

	utils.Logf("   🔧 Installing Istio control plane (profile '%s')...\n", istioProfile)

	installArgs := []string{"install", "--set", "profile=" + istioProfile}

	// ✅ LÓGICA CONDICIONAL: Usar NodePorts diferentes se Nginx está presente
	istioHTTPNodePort := 30080
	istioHTTPSNodePort := 30443
	istioStatusNodePort := 30021

	if withNginx {
		// Nginx já ocupa 30080/30443, usar portas alternativas
		istioHTTPNodePort = 30180
		istioHTTPSNodePort = 30543
		utils.Logf("   ⚠️  Nginx detected (using NodePort 30080/30443)\n")
		utils.Logf("   📊 Istio will use NodePort %d/%d (to avoid conflict)\n",
			istioHTTPNodePort, istioHTTPSNodePort)
	}

	installArgs = append(installArgs,
		"--set", "components.ingressGateways[0].name=istio-ingressgateway",
		"--set", "components.ingressGateways[0].enabled=true",
		"--set", "components.ingressGateways[0].k8s.service.type=NodePort",

		// HTTP Port
		"--set", "components.ingressGateways[0].k8s.service.ports[0].name=http2",
		"--set", "components.ingressGateways[0].k8s.service.ports[0].port=80",
		"--set", "components.ingressGateways[0].k8s.service.ports[0].targetPort=8080",
		"--set", fmt.Sprintf("components.ingressGateways[0].k8s.service.ports[0].nodePort=%d", istioHTTPNodePort),

		// HTTPS Port
		"--set", "components.ingressGateways[0].k8s.service.ports[1].name=https",
		"--set", "components.ingressGateways[0].k8s.service.ports[1].port=443",
		"--set", "components.ingressGateways[0].k8s.service.ports[1].targetPort=8443",
		"--set", fmt.Sprintf("components.ingressGateways[0].k8s.service.ports[1].nodePort=%d", istioHTTPSNodePort),

		// Status Port
		"--set", "components.ingressGateways[0].k8s.service.ports[2].name=status-port",
		"--set", "components.ingressGateways[0].k8s.service.ports[2].port=15021",
		"--set", "components.ingressGateways[0].k8s.service.ports[2].targetPort=15021",
		"--set", fmt.Sprintf("components.ingressGateways[0].k8s.service.ports[2].nodePort=%d", istioStatusNodePort),

		"-y",
	)

	utils.Logf("   ⏳ Installing Istio (may take up to 5 minutes)...\n")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, istioctlPath, installArgs...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	multiReader := io.MultiReader(stdout, stderr)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Istio installation: %w", err)
	}

	buf := make([]byte, 1024)
	var output strings.Builder
	for {
		n, err := multiReader.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			output.WriteString(chunk)
			utils.Logf("      %s", chunk)
		}
		if err != nil {
			break
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout installing Istio")
		}
		return fmt.Errorf("failed to install Istio: %s", output.String())
	}

	utils.Logf("   ✓ Istio control plane installed\n")

	// Verificar NodePorts do Istio
	utils.Logf("   🔍 Verifying Istio Gateway NodePorts...\n")
	svcOutput, err := utils.RunCommand("kubectl", 30*time.Second,
		"get", "svc", "-n", "istio-system", "istio-ingressgateway",
		"-o", "jsonpath={.spec.ports[*].nodePort}")
	if err == nil {
		utils.Logf("   📊 Istio NodePorts: %s\n", svcOutput)

		expectedHTTP := fmt.Sprintf("%d", istioHTTPNodePort)
		expectedHTTPS := fmt.Sprintf("%d", istioHTTPSNodePort)

		if strings.Contains(svcOutput, expectedHTTP) && strings.Contains(svcOutput, expectedHTTPS) {
			utils.Logf("   ✅ Istio Gateway NodePorts correctly configured\n")
		} else {
			utils.Logf("   ⚠️  Unexpected NodePorts (expected %s/%s)\n", expectedHTTP, expectedHTTPS)
		}
	}

	// Aguardar componentes
	utils.Logf("   ⏳ Waiting for Istio components...\n")

	istiodWaitArgs := []string{
		"wait", "--namespace", "istio-system", "--for=condition=ready", "pod",
		"--selector=app=istiod", "--timeout=5m",
	}
	if output, err := utils.RunCommand("kubectl", 6*time.Minute, istiodWaitArgs...); err != nil {
		utils.Logf("   ⚠️  Warning: istiod not ready: %s\n", output)
	} else {
		utils.Logf("   ✓ istiod ready\n")
	}

	gatewayWaitArgs := []string{
		"wait", "--namespace", "istio-system", "--for=condition=ready", "pod",
		"--selector=app=istio-ingressgateway", "--timeout=5m",
	}
	if output, err := utils.RunCommand("kubectl", 6*time.Minute, gatewayWaitArgs...); err != nil {
		utils.Logf("   ⚠️  Warning: gateway not ready: %s\n", output)
	} else {
		utils.Logf("   ✓ Istio gateway ready\n")
	}

	// ✅ ADICIONAR: HA scheduling
	topology, _ := detectIstioClusterTopology()
	if topology != nil && topology.IsHA {
		utils.Logf("   🔧 Applying Istio HA scheduling configuration...\n")
		if err := applyIstioHAScheduling(); err != nil {
			utils.Logf("   ⚠️  Warning: HA scheduling failed: %v\n", err)
		} else {
			utils.Logf("   ✓ Istio HA scheduling applied\n")
		}
	}

	// Habilitar sidecar injection
	utils.Logf("   🔧 Enabling sidecar injection...\n")
	if output, err := utils.RunCommand("kubectl", 30*time.Second,
		"label", "namespace", "default", "istio-injection=enabled", "--overwrite"); err != nil {
		return fmt.Errorf("failed to enable sidecar injection: %s", output)
	}
	utils.Logf("   ✓ Sidecar injection enabled\n")

	// Teste de conectividade
	if withNginx {
		utils.Logf("   🧪 Testing Istio connectivity (port 8080)...\n")
		testOutput, err := utils.RunCommand("curl", 10*time.Second,
			"-s", "-o", "/dev/null", "-w", "%{http_code}", "http://localhost:8080")
		if err == nil {
			statusCode := strings.TrimSpace(testOutput)
			if statusCode == "404" || statusCode == "200" {
				utils.Logf("   ✅ Istio is responding! (HTTP %s)\n", statusCode)
			} else {
				utils.Logf("   ⚠️  Unexpected response: HTTP %s\n", statusCode)
			}
		} else {
			utils.Logf("   ⚠️  Could not connect to localhost:8080\n")
		}
	} else {
		utils.Logf("   🧪 Testing Istio connectivity (port 80)...\n")
		testOutput, err := utils.RunCommand("curl", 10*time.Second,
			"-s", "-o", "/dev/null", "-w", "%{http_code}", "http://localhost")
		if err == nil {
			statusCode := strings.TrimSpace(testOutput)
			utils.Logf("   📊 Istio response: HTTP %s\n", statusCode)
		}
	}

	// Informações de acesso
	utils.Logf("\n   💡 ISTIO ACCESS INFORMATION:\n")
	utils.Logf("   ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
	if withNginx {
		utils.Logf("      • Nginx:  http://localhost       (NodePort 30080)\n")
		utils.Logf("      • Istio:  http://localhost:8080  (NodePort 30180)\n")
		utils.Logf("                https://localhost:8443 (NodePort 30543)\n")
	} else {
		utils.Logf("      • Istio:  http://localhost       (NodePort 30080)\n")
		utils.Logf("                https://localhost:443  (NodePort 30443)\n")
	}
	utils.Logf("   ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	return nil
}

func applyIstioHAScheduling() error {
	// Detectar número de workers
	topology, err := detectIstioClusterTopology()
	if err != nil {
		return err
	}

	workerCount := topology.WorkerCount
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

	// ✅ PATCH: Deployment com múltiplas réplicas + anti-affinity
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
                                "key": "app",
                                "operator": "In",
                                "values": ["istio-ingressgateway"]
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
		"patch", "deployment", "istio-ingressgateway",
		"-n", "istio-system",
		"--type", "strategic",
		"--patch", deploymentPatch,
	}

	output, err := utils.RunCommand("kubectl", utils.ShortTimeout, patchArgs...)
	if err != nil {
		return fmt.Errorf("deployment patch failed: %s", output)
	}
	utils.Logf("   ✓ Deployment configured with %d replicas\n", replicas)

	// ✅ PodDisruptionBudget
	utils.Logf("   🛡️  Creating PodDisruptionBudget...\n")

	pdbYAML := fmt.Sprintf(`apiVersion: policy/v1
    kind: PodDisruptionBudget
    metadata:
      name: istio-ingressgateway
      namespace: istio-system
    spec:
      minAvailable: %d
      selector:
        matchLabels:
          app: istio-ingressgateway
    `, minAvailable)

	pdbFile, err := utils.CreateTempFile("istio-pdb-*.yaml", pdbYAML)
	if err != nil {
		return fmt.Errorf("failed to create PDB file: %w", err)
	}
	defer utils.RemoveFile(pdbFile)

	output, err = utils.RunCommand("kubectl", 30*time.Second, "apply", "-f", pdbFile)
	if err != nil {
		return fmt.Errorf("failed to apply PDB: %s", output)
	}
	utils.Logf("   ✓ PodDisruptionBudget created (minAvailable: %d)\n", minAvailable)

	utils.Logf("   ⏳ Waiting for Istio gateway pods to be distributed (20s)...\n")
	time.Sleep(20 * time.Second)

	// Verificar distribuição
	podDistOutput, err := utils.RunCommand("kubectl", 30*time.Second,
		"get", "pods", "-n", "istio-system",
		"-l", "app=istio-ingressgateway",
		"-o", "custom-columns=POD:metadata.name,NODE:spec.nodeName,STATUS:status.phase",
		"--no-headers")

	if err == nil && strings.TrimSpace(podDistOutput) != "" {
		utils.Logf("   📊 Istio gateway pod distribution:\n")
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
			utils.Logf("   ⚠️  Warning: All pods on same node\n")
		}
	}

	return nil
}

func detectIstioClusterTopology() (*IstioClusterTopology, error) {
	topology := &IstioClusterTopology{}

	output, err := utils.RunCommand("kubectl", 30*time.Second,
		"get", "nodes", "-l", "node-role.kubernetes.io/control-plane", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get control-plane nodes: %w", err)
	}
	topology.ControlPlaneCount = countIstioLines(output)

	output, err = utils.RunCommand("kubectl", 30*time.Second,
		"get", "nodes", "-l", "!node-role.kubernetes.io/control-plane", "--no-headers")
	if err != nil {
		return nil, fmt.Errorf("failed to get worker nodes: %w", err)
	}
	topology.WorkerCount = countIstioLines(output)

	topology.IsHA = topology.ControlPlaneCount >= 3

	return topology, nil
}

type IstioClusterTopology struct {
	ControlPlaneCount int
	WorkerCount       int
	IsHA              bool
}

func countIstioLines(output string) int {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func ensureIstioctl(version string) (string, error) {
	if path, err := exec.LookPath("istioctl"); err == nil {
		utils.Logf("   ✓ istioctl found at: %s\n", path)
		return path, nil
	}

	utils.Logf("   📥 istioctl not found, installing version %s...\n", version)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	installDir := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create install directory: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "istioctl-install-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	istioURL := fmt.Sprintf("https://github.com/istio/istio/releases/download/%s/istio-%s-%s-%s.tar.gz",
		version, version, goos, goarch)
	tarPath := filepath.Join(tempDir, "istio.tar.gz")

	utils.Logf("   📥 Downloading from: %s\n", istioURL)
	if output, err := utils.RunCommand("curl", 5*time.Minute, "-L", "-o", tarPath, istioURL); err != nil {
		return "", fmt.Errorf("failed to download Istio: %s", output)
	}

	utils.Logf("   📦 Extracting...\n")
	var tarArgs []string
	if runtime.GOOS == "darwin" {
		tarArgs = []string{"--no-xattrs", "-xzf", tarPath, "-C", tempDir}
	} else {
		tarArgs = []string{"-xzf", tarPath, "-C", tempDir}
	}

	if output, err := utils.RunCommand("tar", 2*time.Minute, tarArgs...); err != nil {
		return "", fmt.Errorf("failed to extract: %s", output)
	}

	istioctlSource := filepath.Join(tempDir, fmt.Sprintf("istio-%s", version), "bin", "istioctl")
	istioctlDest := filepath.Join(installDir, "istioctl")

	data, err := os.ReadFile(istioctlSource)
	if err != nil {
		return "", fmt.Errorf("failed to read istioctl: %w", err)
	}

	if err := os.WriteFile(istioctlDest, data, 0755); err != nil {
		return "", fmt.Errorf("failed to install istioctl: %w", err)
	}

	utils.Logf("   ✅ istioctl installed at: %s\n", istioctlDest)

	if _, err := exec.LookPath("istioctl"); err != nil {
		utils.Logf("   ⚠️  %s is not in PATH\n", installDir)
		utils.Logf("   Add to ~/.bashrc or ~/.zshrc:\n")
		utils.Logf("   export PATH=\"%s:$PATH\"\n", installDir)
	}

	return istioctlDest, nil
}

func RemoveIstio(confirm bool) error {
	istioctlPath, err := exec.LookPath("istioctl")
	if err != nil {
		return fmt.Errorf("istioctl not found")
	}

	utils.Logf("   🗑️  Using istioctl to uninstall...\n")

	// Remover PDB primeiro
	utils.RunCommand("kubectl", 30*time.Second, "delete", "pdb",
		"-n", "istio-system", "istio-ingressgateway", "--ignore-not-found=true")

	uninstallArgs := []string{"uninstall", "--purge"}
	if confirm {
		uninstallArgs = append(uninstallArgs, "--skip-confirmation")
	} else {
		uninstallArgs = append(uninstallArgs, "-y")
	}

	output, err := utils.RunCommand(istioctlPath, 5*time.Minute, uninstallArgs...)
	if err != nil {
		return fmt.Errorf("failed to uninstall: %s", output)
	}

	utils.Logf("   ✅ Istio removed\n")
	return nil
}
