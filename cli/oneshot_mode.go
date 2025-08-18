package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

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
	PromptFlagUsed bool          // indica se -p/--prompt foi passado explicitamente
}

// HandleOneShotOrFatal executa o modo one-shot se solicitado (flag -p usada ou stdin presente).
// - Em caso de erro, imprime mensagem em Markdown (stderr) e faz logger.Fatal (sem fallback).
// - Retorna true se o one-shot foi tratado (com sucesso ou erro fatal). Retorna false se não foi acionado.
func (cli *ChatCLI) HandleOneShotOrFatal(ctx context.Context, opts *Options) bool {
	if !opts.PromptFlagUsed && !HasStdin() {
		return false
	}

	// Aplica overrides de provider/model
	if err := cli.ApplyOverrides(cli.manager, opts.Provider, opts.Model); err != nil {
		fmt.Fprintln(os.Stderr, " ❌ Erro ao aplicar overrides de provider/model\n\nDetalhes:\n```\n"+err.Error()+"\n```")
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

	// One-shot foi solicitado mas sem conteúdo
	if strings.TrimSpace(input) == "" {
		const md = `
     ❌ Erro no modo one-shot
    
    O modo one-shot foi acionado (via flag -p/--prompt ou stdin), mas nenhum conteúdo de entrada foi fornecido.
    
    - Use a flag -p/--prompt com um texto:
  
  chatcli -p "Seu prompt aqui"
  
    - Ou envie dados via stdin:
  
  echo "Texto" | chatcli
  
    `
		fmt.Fprintln(os.Stderr, md)
		cli.logger.Fatal("One-shot acionado sem input (prompt vazio e sem stdin)")
	}

	// Executa o one-shot com timeout próprio
	ctxOne, cancelOne := context.WithTimeout(ctx, opts.Timeout)
	defer cancelOne()

	if err := cli.RunOnce(ctxOne, input, opts.NoAnim); err != nil {
		fmt.Fprintln(os.Stderr, " ❌ Erro ao executar no modo one-shot\n\nDetalhes:\n```\n"+err.Error()+"\n```")
		cli.logger.Fatal("Erro no modo one-shot", zap.Error(err))
	}

	// One-shot concluído com sucesso
	return true
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

func (cli *ChatCLI) RunOnce(ctx context.Context, input string, disableAnimation bool) error {

	// Processa comandos especiais (@file, @git, @env, @command, > contexto)
	userInput, additionalContext := cli.processSpecialCommands(input)

	// Adiciona a mensagem do usuário ao histórico
	cli.history = append(cli.history, models.Message{
		Role:    "user",
		Content: userInput + additionalContext,
	})

	// Exibe animação (opcional)
	if !disableAnimation {
		cli.animation.ShowThinkingAnimation(cli.Client.GetModelName())
	}

	// Faz a chamada ao LLM (com timeout vindo do contexto)
	aiResponse, err := cli.Client.SendPrompt(ctx, userInput+additionalContext, cli.history)

	if !disableAnimation {
		cli.animation.StopThinkingAnimation()
	}

	if err != nil {
		return err
	}

	// Renderiza e imprime a resposta
	rendered := cli.renderMarkdown(aiResponse)
	fmt.Println(rendered)
	return nil
}

// NewFlagSet cria um FlagSet isolado e as Options para parsing
func NewFlagSet() (*flag.FlagSet, *Options) {
	fs := flag.NewFlagSet("chatcli", flag.ContinueOnError)
	opts := &Options{}

	// Flags
	fs.BoolVar(&opts.Version, "version", false, "Mostra versão e sai")
	fs.BoolVar(&opts.Version, "v", false, "Mostra versão e sai (alias)")

	fs.BoolVar(&opts.Help, "help", false, "Mostra ajuda e sai")
	fs.BoolVar(&opts.Help, "h", false, "Mostra ajuda e sai (alias)")

	fs.StringVar(&opts.Prompt, "p", "", "Prompt a executar uma única vez (modo não interativo) - (alias)")
	fs.StringVar(&opts.Prompt, "prompt", "", "Prompt a executar uma única vez (modo não interativo)")

	fs.StringVar(&opts.Provider, "provider", "", "Override do provider (OPENAI, CLAUDEAI, GOOGLEAI, OPENAI_ASSISTANT, STACKSPOT)")
	fs.StringVar(&opts.Model, "model", "", "Override do modelo(LLM)")

	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Minute, "Timeout da chamada one-shot")
	fs.BoolVar(&opts.NoAnim, "no-anim", false, "Desabilita animações no modo one-shot")

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
