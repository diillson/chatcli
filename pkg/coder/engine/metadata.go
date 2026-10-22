package engine

import (
	"encoding/json"
	"strconv"
)

// Metadata holds @coder plugin identification.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Usage       string `json:"usage"`
	Version     string `json:"version"`
}

// FlagDefinition describes a subcommand flag.
type FlagDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// SubcommandDefinition describes a plugin subcommand.
type SubcommandDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Flags       []FlagDefinition `json:"flags"`
	Examples    []string         `json:"examples,omitempty"`
}

// PluginSchema is the full plugin schema for LLM context.
type PluginSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	ArgsFormat  string                 `json:"args_format"`
	Subcommands []SubcommandDefinition `json:"subcommands"`
}

// PluginVersion is the version of the @coder tool surface the model sees:
// bumped whenever a subcommand, flag or its documented behavior changes
// (the builtin versions test enforces it). Version above is the engine's
// original contract constant and stays as it is for API compatibility.
const PluginVersion = "2.2.0"

// GetMetadata returns the @coder plugin metadata.
func GetMetadata() Metadata {
	return Metadata{
		Name:        "@coder",
		Description: "Full engineering suite (IO, search, exec, git, test, backup, rollback, patch).",
		Usage:       `@coder <read|stat|write|patch|multipatch|find|move|delete|tree|search|outline|map|checkpoint|exec|git-status|git-diff|git-log|git-changed|git-branch|git-show|git-blame|test|rollback|clean> [flags]`,
		Version:     PluginVersion,
	}
}

// GetMetadataJSON returns the metadata as a JSON string.
func GetMetadataJSON() string {
	data, err := json.Marshal(GetMetadata())
	if err != nil {
		return "{}"
	}
	return string(data)
}

// GetSchema returns the @coder plugin schema as a JSON string.
func GetSchema() string {
	schema := PluginSchema{
		Name:        "@coder",
		Description: "Engineering tools for reading, writing, patching, searching, running and git.",
		ArgsFormat:  "Aceita argumentos estilo CLI ou JSON (ex.: args=\"{\\\"cmd\\\":\\\"read\\\",\\\"args\\\":{\\\"file\\\":\\\"main.go\\\"}}\")",
		Subcommands: []SubcommandDefinition{
			{
				Name:        "read",
				Description: "Read one or more files with line numbers, a line range, head/tail and a byte limit.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File path.", Required: true},
					{Name: "--start", Type: "int", Description: "First line (1-based)."},
					{Name: "--end", Type: "int", Description: "Last line (1-based)."},
					{Name: "--head", Type: "int", Description: "First N lines (not with --tail)."},
					{Name: "--tail", Type: "int", Description: "Last N lines (not with --head)."},
					{Name: "--max-bytes", Type: "int", Default: strconv.Itoa(DefaultMaxBytes), Description: "Byte limit for the read."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64"},
				},
				Examples: []string{
					"read --file main.go --start 1 --end 120",
				},
			},
			{
				Name:        "checkpoint",
				Description: "Shadow-git snapshots of the workspace (taken automatically before write/patch/multipatch/exec): list, create and restore rewind the tree to any snapshot without touching the user's .git.",
				Flags: []FlagDefinition{
					{Name: "--list", Type: "bool", Description: "List the snapshots (default)."},
					{Name: "--create", Type: "bool", Description: "Take a snapshot now."},
					{Name: "--restore", Type: "string", Description: "Snapshot hash to restore the workspace to."},
					{Name: "--label", Type: "string", Default: "manual", Description: "Label for the snapshot taken with --create."},
				},
				Examples: []string{
					"checkpoint --list",
					"checkpoint --restore a1b2c3d",
				},
			},
			{
				Name:        "outline",
				Description: "Symbol skeleton of a file (declarations with their lines): go/ast for Go, patterns for other languages.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File path.", Required: true},
				},
				Examples: []string{
					"outline --file cli/cli.go",
				},
			},
			{
				Name:        "map",
				Description: "Structural map of the repository within a character budget: files ranked by structure, each with its declaration skeleton. Use it to orient yourself in a large codebase without reading whole files.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Root directory."},
					{Name: "--budget", Type: "int", Default: strconv.Itoa(DefaultMapBudget), Description: "Character budget of the map."},
				},
				Examples: []string{
					"map --dir . --budget 8000",
				},
			},
			{
				Name:        "write",
				Description: "Write a file (with a .bak backup; parent directories created), text or base64, optionally appending.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File path.", Required: true},
					{Name: "--content", Type: "string", Description: "Content to write.", Required: true},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64"},
					{Name: "--append", Type: "bool", Description: "Append to the end of the file instead of replacing it."},
				},
			},
			{
				Name:        "patch",
				Description: "Patch by search/replace or unified diff. The searched text must occur ONCE (ambiguous = error listing the lines; absent = error naming the closest line); an indentation-only difference is accepted and the replacement is re-indented.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File path (optional when the diff names its files)."},
					{Name: "--search", Type: "string", Description: "Text to replace (text/base64). Include context lines until it is unique in the file."},
					{Name: "--replace", Type: "string", Description: "Replacement (text/base64)."},
					{Name: "--all", Type: "bool", Default: "false", Description: "Replace EVERY occurrence (without it, a repeated text is refused)."},
					{Name: "--encoding", Type: "string", Default: "text", Description: "text|base64 (for search/replace)"},
					{Name: "--diff", Type: "string", Description: "Unified diff (text/base64)."},
					{Name: "--diff-encoding", Type: "string", Default: "text", Description: "text|base64"},
				},
			},
			{
				Name:        "multipatch",
				Description: "Apply several search/replace edits across several files as one atomic transaction (all-or-nothing rollback).",
				Flags: []FlagDefinition{
					{Name: "--edits", Type: "string", Required: true, Description: "JSON array of {file,search,replace,encoding?}. Every edit is validated before anything is written; a failure in any of them restores every touched file."},
				},
			},
			{
				Name:        "search",
				Description: "Search for text or a regex with context lines and limits.",
				Flags: []FlagDefinition{
					{Name: "--term", Type: "string", Description: "Text or regex.", Required: true},
					{Name: "--dir", Type: "string", Default: ".", Description: "Base directory."},
					{Name: "--regex", Type: "bool", Description: "Treat --term as a regex."},
					{Name: "--case-sensitive", Type: "bool", Description: "Case-sensitive match (default: false)."},
					{Name: "--context", Type: "int", Description: "Context lines."},
					{Name: "--max-results", Type: "int", Description: "Cap on matching lines (total)."},
					{Name: "--glob", Type: "string", Description: "Glob filter (e.g. *.go,*.md)."},
					{Name: "--max-bytes", Type: "int", Default: "1048576", Description: "Skip files larger than N bytes (fallback without rg)."},
				},
			},
			{
				Name:        "tree",
				Description: "List a directory tree within limits.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Base directory."},
					{Name: "--max-depth", Type: "int", Default: "6", Description: "Maximum depth."},
					{Name: "--max-entries", Type: "int", Default: strconv.Itoa(DefaultMaxEntries), Description: "Entry cap."},
					{Name: "--include-hidden", Type: "bool", Description: "Include hidden files."},
					{Name: "--ignore", Type: "string", Description: "Comma-separated exact names to skip."},
				},
			},
			{
				Name:        "exec",
				Description: "Run a shell command (dangerous commands are refused; sandboxed where available).",
				Flags: []FlagDefinition{
					{Name: "--cmd", Type: "string", Description: "Command to run.", Required: true},
					{Name: "--dir", Type: "string", Description: "Working directory."},
					{Name: "--timeout", Type: "int", Default: "600", Description: "Timeout in seconds."},
				},
			},
			{
				Name:        "git-status",
				Description: "Short git status.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
				},
			},
			{
				Name:        "git-diff",
				Description: "Git diff with options.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
					{Name: "--staged", Type: "bool", Description: "Staged diff."},
					{Name: "--name-only", Type: "bool", Description: "File names only."},
					{Name: "--stat", Type: "bool", Description: "Stat summary."},
					{Name: "--path", Type: "string", Description: "Filter by path."},
					{Name: "--context", Type: "int", Default: "3", Description: "Context lines."},
				},
			},
			{
				Name:        "git-log",
				Description: "Short git log.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
					{Name: "--limit", Type: "int", Default: "20", Description: "Number of commits."},
					{Name: "--path", Type: "string", Description: "Filter by path."},
				},
			},
			{
				Name:        "git-changed",
				Description: "List changed files (porcelain status).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
				},
			},
			{
				Name:        "git-branch",
				Description: "Current branch.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
				},
			},
			{
				Name:        "stat",
				Description: "Size, line count, text/binary, line endings and modification time of one or more files, so a read can be sized before it is paid for.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File path (several paths may follow).", Required: true},
				},
			},
			{
				Name:        "git-show",
				Description: "Show one commit (message and patch, or --stat) or a file as it was at a ref (--path).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
					{Name: "--ref", Type: "string", Default: "HEAD", Description: "Commit, tag or branch."},
					{Name: "--path", Type: "string", Description: "File to show as it was at the ref (no patch)."},
					{Name: "--stat", Type: "bool", Description: "Only the summary of touched files."},
				},
			},
			{
				Name:        "git-blame",
				Description: "Author, commit and date of every line of a file (or of a --start/--end range).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Repository directory."},
					{Name: "--file", Type: "string", Description: "File.", Required: true},
					{Name: "--start", Type: "int", Description: "First line (1-based)."},
					{Name: "--end", Type: "int", Description: "Last line (1-based)."},
				},
			},
			{
				Name:        "find",
				Description: "List files by NAME (glob) or by a path substring, which search does not do; skips .git, node_modules, vendor and hidden entries.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Base directory."},
					{Name: "--pattern", Type: "string", Description: "File-name glob, e.g. *_test.go, Dockerfile*."},
					{Name: "--contains", Type: "string", Description: "Case-insensitive substring of the relative path."},
					{Name: "--limit", Type: "int", Default: "200", Description: "Maximum results."},
					{Name: "--dirs", Type: "bool", Description: "Include directories in the result."},
				},
			},
			{
				Name:        "move",
				Description: "Move or rename a file inside the workspace (.bak backup of the source; an existing destination is refused without --force).",
				Flags: []FlagDefinition{
					{Name: "--from", Type: "string", Description: "Current path.", Required: true},
					{Name: "--to", Type: "string", Description: "New path (directories created).", Required: true},
					{Name: "--force", Type: "bool", Description: "Replace the destination if it exists."},
				},
			},
			{
				Name:        "delete",
				Description: "Remove a file (never a directory) leaving a .bak beside it; rollback restores it.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File to remove.", Required: true},
				},
			},
			{
				Name:        "test",
				Description: "Run the tests, detecting the stack or via --cmd.",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Base directory."},
					{Name: "--cmd", Type: "string", Description: "Custom test command."},
					{Name: "--timeout", Type: "int", Default: "1800", Description: "Timeout in seconds."},
				},
			},
			{
				Name:        "rollback",
				Description: "Restore a file from its .bak backup.",
				Flags: []FlagDefinition{
					{Name: "--file", Type: "string", Description: "File path.", Required: true},
				},
			},
			{
				Name:        "clean",
				Description: "Remove .bak backups (dry-run by default).",
				Flags: []FlagDefinition{
					{Name: "--dir", Type: "string", Default: ".", Description: "Base directory."},
					{Name: "--force", Type: "bool", Description: "Apply the cleanup (delete the files)."},
					{Name: "--pattern", Type: "string", Default: "*.bak", Description: "File pattern."},
				},
			},
		},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return "{}"
	}
	return string(data)
}
