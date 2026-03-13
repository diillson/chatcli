/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"strings"
)

// isCoderExecDangerous checks if a @coder exec command contains a dangerous
// shell command. It extracts the actual shell command from the parsed args
// and validates it against the agent's CommandValidator.IsDangerous().
// This is the critical security guard that prevents destructive commands
// from executing through @coder exec even when the policy says "allow".
func (a *AgentMode) isCoderExecDangerous(toolArgs []string) (bool, string) {
	if len(toolArgs) == 0 {
		return false, ""
	}
	sub := strings.ToLower(strings.TrimSpace(toolArgs[0]))
	if sub != "exec" {
		return false, ""
	}
	// Extract the --cmd value from parsed args
	for i, arg := range toolArgs {
		if (arg == "--cmd" || arg == "-cmd" || arg == "--command") && i+1 < len(toolArgs) {
			shellCmd := toolArgs[i+1]
			if shellCmd != "" && a.validator.IsDangerous(shellCmd) {
				return true, shellCmd
			}
			return false, shellCmd
		}
	}
	return false, ""
}

// isCoderArgsMissingRequiredValue verifica se o comando @coder contém flags
// que exigem valor, mas estão sem argumento efetivo.
//
// Isso roda ANTES de executar o plugin, para evitar "flag needs an argument"
// e loops inúteis.
func isCoderArgsMissingRequiredValue(args []string) (bool, string) {
	if len(args) == 0 {
		return false, ""
	}

	sub := strings.ToLower(strings.TrimSpace(args[0]))

	// Flags mínimas obrigatórias por subcomando (as que realmente causam quebra no plugin)
	// OBS: patch: replace é opcional, mas se existir precisa ter valor.
	required := map[string][]string{
		"write":  {"--file", "--content"},
		"search": {"--term"},
		"read":   {"--file"},
		"exec":   {"--cmd"},
		// rollback/clean podem ficar sem required estrito, mas se quiser:
		"rollback": {"--file"},
	}

	if sub == "patch" {
		if hasFlag(args, "--diff") {
			required["patch"] = nil
		} else {
			required["patch"] = []string{"--file", "--search"}
		}
	}

	reqFlags, ok := required[sub]
	if !ok || len(reqFlags) == 0 {
		return false, ""
	}

	// mapeia flag -> encontrado
	found := make(map[string]bool, len(reqFlags))

	// percorre args procurando "--flag value"
	for i := 0; i < len(args); i++ {
		t := args[i]

		for _, rf := range reqFlags {
			if t != rf {
				continue
			}

			// Precisa ter próximo token
			if i+1 >= len(args) {
				return true, rf
			}

			val := strings.TrimSpace(args[i+1])

			// Se veio vazio, ou parece outra flag, ou é placeholder lixo (\ ou \,)
			if val == "" || strings.HasPrefix(val, "-") || isClearlyInvalidCoderValue(val) {
				return true, rf
			}

			found[rf] = true
		}

		// Caso especial: flags opcionais mas que, se presentes, exigem valor.
		// Ex: patch --replace <...>
		if sub == "patch" && t == "--replace" {
			if i+1 >= len(args) {
				return true, "--replace"
			}
			val := strings.TrimSpace(args[i+1])
			if val == "" || strings.HasPrefix(val, "-") || isClearlyInvalidCoderValue(val) {
				return true, "--replace"
			}
		}

		// Caso especial: search --dir exige valor se presente
		if sub == "search" && t == "--dir" {
			if i+1 >= len(args) {
				return true, "--dir"
			}
			val := strings.TrimSpace(args[i+1])
			if val == "" || strings.HasPrefix(val, "-") || isClearlyInvalidCoderValue(val) {
				return true, "--dir"
			}
		}
	}

	// se alguma obrigatória não apareceu
	for _, rf := range reqFlags {
		if !found[rf] {
			return true, rf
		}
	}

	return false, ""
}

// isClearlyInvalidCoderValue identifica valores "lixo" gerados por continuação de linha,
// como "\" ou "\," ou "\" seguido apenas de pontuação.
//
// OBS: isso NÃO tenta validar base64 nem conteúdo real; apenas detecta placeholders
// típicos que o modelo usa quando erra a formatação.
func isClearlyInvalidCoderValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}

	// Caso clássico: "\" sozinho
	if v == `\` {
		return true
	}

	// Caso recorrente: "\," (ou "\;" "\." etc.)
	// Aqui consideramos inválido quando começa com "\" e o resto não contém nenhum caractere
	// "útil" (alfanumérico ou base64 charset).
	if strings.HasPrefix(v, `\`) {
		rest := strings.TrimSpace(strings.TrimPrefix(v, `\`))
		if rest == "" {
			return true
		}

		// Se depois da barra só tiver pontuação (e/ou espaços), é lixo.
		// Permitimos A-Z a-z 0-9 e também + / = (para base64) como "úteis".
		allPunct := true
		for _, r := range rest {
			switch {
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
				allPunct = false
			case r == '+' || r == '/' || r == '=':
				allPunct = false
			default:
				// continua (pontuação / espaços / etc.)
			}
			if !allPunct {
				break
			}
		}
		if allPunct {
			return true
		}
	}

	return false
}

// buildCoderToolCallFixPrompt requests a valid tool_call when a required flag is missing.
func buildCoderToolCallFixPrompt(missingFlag string) string {
	return fmt.Sprintf(
		"ERROR: Your @coder tool_call is missing required flag %s (or its value is invalid/empty).\n\n"+
			"Resend a SINGLE <tool_call> with valid args. Use JSON format (recommended):\n\n"+
			`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"search","args":{"term":"LoginService","dir":"."}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"write","args":{"file":"out.go","content":"BASE64","encoding":"base64"}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"go test ./..."}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"patch","args":{"file":"f.go","search":"old","replace":"new"}}' />`,
		missingFlag,
	)
}

// buildCoderSingleLineArgsEnforcementPrompt enforces single-line args requirement.
func buildCoderSingleLineArgsEnforcementPrompt(originalArgs string) string {
	trimmed := strings.TrimSpace(originalArgs)

	preview := trimmed
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}

	return fmt.Sprintf(
		"ERROR: Your tool_call args contain line breaks, which is NOT allowed.\n\n"+
			"The args attribute MUST be a SINGLE LINE. Use JSON format with single quotes around it:\n\n"+
			`<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`+"\n"+
			`<tool_call name="@coder" args='{"cmd":"exec","args":{"cmd":"go build && go test ./..."}}' />`+"\n\n"+
			"Rules:\n"+
			"1. NEVER use backslash (\\) for line continuation\n"+
			"2. NEVER put real newlines inside args\n"+
			"3. For multiline file content, use base64 encoding\n"+
			"4. Use single quotes around JSON args to avoid escaping issues\n\n"+
			"Your args (truncated):\n---\n%s\n---",
		preview,
	)
}
