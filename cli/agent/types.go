/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package agent

// SourceType define o tipo de origem do comando
type SourceType int

const (
	SourceTypeUserInput SourceType = iota
	SourceTypeFile
	SourceTypeCommandOutput
)

// CommandContextInfo contém metadados sobre a origem e natureza de um comando
type CommandContextInfo struct {
	SourceType    SourceType
	FileExtension string
	IsScript      bool
	ScriptType    string // shell, python, etc.
}

// CommandBlock representa um bloco de comandos executáveis
type CommandBlock struct {
	Description string
	Commands    []string
	Language    string
	ContextInfo CommandContextInfo
}

// CommandOutput representa o resultado da execução de um comando
type CommandOutput struct {
	CommandBlock CommandBlock
	Output       string
	ErrorMsg     string
}
