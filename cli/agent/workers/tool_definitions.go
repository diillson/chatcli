package workers

import (
	"fmt"

	"github.com/diillson/chatcli/models"
)

// CoderToolDefinitions returns native tool definitions for providers that support function calling.
// Each coder subcommand becomes a separate tool with proper JSON schema.
// When using native tools, content is passed as plain text (no base64 needed).
func CoderToolDefinitions(allowedCmds []string) []models.ToolDefinition {
	allTools := map[string]models.ToolDefinition{
		"read": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "read_file",
				Description: "Read file contents with optional line range. Always read before editing.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file": map[string]interface{}{
							"type":        "string",
							"description": "File path to read",
						},
						"start": map[string]interface{}{
							"type":        "integer",
							"description": "Start line (1-based, optional)",
						},
						"end": map[string]interface{}{
							"type":        "integer",
							"description": "End line (1-based, optional)",
						},
						"head": map[string]interface{}{
							"type":        "integer",
							"description": "Read first N lines (optional)",
						},
						"tail": map[string]interface{}{
							"type":        "integer",
							"description": "Read last N lines (optional)",
						},
					},
					"required": []string{"file"},
				},
			},
		},
		"write": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "write_file",
				Description: "Create or overwrite a file. Content is plain text (no encoding needed). Creates parent directories automatically.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file": map[string]interface{}{
							"type":        "string",
							"description": "File path to write",
						},
						"content": map[string]interface{}{
							"type":        "string",
							"description": "File content as plain text",
						},
						"append": map[string]interface{}{
							"type":        "boolean",
							"description": "Append to file instead of overwriting (optional)",
						},
					},
					"required": []string{"file", "content"},
				},
			},
		},
		"patch": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "patch_file",
				Description: "Apply search/replace to a file. The search string must be unique in the file. Always read the file first.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file": map[string]interface{}{
							"type":        "string",
							"description": "File path to patch",
						},
						"search": map[string]interface{}{
							"type":        "string",
							"description": "Exact text to find (must be unique in file)",
						},
						"replace": map[string]interface{}{
							"type":        "string",
							"description": "Replacement text",
						},
					},
					"required": []string{"file", "search", "replace"},
				},
			},
		},
		"tree": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "list_directory",
				Description: "List directory tree structure with depth control.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Directory path (default: current dir)",
						},
						"max_depth": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum depth (default: 6)",
						},
						"include_hidden": map[string]interface{}{
							"type":        "boolean",
							"description": "Include hidden files",
						},
					},
				},
			},
		},
		"search": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "search_files",
				Description: "Search for text or regex patterns in files.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"term": map[string]interface{}{
							"type":        "string",
							"description": "Search term or regex pattern",
						},
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Directory to search in (default: current dir)",
						},
						"regex": map[string]interface{}{
							"type":        "boolean",
							"description": "Interpret term as regex",
						},
						"glob": map[string]interface{}{
							"type":        "string",
							"description": "File glob filter (e.g. *.go)",
						},
						"max_results": map[string]interface{}{
							"type":        "integer",
							"description": "Maximum number of results",
						},
						"context": map[string]interface{}{
							"type":        "integer",
							"description": "Lines of context around matches",
						},
					},
					"required": []string{"term"},
				},
			},
		},
		"exec": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "run_command",
				Description: "Execute a shell command. Dangerous commands are blocked unless explicitly allowed.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"cmd": map[string]interface{}{
							"type":        "string",
							"description": "Shell command to execute",
						},
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Working directory (optional)",
						},
						"timeout": map[string]interface{}{
							"type":        "integer",
							"description": "Timeout in seconds (default: 600)",
						},
					},
					"required": []string{"cmd"},
				},
			},
		},
		"git-status": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "git_status",
				Description: "Show git status summary.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Repository directory (default: current dir)",
						},
					},
				},
			},
		},
		"git-diff": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "git_diff",
				Description: "Show git diff with options.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Repository directory",
						},
						"staged": map[string]interface{}{
							"type":        "boolean",
							"description": "Show staged changes",
						},
						"name_only": map[string]interface{}{
							"type":        "boolean",
							"description": "Show only file names",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Filter by path",
						},
					},
				},
			},
		},
		"git-log": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "git_log",
				Description: "Show git log.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Repository directory",
						},
						"limit": map[string]interface{}{
							"type":        "integer",
							"description": "Number of commits (default: 20)",
						},
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Filter by path",
						},
					},
				},
			},
		},
		"git-changed": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "git_changed",
				Description: "List changed files (porcelain format).",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Repository directory",
						},
					},
				},
			},
		},
		"git-branch": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "git_branch",
				Description: "Show current git branch.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Repository directory",
						},
					},
				},
			},
		},
		"test": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "run_tests",
				Description: "Run tests (auto-detects Go/Node/Python/Rust or use custom command).",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Project directory",
						},
						"cmd": map[string]interface{}{
							"type":        "string",
							"description": "Custom test command (optional)",
						},
						"timeout": map[string]interface{}{
							"type":        "integer",
							"description": "Timeout in seconds (default: 1800)",
						},
					},
				},
			},
		},
		"rollback": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "rollback_file",
				Description: "Restore a file from its .bak backup.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file": map[string]interface{}{
							"type":        "string",
							"description": "File path to restore",
						},
					},
					"required": []string{"file"},
				},
			},
		},
		"clean": {
			Type: "function",
			Function: models.ToolFunctionDef{
				Name:        "clean_backups",
				Description: "Remove .bak backup files.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"dir": map[string]interface{}{
							"type":        "string",
							"description": "Directory to clean",
						},
						"force": map[string]interface{}{
							"type":        "boolean",
							"description": "Actually delete (default: dry-run)",
						},
					},
				},
			},
		},
	}

	// nativeToolToSubcmd maps native tool function names back to engine subcommands.
	// This is used in the reverse direction by NativeToolNameToSubcmd.
	if len(allowedCmds) == 0 {
		// Return all tools
		result := make([]models.ToolDefinition, 0, len(allTools))
		for _, td := range allTools {
			result = append(result, td)
		}
		return result
	}

	result := make([]models.ToolDefinition, 0, len(allowedCmds))
	for _, cmd := range allowedCmds {
		if td, ok := allTools[cmd]; ok {
			result = append(result, td)
		}
	}
	return result
}

// nativeToolNameMap maps native function names to engine subcommand names.
var nativeToolNameMap = map[string]string{
	"read_file":      "read",
	"write_file":     "write",
	"patch_file":     "patch",
	"list_directory": "tree",
	"search_files":   "search",
	"run_command":    "exec",
	"git_status":     "git-status",
	"git_diff":       "git-diff",
	"git_log":        "git-log",
	"git_changed":    "git-changed",
	"git_branch":     "git-branch",
	"run_tests":      "test",
	"rollback_file":  "rollback",
	"clean_backups":  "clean",
}

// NativeToolNameToSubcmd converts a native tool function name to an engine subcommand.
func NativeToolNameToSubcmd(toolName string) (string, bool) {
	// Direct match
	if subcmd, ok := nativeToolNameMap[toolName]; ok {
		return subcmd, true
	}
	// Try as-is (might already be a subcommand name)
	return toolName, false
}

// NativeToolArgsToFlags converts structured tool call arguments to CLI-style flags
// for the coder engine. This is the bridge between native function calling and
// the existing engine.Execute(cmd, args) interface.
func NativeToolArgsToFlags(subcmd string, args map[string]interface{}) []string {
	var flags []string

	// Map of arg name aliases to canonical flag names (per subcmd)
	argToFlag := map[string]string{
		"file":           "--file",
		"content":        "--content",
		"search":         "--search",
		"replace":        "--replace",
		"dir":            "--dir",
		"cmd":            "--cmd",
		"term":           "--term",
		"start":          "--start",
		"end":            "--end",
		"head":           "--head",
		"tail":           "--tail",
		"max_depth":      "--max-depth",
		"maxdepth":       "--max-depth",
		"max_results":    "--max-results",
		"include_hidden": "--include-hidden",
		"context":        "--context",
		"timeout":        "--timeout",
		"regex":          "--regex",
		"glob":           "--glob",
		"staged":         "--staged",
		"name_only":      "--name-only",
		"stat":           "--stat",
		"path":           "--path",
		"limit":          "--limit",
		"force":          "--force",
		"pattern":        "--pattern",
		"append":         "--append",
		"allow_unsafe":   "--allow-unsafe",
		"allow_sudo":     "--allow-sudo",
		"case_sensitive": "--case-sensitive",
		"encoding":       "--encoding",
		"diff":           "--diff",
		"diff_encoding":  "--diff-encoding",
		"max_bytes":      "--max-bytes",
	}

	for key, val := range args {
		flagName, ok := argToFlag[key]
		if !ok {
			flagName = "--" + key
		}

		switch v := val.(type) {
		case bool:
			if v {
				flags = append(flags, flagName)
			}
		case string:
			if v != "" {
				flags = append(flags, flagName, v)
			}
		case float64:
			flags = append(flags, flagName, fmt.Sprintf("%g", v))
		default:
			flags = append(flags, flagName, fmt.Sprintf("%v", v))
		}
	}

	// For write/patch with native tools, content is always plain text (no base64)
	if subcmd == "write" || subcmd == "patch" {
		hasEncoding := false
		for _, f := range flags {
			if f == "--encoding" {
				hasEncoding = true
				break
			}
		}
		if !hasEncoding {
			flags = append(flags, "--encoding", "text")
		}
	}

	return flags
}
