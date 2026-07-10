/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * Package cmd — mcpconfig.go
 *
 * Implements `chatcli mcp {add|list|get|remove}`: shell-level management of
 * the MCP client configuration (~/.chatcli/mcp_servers.json), mirroring the
 * `claude mcp add` developer experience:
 *
 *   chatcli mcp add <name> [--env K=V]... [--cwd DIR] -- <command> [args...]
 *   chatcli mcp add --transport sse  <name> <url> [--header 'K: V']...
 *   chatcli mcp add --transport http <name> <url> [--header 'K: V']...
 *   chatcli mcp list
 *   chatcli mcp get <name>
 *   chatcli mcp remove <name>
 *
 * The file is round-tripped through mcp.ServerConfig, whose JSON codec
 * preserves unknown keys — hand-maintained extensions survive an add/remove.
 */
package cmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/cli/mcp"
	"github.com/diillson/chatcli/i18n"
)

// mcpConfigFile is the on-disk wrapper shape shared with mcp.Manager.LoadConfig.
type mcpConfigFile struct {
	Servers []mcp.ServerConfig `json:"mcpServers"`
}

// RunMCPConfig dispatches the `chatcli mcp <verb>` subcommands.
func RunMCPConfig(args []string) error {
	if len(args) == 0 {
		printMCPUsage()
		return nil
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "add":
		return mcpAdd(rest)
	case "list", "ls":
		return mcpList()
	case "get":
		return mcpGet(rest)
	case "remove", "rm":
		return mcpRemove(rest)
	case "help", "-h", "--help":
		printMCPUsage()
		return nil
	default:
		printMCPUsage()
		return fmt.Errorf("%s", i18n.T("mcpcfg.unknown_verb", verb))
	}
}

func printMCPUsage() {
	fmt.Println(`Usage:
  chatcli mcp add <name> [flags] -- <command> [args...]   add a stdio server
  chatcli mcp add --transport sse|http <name> <url>       add a remote server
  chatcli mcp list                                        list configured servers
  chatcli mcp get <name>                                  show one server's config
  chatcli mcp remove <name>                               remove a server

Flags for add:
  --transport stdio|sse|http   transport (default: stdio)
  --env KEY=VALUE              environment variable (repeatable, stdio)
  --header 'Key: Value'        HTTP header (repeatable, sse/http)
  --cwd DIR                    working directory (stdio)
  --description TEXT           human description shown in /mcp status
  --disabled                   add the server disabled (enabled by default)

Examples:
  chatcli mcp add filesystem -- npx -y @modelcontextprotocol/server-filesystem ~/Documents
  chatcli mcp add github --env GITHUB_TOKEN=ghp_xxx -- npx -y @modelcontextprotocol/server-github
  chatcli mcp add --transport sse linear https://mcp.linear.app/sse
  chatcli mcp add chatcli-nested -- chatcli mcp-server`)
}

// repeatable implements flag.Value for repeatable string flags.
type repeatable []string

func (r *repeatable) String() string { return strings.Join(*r, ",") }
func (r *repeatable) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func mcpAdd(args []string) error {
	// Everything after a literal "--" is the stdio command line and must
	// not be parsed as flags (mirrors `claude mcp add`).
	var cmdline []string
	for i, a := range args {
		if a == "--" {
			cmdline = args[i+1:]
			args = args[:i]
			break
		}
	}

	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	transport := fs.String("transport", "stdio", "transport: stdio|sse|http")
	cwd := fs.String("cwd", "", "working directory (stdio)")
	description := fs.String("description", "", "human description")
	disabled := fs.Bool("disabled", false, "add the server disabled")
	var envs, headers repeatable
	fs.Var(&envs, "env", "KEY=VALUE (repeatable)")
	fs.Var(&headers, "header", "'Key: Value' (repeatable)")
	// Iterative parse so flags may appear before or after positionals
	// (stdlib flag stops at the first non-flag token, but the natural CLI
	// shape is `mcp add <name> --env K=V -- cmd`).
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	if len(pos) < 1 {
		return fmt.Errorf("%s", i18n.T("mcpcfg.add.name_required"))
	}
	name := pos[0]

	cfg := mcp.ServerConfig{
		Name:        name,
		Enabled:     !*disabled,
		Description: *description,
	}

	switch strings.ToLower(*transport) {
	case "stdio", "":
		cfg.Transport = mcp.TransportStdio
		if len(cmdline) == 0 {
			return fmt.Errorf("%s", i18n.T("mcpcfg.add.command_required"))
		}
		cfg.Command = cmdline[0]
		cfg.Args = cmdline[1:]
		cfg.Cwd = *cwd
		if len(envs) > 0 {
			cfg.Env = map[string]string{}
			for _, kv := range envs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("%s", i18n.T("mcpcfg.add.bad_env", kv))
				}
				cfg.Env[k] = v
			}
		}
	case "sse", "http":
		if strings.ToLower(*transport) == "sse" {
			cfg.Transport = mcp.TransportSSE
		} else {
			cfg.Transport = mcp.TransportStreamableHTTP
		}
		if len(pos) < 2 || !strings.HasPrefix(pos[1], "http") {
			return fmt.Errorf("%s", i18n.T("mcpcfg.add.url_required", *transport))
		}
		cfg.URL = pos[1]
		if len(headers) > 0 {
			cfg.Headers = map[string]string{}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok || strings.TrimSpace(k) == "" {
					return fmt.Errorf("%s", i18n.T("mcpcfg.add.bad_header", h))
				}
				cfg.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	default:
		return fmt.Errorf("%s", i18n.T("mcpcfg.add.bad_transport", *transport))
	}

	file, err := loadMCPConfigFile()
	if err != nil {
		return err
	}
	replaced := false
	for i := range file.Servers {
		if file.Servers[i].Name == name {
			file.Servers[i] = cfg
			replaced = true
			break
		}
	}
	if !replaced {
		file.Servers = append(file.Servers, cfg)
	}
	if err := saveMCPConfigFile(file); err != nil {
		return err
	}
	if replaced {
		fmt.Println(i18n.T("mcpcfg.add.updated", name, mcp.DefaultConfigPath()))
	} else {
		fmt.Println(i18n.T("mcpcfg.add.added", name, mcp.DefaultConfigPath()))
	}
	return nil
}

func mcpList() error {
	file, err := loadMCPConfigFile()
	if err != nil {
		return err
	}
	if len(file.Servers) == 0 {
		fmt.Println(i18n.T("mcpcfg.list.empty"))
		return nil
	}
	for _, s := range file.Servers {
		state := "enabled"
		if !s.Enabled {
			state = "disabled"
		}
		target := s.Command
		if s.URL != "" {
			target = s.URL
		}
		if len(s.Args) > 0 {
			target += " " + strings.Join(s.Args, " ")
		}
		fmt.Printf("%-20s %-6s %-8s %s\n", s.Name, s.Transport, state, target)
	}
	return nil
}

func mcpGet(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("mcpcfg.add.name_required"))
	}
	file, err := loadMCPConfigFile()
	if err != nil {
		return err
	}
	for _, s := range file.Servers {
		if s.Name == args[0] {
			out, merr := json.MarshalIndent(s, "", "  ")
			if merr != nil {
				return merr
			}
			fmt.Println(string(out))
			return nil
		}
	}
	return fmt.Errorf("%s", i18n.T("mcpcfg.not_found", args[0]))
}

func mcpRemove(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("%s", i18n.T("mcpcfg.add.name_required"))
	}
	file, err := loadMCPConfigFile()
	if err != nil {
		return err
	}
	kept := file.Servers[:0]
	found := false
	for _, s := range file.Servers {
		if s.Name == args[0] {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return fmt.Errorf("%s", i18n.T("mcpcfg.not_found", args[0]))
	}
	file.Servers = kept
	if err := saveMCPConfigFile(file); err != nil {
		return err
	}
	fmt.Println(i18n.T("mcpcfg.removed", args[0]))
	return nil
}

func loadMCPConfigFile() (*mcpConfigFile, error) {
	path := mcp.DefaultConfigPath()
	data, err := os.ReadFile(path) //#nosec G304 -- fixed, user-owned config path
	if err != nil {
		if os.IsNotExist(err) {
			return &mcpConfigFile{}, nil
		}
		return nil, fmt.Errorf("%s: %w", i18n.T("mcpcfg.read_failed", path), err)
	}
	var file mcpConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("mcpcfg.parse_failed", path), err)
	}
	return &file, nil
}

// saveMCPConfigFile writes atomically (tmp + rename) so a crash mid-write
// never truncates the user's MCP configuration.
func saveMCPConfigFile(file *mcpConfigFile) error {
	path := mcp.DefaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
