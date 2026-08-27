/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Mode controla o comportamento do updater no boot.
type Mode int

const (
	// ModeNotify (default): checa em background e mostra aviso no welcome;
	// a atualização só roda quando o usuário chama /update.
	ModeNotify Mode = iota
	// ModeAuto: além de notificar, aplica silenciosamente em background nos
	// canais elegíveis (go install e binário de release — staging: o processo
	// atual segue na versão antiga, o próximo start já abre na nova).
	ModeAuto
	// ModeOff: nem checagem de update nem notificação.
	ModeOff
)

// String devolve o identificador estável do modo para exibição em /config.
func (m Mode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModeOff:
		return "off"
	default:
		return "notify"
	}
}

// ResolveMode lê a política de update do ambiente. CHATCLI_DISABLE_VERSION_CHECK
// desliga a checagem de release inteira, então implica ModeOff aqui também.
func ResolveMode() Mode {
	if strings.EqualFold(os.Getenv("CHATCLI_DISABLE_VERSION_CHECK"), "true") {
		return ModeOff
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_AUTO_UPDATE"))) {
	case "auto":
		return ModeAuto
	case "off", "never", "false", "disabled":
		return ModeOff
	default:
		return ModeNotify
	}
}

// BrewUpgradeArgs é o comando canônico de atualização via Homebrew. O nome
// totalmente qualificado prende o upgrade à fórmula do tap oficial mesmo se
// existir outra "chatcli" em algum tap concorrente.
func BrewUpgradeArgs() []string {
	return []string{"brew", "upgrade", "diillson/chatcli/chatcli"}
}

// GoInstallArgs é o comando canônico de atualização via toolchain Go.
func GoInstallArgs() []string {
	return []string{"go", "install", "github.com/diillson/chatcli@latest"}
}

// goInstallTagRe valida a tag antes de embuti-la no argumento do go install.
// A tag vem da API de releases do GitHub (via cache), não do usuário — a
// validação garante que só um semver real vira sufixo @vX.Y.Z e qualquer
// outra coisa cai no fallback @latest.
var goInstallTagRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-.][0-9A-Za-z.-]+)?$`)

// GoInstallArgsVersion pina o go install na versão exata que a checagem de
// release viu como mais recente (@vX.Y.Z), em vez de delegar ao @latest do
// module proxy — que pode estar defasado em relação ao GitHub e instalar
// silenciosamente uma versão diferente da anunciada. Tag vazia ou fora do
// formato semver cai no comando canônico @latest.
func GoInstallArgsVersion(latestTag string) []string {
	tag := strings.TrimPrefix(strings.TrimSpace(latestTag), "v")
	if !goInstallTagRe.MatchString(tag) {
		return GoInstallArgs()
	}
	return []string{"go", "install", "github.com/diillson/chatcli@v" + tag}
}

// CommandFor devolve o comando externo que Apply executaria para o método,
// ou nil quando o canal não usa comando externo (self-replace ou manual).
// A camada cli usa isso para mostrar ao usuário o que vai rodar.
func CommandFor(m Method) []string {
	switch m {
	case MethodHomebrew:
		return BrewUpgradeArgs()
	case MethodGoInstall:
		return GoInstallArgs()
	default:
		return nil
	}
}

// CommandForVersion é o CommandFor ciente da versão-alvo: no canal go install
// devolve o comando pinado na tag detectada (o mesmo argv que Apply executa),
// nos demais delega ao CommandFor. Brew fica de fora de propósito: o upgrade
// resolve a fórmula do tap, que não aceita pin por versão.
func CommandForVersion(m Method, latestTag string) []string {
	if m == MethodGoInstall {
		return GoInstallArgsVersion(latestTag)
	}
	return CommandFor(m)
}

// Options parametriza Apply. Stdout/Stderr recebem a saída dos comandos
// externos (brew/go install); nil descarta — o modo auto em background usa
// nil para nunca sujar o prompt.
type Options struct {
	Stdout io.Writer
	Stderr io.Writer
}

// runCommandFn executa um comando externo com a saída plugada — seam para
// testes não dispararem brew/go de verdade.
var runCommandFn = func(ctx context.Context, opts Options, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //#nosec G204 -- argv vem dos literais fixos BrewUpgradeArgs/GoInstallArgs; a única parte variável é a tag semver validada por goInstallTagRe, nunca entrada livre do usuário
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return cmd.Run()
}

// lookPathFn resolve a ferramenta no PATH — seam para testes.
var lookPathFn = exec.LookPath

// Apply atualiza pelo canal detectado em info, para a versão latestTag
// (com ou sem prefixo "v"). O self-replace baixa o asset dessa tag e o go
// install instala pinado nela (@vX.Y.Z) — instalar exatamente o que a
// checagem anunciou, nunca o @latest do module proxy, que pode estar
// defasado. Só o brew resolve a versão sozinho (a fórmula do tap não
// aceita pin). Erros tipados deste pacote descrevem cada modo de falha;
// ErrManualMethod cobre os canais sem atualização automática.
func Apply(ctx context.Context, info Info, latestTag string, opts Options) error {
	switch info.Method {
	case MethodHomebrew:
		return runTool(ctx, opts, BrewUpgradeArgs())
	case MethodGoInstall:
		return runTool(ctx, opts, GoInstallArgsVersion(latestTag))
	case MethodReleaseBinary:
		return SelfReplace(ctx, info.ExecPath, latestTag)
	default:
		return ErrManualMethod
	}
}

// runTool valida a presença da ferramenta e executa o comando do canal.
func runTool(ctx context.Context, opts Options, argv []string) error {
	if _, err := lookPathFn(argv[0]); err != nil {
		return &MissingToolError{Tool: argv[0]}
	}
	return runCommandFn(ctx, opts, argv[0], argv[1:]...)
}
