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

// extractSentinelJSON tenta extrair JSON de blocos sentinela do @coder.
// Retorna (jsonString, true) quando encontra; senão ("", false).
func extractSentinelJSON(output string) (string, bool) {
	s := stripANSI(output)

	// suportar dois tipos
	patterns := []struct {
		start string
		end   string
	}{
		{"<<<CHATCLI_EXEC_RESULT_JSON>>>", "<<<END_CHATCLI_EXEC_RESULT_JSON>>>"},
		{"<<<CHATCLI_EXECSCRIPT_RESULT_JSON>>>", "<<<END_CHATCLI_EXECSCRIPT_RESULT_JSON>>>"},
	}

	for _, p := range patterns {
		startIdx := strings.Index(s, p.start)
		if startIdx == -1 {
			continue
		}
		endIdx := strings.Index(s, p.end)
		if endIdx == -1 || endIdx <= startIdx {
			continue
		}

		mid := s[startIdx+len(p.start) : endIdx]
		mid = strings.TrimSpace(mid)

		// valida se é JSON (pode ter \n)
		if json.Valid([]byte(mid)) {
			return mid, true
		}

		// às vezes o json vem com newline extra no final; tenta limpar de novo
		mid2 := strings.TrimSpace(strings.Trim(mid, "\uFEFF"))
		if json.Valid([]byte(mid2)) {
			return mid2, true
		}
	}
	return "", false
}

// Execute invoca o binário do plugin, captura sua saída e trata erros.
// Melhorias:
// - stdin fechado por padrão (evita travas por input)
// - watchdog de silêncio (avisa usuário)
// - se houver sentinela JSON (@coder), retorna apenas o JSON (melhor para o loop ReAct)
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

	// 1.1 stdin FECHADO por padrão (evita travar esperando input)
	cmd.Stdin = nil

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

	// Watchdog de silêncio (configurável)
	silenceWarnAfter := 2 * time.Minute
	if v := utils.GetEnvOrDefault("CHATCLI_AGENT_PLUGIN_SILENCE_WARN", ""); v != "" {
		if d, derr := time.ParseDuration(v); derr == nil && d > 0 {
			silenceWarnAfter = d
		}
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	// Timestamp do último output (stdout/stderr)
	var lastMu sync.Mutex
	lastOutputAt := time.Now()
	touch := func() {
		lastMu.Lock()
		lastOutputAt = time.Now()
		lastMu.Unlock()
	}
	getLast := func() time.Time {
		lastMu.Lock()
		defer lastMu.Unlock()
		return lastOutputAt
	}

	// Watchdog goroutine
	watchdogDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		warned := false

		for {
			select {
			case <-watchdogDone:
				return
			case <-execCtx.Done():
				return
			case <-ticker.C:
				if warned {
					continue
				}
				if time.Since(getLast()) >= silenceWarnAfter {
					// Aviso humano (vai aparecer na timeline do tool result também)
					fmt.Fprintf(os.Stderr, "\n⚠️  Plugin '%s' sem saída há %s. Se parecer travado, pressione Ctrl+C para cancelar.\n",
						p.Name(), silenceWarnAfter)
					_ = os.Stderr.Sync()
					warned = true
				}
			}
		}
	}()

	// Captura Stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		// copia e marca atividade
		tee := io.TeeReader(stdoutPipe, &stdoutBuf)
		buf := make([]byte, 8192)
		for {
			n, rerr := tee.Read(buf)
			if n > 0 {
				touch()
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Captura Stderr
	wg.Add(1)
	go func() {
		defer wg.Done()
		tee := io.TeeReader(stderrPipe, &stderrBuf)
		buf := make([]byte, 8192)
		for {
			n, rerr := tee.Read(buf)
			if n > 0 {
				touch()
			}
			if rerr != nil {
				return
			}
		}
	}()

	// 5. Aguardar I/O terminar
	wg.Wait()
	close(watchdogDone)

	// 6. Aguardar o comando finalizar
	err = cmd.Wait()

	// Preparar as saídas limpas (sem ANSI codes)
	stdoutStr := stripANSI(stdoutBuf.String())
	stderrStr := stripANSI(stderrBuf.String())

	// Se o plugin é @coder e emitiu JSON sentinela, priorizar retorno “limpo”
	if js, ok := extractSentinelJSON(stdoutStr); ok {
		// Em caso de erro, ainda retornamos JSON + erro? Melhor:
		// - retornar JSON como output (para IA)
		// - e erro separado (para o ChatCLI mostrar/registrar)
		if err != nil {
			// Se timeout:
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				return js, fmt.Errorf("plugin '%s' excedeu timeout (%v)", p.Name(), timeout)
			}
			return js, fmt.Errorf("plugin '%s' falhou: %v", p.Name(), err)
		}
		return js, nil
	}

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

		if strings.TrimSpace(stdoutStr) != "" {
			errMsgBuilder.WriteString("\n--- SAÍDA PADRÃO (STDOUT) ---\n")
			errMsgBuilder.WriteString(stdoutStr)
			errMsgBuilder.WriteString("\n-----------------------------\n")
		}

		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			errMsgBuilder.WriteString(fmt.Sprintf("\nNota: A execução excedeu o tempo limite de %v.", timeout))
		}

		return "", fmt.Errorf("%s", errMsgBuilder.String())
	}

	// sucesso: retorna stdout
	return stdoutStr, nil
}

// NewPluginFromPath valida um arquivo executável e o carrega como um plugin.
func NewPluginFromPath(path string) (Plugin, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("caminho '%s' não é um arquivo válido", path)
	}
	if info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("plugin '%s' não possui permissão de execução", path)
	}

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

	var schemaStr string
	schemaCmd := exec.Command(path, "--schema")
	if schemaOutput, err := schemaCmd.Output(); err == nil {
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
