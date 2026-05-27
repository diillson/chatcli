/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package lsp

import (
	"os"
	"path/filepath"
	"strings"
)

// ServerSpec describes how to launch a language server and the LSP
// languageId to use for documents it serves.
type ServerSpec struct {
	Command    []string
	LanguageID string
	EnvKey     string // env var that overrides Command (space-separated)
}

// extensionServers maps file extensions to their default language server.
// Commands are the conventional stdio invocations; users can override any of
// them via the EnvKey (e.g. CHATCLI_LSP_GO_CMD="gopls -rpc.trace").
var extensionServers = map[string]ServerSpec{
	".go":   {Command: []string{"gopls"}, LanguageID: "go", EnvKey: "CHATCLI_LSP_GO_CMD"},
	".py":   {Command: []string{"pyright-langserver", "--stdio"}, LanguageID: "python", EnvKey: "CHATCLI_LSP_PYTHON_CMD"},
	".ts":   {Command: []string{"typescript-language-server", "--stdio"}, LanguageID: "typescript", EnvKey: "CHATCLI_LSP_TS_CMD"},
	".tsx":  {Command: []string{"typescript-language-server", "--stdio"}, LanguageID: "typescriptreact", EnvKey: "CHATCLI_LSP_TS_CMD"},
	".js":   {Command: []string{"typescript-language-server", "--stdio"}, LanguageID: "javascript", EnvKey: "CHATCLI_LSP_TS_CMD"},
	".jsx":  {Command: []string{"typescript-language-server", "--stdio"}, LanguageID: "javascriptreact", EnvKey: "CHATCLI_LSP_TS_CMD"},
	".rs":   {Command: []string{"rust-analyzer"}, LanguageID: "rust", EnvKey: "CHATCLI_LSP_RUST_CMD"},
	".c":    {Command: []string{"clangd"}, LanguageID: "c", EnvKey: "CHATCLI_LSP_C_CMD"},
	".h":    {Command: []string{"clangd"}, LanguageID: "c", EnvKey: "CHATCLI_LSP_C_CMD"},
	".cpp":  {Command: []string{"clangd"}, LanguageID: "cpp", EnvKey: "CHATCLI_LSP_CPP_CMD"},
	".cc":   {Command: []string{"clangd"}, LanguageID: "cpp", EnvKey: "CHATCLI_LSP_CPP_CMD"},
	".java": {Command: []string{"jdtls"}, LanguageID: "java", EnvKey: "CHATCLI_LSP_JAVA_CMD"},
	".rb":   {Command: []string{"solargraph", "stdio"}, LanguageID: "ruby", EnvKey: "CHATCLI_LSP_RUBY_CMD"},
}

// ServerForFile returns the server spec for a file based on its extension,
// applying any env override of the command.
func ServerForFile(path string) (ServerSpec, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	spec, ok := extensionServers[ext]
	if !ok {
		return ServerSpec{}, false
	}
	if spec.EnvKey != "" {
		if override := strings.TrimSpace(os.Getenv(spec.EnvKey)); override != "" {
			spec.Command = strings.Fields(override)
		}
	}
	return spec, true
}

// SupportedExtensions lists the recognized file extensions (for help/UX).
func SupportedExtensions() []string {
	exts := make([]string, 0, len(extensionServers))
	for e := range extensionServers {
		exts = append(exts, e)
	}
	return exts
}
