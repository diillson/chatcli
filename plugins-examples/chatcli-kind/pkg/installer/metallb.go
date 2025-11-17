package installer

import (
	"fmt"
	"runtime"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

func InstallMetalLB(addressPool string, skipWarning bool) error {
	// Aviso para macOS
	if runtime.GOOS == "darwin" && !skipWarning {
		utils.Logf("   ⚠️  AVISO: MetalLB no macOS com Docker Desktop não é acessível do host.\n")
		utils.Logf("   💡 Para automação, use a flag --skip-metallb-warning no comando 'create'.\n")
	}

	utils.Logf("   📦 Applying MetalLB manifests...\n")

	manifestURL := "https://raw.githubusercontent.com/metallb/metallb/v0.13.12/config/manifests/metallb-native.yaml"

	if output, err := utils.RunCommand("kubectl", 2*time.Minute, "apply", "-f", manifestURL); err != nil {
		return fmt.Errorf("failed to apply manifests: %s", output)
	}

	utils.Logf("   ⏳ Waiting for MetalLB controller...\n")
	time.Sleep(10 * time.Second)

	if !utils.WaitForPodsReady("metallb-system", "app=metallb", 3*time.Minute, 5*time.Second) {
		return fmt.Errorf("timeout waiting for MetalLB pods")
	}

	utils.Logf("   🔧 Configuring IP address pool...\n")

	ipPoolConfig := fmt.Sprintf(`apiVersion: metallb.io/v1beta1
    kind: IPAddressPool
    metadata:
      name: default-pool
      namespace: metallb-system
    spec:
      addresses:
      - %s
    ---
    apiVersion: metallb.io/v1beta1
    kind: L2Advertisement
    metadata:
      name: default-l2
      namespace: metallb-system
    spec:
      ipAddressPools:
      - default-pool
    `, addressPool)

	tempFile, err := utils.CreateTempFile("metallb-config-*.yaml", ipPoolConfig)
	if err != nil {
		return fmt.Errorf("failed to create config file: %w", err)
	}
	defer utils.RemoveFile(tempFile)

	if output, err := utils.RunCommand("kubectl", 30*time.Second, "apply", "-f", tempFile); err != nil {
		return fmt.Errorf("failed to apply IP pool config: %s", output)
	}

	utils.Logf("   ✓ MetalLB configured with address pool: %s\n", addressPool)

	if runtime.GOOS == "darwin" {
		utils.Logf("\n   📌 LEMBRE-SE (macOS):\n")
		utils.Logf("   • IPs do LoadBalancer funcionam APENAS dentro do cluster\n")
		utils.Logf("   • Para acessar do macOS, use:\n")
		utils.Logf("     kubectl port-forward svc/<nome> 8080:80\n")
		utils.Logf("     curl http://localhost:8080\n")
	}

	return nil
}

func RemoveMetalLB() error {
	utils.Logf("   🗑️  Removing MetalLB manifests...\n")
	manifestURL := "https://raw.githubusercontent.com/metallb/metallb/v0.13.12/config/manifests/metallb-native.yaml"
	output, err := utils.RunCommand("kubectl", 2*time.Minute, "delete", "-f", manifestURL, "--ignore-not-found=true")
	if err != nil {
		return fmt.Errorf("failed to delete MetalLB manifests: %s", output)
	}
	return nil
}
