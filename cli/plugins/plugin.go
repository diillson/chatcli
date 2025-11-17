package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/utils"
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
	Schema() string
	Execute(ctx context.Context, args []string) (string, error)
}

// ExecutablePlugin é a implementação concreta para binários externos.
type ExecutablePlugin struct {
	metadata Metadata
	path     string
	schema   string
}

func (p *ExecutablePlugin) Name() string        { return p.metadata.Name }
func (p *ExecutablePlugin) Description() string { return p.metadata.Description }
func (p *ExecutablePlugin) Usage() string       { return p.metadata.Usage }
func (p *ExecutablePlugin) Version() string     { return p.metadata.Version }
func (p *ExecutablePlugin) Path() string        { return p.path }
func (p *ExecutablePlugin) Schema() string      { return p.schema }

// Execute invoca o binário do plugin, captura sua saída e trata erros.
func (p *ExecutablePlugin) Execute(ctx context.Context, args []string) (string, error) {
	// 1. Obter timeout configurável (da sua implementação anterior)
	timeoutStr := utils.GetEnvOrDefault("CHATCLI_AGENT_PLUGIN_TIMEOUT", "")
	timeout := config.DefaultPluginTimeout // Padrão de 15 minutos

	if timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			timeout = d
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, p.path, args...)

	// 2. Obter os pipes de stdout e stderr em vez de usar CombinedOutput
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("falha ao criar stdout pipe para o plugin: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("falha ao criar stderr pipe para o plugin: %w", err)
	}

	// 3. Iniciar o comando de forma não-bloqueante
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("falha ao iniciar o plugin '%s': %w", p.Name(), err)
	}

	// 4. Capturar stdout (resultado final para a IA) em um buffer
	var stdoutBuf bytes.Buffer

	// 5. Ler e exibir stderr (logs de progresso) em tempo real em uma goroutine
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Copia o stderr do plugin diretamente para o stderr do chatcli
		_, _ = io.Copy(os.Stderr, stderrPipe)
	}()

	_, _ = io.Copy(&stdoutBuf, stdoutPipe)

	// 6. Aguardar a goroutine de stderr terminar
	wg.Wait()

	// 7. Aguardar o comando do plugin finalizar e capturar seu erro de saída
	err = cmd.Wait()
	finalOutput := stdoutBuf.String()

	if err != nil {
		// Se o erro foi de timeout, a mensagem será mais clara
		if execCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("plugin '%s' falhou: timeout de %v excedido. O progresso foi exibido acima", p.Name(), timeout)
		}
		// Para outros erros, inclua a saída final (se houver) para depuração
		return "", fmt.Errorf("plugin '%s' falhou com erro: %v. Saída final (stdout):\n%s", p.Name(), err, finalOutput)
	}

	// 8. Retornar o conteúdo capturado de stdout como resultado
	return finalOutput, nil
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

	schemaCmd := exec.Command(path, "--schema")
	schemaOutput, err := schemaCmd.Output()
	var schemaStr string
	if err == nil {
		// Validar se é um JSON válido antes de armazenar
		if json.Valid(schemaOutput) {
			schemaStr = string(schemaOutput)
		}
	}

	return &ExecutablePlugin{
		metadata: meta,
		path:     path,
		schema:   schemaStr,
	}, nil
}
