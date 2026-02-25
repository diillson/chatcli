package plugins

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

// BuiltinCoderPlugin adapts the engine package to the Plugin interface,
// providing @coder functionality without requiring an external binary.
type BuiltinCoderPlugin struct {
	meta   engine.Metadata
	schema string
}

// NewBuiltinCoderPlugin creates a builtin @coder plugin backed by the engine package.
func NewBuiltinCoderPlugin() *BuiltinCoderPlugin {
	return &BuiltinCoderPlugin{
		meta:   engine.GetMetadata(),
		schema: engine.GetSchema(),
	}
}

func (p *BuiltinCoderPlugin) Name() string        { return p.meta.Name }
func (p *BuiltinCoderPlugin) Description() string { return p.meta.Description }
func (p *BuiltinCoderPlugin) Usage() string       { return p.meta.Usage }
func (p *BuiltinCoderPlugin) Version() string     { return p.meta.Version }
func (p *BuiltinCoderPlugin) Path() string        { return "[builtin]" }
func (p *BuiltinCoderPlugin) Schema() string      { return p.schema }

// Execute runs without streaming — collects all output and returns.
func (p *BuiltinCoderPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the engine and streams output line-by-line via onOutput.
func (p *BuiltinCoderPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("subcommand required")
	}

	subcmd := args[0]
	var subArgs []string
	if len(args) > 1 {
		subArgs = args[1:]
	}

	var fullOutput strings.Builder
	var mu sync.Mutex

	emit := func(line string, isError bool) {
		mu.Lock()
		defer mu.Unlock()
		if onOutput != nil {
			prefix := ""
			if isError {
				prefix = "ERR: "
			}
			onOutput(prefix + line)
		}
		fullOutput.WriteString(line)
		fullOutput.WriteString("\n")
	}

	outWriter := engine.NewStreamWriter(func(line string) {
		emit(line, false)
	})
	errWriter := engine.NewStreamWriter(func(line string) {
		emit(line, true)
	})

	eng := engine.NewEngine(outWriter, errWriter)
	err := eng.Execute(ctx, subcmd, subArgs)

	outWriter.Flush()
	errWriter.Flush()

	output := fullOutput.String()
	if err != nil {
		return output, fmt.Errorf("plugin execution failed: %w", err)
	}
	return output, nil
}
