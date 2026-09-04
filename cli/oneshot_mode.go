/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/commands"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// Options representa as flags suportadas pelo binário
type Options struct {
	// Geral
	Version bool // --version | -v
	Help    bool // --help | -h

	// Modo one-shot
	Prompt         string        // -p | --prompt
	Provider       string        // --provider
	Model          string        // --model
	Timeout        time.Duration // --timeout
	NoAnim         bool          // --no-anim
	Raw            bool          // --raw
	PromptFlagUsed bool          // indica se -p/--prompt foi passado explicitamente
	AgentAutoExec  bool          // --agent-auto-exec
	MaxTokens      int           // --max-tokens
	Realm          string        // --realm
	AgentID        string        // --agent-id
}

// HandleOneShotOrFatal executa o modo one-shot se solicitado (flag -p usada ou stdin presente).
// - Em caso de erro, imprime mensagem em Markdown (stderr) e faz logger.Fatal (sem fallback).
// - Retorna true se o one-shot foi tratado (com sucesso ou erro fatal). Retorna false se não foi acionado.
func (cli *ChatCLI) HandleOneShotOrFatal(ctx context.Context, opts *Options) bool {
	if !opts.PromptFlagUsed && !HasStdin() {
		return false
	}
	cli.SetAuditSurface("oneshot")

	// Aplica overrides de provider/model
	if err := cli.ApplyOverrides(ctx, cli.manager, opts.Provider, opts.Model); err != nil {
		fmt.Fprintln(os.Stderr, i18n.T("manager.error_provider_not_supported", opts.Provider)+"\n\n"+i18n.T("oneshot.details_label")+":\n```\n"+err.Error()+"\n```")
		cli.logger.Fatal("Erro ao aplicar provider/model via flags", zap.Error(err))
	}

	// Monta input a partir de -p e/ou stdin
	input := strings.TrimSpace(opts.Prompt)
	if HasStdin() {
		b, _ := io.ReadAll(os.Stdin)
		stdinText := strings.TrimSpace(string(b))
		if input == "" {
			input = stdinText
		} else if stdinText != "" {
			input = input + "\n" + stdinText
		}
	}

	if cli.Client == nil {
		fmt.Fprintln(os.Stderr, i18n.T("oneshot.error.no_provider"))
		cli.logger.Fatal("One-shot acionado sem provedor LLM configurado")
	}

	if strings.TrimSpace(input) == "" {
		fmt.Fprintln(os.Stderr, i18n.T("oneshot.error.empty_input"))
		cli.logger.Fatal("One-shot acionado sem input (prompt vazio e sem stdin)")
	}

	ctxOne, cancelOne := context.WithTimeout(ctx, opts.Timeout)
	defer cancelOne()

	// Slash-command expansion: `chatcli -p "/review-pr 12"` resolves the
	// template before mode routing, so a command behaves identically in
	// scripts and in the REPL. Interactive only when stdin is a real
	// terminal — a piped stdin cannot answer a pre-exec approval prompt,
	// so the gate resolves through policy there (fail-safe deny on ask).
	input, coderRoute := cli.resolveOneShotCommand(ctxOne, input)
	if coderRoute {
		// mode:coder command: same engine and error contract as -p "/coder …".
		if err := cli.runCoderQuery(ctxOne, input, false); err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("oneshot.error.coder_failed")+"\n\n"+i18n.T("oneshot.details_label")+":\n```\n"+err.Error()+"\n```")
			cli.logger.Fatal("Erro no modo coder one-shot", zap.Error(err))
		}
		cli.queueOneShotMemory()
		return true
	}

	if strings.HasPrefix(input, "/agent ") || strings.HasPrefix(input, "/run ") {
		if err := cli.RunAgentOnce(ctxOne, input, opts.AgentAutoExec); err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("oneshot.error.agent_failed")+"\n\n"+i18n.T("oneshot.details_label")+":\n```\n"+err.Error()+"\n```")
			cli.logger.Fatal("Erro no modo agente one-shot", zap.Error(err))
		}
	} else if strings.HasPrefix(input, "/coder ") {
		// coder one-shot (mesma experiência do modo /coder interativo)
		if err := cli.RunCoderOnce(ctxOne, input); err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("oneshot.error.coder_failed")+"\n\n"+i18n.T("oneshot.details_label")+":\n```\n"+err.Error()+"\n```")
			cli.logger.Fatal("Erro no modo coder one-shot", zap.Error(err))
		}
	} else {
		if err := cli.RunOnce(ctxOne, input, opts.NoAnim, opts.Raw); err != nil {
			fmt.Fprintln(os.Stderr, i18n.T("oneshot.error.run_failed")+"\n\n"+i18n.T("oneshot.details_label")+":\n```\n"+err.Error()+"\n```")
			cli.logger.Fatal("Erro no modo one-shot", zap.Error(err))
		}
	}

	cli.queueOneShotMemory()
	return true
}

// queueOneShotMemory persists the one-shot conversation (chat turn or full
// agent/coder transcript) to the memory worker's pending WAL. One-shot exits
// immediately, so no extraction pass runs here — the NEXT session's worker
// drains the backlog, giving `-p` invocations the same long-term memory and
// self-evolve treatment every other surface gets. System messages are prompt
// scaffolding and are skipped.
func (cli *ChatCLI) queueOneShotMemory() {
	if cli.memWorker == nil || len(cli.history) == 0 {
		return
	}
	seg := make([]models.Message, 0, len(cli.history))
	for _, m := range cli.history {
		if m.Role == "system" {
			continue
		}
		seg = append(seg, m)
	}
	cli.memWorker.queueSegmentForNextSession(seg)
}

// resolveOneShotCommand expands a slash-command input for the -p surface
// and decides its route. coderRoute is true when the command's resolved
// mode is coder and the auto-route is enabled: the caller then runs the
// expanded prompt through the coder one-shot engine instead of prefix
// routing. Non-command input passes through unchanged.
func (cli *ChatCLI) resolveOneShotCommand(ctx context.Context, input string) (string, bool) {
	cmd, _, ok := cli.peekSlashCommand(input)
	if !ok {
		return input, false
	}
	coderRoute := cmd.ResolvedMode() == commands.ExecModeCoder && commandsAutorouteEnabled()
	expanded, isCmd := cli.expandSlashCommandInput(ctx, input, !HasStdin())
	if !isCmd {
		return input, false
	}
	if coderRoute {
		fmt.Fprintln(os.Stderr, i18n.T("commands.autoroute.notice_oneshot", cmd.InvocationName()))
	}
	return expanded, coderRoute
}

// Detecta se há dados no stdin (pipe/arquivo ao invés de TTY).
func HasStdin() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	// Se não for dispositivo de caractere (tty), então veio de pipe/arquivo
	return (fi.Mode() & os.ModeCharDevice) == 0
}

// PreprocessArgs normaliza o caso de -p/--prompt sem valor, convertendo para -p= / --prompt=
// Ex.: echo "msg" | chatcli -p  -> trata como prompt vazio + stdin (não quebra o flag parser)
func PreprocessArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-p" || a == "--prompt" {
			// Se existir próximo arg e não começar com "-", mantém normal (valor presente).
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && args[i+1] != "" {
				out = append(out, a)
				continue
			}
			// Sem valor explícito: força formato com "=" (string vazia)
			if a == "-p" {
				out = append(out, "-p=")
			} else {
				out = append(out, "--prompt=")
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func (cli *ChatCLI) RunOnce(ctx context.Context, input string, disableAnimation bool, rawOutput bool) error {
	userInput, additionalContext, images := cli.processSpecialCommands(ctx, input)
	images, visionDesc := cli.gateImagesForModel(ctx, images)
	additionalContext += visionDesc

	// Skill parity with the interactive chat turn: pinned skills and
	// trigger/path auto-activation fire on the one-shot prompt exactly like
	// they would in the REPL (`-p "/coder …"` already had them through the
	// agent engine; plain `-p` chat was the one surface without any).
	pinned, autoSkills, filePaths := cli.resolveSkillsForTurn(userInput, additionalContext)
	modelHint, skillEffort := cli.pickSkillHints(pinned, autoSkills, filePaths)

	// One-shot used to send the bare user prompt with NO system message at
	// all — no mode hint, no language directive, no memory. A user with
	// months of persisted facts got a model with total amnesia ("each
	// session starts fresh"). Inject the chat-mode baseline plus memory.
	if sys := cli.oneShotSystemMessage(ctx, userInput); sys != "" {
		if sb := concatSkillBlocks(pinned, autoSkills); sb != "" {
			sys += "\n\n" + sb
		}
		cli.history = append(cli.history, models.Message{Role: "system", Content: sys})
	}

	// Skill model/effort routing, same precedence as the interactive turn
	// and the MCP/ACP chat turn (explicit CLI flags already resolved
	// cli.Client, and a hint failure degrades to the session client).
	activeClient := cli.Client
	if modelHint != "" {
		if c, _, _, err := cli.resolveRPCChatClient(modelHint, RPCChatOpts{}); err == nil && c != nil {
			activeClient = c
		}
	}
	ctx = cli.applyChatEffortHint(ctx, routeEffortForPrompt(userInput, skillEffort))

	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: userInput + additionalContext,
		Images:  images,
	})

	// Compact history if over budget. In one-shot mode we keep the status
	// output on stderr so it doesn't pollute the stdout result (which may
	// be piped into other tools).
	cfg := cli.compactConfig(cli.Provider, cli.Model)
	if cli.historyCompactor.NeedsCompaction(cli.history, cfg) {
		cli.queueMemoryBeforeCompaction()
		cli.historyCompactor.SetStatusCallback(func(stage CompactStage, msg string) {
			fmt.Fprintf(os.Stderr, "  %s\n", msg)
		})
		cli.beforeCompaction(ctx, compactTriggerAuto)
		if compacted, compactErr := cli.historyCompactor.Compact(ctx, cli.history, activeClient, cfg); compactErr == nil && !historiesEqual(compacted, cli.history) {
			cli.history = compacted
			cli.noteCompactionApplied(ctx, compactTriggerAuto)
		} else {
			cli.compactionSkipped(ctx, compactTriggerAuto)
		}
		cli.historyCompactor.SetStatusCallback(nil)
	}

	if !disableAnimation {
		cli.animation.ShowThinkingAnimation(activeClient.GetModelName())
	}

	effectiveMaxTokens := cli.getMaxTokensForCurrentLLM()
	aiResponse, err := activeClient.SendPrompt(ctx, userInput+additionalContext, cli.history, effectiveMaxTokens)
	// Auto-retry on OAuth token expiration (401)
	if cli.refreshClientOnAuthError(err) {
		aiResponse, err = activeClient.SendPrompt(ctx, userInput+additionalContext, cli.history, effectiveMaxTokens)
	}
	// Overflow recovery (bounded), notices on stderr so stdout stays pipeable.
	rec := cli.newOverflowRecovery("oneshot", func(msg string) { fmt.Fprintf(os.Stderr, "  %s\n", msg) })
	for err != nil && cli.recoverOverflow(ctx, rec, err) {
		aiResponse, err = activeClient.SendPrompt(ctx, userInput+additionalContext, cli.history, effectiveMaxTokens)
	}

	if !disableAnimation {
		cli.animation.StopThinkingAnimation()
	}

	if err != nil {
		return err
	}

	// Track cost for one-shot mode — prefer real API usage
	if cli.costTracker != nil {
		usage := client.GetUsageOrEstimate(activeClient, len(userInput+additionalContext), len(aiResponse))
		cli.costTracker.RecordRealUsage(cli.Provider, cli.Model, usage)
	}

	// Keep the assistant reply in the (process-local) history so the
	// dispatcher can queue the complete turn for memory extraction.
	cli.history = append(cli.history, models.Message{Role: "assistant", Content: aiResponse})

	if rawOutput {
		fmt.Println(aiResponse) // Imprime texto limpo
	} else {
		rendered := cli.renderMarkdown(aiResponse) // Imprime com cores ANSI
		fmt.Println(rendered)
	}
	return nil
}

// oneShotSystemMessage builds the single system message for -p one-shot
// chat. One-shot has NO pull surface (no tool loop), so the "index" memory
// mode would inject a digest plus a directive to call a tool that cannot be
// called — it therefore promotes to "full" (push the hint-relevant
// retrieval), while "off" still suppresses memory entirely. Saved-session
// pointers ride along with the chat-variant header, whose contract (surface
// the pointer, suggest /session attach) needs no tools either.
func (cli *ChatCLI) oneShotSystemMessage(ctx context.Context, userInput string) string {
	sys := ChatModeSystemHint + "\n" + i18n.T("ai.response_language")

	hints := cli.turnHints(userInput)
	if cli.contextBuilder != nil {
		mode := loadMemoryMode()
		if mode == memModeIndex {
			mode = memModeFull
		}
		if ws := cli.contextBuilder.BuildWorkspaceContextMode(ctx, userInput, hints, nil, mode, ""); strings.TrimSpace(ws) != "" {
			sys += "\n\n" + ws
		}
	}
	if sr := cli.chatSessionAutoRecallBlock(hints, userInput); sr != "" {
		sys += "\n\n" + sr
	}
	return sys
}

// NewFlagSet cria um FlagSet isolado e as Options para parsing
func NewFlagSet() (*flag.FlagSet, *Options) {
	fs := flag.NewFlagSet("chatcli", flag.ContinueOnError)
	opts := &Options{}

	fs.BoolVar(&opts.Version, "version", false, "Mostra versão e sai")
	fs.BoolVar(&opts.Version, "v", false, "Mostra versão e sai (alias)")
	fs.BoolVar(&opts.Help, "help", false, "Mostra ajuda e sai")
	fs.BoolVar(&opts.Help, "h", false, "Mostra ajuda e sai (alias)")

	fs.StringVar(&opts.Prompt, "p", "", "Prompt a executar uma única vez (modo não interativo) - (alias)")
	fs.StringVar(&opts.Prompt, "prompt", "", "Prompt a executar uma única vez (modo não interativo)")
	fs.StringVar(&opts.Provider, "provider", "", "Override do provider (OPENAI, OPENAI_ASSISTANT, CLAUDEAI, BEDROCK, GOOGLEAI, XAI, ZAI, MINIMAX, MOONSHOT, STACKSPOT, OLLAMA, COPILOT, GITHUB_MODELS, OPENROUTER, DEVIN)")
	fs.StringVar(&opts.Model, "model", "", "Override do modelo(LLM)")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Minute, "Timeout da chamada one-shot")
	fs.IntVar(&opts.MaxTokens, "max-tokens", 0, "Override do máximo de tokens para a resposta")
	fs.BoolVar(&opts.NoAnim, "no-anim", false, "Desabilita animações no modo one-shot")
	fs.BoolVar(&opts.Raw, "raw", false, "Desabilita formatação markdown/ANSI no output (útil para CI/CD)")
	fs.BoolVar(&opts.AgentAutoExec, "agent-auto-exec", false, "No modo agente one-shot, executa o primeiro comando sugerido automaticamente se for seguro.")

	fs.StringVar(&opts.Realm, "realm", "", "Override do realm (apenas para StackSpot)")
	fs.StringVar(&opts.AgentID, "agent-id", "", "Override do Agent ID (apenas para StackSpot)")

	return fs, opts
}

// Parse analisa os args, valida e retorna Options
func Parse(args []string) (*Options, error) {
	fs, opts := NewFlagSet()
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Validações simples
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("timeout inválido: deve ser > 0")
	}

	// Detectar se a flag -p/--prompt foi usada explicitamente
	used := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "p" || f.Name == "prompt" {
			used = true
		}
	})
	opts.PromptFlagUsed = used

	return opts, nil
}
