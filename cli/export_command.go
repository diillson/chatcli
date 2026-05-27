/*
 * ChatCLI - export_command.go
 *
 * /export [path] writes the current conversation as a ShareGPT-style JSONL
 * trajectory (training-data format). Provider-agnostic: it serializes the
 * unified history regardless of which provider produced the turns. Defaults
 * to ~/.chatcli/exports/trajectory-<timestamp>.jsonl.
 */
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/trajectory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
)

func (cli *ChatCLI) handleExportCommand(input string) {
	if len(cli.history) == 0 {
		fmt.Println(colorize("  "+i18n.T("export.empty"), ColorGray))
		return
	}

	path := strings.TrimSpace(strings.TrimPrefix(input, "/export"))
	if path == "" {
		dir, err := defaultExportDir()
		if err != nil {
			fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
			return
		}
		path = filepath.Join(dir, fmt.Sprintf("trajectory-%s.jsonl", time.Now().Format("20060102-150405")))
	}
	if expanded, err := utils.ExpandPath(path); err == nil {
		path = expanded
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}

	f, err := os.Create(path) //#nosec G304 -- user-provided export path
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	defer func() { _ = f.Close() }()

	n, err := trajectory.WriteJSONL(f, cli.history)
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}

	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("export.done", n, path))
}

func defaultExportDir() (string, error) {
	home, err := utils.GetHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chatcli", "exports"), nil
}
