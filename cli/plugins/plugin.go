package plugins

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	// execPath is what actually runs. For a signature-verified plugin it is
	// a private, content-addressed copy of the exact bytes that verified
	// (see stageVerifiedBinary); path stays the installed location shown to
	// the user and used to uninstall. Empty means path itself runs.
	execPath string
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

	cmd := exec.CommandContext(execCtx, p.execTarget(), args...) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	// Executable plugins inherit the environment minus ChatCLI's own
	// secrets (encryption key, JWT secret, auth dir, audit path).
	cmd.Env = utils.ScrubSecretEnv(os.Environ(), nil)

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
		readerBuf := bufio.NewReader(reader)
		buf := make([]byte, 4096)
		pending := make([]byte, 0, 4096)

		emit := func(line []byte, hasDelimiter bool) {
			if len(line) == 0 && !hasDelimiter {
				return
			}
			text := string(line)
			if onOutput != nil {
				prefix := ""
				if isError {
					prefix = "ERR: "
				}
				onOutput(prefix + text)
			}
			fullOutput.WriteString(text)
			if hasDelimiter {
				fullOutput.WriteString("\n")
			}
		}

		for {
			n, err := readerBuf.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)

				for {
					idx := bytes.IndexAny(pending, "\r\n")
					if idx < 0 {
						break
					}
					emit(pending[:idx], true)
					skip := 1
					if pending[idx] == '\r' && len(pending) > idx+1 && pending[idx+1] == '\n' {
						skip = 2
					}
					pending = pending[idx+skip:]
				}

				if len(pending) > 4096 {
					emit(pending, false)
					pending = pending[:0]
				}
			}
			if err != nil {
				if err == io.EOF && len(pending) > 0 {
					emit(pending, false)
				}
				break
			}
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

// execTarget is the file ExecuteWithStream runs.
func (p *ExecutablePlugin) execTarget() string {
	if p.execPath != "" {
		return p.execPath
	}
	return p.path
}

func NewPluginFromPath(path string) (Plugin, error) {
	p, err := newPluginFromExec(path, path)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// pluginProbeTimeout bounds each --metadata / --schema call. Both are
// expected to print a small JSON document and exit at once; a plugin that
// hangs must not stall the loader (or anything waiting on it).
var pluginProbeTimeout = 10 * time.Second

// maxProbeOutput bounds what a probe may print.
const maxProbeOutput = 1 << 20

// newPluginFromExec builds a plugin whose user-visible location is
// displayPath and whose probes and executions run execPath.
func newPluginFromExec(displayPath, execPath string) (*ExecutablePlugin, error) {
	info, err := os.Stat(execPath)
	if err != nil || info.IsDir() {
		return nil, fmt.Errorf("invalid path")
	}
	// (Mantém lógica original de validação Windows vs Unix)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("not executable")
	}

	output, err := probePlugin(execPath, "--metadata")
	if err != nil {
		return nil, err
	}

	var meta Metadata
	if err := json.Unmarshal(output, &meta); err != nil {
		return nil, err
	}

	var schemaStr string
	if out, err := probePlugin(execPath, "--schema"); err == nil {
		if json.Valid(out) {
			schemaStr = string(out)
		}
	}

	p := &ExecutablePlugin{
		metadata: meta,
		path:     displayPath,
		schema:   schemaStr,
	}
	if execPath != displayPath {
		p.execPath = execPath
	}
	return p, nil
}

// probePlugin runs one discovery call (--metadata, --schema) under the same
// rules as a real execution: ChatCLI's own secrets scrubbed from the
// environment, a hard deadline, and bounded output.
func probePlugin(execPath, flag string) ([]byte, error) {
	cmd := exec.Command(execPath, flag) //#nosec G204 -- execPath is a plugin binary admitted by the loader; flag is a fixed literal
	cmd.Env = utils.ScrubSecretEnv(os.Environ(), nil)
	// A plugin that forks a child holding stdout open must not keep the
	// probe alive once the plugin itself is gone.
	cmd.WaitDelay = time.Second
	out := &boundedBuffer{max: maxProbeOutput}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(pluginProbeTimeout, func() {
		timedOut.Store(true)
		_ = cmd.Process.Kill()
	})
	err := cmd.Wait()
	timer.Stop()
	if timedOut.Load() {
		return nil, fmt.Errorf("plugin %s %s timed out after %s", execPath, flag, pluginProbeTimeout)
	}
	if err != nil {
		return nil, err
	}
	if out.overflow {
		return nil, fmt.Errorf("plugin %s %s printed more than %d bytes", execPath, flag, maxProbeOutput)
	}
	return out.buf.Bytes(), nil
}

// boundedBuffer keeps at most max bytes and records that more arrived.
type boundedBuffer struct {
	buf      bytes.Buffer
	max      int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}
