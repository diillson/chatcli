package installer

import (
	"fmt"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

func InstallCertManager(version string) error {
	utils.Logf("   📦 Installing Cert-Manager %s...\n", version)
	manifestURL := fmt.Sprintf("https://github.com/cert-manager/cert-manager/releases/download/%s/cert-manager.yaml", version)
	if output, err := utils.RunCommand("kubectl", 3*time.Minute, "apply", "-f", manifestURL); err != nil {
		return fmt.Errorf("failed to apply manifests: %s", output)
	}

	utils.Logf("   ⏳ Waiting for Cert-Manager to be ready...\n")
	waitArgs := []string{
		"wait", "--namespace", "cert-manager", "--for=condition=ready", "pod",
		"--selector=app.kubernetes.io/instance=cert-manager", "--timeout=5m",
	}
	if output, err := utils.RunCommand("kubectl", 6*time.Minute, waitArgs...); err != nil {
		return fmt.Errorf("timeout waiting for Cert-Manager pods. Output: %s", output)
	}

	utils.Logf("   ✓ Cert-Manager is ready\n")
	return nil
}

func RemoveCertManager() error {
	utils.Logf("   🗑️  Removing Cert-Manager manifests...\n")
	// Usamos o manifesto da última versão estável, pois a URL da versão exata pode não estar disponível
	manifestURL := "https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml"
	output, err := utils.RunCommand("kubectl", 2*time.Minute, "delete", "-f", manifestURL, "--ignore-not-found=true")
	if err != nil {
		return fmt.Errorf("failed to delete Cert-Manager manifests: %s", output)
	}
	return nil
}
