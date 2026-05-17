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

// IsReadOnly reports whether the @coder subcommand is read-only for the
// given args. Only `read`, `search`, and `tree` are pure reads — every
// other subcommand (`exec`, `write`, `patch`, `test`) mutates state.
// We base the decision on the first arg (subcommand), which is the
// stable contract of the @coder schema.
func (p *BuiltinCoderPlugin) IsReadOnly(args []string) bool {
	sub := coderSubcommand(args)
	switch sub {
	case "read", "search", "tree", "list", "stat":
		return true
	}
	return false
}

// IsConcurrencySafe mirrors IsReadOnly: read/search/tree on independent
// paths can run in parallel because they only allocate goroutine-local
// state on top of the OS filesystem syscalls. Mutating subcommands stay
// in the serial bucket so an `exec` and a `write` in the same batch
// never race against each other.
func (p *BuiltinCoderPlugin) IsConcurrencySafe(args []string) bool {
	return p.IsReadOnly(args)
}

// DescribeCall surfaces what @coder is about to do: which subcommand,
// against which target. For `exec` we show the command; for `read` we
// show the file; for `search` we show the term. Anything else falls
// back to the static description.
func (p *BuiltinCoderPlugin) DescribeCall(args []string) string {
	sub := coderSubcommand(args)
	switch sub {
	case "read":
		if f := extractPathArg(args); f != "" {
			return fmt.Sprintf("Reading: %s", f)
		}
	case "search":
		if t := extractStringArg(args, "term", "pattern", "query"); t != "" {
			return fmt.Sprintf("Searching: %s", t)
		}
	case "exec":
		// Use extractNestedArg so we read args.cmd (the user-supplied
		// command), not the outer cmd field (which is just the subcommand
		// name "exec" and would collide here).
		if c := extractNestedArg(args, "cmd", "command"); c != "" {
			if len(c) > 60 {
				c = c[:60] + "..."
			}
			return fmt.Sprintf("Executing: %s", c)
		}
	case "write":
		if f := extractPathArg(args); f != "" {
			return fmt.Sprintf("Writing: %s", f)
		}
	case "patch":
		if f := extractPathArg(args); f != "" {
			return fmt.Sprintf("Patching: %s", f)
		}
	case "tree":
		if d := extractStringArg(args, "dir", "path"); d != "" {
			return fmt.Sprintf("Listing: %s", d)
		}
	}
	if sub != "" {
		return fmt.Sprintf("@coder %s", sub)
	}
	return p.Description()
}

// coderSubcommand extracts the @coder subcommand from the first arg,
// handling both the JSON envelope ({"cmd":"read","args":{…}}) and the
// positional form (`read --file foo`).
func coderSubcommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	first := strings.TrimSpace(args[0])
	if strings.HasPrefix(first, "{") {
		if v := extractStringArg([]string{first}, "cmd", "command"); v != "" {
			return v
		}
	}
	return first
}

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

	eng := engine.NewEngine(outWriter, errWriter, "")
	err := eng.Execute(ctx, subcmd, subArgs)

	outWriter.Flush()
	errWriter.Flush()

	output := fullOutput.String()
	if err != nil {
		return output, fmt.Errorf("plugin execution failed: %w", err)
	}
	return output, nil
}
