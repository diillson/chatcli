package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// Options representa as flags suportadas pelo binário
type Options struct {
	// Geral
	Version bool // --version | -v
	Help    bool // --help | -h

	// Modo one-shot
	Prompt   string        // -p | --prompt
	Provider string        // --provider
	Model    string        // --model
	Timeout  time.Duration // --timeout
	NoAnim   bool          // --no-anim
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

	return opts, nil
}
