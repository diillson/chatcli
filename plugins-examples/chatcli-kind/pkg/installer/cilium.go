package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/diillson/chatcli/plugins-examples/chatcli-kind/pkg/utils"
)

type CiliumOptions struct {
	Version              string
	EnableHubble         bool
	KubeProxyReplacement bool
}

func InstallCilium(opts CiliumOptions) error {
	ciliumPath, err := ensureCiliumCLI(opts.Version)
	if err != nil {
		return fmt.Errorf("failed to ensure cilium CLI: %w", err)
	}

	utils.Logf("   📦 Installing Cilium %s...\n", opts.Version)

	installArgs := []string{"install", "--version", opts.Version}

	if opts.KubeProxyReplacement {
		installArgs = append(installArgs, "--set", "kubeProxyReplacement=strict")
		utils.Logf("   🔧 Enabling kube-proxy replacement\n")
	}

	if opts.EnableHubble {
		installArgs = append(installArgs,
			"--set", "hubble.relay.enabled=true",
			"--set", "hubble.ui.enabled=true",
		)
		utils.Logf("   🔭 Enabling Hubble observability\n")
	}

	output, err := utils.RunCommand(ciliumPath, 5*time.Minute, installArgs...)
	if err != nil {
		return fmt.Errorf("failed to install Cilium: %s", output)
	}

	utils.Logf("   ⏳ Waiting for Cilium to be ready...\n")

	statusArgs := []string{"status", "--wait"}
	if output, err := utils.RunCommand(ciliumPath, 5*time.Minute, statusArgs...); err != nil {
		utils.Logf("   ⚠️  Cilium status check failed: %s\n", output)
		return fmt.Errorf("Cilium is not ready: %s", output)
	}

	utils.Logf("   ✓ Cilium is ready\n")

	if opts.EnableHubble {
		utils.Logf("   🔭 Enabling Hubble UI port-forward...\n")
		utils.Logf("   💡 Run: cilium hubble port-forward &\n")
		utils.Logf("   💡 Access Hubble UI at: http://localhost:12000\n")
	}

	return nil
}

func ensureCiliumCLI(version string) (string, error) {
	if path, err := exec.LookPath("cilium"); err == nil {
		utils.Logf("   ✓ cilium CLI found at: %s\n", path)
		return path, nil
	}

	utils.Logf("   📥 cilium CLI not found, installing version %s...\n", version)

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	installDir := filepath.Join(homeDir, ".local", "bin")
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create install directory: %w", err)
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH

	if goarch == "amd64" {
		goarch = "amd64"
	} else if goarch == "arm64" {
		goarch = "arm64"
	}

	ciliumURL := fmt.Sprintf("https://github.com/cilium/cilium-cli/releases/download/v%s/cilium-%s-%s.tar.gz",
		version, goos, goarch)

	tempDir, err := os.MkdirTemp("", "cilium-install-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	tarPath := filepath.Join(tempDir, "cilium.tar.gz")

	utils.Logf("   📥 Downloading from: %s\n", ciliumURL)
	if output, err := utils.RunCommand("curl", 3*time.Minute, "-L", "-o", tarPath, ciliumURL); err != nil {
		return "", fmt.Errorf("failed to download Cilium CLI: %s", output)
	}

	utils.Logf("   📦 Extracting...\n")
	if output, err := utils.RunCommand("tar", 1*time.Minute, "-xzf", tarPath, "-C", tempDir); err != nil {
		return "", fmt.Errorf("failed to extract: %s", output)
	}

	ciliumBinary := filepath.Join(tempDir, "cilium")
	ciliumDest := filepath.Join(installDir, "cilium")

	data, err := os.ReadFile(ciliumBinary)
	if err != nil {
		return "", fmt.Errorf("failed to read cilium binary: %w", err)
	}

	if err := os.WriteFile(ciliumDest, data, 0755); err != nil {
		return "", fmt.Errorf("failed to install cilium: %w", err)
	}

	utils.Logf("   ✅ cilium CLI installed at: %s\n", ciliumDest)

	if _, err := exec.LookPath("cilium"); err != nil {
		utils.Logf("   ⚠️  %s is not in PATH\n", installDir)
		utils.Logf("   Add to ~/.bashrc or ~/.zshrc:\n")
		utils.Logf("   export PATH=\"%s:$PATH\"\n", installDir)
	}

	return ciliumDest, nil
}

func RemoveCilium(confirm bool) error {
	ciliumPath, err := exec.LookPath("cilium")
	if err != nil {
		return fmt.Errorf("cilium CLI not found, cannot uninstall")
	}
	utils.Logf("   🗑️  Using cilium CLI to uninstall...\n")

	uninstallArgs := []string{"uninstall"}
	if !confirm {
		// Cilium uninstall não tem flag de confirmação, ele já pergunta
		utils.Logf("   ⚠️  Cilium CLI may ask for confirmation.\n")
	}

	output, err := utils.RunCommand(ciliumPath, 5*time.Minute, uninstallArgs...)
	if err != nil {
		return fmt.Errorf("failed to run cilium uninstall: %s", output)
	}
	return nil
}
