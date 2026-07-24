/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package update implementa o auto-update do ChatCLI: detecta por qual canal
// o binário foi instalado (Homebrew, go install, binário oficial da release,
// Docker ou build local) e aplica a atualização pelo MESMO canal — nunca por
// outro. Builds locais e containers nunca são tocados: para eles o pacote
// devolve erros tipados que a camada de CLI converte em instruções manuais.
package update

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/diillson/chatcli/version"
)

// Method identifica o canal de instalação detectado para o binário em
// execução. A estratégia de atualização é derivada exclusivamente dele.
type Method int

const (
	// MethodUnknown indica que nenhum sinal permitiu classificar o binário
	// (ex.: build info indisponível). Tratado como manual: nunca auto-update.
	MethodUnknown Method = iota
	// MethodHomebrew indica binário instalado pelo tap diillson/chatcli.
	MethodHomebrew
	// MethodGoInstall indica binário compilado pelo module proxy via
	// `go install github.com/diillson/chatcli@<versão>`.
	MethodGoInstall
	// MethodReleaseBinary indica o binário oficial baixado da release do
	// GitHub (estampado com ldflags pelo CI) — atualizável por self-replace.
	MethodReleaseBinary
	// MethodDocker indica execução dentro de container; o update correto é
	// puxar a imagem nova, nunca trocar o binário do filesystem efêmero.
	MethodDocker
	// MethodSourceBuild indica build local (go build/go run de um checkout).
	// Nunca auto-atualizar: o binário pode carregar mudanças locais.
	MethodSourceBuild
)

// String devolve o identificador estável (não traduzido) do método — para
// logs e para a camada de exibição escolher a chave i18n correspondente.
func (m Method) String() string {
	switch m {
	case MethodHomebrew:
		return "homebrew"
	case MethodGoInstall:
		return "go-install"
	case MethodReleaseBinary:
		return "release-binary"
	case MethodDocker:
		return "docker"
	case MethodSourceBuild:
		return "source-build"
	default:
		return "unknown"
	}
}

// Automatable informa se o canal suporta atualização iniciada pelo ChatCLI
// (seja interativa via /update, seja em background no modo auto).
func (m Method) Automatable() bool {
	switch m {
	case MethodHomebrew, MethodGoInstall, MethodReleaseBinary:
		return true
	default:
		return false
	}
}

// AutoApplicable informa se o canal pode ser atualizado silenciosamente em
// background (CHATCLI_AUTO_UPDATE=auto). Homebrew fica de fora por decisão de
// produto: `brew upgrade` dispara o auto-update do brew inteiro e pega lock
// global — só roda quando o usuário pede explicitamente via /update.
func (m Method) AutoApplicable() bool {
	return m == MethodGoInstall || m == MethodReleaseBinary
}

// Info é o resultado da detecção: o canal e o caminho real do executável
// (symlinks resolvidos), que o self-replace usa como alvo.
type Info struct {
	Method   Method
	ExecPath string
}

// Seams injetáveis para testes — a detecção depende só deles, nunca de
// os.* / debug.* diretamente.
var (
	executableFn    = os.Executable
	evalSymlinksFn  = filepath.EvalSymlinks
	readBuildInfoFn = debug.ReadBuildInfo
	// ldflagsVersionFn lê a global crua estampada (ou não) pelo CI — de
	// propósito NÃO usa version.GetBuildInfo(), que preenche a versão a
	// partir do module info e apagaria o sinal "este build tem ldflags".
	ldflagsVersionFn = func() string { return version.Version }
	fileExistsFn     = func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
)

// Detect classifica o binário em execução. A ordem dos testes importa:
//  1. Homebrew primeiro — o binário do brew é o MESMO asset da release
//     (também estampado com ldflags), distinguível apenas pelo Cellar no path.
//  2. Container — mesmo um binário estampado não deve se auto-substituir num
//     filesystem efêmero.
//  3. ldflags estampados — só o CI de release estampa version.Version.
//  4. Module info — go install carrega a versão do módulo sem ldflags.
//  5. Resto — build local ((devel)) ou inclassificável.
func Detect() Info {
	info := Info{Method: MethodUnknown}

	if p, err := executableFn(); err == nil {
		info.ExecPath = p
		if rp, err := evalSymlinksFn(p); err == nil {
			info.ExecPath = rp
		}
	}

	switch {
	case isHomebrewPath(info.ExecPath):
		info.Method = MethodHomebrew
	case inContainer():
		info.Method = MethodDocker
	case isStampedVersion(ldflagsVersionFn()):
		info.Method = MethodReleaseBinary
	default:
		if bi, ok := readBuildInfoFn(); ok {
			if v := bi.Main.Version; v != "" && v != "(devel)" {
				info.Method = MethodGoInstall
			} else {
				info.Method = MethodSourceBuild
			}
		}
	}
	return info
}

// isStampedVersion reconhece uma versão gravada via ldflags pelo CI de
// release ("v1.2.3"), distinta do default de código-fonte ("dev").
func isStampedVersion(v string) bool {
	return v != "" && v != "dev" && v != "unknown"
}

// isHomebrewPath reconhece o layout do Homebrew/Linuxbrew: o binário real
// vive sob .../Cellar/chatcli/<versão>/bin/chatcli em todos os prefixos
// (/opt/homebrew, /usr/local e /home/linuxbrew/.linuxbrew).
func isHomebrewPath(path string) bool {
	if path == "" {
		return false
	}
	return strings.Contains(filepath.ToSlash(path), "/Cellar/chatcli/")
}

// inContainer detecta execução em container via os marcadores padrão do
// Docker e do Podman.
func inContainer() bool {
	return fileExistsFn("/.dockerenv") || fileExistsFn("/run/.containerenv")
}
