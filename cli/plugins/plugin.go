package plugins

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/utils"
)

type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

type Plugin interface {
	Name() string
	Description() string
	Usage() string
	Version() string
	Path() string
	Schema() string
	Execute(ctx context.Context, args []string) (string, error)
	ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error)
}

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

//func stripANSI(str string) string {
//	const ansi = "[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))"
//	re := regexp.MustCompile(ansi)
//	return re.ReplaceAllString(str, "")
//}

// Execute mantém compatibilidade
func (p *ExecutablePlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream é a implementação real com callback
func (p *ExecutablePlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	timeoutStr := utils.GetEnvOrDefault("CHATCLI_AGENT_PLUGIN_TIMEOUT", "")
	timeout := config.DefaultPluginTimeout
	if timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil && d > 0 {
			timeout = d
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, p.path, args...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("falha ao iniciar plugin: %w", err)
	}

	var fullOutput strings.Builder
	var wg sync.WaitGroup

	// Helper para ler streams em paralelo
	readStream := func(reader io.Reader, isError bool) {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 5*1024*1024)

		for scanner.Scan() {
			line := scanner.Text()

			// Envia para o callback se existir (UI)
			if onOutput != nil {
				prefix := ""
				if isError {
					prefix = "ERR: "
				}
				onOutput(prefix + line)
			}

			// Acumula para o histórico da LLM
			fullOutput.WriteString(line + "\n")
		}
	}

	wg.Add(2)
	go readStream(stdoutPipe, false)
	go readStream(stderrPipe, true)

	wg.Wait()
	err = cmd.Wait()

	if ctx.Err() == context.Canceled {
		return "", context.Canceled
	}

	if err != nil {
		return fullOutput.String(), fmt.Errorf("plugin execution failed: %w", err)
	}

	return fullOutput.String(), nil
}

func NewPluginFromPath(path string) (Plugin, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("invalid path")
	}
	// (Mantém lógica original de validação Windows vs Unix)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("not executable")
	}

	cmd := exec.Command(path, "--metadata")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var meta Metadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, err
	}

	var schemaStr string
	schemaCmd := exec.Command(path, "--schema")
	if out, err := schemaCmd.Output(); err == nil {
		if json.Valid(out) {
			schemaStr = string(out)
		}
	}

	return &ExecutablePlugin{
		metadata: meta,
		path:     path,
		schema:   schemaStr,
	}, nil
}
