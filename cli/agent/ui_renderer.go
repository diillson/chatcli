/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package agent

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	"go.uber.org/zap"
)

// ANSI Color codes exportados
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorLime   = "\033[92m"
	ColorCyan   = "\033[36m"
	ColorGray   = "\033[90m"
	ColorPurple = "\033[35m"
	ColorBold   = "\033[1m"
	ColorYellow = "\033[33m"
)

// UIRenderer gerencia a renderização da interface do modo agente
type UIRenderer struct {
	logger              *zap.Logger
	skipClearOnNextDraw bool
}

// NewUIRenderer cria uma nova instância do renderizador de UI
func NewUIRenderer(logger *zap.Logger) *UIRenderer {
	return &UIRenderer{
		logger:              logger,
		skipClearOnNextDraw: true,
	}
}

// Colorize aplica cores ANSI (exportada com C maiúsculo)
func (r *UIRenderer) Colorize(text string, color string) string {
	return fmt.Sprintf("%s%s%s", color, text, ColorReset)
}

// ClearScreen limpa a tela (se permitido)
func (r *UIRenderer) ClearScreen() {
	if r.skipClearOnNextDraw {
		r.skipClearOnNextDraw = false
		return
	}
	fmt.Print("\033[2J\033[H")
}

// SetSkipClearOnNextDraw define se o próximo clear deve ser pulado
func (r *UIRenderer) SetSkipClearOnNextDraw(skip bool) {
	r.skipClearOnNextDraw = skip
}

// ShowInPager abre texto em pager (less/more)
func (r *UIRenderer) ShowInPager(text string) error {
	pager := "less"
	args := []string{"-R"}
	if runtime.GOOS == "windows" {
		pager = "more"
		args = nil
	}
	cmd := exec.Command(pager, args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PrintPlanCompact imprime plano em formato compacto
func (r *UIRenderer) PrintPlanCompact(blocks []CommandBlock, outputs []*CommandOutput) {
	fmt.Println(r.Colorize(" 📋 PLANO (visão compacta)", ColorLime+ColorBold))
	for i, b := range blocks {
		status := "⏳"
		if i < len(outputs) && outputs[i] != nil {
			if strings.TrimSpace(outputs[i].ErrorMsg) == "" {
				status = "✅"
			} else {
				status = "❌"
			}
		}
		title := b.Description
		if title == "" {
			title = "Executar comandos"
		}

		firstLine := ""
		if len(b.Commands) > 0 {
			firstLine = strings.Split(b.Commands[0], "\n")[0]
		}
		fmt.Printf("  %s #%d: %s — %s\n",
			status, i+1, title, r.Colorize(firstLine, ColorGray))
	}
	fmt.Println()
}

// PrintPlanFull imprime plano em formato completo
func (r *UIRenderer) PrintPlanFull(blocks []CommandBlock, outputs []*CommandOutput, validator *CommandValidator) {
	fmt.Println(r.Colorize(" 📋 PLANO (visão completa)", ColorLime+ColorBold))

	for i, b := range blocks {
		status := "⏳ Pendente"
		statusColor := ColorGray
		if i < len(outputs) && outputs[i] != nil {
			if strings.TrimSpace(outputs[i].ErrorMsg) == "" {
				status = "✅ OK"
				statusColor = ColorGreen
			} else {
				status = "❌ ERRO"
				statusColor = ColorYellow
			}
		}

		title := b.Description
		if title == "" {
			title = "Executar comandos"
		}
		danger := ""
		if r.isBlockDangerous(b, validator) {
			danger = r.Colorize("⚠️ Potencialmente perigoso", ColorYellow)
		} else {
			danger = r.Colorize("Seguro", ColorGray)
		}

		fmt.Printf("\n%s\n", r.Colorize(fmt.Sprintf(" 🔷 COMANDO #%d: %s", i+1, title), ColorPurple+ColorBold))
		fmt.Printf("    %s %s\n", r.Colorize("Tipo:", ColorGray), b.Language)
		fmt.Printf("    %s %s\n", r.Colorize("Risco:", ColorGray), danger)
		fmt.Printf("    %s %s\n", r.Colorize("Status:", ColorGray), r.Colorize(status, statusColor))

		fmt.Println(r.Colorize("    Código:", ColorGray))
		for idx, cmd := range b.Commands {
			if len(b.Commands) > 1 {
				fmt.Printf(r.Colorize("      ( %d / %d )\n", ColorGray), idx+1, len(b.Commands))
			}
			prefix := ""
			if b.Language == "shell" || b.Language == "bash" || b.Language == "sh" {
				prefix = "$ "
			}
			for _, ln := range strings.Split(cmd, "\n") {
				fmt.Printf(r.Colorize("      %s%s\n", ColorCyan), prefix, ln)
			}
			if idx < len(b.Commands)-1 {
				fmt.Println(r.Colorize("      ─────────────────────────────────────────", ColorGray))
			}
		}
	}
	fmt.Println()
}

// isBlockDangerous verifica se algum comando do bloco é perigoso
func (r *UIRenderer) isBlockDangerous(b CommandBlock, validator *CommandValidator) bool {
	for _, c := range b.Commands {
		if validator.IsDangerous(c) {
			return true
		}
	}
	return false
}

// PrintLastResult imprime o último resultado
func (r *UIRenderer) PrintLastResult(outputs []*CommandOutput, lastIdx int) {
	if lastIdx < 0 || lastIdx >= len(outputs) || outputs[lastIdx] == nil {
		return
	}
	fmt.Println(r.Colorize(" 🧾 ÚLTIMO RESULTADO", ColorLime+ColorBold))

	out := outputs[lastIdx].Output
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	max := 30
	if len(lines) > max {
		preview := strings.Join(lines[:max], "\n") + "\n...\n"
		fmt.Print(preview)
	} else {
		fmt.Println(out)
	}

	fmt.Printf("\nDicas: v%d = ver completo | w%d = salvar em arquivo | Enter = continuar\n", lastIdx+1, lastIdx+1)
}

// PrintHeader imprime o cabeçalho do modo agente
func (r *UIRenderer) PrintHeader() {
	fmt.Println("\n" + r.Colorize(" "+strings.Repeat("━", 58), ColorGray))
	fmt.Println(r.Colorize(" 🤖 MODO AGENTE: PLANO DE AÇÃO", ColorLime+ColorBold))
	fmt.Println(r.Colorize(" "+strings.Repeat("━", 58), ColorGray))
	fmt.Println(r.Colorize(" A IA sugeriu os seguintes comandos para executar sua tarefa.", ColorGray))
}

// PrintMenu imprime o menu de opções
func (r *UIRenderer) PrintMenu() {
	fmt.Println("\n" + r.Colorize(strings.Repeat("-", 60), ColorGray))
	fmt.Println(r.Colorize(" O QUE VOCÊ DESEJA FAZER?", ColorLime+ColorBold))
	fmt.Println(r.Colorize(strings.Repeat("-", 60), ColorGray))
	fmt.Printf("  %s: Executa um comando específico (ex: 1, 2, ...)\n", r.Colorize(fmt.Sprintf("%-6s", "[1..N]"), ColorYellow))
	fmt.Printf("  %s: Executa todos os comandos em sequência\n", r.Colorize(fmt.Sprintf("%-6s", "a"), ColorYellow))
	fmt.Printf("  %s: Edita o comando N (ex: e1)\n", r.Colorize(fmt.Sprintf("%-6s", "eN"), ColorYellow))
	fmt.Printf("  %s: Simula (dry-run) o comando N (ex: t2)\n", r.Colorize(fmt.Sprintf("%-6s", "tN"), ColorYellow))
	fmt.Printf("  %s: Pede continuação à IA com a saída do N (ex: c2)\n", r.Colorize(fmt.Sprintf("%-6s", "cN"), ColorYellow))
	fmt.Printf("  %s: Adiciona pré-contexto ao N antes de executar (ex: pc1)\n", r.Colorize(fmt.Sprintf("%-6s", "pcN"), ColorYellow))
	fmt.Printf("  %s: Adiciona contexto à SAÍDA do N (ex: ac1)\n", r.Colorize(fmt.Sprintf("%-6s", "acN"), ColorYellow))
	fmt.Printf("  %s: Ver saída completa do N no pager\n", r.Colorize(fmt.Sprintf("%-6s", "vN"), ColorYellow))
	fmt.Printf("  %s: Salvar saída do N em arquivo\n", r.Colorize(fmt.Sprintf("%-6s", "wN"), ColorYellow))
	fmt.Printf("  %s: Alterna plano completo/compacto\n", r.Colorize(fmt.Sprintf("%-6s", "p"), ColorYellow))
	fmt.Printf("  %s: Atualiza a tela (clear)\n", r.Colorize(fmt.Sprintf("%-6s", "r"), ColorYellow))
	fmt.Printf("  %s: Sai do Modo Agente\n", r.Colorize(fmt.Sprintf("%-6s", "q"), ColorYellow))
	fmt.Println(r.Colorize(strings.Repeat("-", 60), ColorGray))
}

// PrintPrompt imprime o prompt de entrada
func (r *UIRenderer) PrintPrompt() string {
	return r.Colorize("\n ➤ Sua escolha: ", ColorLime)
}

// VisibleLen calcula comprimento visível (sem ANSI codes) - EXPORTADA
func VisibleLen(s string) int {
	ansiRe := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	cleaned := ansiRe.ReplaceAllString(s, "")
	return len(cleaned)
}
