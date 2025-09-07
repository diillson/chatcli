/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// MultilineInputManager gerencia entrada de múltiplas linhas
type MultilineInputManager struct {
	isMultilineMode bool
	buffer          []string
}

// NewMultilineInputManager cria um novo gerenciador de entrada multilinha
func NewMultilineInputManager() *MultilineInputManager {
	return &MultilineInputManager{
		isMultilineMode: false,
		buffer:          []string{},
	}
}

// restoreTerminal restaura o terminal para o modo normal
func (m *MultilineInputManager) restoreTerminal() {
	if runtime.GOOS == "windows" {
		return
	}

	// Restaura o terminal para modo "sane" (normal)
	cmd := exec.Command("stty", "sane")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()

	// Habilita echo para garantir que o usuário veja o que digita
	cmd = exec.Command("stty", "echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
}

// IsMultilineMode retorna se está em modo multilinha
func (m *MultilineInputManager) IsMultilineMode() bool {
	return m.isMultilineMode
}

// StartMultilineMode com texto de cancelamento corrigido (/mcancel)
func (m *MultilineInputManager) StartMultilineMode() {
	m.isMultilineMode = true
	m.buffer = []string{}

	// Restaurar o terminal ANTES de ler (sai do raw-mode do go-prompt)
	m.restoreTerminal()

	fmt.Println("\n" + colorize("📝 MODO MULTILINHA ATIVADO", ColorCyan))
	fmt.Println(colorize("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", ColorGray))
	fmt.Println("• Digite ou cole seu texto (múltiplas linhas permitidas)")
	fmt.Println("• Digite " + colorize("/mcancel", ColorPurple) + " para cancelar")
	fmt.Println("• Use " + colorize("Ctrl+D", ColorYellow) + " (Linux/Mac) ou " + colorize("Ctrl+Z + Enter", ColorYellow) + " (Windows) para finalizar")
	fmt.Println(colorize("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━", ColorGray))
	fmt.Println()

}

// GetMultilineInput garante restauração do terminal e lida corretamente com /mcancel
func (m *MultilineInputManager) GetMultilineInput() (string, bool) {
	// Garante que o terminal está em modo normal
	m.restoreTerminal()

	reader := bufio.NewReader(os.Stdin)
	var lines []string
	lineNumber := 1

	for {
		fmt.Printf(colorize(fmt.Sprintf("[%d] ", lineNumber), ColorGray))
		_ = os.Stdout.Sync()

		line, err := reader.ReadString('\n')
		if err != nil {
			// EOF
			fmt.Println("\n" + colorize("✓ Entrada finalizada (EOF)", ColorGreen))
			break
		}

		line = strings.TrimRight(line, "\r\n")

		if line == "/mcancel" {
			fmt.Println(colorize("\n✗ Entrada cancelada", ColorPurple))
			m.isMultilineMode = false
			m.restoreTerminal()
			// drena quaisquer bytes residuais do buffer
			for reader.Buffered() > 0 {
				_, _ = reader.ReadByte()
			}
			return "", false
		}

		lines = append(lines, line)
		lineNumber++
	}

	m.isMultilineMode = false
	// Garante restaurar terminal ao sair por EOF
	m.restoreTerminal()

	if len(lines) == 0 {
		return "", false
	}

	result := strings.Join(lines, "\n")

	// Preview opcional
	if len(lines) > 3 {
		fmt.Println(colorize("\n━━━ PREVIEW ━━━", ColorGray))
		preview := lines
		maxPreviewLines := 5
		if len(preview) > maxPreviewLines {
			for i := 0; i < maxPreviewLines-1; i++ {
				fmt.Println(colorize("│ ", ColorGray) + preview[i])
			}
			fmt.Println(colorize(fmt.Sprintf("│ ... (%d linhas restantes)", len(preview)-maxPreviewLines+1), ColorGray))
		} else {
			for _, l := range preview {
				fmt.Println(colorize("│ ", ColorGray) + l)
			}
		}
		fmt.Println(colorize("━━━━━━━━━━━━━━━", ColorGray))
	}

	fmt.Println()
	return result, true

}

// ClearBuffer limpa o buffer interno
func (m *MultilineInputManager) ClearBuffer() {
	m.buffer = []string{}
}

// Reset reseta completamente o estado do gerenciador
func (m *MultilineInputManager) Reset() {
	m.isMultilineMode = false
	m.buffer = []string{}
	m.restoreTerminal()
}
