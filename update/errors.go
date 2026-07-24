/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"errors"
	"fmt"
)

// Erros tipados devolvidos pelo pacote. As mensagens são técnicas (inglês,
// não traduzidas) porque o consumidor de user-facing é a camada cli, que
// mapeia cada tipo para a chave i18n adequada.
var (
	// ErrManualMethod: o canal detectado não suporta atualização iniciada
	// pelo ChatCLI (Docker, build local, desconhecido).
	ErrManualMethod = errors.New("update: install method requires manual update")
	// ErrNoUpdate: já está na versão mais recente.
	ErrNoUpdate = errors.New("update: already up to date")
)

// MissingToolError: a ferramenta do canal (brew/go) não está no PATH.
type MissingToolError struct {
	Tool string
}

func (e *MissingToolError) Error() string {
	return fmt.Sprintf("update: required tool %q not found in PATH", e.Tool)
}

// UnsupportedPlatformError: a release não publica asset para este GOOS/GOARCH
// (hoje: linux/amd64, darwin/amd64, darwin/arm64, windows/amd64).
type UnsupportedPlatformError struct {
	OS   string
	Arch string
}

func (e *UnsupportedPlatformError) Error() string {
	return fmt.Sprintf("update: no release asset for %s/%s", e.OS, e.Arch)
}

// NotWritableError: o diretório do binário não é gravável pelo processo —
// a camada cli degrada para instruções com sudo.
type NotWritableError struct {
	Dir string
}

func (e *NotWritableError) Error() string {
	return fmt.Sprintf("update: binary directory %q is not writable", e.Dir)
}

// ChecksumsUnavailableError: a release alvo não publica checksums.txt
// (releases anteriores à introdução do asset). Sem verificação não há
// instalação: o self-replace aborta.
type ChecksumsUnavailableError struct {
	Tag string
}

func (e *ChecksumsUnavailableError) Error() string {
	return fmt.Sprintf("update: release %s does not publish checksums.txt; refusing unverified install", e.Tag)
}

// ChecksumMismatchError: o SHA-256 do asset baixado não bate com o publicado.
type ChecksumMismatchError struct {
	Asset    string
	Expected string
	Actual   string
}

func (e *ChecksumMismatchError) Error() string {
	return fmt.Sprintf("update: checksum mismatch for %s: expected %s, got %s", e.Asset, e.Expected, e.Actual)
}
