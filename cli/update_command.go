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

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/utils"
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
	_ = runUpdateFlow(ctx, cli.logger, checkOnly, func(latest string) {
		// O processo segue na versão antiga até reiniciar; no REPL o
		// /restart faz isso sem perder a conversa.
		if cli.replActive {
			fmt.Println("  " + colorize("↻ "+i18n.T("restart.invite", latest), ColorCyan))
		}
	})
}

// RunUpdateOneShot é a superfície one-shot do /update (`chatcli update`),
// para scripts e automação: mesmo fluxo, mesma saída, sem boot do REPL. O
// erro devolvido vira exit code no main — nil cobre "atualizado" e "já na
// última versão"; falha de checagem, canal manual com update pendente e
// falha de aplicação retornam o erro já impresso.
func RunUpdateOneShot(ctx context.Context, logger *zap.Logger, checkOnly bool) error {
	return runUpdateFlow(ctx, logger, checkOnly, nil)
}

// UpdateSubcommandMain é o entrypoint completo do subcomando `chatcli update`:
// boot mínimo (dotenv+i18n+tema+logger+config — sem LLM manager) seguido do
// contrato de exit code de RunUpdateSubcommand. Vive no pacote cli para boot
// e contrato ficarem sob teste — no main resta apenas o os.Exit.
func UpdateSubcommandMain(args []string) int {
	// Mesma descoberta do boot: CHATCLI_DOTENV → ./.env → ~/.chatcli/.env →
	// ~/.env (o canal/pin de update mora no arquivo de ambiente).
	_, _ = config.LoadDotenv()
	i18n.Init()
	theme.InitFromEnv()

	logger, err := utils.InitializeLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		return 1
	}
	// Sync roda no return da função (diferente de um defer atrás de os.Exit,
	// que nunca executaria); falha de sync em stdout é ruído conhecido do zap.
	defer func() { _ = logger.Sync() }()

	config.InitGlobal(logger)
	config.Global.Load()
	utils.ApplyGlobalTLSTrust(logger)

	return RunUpdateSubcommand(context.Background(), logger, args)
}

// RunUpdateSubcommand é o corpo do subcomando `chatcli update [check]`:
// parseia os args e mapeia o desfecho no exit code do processo — 0 atualizado
// ou já na última versão (check incluso), 1 falha de checagem/aplicação ou
// canal manual com update pendente, 2 uso inválido. Vive no pacote cli (e não
// no main) para o contrato ficar sob teste; o main só faz o boot e repassa.
func RunUpdateSubcommand(ctx context.Context, logger *zap.Logger, args []string) int {
	checkOnly := false
	for _, a := range args {
		switch a {
		case "check", "--check", "-check":
			checkOnly = true
		default:
			fmt.Fprintln(os.Stderr, i18n.T("update.oneshot.usage"))
			return 2
		}
	}
	if err := RunUpdateOneShot(ctx, logger, checkOnly); err != nil {
		return 1
	}
	return 0
}

// runUpdateFlow é o núcleo compartilhado entre o /update interativo e o
// subcomando one-shot: checa a release, compara com o binário atual e aplica
// pelo canal detectado, imprimindo cada passo. onApplied (opcional) roda
// depois de um update aplicado com sucesso, com a versão instalada.
func runUpdateFlow(ctx context.Context, logger *zap.Logger, checkOnly bool, onApplied func(latest string)) error {
	info := detectInstallFn()
	anchor := screenWidth()
	fmt.Println()
	fmt.Println(kit.RuleHeader(" "+kit.Bold(i18n.T("update.card.title"), theme.RoleHeader)+" ", "", anchor))
	fmt.Println()
	fmt.Println(colorize("  "+i18n.T("update.checking"), ColorGray))

	notifier := updateNotifier{}
	// The release check is a network step like the others: behind a flaky
	// proxy it gets the same retries, announced the same way.
	var rep version.Report
	_ = update.Retry(ctx, notifier, "check", func() error {
		rep = version.GetReport(ctx)
		return rep.CheckErr
	})
	if rep.CheckErr != nil {
		fmt.Println(colorize("  ❌ "+i18n.T("update.err.check", rep.CheckErr.Error()), ColorYellow))
		printRetryHint(rep.CheckErr)
		return rep.CheckErr
	}
	// Latest vazio sem erro = checagem desabilitada por política
	// (CHATCLI_DISABLE_VERSION_CHECK); sem release para comparar, não há
	// o que aplicar — informa em vez de exibir uma comparação vazia.
	if rep.Latest == "" {
		fmt.Println("  " + colorize(i18n.T("update.check_disabled"), ColorYellow))
		return errors.New(i18n.T("update.check_disabled"))
	}

	fmt.Println("  " + colorize(i18n.T("update.method_label", methodLabel(info.Method)), ColorGray))
	fmt.Println("  " + colorize(i18n.T("update.versions",
		version.ExtractBaseVersion(rep.Current.Version), rep.Latest), ColorGray))

	if !rep.NeedsUpdate {
		fmt.Println("  " + colorize("✓ "+i18n.T("update.uptodate"), ColorGreen))
		return nil
	}
	// O usuário está prestes a atualizar (ou decidir se atualiza): mostra o
	// que a versão nova traz antes de qualquer ação.
	if notes := renderReleaseNotes(rep, anchor); notes != "" {
		fmt.Print(notes)
		fmt.Println()
	}
	if checkOnly {
		fmt.Println("  " + colorize("⬆ "+i18n.T("update.available_hint"), ColorYellow))
		return nil
	}

	switch info.Method {
	case update.MethodDocker:
		fmt.Println("  " + colorize(i18n.T("update.manual.docker"), ColorYellow))
		return update.ErrManualMethod
	case update.MethodSourceBuild:
		fmt.Println("  " + colorize(i18n.T("update.manual.source"), ColorYellow))
		return update.ErrManualMethod
	case update.MethodUnknown:
		fmt.Println("  " + colorize(i18n.T("update.manual.unknown"), ColorYellow))
		return update.ErrManualMethod
	}

	if argv := update.CommandForVersion(info.Method, rep.Latest); argv != nil {
		fmt.Println("  " + colorize(i18n.T("update.running", strings.Join(argv, " ")), ColorCyan))
	} else {
		asset, _ := update.AssetName()
		fmt.Println("  " + colorize(i18n.T("update.downloading", rep.Latest, asset), ColorCyan))
	}

	opts := update.Options{Stdout: os.Stdout, Stderr: os.Stderr, Notify: notifier}
	if err := applyUpdateFn(ctx, info, rep.Latest, opts); err != nil {
		printUpdateError(logger, err, info, rep.Latest)
		printRetryHint(err)
		return err
	}
	fmt.Println("  " + colorize(i18n.T("update.success_restart", rep.Latest), ColorGreen))
	if onApplied != nil {
		onApplied(rep.Latest)
	}
	return nil
}

// printUpdateError converte os erros tipados do pacote update em mensagens
// acionáveis — cada modo de falha diz exatamente o que fazer em seguida.
func printUpdateError(logger *zap.Logger, err error, info update.Info, latest string) {
	var notWritable *update.NotWritableError
	var missingTool *update.MissingToolError
	var unsupported *update.UnsupportedPlatformError
	var noChecksums *update.ChecksumsUnavailableError
	var mismatch *update.ChecksumMismatchError
	var noSig *update.SignatureUnavailableError
	var badSig *update.SignatureInvalidError
	var badTag *update.InvalidTagError

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
	case errors.Is(err, update.ErrNoReleaseKeys):
		warn(i18n.T("update.integrity.no_release_keys",
			"https://github.com/diillson/chatcli/releases/latest"))
	case errors.As(err, &noSig):
		warn(i18n.T("update.integrity.no_signature", latest,
			"https://github.com/diillson/chatcli/releases/latest"))
	case errors.As(err, &badSig):
		warn(i18n.T("update.integrity.bad_signature", latest))
	case errors.As(err, &badTag):
		warn(i18n.T("update.integrity.bad_tag", badTag.Tag))
	case errors.As(err, &mismatch):
		warn(i18n.T("update.err.mismatch"))
		logger.Warn("self-update: checksum mismatch",
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
		// Staging downloads and verifies a release in the background: on the
		// live dashboard it is a span on the auto-update node.
		span := pulse.Begin(pulse.KindBackground, "auto-update", "")
		staged := cli.stageAutoUpdate(ctx, info, rep)
		if staged {
			span.With("state", "staged for next boot").End(pulse.StatusOK)
			return // staged (aqui ou por outro processo) — o próximo boot anuncia
		}
		span.With("state", "staging failed").End(pulse.StatusError)
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
	// Já está em disco: o /restart abre a versão nova nesta mesma sessão.
	cli.queueRestartNotice(rep.Latest)
	cli.logger.Info("auto-update: nova versão instalada em staging",
		zap.String("from", rep.Current.Version),
		zap.String("to", rep.Latest),
		zap.String("method", info.Method.String()))
	return true
}

// updateNotifier prints the non-fatal notices of an interactive update:
// the go install fallback and every retry of a network step.
type updateNotifier struct{}

// Notify implements update.Notifier.
func (updateNotifier) Notify(n update.Notice) {
	if n == update.NoticeGoInstallDirectiveFallback {
		fmt.Println("  " + colorize(i18n.T("update.goinstall_fallback"), ColorYellow))
	}
}

// NotifyRetry implements update.RetryNotifier.
func (updateNotifier) NotifyRetry(n update.RetryNotice) {
	fmt.Println("  " + colorize("⚠ "+i18n.T("update.retry.notice",
		i18n.T("update.retry.step."+n.Step), n.Attempt, n.Max, n.Err, n.Delay), ColorYellow))
}

// printRetryHint closes a failure that went through every retry with what
// usually causes it on a corporate network. A failure that stopped early
// (a 404, a bad signature, Ctrl+C) already says what to do.
func printRetryHint(err error) {
	if !update.Retryable(err) {
		return
	}
	retries, _ := update.RetryPolicy()
	fmt.Println("  " + colorize(i18n.T("update.retry.gave_up", 1+retries), ColorGray))
}
