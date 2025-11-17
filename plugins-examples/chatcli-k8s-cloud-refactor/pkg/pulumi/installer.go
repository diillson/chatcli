package pulumi

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/diillson/plugins-examples/chatcli-k8s-cloud/pkg/logger"
)

const (
	pulumiVersion = "v3.131.0"
	downloadURL   = "https://get.pulumi.com/releases/sdk"
)

// EnsurePulumiInstalled garante que Pulumi CLI está disponível
func EnsurePulumiInstalled(ctx context.Context) (string, error) {
	// 1. Verificar se já está no PATH
	if path, err := exec.LookPath("pulumi"); err == nil {
		logger.Debugf("✅ Pulumi encontrado no PATH: %s", path)
		return path, nil
	}

	// 2. Verificar no diretório local do plugin
	localPath := getPulumiLocalPath()
	if _, err := os.Stat(localPath); err == nil {
		logger.Debugf("✅ Pulumi encontrado localmente: %s", localPath)
		return localPath, nil
	}

	// 3. Baixar e instalar automaticamente
	logger.Info("📦 Pulumi CLI não encontrado. Baixando automaticamente...")
	return downloadPulumi(ctx)
}

// getPulumiLocalPath retorna o caminho local onde Pulumi será instalado
func getPulumiLocalPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".k8s-cloud", "bin", "pulumi")
}

// downloadPulumi baixa e instala Pulumi CLI
func downloadPulumi(ctx context.Context) (string, error) {
	// Determinar OS e arquitetura
	osName := runtime.GOOS
	arch := runtime.GOARCH

	// Mapear arquitetura Go para Pulumi
	pulumiArch := arch
	if arch == "amd64" {
		pulumiArch = "x64"
	} else if arch == "arm64" {
		pulumiArch = "arm64"
	}

	// URL do download
	// Formato: https://get.pulumi.com/releases/sdk/pulumi-v3.131.0-darwin-arm64.tar.gz
	fileName := fmt.Sprintf("pulumi-%s-%s-%s.tar.gz", pulumiVersion, osName, pulumiArch)
	url := fmt.Sprintf("%s/%s", downloadURL, fileName)

	logger.Infof("📥 Baixando Pulumi %s para %s/%s...", pulumiVersion, osName, arch)
	logger.Debugf("   URL: %s", url)

	// Criar diretório temporário
	tmpDir, err := os.MkdirTemp("", "pulumi-download-*")
	if err != nil {
		return "", fmt.Errorf("erro ao criar diretório temporário: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Baixar arquivo
	tarPath := filepath.Join(tmpDir, fileName)
	if err := downloadFile(ctx, url, tarPath); err != nil {
		return "", fmt.Errorf("erro ao baixar Pulumi: %w", err)
	}

	logger.Progress("📦 Extraindo Pulumi...")

	// Extrair tar.gz
	extractDir := filepath.Join(tmpDir, "extracted")
	if err := extractTarGz(tarPath, extractDir); err != nil {
		return "", fmt.Errorf("erro ao extrair Pulumi: %w", err)
	}

	// Mover para diretório local
	installDir := filepath.Join(os.Getenv("HOME"), ".k8s-cloud", "bin")
	if err := os.MkdirAll(installDir, 0755); err != nil {
		return "", fmt.Errorf("erro ao criar diretório de instalação: %w", err)
	}

	// Encontrar binário do Pulumi (geralmente em pulumi/bin/pulumi)
	pulumiBin := filepath.Join(extractDir, "pulumi", "pulumi")
	if runtime.GOOS == "windows" {
		pulumiBin += ".exe"
	}

	// Verificar se binário existe
	if _, err := os.Stat(pulumiBin); err != nil {
		return "", fmt.Errorf("binário Pulumi não encontrado em: %s", pulumiBin)
	}

	// Copiar para diretório de instalação
	destPath := filepath.Join(installDir, "pulumi")
	if runtime.GOOS == "windows" {
		destPath += ".exe"
	}

	if err := copyFile(pulumiBin, destPath); err != nil {
		return "", fmt.Errorf("erro ao copiar binário: %w", err)
	}

	// Tornar executável (Unix)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(destPath, 0755); err != nil {
			return "", fmt.Errorf("erro ao tornar executável: %w", err)
		}
	}

	logger.Successf("✅ Pulumi instalado em: %s", destPath)
	logger.Infof("   Versão: %s", pulumiVersion)

	return destPath, nil
}

// downloadFile baixa um arquivo via HTTP
func downloadFile(ctx context.Context, url, filepath string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("erro HTTP: %d %s", resp.StatusCode, resp.Status)
	}

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Copiar com progresso
	_, err = io.Copy(out, resp.Body)
	return err
}

// extractTarGz extrai arquivo .tar.gz
func extractTarGz(tarPath, destDir string) error {
	file, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			// Criar diretórios pai
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}

			outFile, err := os.Create(target)
			if err != nil {
				return err
			}

			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()

			// Preservar permissões
			if err := os.Chmod(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		}
	}

	return nil
}

// copyFile copia um arquivo
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// GetPulumiPath retorna o caminho do Pulumi CLI
func GetPulumiPath(ctx context.Context) (string, error) {
	return EnsurePulumiInstalled(ctx)
}

// InstallLanguagePlugins instala plugins de linguagem necessários
func InstallLanguagePlugins(ctx context.Context, pulumiPath string) error {
	logger.Progress("📦 Instalando plugins de linguagem Pulumi...")

	// Plugin Go
	if err := installGoPlugin(ctx, pulumiPath); err != nil {
		return fmt.Errorf("erro ao instalar plugin Go: %w", err)
	}

	logger.Success("✅ Plugins de linguagem instalados")
	return nil
}

// installGoPlugin instala o plugin de linguagem Go
func installGoPlugin(ctx context.Context, pulumiPath string) error {
	logger.Debug("Instalando pulumi-language-go...")

	// Executar: pulumi plugin install language go
	cmd := exec.CommandContext(ctx, pulumiPath, "plugin", "install", "language", "go")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("falha ao instalar plugin Go: %w", err)
	}

	logger.Debug("  ✓ pulumi-language-go instalado")
	return nil
}
