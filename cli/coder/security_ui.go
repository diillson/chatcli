package coder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type SecurityDecision int

const (
	DecisionRunOnce SecurityDecision = iota
	DecisionAllowAlways
	DecisionDenyOnce
	DecisionDenyForever
)

// SecurityContext provides optional metadata for richer security prompts.
// When provided, the prompt shows which agent is requesting the action and why.
type SecurityContext struct {
	AgentName string // e.g., "shell", "coder", "tester"
	TaskDesc  string // natural language task description
}

// sttyPath is resolved once at init to avoid PATH manipulation attacks.
var sttyPath = func() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if p, err := exec.LookPath("stty"); err == nil {
		return p
	}
	return ""
}()

// PromptSecurityCheck prompts the user for a security decision (no agent context).
func PromptSecurityCheck(ctx context.Context, toolName, args string) SecurityDecision {
	return PromptSecurityCheckWithContext(ctx, toolName, args, nil)
}

// PromptSecurityCheckWithContext prompts the user with full context about what
// is being attempted, which agent is requesting it, and the parsed command details.
func PromptSecurityCheckWithContext(ctx context.Context, toolName, args string, secCtx *SecurityContext) SecurityDecision {
	if runtime.GOOS != "windows" && sttyPath != "" {
		_ = exec.Command(sttyPath, "sane").Run()
	}

	purple := "\u001b[35m"
	cyan := "\u001b[36m"
	yellow := "\u001b[33m"
	green := "\u001b[32m"
	red := "\u001b[31m"
	gray := "\u001b[90m"
	bold := "\u001b[1m"
	white := "\u001b[37m"
	reset := "\u001b[0m"

	fmt.Println()
	fmt.Println(purple + bold + "╔══════════════════════════════════════════════════════════╗" + reset)
	fmt.Println(purple + bold + "║              🔒 SECURITY CHECK                            ║" + reset)
	fmt.Println(purple + bold + "╚══════════════════════════════════════════════════════════╝" + reset)

	// --- Agent context (parallel mode) ---
	if secCtx != nil && secCtx.AgentName != "" {
		fmt.Printf(" %s🤖 Agent:%s  %s%s%s\n", gray, reset, cyan+bold, secCtx.AgentName, reset)
		if secCtx.TaskDesc != "" {
			taskDisplay := secCtx.TaskDesc
			if len(taskDisplay) > 120 {
				taskDisplay = taskDisplay[:120] + "..."
			}
			fmt.Printf(" %s📋 Tarefa:%s %s%s%s\n", gray, reset, white, taskDisplay, reset)
		}
		fmt.Println(gray + " " + strings.Repeat("─", 58) + reset)
	}

	// --- Parse and display the action in human-readable form ---
	sub, _ := NormalizeCoderArgs(args)
	actionLabel, details := formatActionDetails(sub, args)

	fmt.Printf(" %s⚡ Ação:%s   %s%s%s\n", gray, reset, yellow+bold, actionLabel, reset)
	for _, d := range details {
		fmt.Printf("           %s%s%s\n", cyan, d, reset)
	}

	// --- Policy rule info ---
	pattern := GetSuggestedPattern(toolName, args)
	isExecCmd := pattern == ""

	if isExecCmd {
		fmt.Printf(" %s📜 Regra:%s  %sexec requer aprovação individual%s\n", gray, reset, gray, reset)
	} else {
		fmt.Printf(" %s📜 Regra:%s  %snenhuma regra para '%s'%s\n", gray, reset, gray, pattern, reset)
	}

	fmt.Println(gray + " " + strings.Repeat("─", 58) + reset)

	// --- Choices ---
	fmt.Println(bold + " Escolha:" + reset)
	fmt.Printf("   [%s] %s\n", green+"y"+reset, "Sim, executar (uma vez)")
	if !isExecCmd {
		fmt.Printf("   [%s] %s\n", green+"a"+reset, "Permitir sempre ("+pattern+")")
	}
	fmt.Printf("   [%s] %s\n", red+"n"+reset, "Não, pular")
	if !isExecCmd {
		fmt.Printf("   [%s] %s\n", red+"d"+reset, "Bloquear sempre ("+pattern+")")
	}

	fmt.Print("\n" + purple + " > " + reset)

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
		_ = os.Stdin.SetReadDeadline(time.Now())
		fmt.Println("\n" + red + " [Cancelado]" + reset)
		return DecisionDenyOnce
	case input := <-resultChan:
		if isExecCmd {
			switch input {
			case "n", "no":
				return DecisionDenyOnce
			default:
				return DecisionRunOnce
			}
		}
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

// formatActionDetails parses the raw tool args and returns a human-readable
// action label and detail lines for the security prompt.
func formatActionDetails(subcmd, rawArgs string) (label string, details []string) {
	// Try parsing JSON args for structured display
	var parsed struct {
		Cmd  string          `json:"cmd"`
		Args json.RawMessage `json:"args"`
	}
	var argsMap map[string]interface{}

	if err := json.Unmarshal([]byte(rawArgs), &parsed); err == nil && parsed.Cmd != "" {
		subcmd = parsed.Cmd
		_ = json.Unmarshal(parsed.Args, &argsMap)
	}

	switch subcmd {
	case "exec":
		label = "Executar comando no shell"
		cmd := extractField(argsMap, rawArgs, "cmd", "command")
		if cmd != "" {
			details = append(details, "$ "+cmd)
		}
		if dir := extractField(argsMap, "", "cwd", "dir", "workdir"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	case "test":
		label = "Executar testes"
		cmd := extractField(argsMap, rawArgs, "cmd", "command")
		if cmd != "" {
			details = append(details, "$ "+cmd)
		}
		if dir := extractField(argsMap, "", "dir", "cwd", "workdir"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	case "write":
		label = "Escrever arquivo"
		file := extractField(argsMap, rawArgs, "file", "path", "filepath")
		if file != "" {
			details = append(details, "arquivo: "+file)
		}

	case "patch":
		label = "Modificar arquivo (patch)"
		file := extractField(argsMap, rawArgs, "file", "path", "filepath")
		if file != "" {
			details = append(details, "arquivo: "+file)
		}

	case "read":
		label = "Ler arquivo"
		file := extractField(argsMap, rawArgs, "file", "path", "filepath")
		if file != "" {
			details = append(details, "arquivo: "+file)
		}

	case "search":
		label = "Pesquisar no código"
		term := extractField(argsMap, rawArgs, "term", "pattern", "query")
		if term != "" {
			details = append(details, "termo: "+term)
		}
		if dir := extractField(argsMap, "", "dir"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	case "tree":
		label = "Listar estrutura de diretórios"
		if dir := extractField(argsMap, rawArgs, "dir", "path"); dir != "" {
			details = append(details, "dir: "+dir)
		}

	default:
		if subcmd != "" {
			label = subcmd
		} else {
			label = "Ação desconhecida"
		}
		// Show raw args truncated as fallback
		display := rawArgs
		if len(display) > 150 {
			display = display[:150] + "..."
		}
		details = append(details, display)
	}

	if len(details) == 0 {
		// Fallback: show truncated raw args
		display := rawArgs
		if len(display) > 150 {
			display = display[:150] + "..."
		}
		details = append(details, display)
	}

	return label, details
}

// extractField tries to get a string value from the parsed args map using
// multiple possible key names. Falls back to parsing from raw CLI-style args.
func extractField(argsMap map[string]interface{}, rawArgs string, keys ...string) string {
	// Try from parsed JSON map first
	if argsMap != nil {
		for _, k := range keys {
			if v, ok := argsMap[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}

	// Try CLI-style: --key value
	if rawArgs != "" {
		fields := strings.Fields(rawArgs)
		for _, k := range keys {
			flag := "--" + k
			for i, f := range fields {
				if f == flag && i+1 < len(fields) {
					return fields[i+1]
				}
			}
		}
	}

	return ""
}
