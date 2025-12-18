package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
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

// stripANSI remove códigos de escape ANSI de uma string para facilitar a leitura pela IA.
func stripANSI(str string) string {
	const ansi = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"
	re := regexp.MustCompile(ansi)
	return re.ReplaceAllString(str, "")
}

// Execute invoca o binário do plugin, captura sua saída e trata erros.
func (p *ExecutablePlugin) Execute(ctx context.Context, args []string) (string, error) {
	// 1. Obter timeout configurável
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

	// 2. Obter os pipes de stdout e stderr
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("falha ao criar stdout pipe para o plugin: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("falha ao criar stderr pipe para o plugin: %w", err)
	}

	// 3. Iniciar o comando
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("falha ao iniciar o plugin '%s': %w", p.Name(), err)
	}

	// 4. Capturar stdout e stderr em buffers separados
	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	// Captura Stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutPipe)
	}()

	// Captura Stderr (e também envia para o console para feedback visual ao usuário)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrPipe)
	}()

	// 5. Aguardar as goroutines de I/O terminarem
	wg.Wait()

	// 6. Aguardar o comando finalizar
	err = cmd.Wait()

	// Preparar as saídas limpas (sem ANSI codes)
	stdoutStr := stripANSI(stdoutBuf.String())
	stderrStr := stripANSI(stderrBuf.String())

	if err != nil {
		// Se houver erro (exit code != 0 ou timeout), construímos uma mensagem rica para a IA
		var errMsgBuilder strings.Builder
		errMsgBuilder.WriteString(fmt.Sprintf("O plugin '%s' falhou na execução (Erro: %v).\n", p.Name(), err))

		if strings.TrimSpace(stderrStr) != "" {
			errMsgBuilder.WriteString("\n--- SAÍDA DE ERRO (STDERR) ---\n")
			errMsgBuilder.WriteString(stderrStr)
			errMsgBuilder.WriteString("\n------------------------------\n")
		} else {
			errMsgBuilder.WriteString("\n(Nenhuma saída de erro capturada no stderr)\n")
		}

		// Às vezes ferramentas CLI escrevem erros no stdout, então incluímos também se houver
		if strings.TrimSpace(stdoutStr) != "" {
			errMsgBuilder.WriteString("\n--- SAÍDA PADRÃO (STDOUT) ---\n")
			errMsgBuilder.WriteString(stdoutStr)
			errMsgBuilder.WriteString("\n-----------------------------\n")
		}

		// Verificar se foi timeout
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			errMsgBuilder.WriteString(fmt.Sprintf("\nNota: A execução excedeu o tempo limite de %v.", timeout))
		}

		return "", fmt.Errorf("%s", errMsgBuilder.String())
	}

	// 7. Retornar o conteúdo capturado de stdout como resultado em caso de sucesso
	return stdoutStr, nil
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

	// Tenta obter o schema (opcional)
	var schemaStr string
	schemaCmd := exec.Command(path, "--schema")
	if schemaOutput, err := schemaCmd.Output(); err == nil {
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
