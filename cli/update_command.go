/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /update atualiza o ChatCLI pelo MESMO canal em que foi instalado
 * (Homebrew, go install ou binário oficial da release — detectados pelo
 * pacote update). Canais manuais (Docker, build local) recebem instruções
 * em vez de ação. Este arquivo também abriga o fluxo de boot: refresh do
 * cache de release + staging silencioso no modo CHATCLI_AUTO_UPDATE=auto.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
	"go.uber.org/zap"
)

// Seams injetáveis no padrão de version.FetchLatestReleaseImpl: os testes
// substituem a detecção de canal e a aplicação do update sem tocar em rede,
// brew ou go toolchain.
var (
	detectInstallFn = update.Detect
	applyUpdateFn   = update.Apply
)

// handleUpdateCommand trata /update e /update check. O bare /update é um
// pedido explícito do usuário: quando há versão nova num canal automatizável,
// aplica direto (semântica de brew upgrade), sem confirmação intermediária.
func (cli *ChatCLI) handleUpdateCommand(ctx context.Context, userInput string) {
	args := strings.Fields(userInput)
	checkOnly := len(args) > 1 && (args[1] == "check" || args[1] == "--check")

	info := detectInstallFn()
	fmt.Println(colorize("  "+i18n.T("update.checking"), ColorGray))

	rep := version.GetReport(ctx)
	if rep.CheckErr != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("update.err.check", rep.CheckErr.Error()), ColorYellow))
		return
	}
	// Latest vazio sem erro = checagem desabilitada por política
	// (CHATCLI_DISABLE_VERSION_CHECK); sem release para comparar, não há
	// o que aplicar — informa em vez de exibir uma comparação vazia.
	if rep.Latest == "" {
		fmt.Println("  " + colorize(i18n.T("update.check_disabled"), ColorYellow))
		return
	}

	fmt.Println("  " + colorize(i18n.T("update.method_label", methodLabel(info.Method)), ColorGray))
	fmt.Println("  " + colorize(i18n.T("update.versions",
		version.ExtractBaseVersion(rep.Current.Version), rep.Latest), ColorGray))

	if !rep.NeedsUpdate {
		fmt.Println("  " + colorize("✅ "+i18n.T("update.uptodate"), ColorGreen))
		return
	}
	if checkOnly {
		fmt.Println("  " + colorize("⬆ "+i18n.T("update.available_hint"), ColorYellow))
		return
	}

	switch info.Method {
	case update.MethodDocker:
		fmt.Println("  " + colorize(i18n.T("update.manual.docker"), ColorYellow))
		return
	case update.MethodSourceBuild:
		fmt.Println("  " + colorize(i18n.T("update.manual.source"), ColorYellow))
		return
	case update.MethodUnknown:
		fmt.Println("  " + colorize(i18n.T("update.manual.unknown"), ColorYellow))
		return
	}

	if argv := update.CommandFor(info.Method); argv != nil {
		fmt.Println("  " + colorize(i18n.T("update.running", strings.Join(argv, " ")), ColorCyan))
	} else {
		asset, _ := update.AssetName()
		fmt.Println("  " + colorize(i18n.T("update.downloading", rep.Latest, asset), ColorCyan))
	}

	if err := applyUpdateFn(ctx, info, rep.Latest, update.Options{Stdout: os.Stdout, Stderr: os.Stderr}); err != nil {
		cli.printUpdateError(err, info, rep.Latest)
		return
	}
	fmt.Println("  " + colorize(i18n.T("update.success_restart", rep.Latest), ColorGreen))
}

// printUpdateError converte os erros tipados do pacote update em mensagens
// acionáveis — cada modo de falha diz exatamente o que fazer em seguida.
func (cli *ChatCLI) printUpdateError(err error, info update.Info, latest string) {
	var notWritable *update.NotWritableError
	var missingTool *update.MissingToolError
	var unsupported *update.UnsupportedPlatformError
	var noChecksums *update.ChecksumsUnavailableError
	var mismatch *update.ChecksumMismatchError

	warn := func(msg string) { fmt.Println(colorize("  ❌ "+msg, ColorYellow)) }

	switch {
	case errors.As(err, &notWritable):
		warn(i18n.T("update.err.not_writable", notWritable.Dir))
		if url, uerr := update.ManualDownloadURL(latest); uerr == nil {
			fmt.Println("  " + colorize(i18n.T("update.err.not_writable_cmd",
				info.ExecPath, url, info.ExecPath), ColorGray))
		}
	case errors.As(err, &missingTool):
		warn(i18n.T("update.err.missing_tool", missingTool.Tool))
	case errors.As(err, &unsupported):
		warn(i18n.T("update.err.unsupported", unsupported.OS, unsupported.Arch))
	case errors.As(err, &noChecksums):
		warn(i18n.T("update.err.no_checksums", latest,
			"https://github.com/diillson/chatcli/releases/latest"))
	case errors.As(err, &mismatch):
		warn(i18n.T("update.err.mismatch"))
		cli.logger.Warn("self-update: checksum mismatch",
			zap.String("asset", mismatch.Asset),
			zap.String("expected", mismatch.Expected),
			zap.String("actual", mismatch.Actual))
	default:
		warn(i18n.T("update.err.generic", err.Error()))
	}
}

// methodLabel resolve o nome traduzido do canal de instalação.
func methodLabel(m update.Method) string {
	return i18n.T("update.method." + m.String())
}

// backgroundUpdateFlow roda em goroutine no boot: limpa restos de updates
// anteriores, renova o cache de release (que alimenta o welcome e o /version
// offline) e, no modo auto, aplica o update silencioso por staging — o
// processo atual segue na versão corrente e o próximo start abre na nova.
func (cli *ChatCLI) backgroundUpdateFlow(ctx context.Context) {
	info := detectInstallFn()
	update.CleanupStaleArtifacts(info.ExecPath)

	version.RefreshReleaseCacheIfStale(ctx)

	if update.ResolveMode() != update.ModeAuto {
		return
	}
	rep := version.OfflineReport()
	if !rep.NeedsUpdate || !info.Method.AutoApplicable() {
		return
	}
	// Serializa entre processos chatcli concorrentes; quem perde o lock
	// simplesmente herda o staging do vencedor no próximo boot.
	release, ok := update.TryAcquireAutoLock()
	if !ok {
		return
	}
	defer release()

	actx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Saída descartada de propósito: nada pode sujar o prompt interativo.
	if err := applyUpdateFn(actx, info, rep.Latest, update.Options{}); err != nil {
		cli.logger.Debug("auto-update: staging em background falhou",
			zap.String("method", info.Method.String()), zap.Error(err))
		return
	}
	update.SaveStagedRecord(update.StagedRecord{
		From:     rep.Current.Version,
		To:       rep.Latest,
		Method:   info.Method.String(),
		StagedAt: time.Now().UTC(),
	})
	cli.logger.Info("auto-update: nova versão instalada em staging",
		zap.String("from", rep.Current.Version),
		zap.String("to", rep.Latest),
		zap.String("method", info.Method.String()))
}
