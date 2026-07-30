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
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
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
	anchor := screenWidth()
	fmt.Println()
	fmt.Println(kit.RuleHeader(" "+kit.Bold(i18n.T("update.card.title"), theme.RoleHeader)+" ", "", anchor))
	fmt.Println()
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
		fmt.Println("  " + colorize("✓ "+i18n.T("update.uptodate"), ColorGreen))
		return
	}
	// O usuário está prestes a atualizar (ou decidir se atualiza): mostra o
	// que a versão nova traz antes de qualquer ação.
	if notes := renderReleaseNotes(rep, anchor); notes != "" {
		fmt.Print(notes)
		fmt.Println()
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

// bootUpdateCheckBudget limita quanto o boot espera pela consulta de release
// antes de imprimir a welcome screen. Curto de propósito: rede lenta ou
// ausente não pode segurar a abertura — no timeout, o backgroundUpdateFlow
// completa o refresh e o aviso drena no turno seguinte. Com cache fresco
// (≤1 consulta/dia) o custo é zero.
const bootUpdateCheckBudget = 1500 * time.Millisecond

// preWelcomeUpdateCheck renova o cache de release de forma SÍNCRONA e com
// orçamento curto, antes da welcome screen ser impressa — é o que permite ao
// banner anunciar uma release recém-publicada já neste boot, em vez de só no
// próximo input (aviso drenado) ou no próximo boot. Política off e checagem
// desabilitada retornam na hora, sem tocar a rede.
func (cli *ChatCLI) preWelcomeUpdateCheck(ctx context.Context) {
	if update.ResolveMode() == update.ModeOff {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, bootUpdateCheckBudget)
	defer cancel()
	version.RefreshReleaseCacheIfStale(ctx)
}

// backgroundUpdateFlow roda em goroutine no boot: limpa restos de updates
// anteriores, renova o cache de release (que alimenta o welcome e o /version
// offline) e, no modo auto, aplica o update silencioso por staging — o
// processo atual segue na versão corrente e o próximo start abre na nova.
// Quando o refresh descobre uma versão nova que a welcome (impressa antes,
// com o cache antigo) não anunciou — e nenhum staging vai cobri-la —
// enfileira o aviso mid-session drenado pelo executor.
func (cli *ChatCLI) backgroundUpdateFlow(ctx context.Context) {
	info := detectInstallFn()
	update.CleanupStaleArtifacts(info.ExecPath)

	version.RefreshReleaseCacheIfStale(ctx)

	mode := update.ResolveMode()
	if mode == update.ModeOff {
		return
	}
	rep := version.OfflineReport()
	if !rep.NeedsUpdate {
		return
	}
	if mode == update.ModeAuto && info.Method.AutoApplicable() {
		if cli.stageAutoUpdate(ctx, info, rep) {
			return // staged (aqui ou por outro processo) — o próximo boot anuncia
		}
	}
	cli.queueUpdateNotice(rep.Latest)
}

// stageAutoUpdate aplica o update silencioso por staging. Retorna true
// quando a versão nova ficou coberta — staging concluído aqui ou em curso
// num processo concorrente (que herda o anúncio no próximo boot) — e false
// quando falhou e o usuário deve ser avisado para atualizar manualmente.
func (cli *ChatCLI) stageAutoUpdate(ctx context.Context, info update.Info, rep version.Report) bool {
	// Serializa entre processos chatcli concorrentes; quem perde o lock
	// simplesmente herda o staging do vencedor no próximo boot.
	release, ok := update.TryAcquireAutoLock()
	if !ok {
		return true
	}
	defer release()

	actx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	// Saída descartada de propósito: nada pode sujar o prompt interativo.
	if err := applyUpdateFn(actx, info, rep.Latest, update.Options{}); err != nil {
		cli.logger.Debug("auto-update: staging em background falhou",
			zap.String("method", info.Method.String()), zap.Error(err))
		return false
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
	return true
}
