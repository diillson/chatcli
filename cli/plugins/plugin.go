package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Metadata é a estrutura de descoberta que todo plugin DEVE fornecer via --metadata.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// Plugin define a interface para qualquer plugin executável pelo ChatCLI.
type Plugin interface {
	Name() string
	Description() string
	Usage() string
	Version() string
	Path() string // Expõe o caminho do executável para inspeção.
	Execute(ctx context.Context, args []string) (string, error)
}

// ExecutablePlugin é a implementação concreta para binários externos.
type ExecutablePlugin struct {
	metadata Metadata
	path     string
}

func (p *ExecutablePlugin) Name() string        { return p.metadata.Name }
func (p *ExecutablePlugin) Description() string { return p.metadata.Description }
func (p *ExecutablePlugin) Usage() string       { return p.metadata.Usage }
func (p *ExecutablePlugin) Version() string     { return p.metadata.Version }
func (p *ExecutablePlugin) Path() string        { return p.path }

// Execute invoca o binário do plugin, captura sua saída e trata erros.
func (p *ExecutablePlugin) Execute(ctx context.Context, args []string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, 45*time.Second) // Timeout de 45s para plugins.
	defer cancel()

	cmd := exec.CommandContext(execCtx, p.path, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("plugin '%s' falhou com erro: %s\nSaída (stderr):\n%s", p.Name(), err, string(output))
	}
	return string(output), nil
}

// NewPluginFromPath valida um arquivo executável e o carrega como um plugin.
func NewPluginFromPath(path string) (Plugin, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("caminho '%s' não é um arquivo válido", path)
	}
	// Verificação de permissão de execução (cross-platform).
	if info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("plugin '%s' não possui permissão de execução", path)
	}

	// Valida o contrato: executa com --metadata e espera um JSON válido.
	cmd := exec.Command(path, "--metadata")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("plugin em '%s' não respondeu ao contrato --metadata: %w", path, err)
	}

	var meta Metadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, fmt.Errorf("metadados do plugin em '%s' não são um JSON válido: %w", path, err)
	}

	if meta.Name == "" || meta.Description == "" || meta.Usage == "" {
		return nil, fmt.Errorf("metadados do plugin em '%s' estão incompletos (name, description, usage são obrigatórios)", path)
	}

	return &ExecutablePlugin{
		metadata: meta,
		path:     path,
	}, nil
}
