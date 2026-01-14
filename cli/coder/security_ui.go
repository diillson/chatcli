package coder

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type SecurityDecision int

const (
	DecisionRunOnce SecurityDecision = iota
	DecisionAllowAlways
	DecisionDenyOnce
	DecisionDenyForever
)

func PromptSecurityCheck(ctx context.Context, toolName, args string) SecurityDecision {
	_ = exec.Command("stty", "sane").Run()

	purple := "\u001b[35m"
	cyan := "\u001b[36m"
	yellow := "\u001b[33m"
	green := "\u001b[32m"
	red := "\u001b[31m"
	gray := "\u001b[90m"
	bold := "\u001b[1m"
	reset := "\u001b[0m"

	fmt.Println()
	fmt.Println(purple + bold + "[SECURITY CHECK]" + reset)
	fmt.Println(gray + strings.Repeat("-", 60) + reset)

	fmt.Printf(" %s %s: %s\n", yellow+"[1]", bold+"Acao requer aprovacao"+reset, toolName)

	displayArgs := args
	if len(displayArgs) > 200 {
		displayArgs = displayArgs[:200] + "..."
	}
	fmt.Printf(" %s %s\n", cyan+"Params:", displayArgs+reset)

	pattern := GetSuggestedPattern(toolName, args)
	fmt.Printf(" %s %s '%s'\n", gray+"Regra:", "Nenhuma regra encontrada para", pattern+reset)
	fmt.Println(gray + strings.Repeat("-", 60) + reset)

	fmt.Println(bold + "Escolha:" + reset)
	fmt.Printf("  [%s] %s\n", green+"y"+reset, "Sim (uma vez)")
	fmt.Printf("  [%s] %s\n", green+"a"+reset, "ALLOW ALWAYS (Permitir '"+pattern+"' sempre)")
	fmt.Printf("  [%s] %s\n", red+"n"+reset, "Nao (pular)")
	fmt.Printf("  [%s] %s\n", red+"d"+reset, "DENY FOREVER (Bloquear '"+pattern+"' sempre)")

	fmt.Print("\n" + purple + "> " + reset)

	// Usa goroutine para leitura não-bloqueante do ponto de vista do contexto
	resultChan := make(chan string, 1)

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			resultChan <- strings.TrimSpace(strings.ToLower(scanner.Text()))
		} else {
			resultChan <- ""
		}
	}()

	select {
	case <-ctx.Done():
		// Contexto cancelado (ex: Ctrl+C)
		fmt.Println("\n" + red + "[Cancelado]" + reset)
		return DecisionDenyOnce
	case input := <-resultChan:
		switch input {
		case "a", "always":
			return DecisionAllowAlways
		case "n", "no":
			return DecisionDenyOnce
		case "d", "deny":
			return DecisionDenyForever
		default:
			return DecisionRunOnce
		}
	}
}
