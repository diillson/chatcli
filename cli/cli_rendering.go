package cli

import (
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/diillson/chatcli/cli/agent"
)

// renderMarkdown renderiza o texto em Markdown
func (cli *ChatCLI) renderMarkdown(input string) string {
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(0),
	)
	out, err := renderer.Render(input)
	if err != nil {
		return input
	}

	out = strings.TrimRight(out, " \n\t")
	if !strings.HasSuffix(out, "\033[0m") {
		out += "\033[0m"
	}

	return out
}

// ensureANSIReset garante que string termina com reset ANSI
func ensureANSIReset(s string) string {
	if !strings.HasSuffix(s, "\033[0m") && !strings.HasSuffix(s, "\033[m") {
		return s + "\033[0m"
	}
	return s
}

// typewriterEffect exibe o texto com efeito de máquina de escrever
// usando o pacing adaptativo do pacote agent: respostas curtas mantêm
// a cadência solicitada (delay por rune), respostas longas têm o delay
// escalonado para caber no orçamento total (~800ms por padrão), e
// respostas muito grandes (acima de 8k runas visíveis) são pintadas
// instantaneamente. Variáveis de ambiente CHATCLI_NO_TYPEWRITER,
// CHATCLI_TYPEWRITER_BUDGET_MS e CHATCLI_TYPEWRITER_DELAY_MS permitem
// ajuste fino sem rebuild.
//
// Mantemos esta função como método do ChatCLI por compatibilidade com
// os call sites históricos; a lógica real vive em agent.PaceText e é
// compartilhada com o envelope de resposta.
func (cli *ChatCLI) typewriterEffect(text string, delay time.Duration) {
	agent.PaceText(text, delay)
}
