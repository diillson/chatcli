package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
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
func (cli *ChatCLI) typewriterEffect(text string, delay time.Duration) {
	reader := strings.NewReader(text)
	inEscapeSequence := false

	for {
		char, _, err := reader.ReadRune()
		if err != nil {
			break
		}

		if char == '\033' {
			inEscapeSequence = true
		}

		fmt.Printf("%c", char)
		os.Stdout.Sync()

		if inEscapeSequence {
			if char == 'm' {
				inEscapeSequence = false
			}
			continue
		}

		time.Sleep(delay)
	}
}
