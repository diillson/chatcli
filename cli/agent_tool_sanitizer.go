/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

func sanitizeToolCallArgs(rawArgs string, logger *zap.Logger, toolName string, isCoderMode bool) string {
	// 1) Decodifica entidades HTML (&quot; -> ", &#10; -> \n, etc.)
	unescaped := html.UnescapeString(rawArgs)

	// 2) Normaliza quebras de linha (CRLF -> LF)
	normalized := strings.ReplaceAll(unescaped, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	// 3) Processa continuações de linha estilo shell: "\" + espaços opcionais + newline
	//    Substitui por um único espaço (para não colar argumentos)
	normalized = processLineContinuations(normalized)

	// 3b) Se parecer JSON escapado, faz unescape antes de correções de aspas
	if unescaped, ok := utils.MaybeUnescapeJSONishArgs(normalized); ok {
		normalized = unescaped
	}

	// 4) Remove "\ " (barra + espaço) fora de aspas que não faz sentido como escape
	//    Comum quando a IA erra a formatação: --search \ "valor"
	normalized = removeBogusBackslashSpace(normalized)

	// 4b) Corrige aspas desbalanceadas com barra final (ex. --search "\)
	if fixed, changed := fixUnbalancedQuotesWithTrailingBackslash(normalized); changed {
		if logger != nil {
			logger.Debug("Corrigidas aspas desbalanceadas com barra final",
				zap.String("tool", toolName))
		}
		normalized = fixed
	}

	// 5) Remove barras invertidas finais pendentes fora de aspas
	if fixed, changed := trimTrailingBackslashesOutsideQuotes(normalized); changed {
		if logger != nil {
			logger.Debug("Removida barra invertida final pendente",
				zap.String("tool", toolName))
		}
		normalized = fixed
	}

	// 6) Normaliza espaços múltiplos (mas preserva dentro de aspas)
	normalized = normalizeSpacesOutsideQuotes(normalized)

	// 7) Correções semânticas específicas para @coder
	if isCoderMode && strings.EqualFold(strings.TrimSpace(toolName), "@coder") {
		if fixed, changed := fixDanglingBackslashArgsForCoderTool(normalized); changed {
			if logger != nil {
				logger.Debug("Aplicada correção semântica para @coder",
					zap.String("tool", toolName))
			}
			normalized = fixed
		}
	}

	return strings.TrimSpace(normalized)
}

// tryCompactJSON attempts to compact a multiline string that contains valid JSON.
// This handles the common case where AI models send pretty-printed JSON args.
// Returns the compacted single-line JSON string, or empty string if not valid JSON.
func tryCompactJSON(input string) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) == 0 {
		return ""
	}

	// If it starts with { or [ it might be JSON - try to compact it
	if trimmed[0] == '{' || trimmed[0] == '[' {
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(trimmed)); err == nil {
			return buf.String()
		}
	}

	// Not valid JSON - try collapsing newlines to spaces for CLI-style args
	collapsed := strings.Join(strings.Fields(trimmed), " ")
	if !hasAnyNewline(collapsed) {
		return collapsed
	}

	return ""
}

// processLineContinuations processa "\" + whitespace + newline de forma robusta
// Respeita aspas e mantém o conteúdo que vem depois do newline
func processLineContinuations(input string) string {
	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	inSingle := false
	inDouble := false
	i := 0

	for i < n {
		ch := runes[i]

		// Controle de aspas (não escapadas)
		if ch == '\'' && !inDouble && (i == 0 || runes[i-1] != '\\') {
			inSingle = !inSingle
			result.WriteRune(ch)
			i++
			continue
		}
		if ch == '"' && !inSingle && (i == 0 || runes[i-1] != '\\') {
			inDouble = !inDouble
			result.WriteRune(ch)
			i++
			continue
		}

		// Detecta barra invertida seguida de newline (continuação de linha)
		if ch == '\\' {
			j := i + 1

			// Pula espaços opcionais até o newline
			for j < n && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\r') {
				j++
			}

			// Se encontrar newline, é continuação de linha
			if j < n && runes[j] == '\n' {
				// Pula o newline
				j++

				// Pula espaços depois do newline
				for j < n && (runes[j] == ' ' || runes[j] == '\t' || runes[j] == '\r') {
					j++
				}

				// Dentro de aspas: remove a barra e o newline, concatena direto
				// Fora de aspas: substitui por espaço
				if !inSingle && !inDouble {
					result.WriteRune(' ')
				}
				// Dentro de aspas: não escreve nada (concatena)

				// Avança o índice para depois do newline e espaços
				i = j - 1 // -1 porque o loop faz i++
				i++
				continue
			}
		}

		// Não é continuação de linha, mantém o caractere
		result.WriteRune(ch)
		i++
	}

	return result.String()
}

// removeBogusBackslashSpace remove "\ " fora de aspas que não faz sentido
// Exemplo: --search \ "valor" -> --search "valor"
//
// IMPORTANT: When input looks like JSON (starts with { or [), escaped quotes \"
// are valid JSON escapes and MUST be preserved. Only strip bogus backslashes
// in CLI-style args.
func removeBogusBackslashSpace(input string) string {
	// If the input looks like JSON, don't strip \" — it's a valid JSON escape.
	// Stripping it breaks commands like: {"cmd":"exec","args":{"cmd":"docker --format \"{{.V}}\""}}
	trimmed := strings.TrimSpace(input)
	isJSON := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')

	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	inSingle := false
	inDouble := false
	i := 0

	for i < n {
		ch := runes[i]

		// Controle de aspas
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			result.WriteRune(ch)
			i++
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			result.WriteRune(ch)
			i++
			continue
		}

		// Fora de aspas, detecta "\ " ou "\t" que não é escape válido
		if ch == '\\' && !inSingle && !inDouble {
			// Olha o próximo caractere
			if i+1 < n {
				next := runes[i+1]

				// Se for espaço ou tab, é provavelmente erro da IA
				if next == ' ' || next == '\t' {
					// Pula a barra, mas mantém o espaço
					i++
					continue
				}

				// Se for outra barra (\\), é escape válido - mantém ambas
				if next == '\\' {
					result.WriteRune(ch)
					result.WriteRune(next)
					i += 2
					continue
				}

				// Se for aspa (\" ou \'), em JSON é escape válido — preservar.
				// Em CLI-style args pode ser erro da IA — remover barra.
				if next == '"' || next == '\'' {
					if isJSON {
						// JSON: \" é escape válido, preservar a barra
						result.WriteRune(ch)
						result.WriteRune(next)
						i += 2
						continue
					}
					// CLI: provavelmente erro da IA, remover a barra
					i++
					continue
				}
			}
		}

		result.WriteRune(ch)
		i++
	}

	return result.String()
}

// normalizeSpacesOutsideQuotes reduz espaços múltiplos para um só, fora de aspas
func normalizeSpacesOutsideQuotes(input string) string {
	var result strings.Builder
	runes := []rune(input)
	n := len(runes)

	inSingle := false
	inDouble := false
	lastWasSpace := false
	i := 0

	for i < n {
		ch := runes[i]

		// Controle de aspas
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			result.WriteRune(ch)
			lastWasSpace = false
			i++
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			result.WriteRune(ch)
			lastWasSpace = false
			i++
			continue
		}

		// Dentro de aspas, preserva tudo
		if inSingle || inDouble {
			result.WriteRune(ch)
			lastWasSpace = false
			i++
			continue
		}

		// Fora de aspas, normaliza espaços
		if ch == ' ' || ch == '\t' {
			if !lastWasSpace {
				result.WriteRune(' ')
				lastWasSpace = true
			}
			// Se já teve espaço, pula este
			i++
			continue
		}

		result.WriteRune(ch)
		lastWasSpace = false
		i++
	}

	return result.String()
}

// fixUnbalancedQuotesWithTrailingBackslash corrige o caso onde a IA gera aspas desbalanceadas terminando com barra
// Exemplo: --search "\ -> --search "
// Exemplo: exec --cmd 'curl ... {\ -> exec --cmd 'curl ... {'
func fixUnbalancedQuotesWithTrailingBackslash(input string) (string, bool) {
	trimmed := strings.TrimRight(input, " \t\r\n")
	if trimmed == "" {
		return input, false
	}

	// Verifica se termina com barra
	if !strings.HasSuffix(trimmed, `\`) {
		return input, false
	}

	// Conta aspas para ver se estão balanceadas
	doubleQuotes := 0
	singleQuotes := 0
	inEscape := false

	for _, ch := range trimmed {
		if inEscape {
			inEscape = false
			continue
		}

		if ch == '\\' {
			inEscape = true
			continue
		}

		if ch == '"' {
			doubleQuotes++
		} else if ch == '\'' {
			singleQuotes++
		}
	}

	// Se aspas desbalanceadas E termina com barra, remove a barra e fecha as aspas
	if doubleQuotes%2 != 0 || singleQuotes%2 != 0 {
		fixed := strings.TrimSuffix(trimmed, `\`)

		// Fecha as aspas desbalanceadas
		if doubleQuotes%2 != 0 {
			fixed += `"`
		}
		if singleQuotes%2 != 0 {
			fixed += `'`
		}

		return fixed, true
	}

	return input, false
}

// fixDanglingBackslashArgsForCoderTool corrige casos comuns onde o modelo gera:
//
//	patch ... --search \
//	patch ... --search \,
//	write ... --content \
//
// sem realmente colocar o conteúdo na mesma linha/argumento.
// Isso quebra o flag parser do plugin (@coder) porque --search/--content exigem argumento.
//
// A função tenta:
// - remover argumento inválido "\" (ou "\" + pontuação) após flags de conteúdo
// - se houver um próximo token, usar ele como argumento real
//
// Retorna (novoTexto, mudou?).
func fixDanglingBackslashArgsForCoderTool(argLine string) (string, bool) {
	// Primeiro, tokeniza de forma robusta (respeita aspas), reaproveitando o mesmo
	// comportamento do splitToolArgsMultiline, mas sem retornar erro aqui.
	toks, err := splitToolArgsMultilineLenient(argLine)
	if err != nil || len(toks) == 0 {
		return argLine, false
	}

	// Flags do @coder que exigem valor imediato (no seu plugin):
	// - patch: --search, --replace (replace é opcional, mas quando aparece precisa valor)
	// - write: --content
	needsValue := map[string]bool{
		"--search":        true,
		"--replace":       true,
		"--content":       true,
		"--file":          true,
		"--cmd":           true,
		"--dir":           true,
		"--term":          true,
		"--encoding":      true,
		"--diff":          true,
		"--diff-encoding": true,
		"--start":         true,
		"--end":           true,
		"--head":          true,
		"--tail":          true,
		"--max-bytes":     true,
		"--context":       true,
		"--max-results":   true,
		"--glob":          true,
		"--timeout":       true,
		"--path":          true,
		"--limit":         true,
		"--pattern":       true,
	}

	isClearlyInvalidValue := func(v string) bool {
		v = strings.TrimSpace(v)
		if v == "" {
			return true
		}
		// Caso clássico: "\" sozinho
		if v == `\` {
			return true
		}
		// Casos vistos: "\," "\;" "\." etc (barra + pontuação, sem payload)
		if strings.HasPrefix(v, `\`) {
			rest := strings.TrimSpace(strings.TrimPrefix(v, `\`))
			// Se depois da barra só tiver pontuação (e/ou estiver vazio), é lixo
			if rest == "" {
				return true
			}
			allPunct := true
			for _, r := range rest {
				if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '/' || r == '=' {
					allPunct = false
					break
				}
			}
			if allPunct {
				return true
			}
		}
		return false
	}

	changed := false
	out := make([]string, 0, len(toks))

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		out = append(out, t)

		if !needsValue[t] {
			continue
		}

		// Se é flag que precisa valor, olhe o próximo token
		if i+1 >= len(toks) {
			continue
		}

		val := toks[i+1]
		if !isClearlyInvalidValue(val) {
			// ok, mantém
			continue
		}

		// Valor inválido detectado: remove-o e tenta usar o próximo como valor real
		changed = true

		// Descarta o valor inválido
		i++ // pula o token inválido

		// Se existir um próximo token, ele vira o valor
		if i+1 < len(toks) {
			next := toks[i+1]
			out = append(out, next)
			i++ // consumiu o próximo também
		} else {
			return "", false
		}
	}

	rebuilt := strings.Join(out, " ")
	if rebuilt == strings.TrimSpace(argLine) {
		return argLine, changed
	}
	return rebuilt, changed
}

// splitToolArgsMultilineLenient é um tokenizer leniente para permitir correções.
// Ele tenta fazer split parecido com splitToolArgsMultiline, mas:
// - não retorna erro em escape pendente no final: trata "\" final como literal
// - se aspas não forem balanceadas, retorna erro
func splitToolArgsMultilineLenient(s string) ([]string, error) {
	var args []string
	var buf strings.Builder

	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if buf.Len() > 0 {
			args = append(args, buf.String())
			buf.Reset()
		}
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') && !inSingle && !inDouble {
			flush()
			continue
		}

		buf.WriteByte(ch)
	}

	// leniente: se terminou "escaped", consideramos "\" literal
	if escaped {
		buf.WriteByte('\\')
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("aspas não balanceadas nos argumentos")
	}

	flush()
	return args, nil
}

// parseToolArgsWithJSON aceita args no formato JSON (object/array) e
// faz fallback para splitToolArgsMultiline no formato CLI tradicional.
func parseToolArgsWithJSON(argLine string) ([]string, error) {
	if args, ok, err := parseToolArgsMaybeJSON(argLine); ok {
		return args, err
	}
	return splitToolArgsMultiline(argLine)
}

// parseToolArgsMaybeJSON tenta interpretar os args como JSON.
// Retorna (args, true, nil) se parseou como JSON válido.
// Retorna (nil, true, err) se parecia JSON mas falhou.
// Retorna (nil, false, nil) se não parecia JSON.
func parseToolArgsMaybeJSON(argLine string) ([]string, bool, error) {
	trimmed := strings.TrimSpace(argLine)
	if trimmed == "" {
		return nil, false, nil
	}
	if unescaped, ok := utils.MaybeUnescapeJSONishArgs(trimmed); ok {
		trimmed = unescaped
	}
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return nil, false, nil
	}

	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return nil, true, err
	}

	switch v := payload.(type) {
	case []any:
		argv := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("argv JSON deve conter apenas strings")
			}
			argv = append(argv, s)
		}
		return argv, true, nil
	case map[string]any:
		return buildArgvFromJSONMap(v)
	default:
		return nil, true, fmt.Errorf("JSON inválido para args")
	}
}

func buildArgvFromJSONMap(m map[string]any) ([]string, bool, error) {
	getString := func(key string) string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	cmd := getString("cmd")
	if cmd == "" {
		if argvRaw, ok := m["argv"]; ok {
			if argvSlice, ok := argvRaw.([]any); ok && len(argvSlice) > 0 {
				if first, ok := argvSlice[0].(string); ok {
					cmd = first
				}
			}
		}
	}

	if argvRaw, ok := m["argv"]; ok {
		if argvSlice, ok := argvRaw.([]any); ok && len(argvSlice) > 0 {
			argv := make([]string, 0, len(argvSlice))
			for _, item := range argvSlice {
				s, ok := item.(string)
				if !ok {
					return nil, true, fmt.Errorf("argv JSON deve conter apenas strings")
				}
				argv = append(argv, s)
			}
			if cmd != "" && (len(argv) == 0 || argv[0] != cmd) {
				argv = append([]string{cmd}, argv...)
			}
			return argv, true, nil
		}
	}

	if cmd == "" {
		// No "cmd" or "argv" found, but the JSON has other fields.
		// Pass the entire JSON as a single argument so the plugin can parse
		// it directly. This supports flat argument formats like
		// {"query":"..."} from native tool calling without requiring the
		// {"cmd":"search","args":{...}} wrapper.
		raw, err := json.Marshal(m)
		if err != nil {
			return nil, true, fmt.Errorf("JSON args requer campo 'cmd' ou 'argv'")
		}
		return []string{string(raw)}, true, nil
	}

	argv := []string{cmd}
	argsMap := map[string]any{}

	if raw, ok := m["args"]; ok {
		if mm, ok := raw.(map[string]any); ok {
			for k, v := range mm {
				if k == "command" {
					argsMap["cmd"] = v
					continue
				}
				argsMap[k] = v
			}
		}
	}
	if raw, ok := m["flags"]; ok {
		if mm, ok := raw.(map[string]any); ok {
			for k, v := range mm {
				if k == "command" {
					argsMap["cmd"] = v
					continue
				}
				argsMap[k] = v
			}
		}
	}

	keys := make([]string, 0, len(argsMap))
	for k := range argsMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		appendFlagValue(&argv, k, argsMap[k])
	}

	if posRaw, ok := m["positional"]; ok {
		appendPositionals(&argv, posRaw)
	}
	if posRaw, ok := m["_"]; ok {
		appendPositionals(&argv, posRaw)
	}

	return argv, true, nil
}

func normalizeFlagName(name string) string {
	if strings.HasPrefix(name, "-") {
		return name
	}
	return "--" + name
}

func appendPositionals(argv *[]string, raw any) {
	if raw == nil {
		return
	}
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				*argv = append(*argv, s)
			}
		}
	case []string:
		*argv = append(*argv, v...)
	case string:
		*argv = append(*argv, v)
	}
}

func appendFlagValue(argv *[]string, key string, value any) {
	if value == nil {
		return
	}
	flag := normalizeFlagName(key)
	switch v := value.(type) {
	case bool:
		if v {
			*argv = append(*argv, flag)
		}
	case string:
		*argv = append(*argv, flag, v)
	case float64, float32, int, int64, int32, uint, uint64, uint32:
		*argv = append(*argv, flag, fmt.Sprint(v))
	case []any:
		for _, item := range v {
			appendFlagValue(argv, key, item)
		}
	case []string:
		for _, item := range v {
			*argv = append(*argv, flag, item)
		}
	default:
		b, err := json.Marshal(v)
		if err != nil {
			*argv = append(*argv, flag, fmt.Sprint(v))
		} else {
			*argv = append(*argv, flag, string(b))
		}
	}
}

// removeXMLTags remove tags conhecidas do texto, mantendo o conteúdo.
// Não mexe em markdown nem em conteúdo legítimo.
// Nota: Go regexp (RE2) não suporta backreferences (\1), então usamos stripXMLTagBlock
// para tags paired e aqui removemos apenas self-closing e orphan tags.
func removeXMLTags(text string) string {
	// Remove self-closing tool_call/agent_call tags (e.g. <tool_call ... />)
	text = regexp.MustCompile(`(?i)<(?:tool_call|agent_call)\b[^>]*/\s*>`).ReplaceAllString(text, "")
	// Remove any remaining orphan opening/closing tags
	text = regexp.MustCompile(`(?i)</?\s*(reasoning|explanation|thought|plan|summary|final_summary|action|action_type|command|step)\s*>`).ReplaceAllString(text, "")
	return text
}

// splitToolArgsMultiline faz split de argv estilo shell, mas com suporte a multilinha.
// Regras:
// - separa por whitespace (inclui \n) quando NÃO estiver dentro de aspas
// - suporta aspas simples e duplas
// - permite newline dentro de aspas (vira parte do mesmo argumento)
// - "\" funciona como escape fora de aspas simples (ex: \" ou \n literal etc.)
// - não interpreta sequências como \n => newline; mantém literal \ + n (quem interpreta é o plugin, se quiser)
// - retorna erro se aspas não balanceadas ou escape pendente no final
func splitToolArgsMultiline(s string) ([]string, error) {
	var args []string
	var buf strings.Builder

	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if buf.Len() > 0 {
			args = append(args, buf.String())
			buf.Reset()
		}
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			buf.WriteByte(ch)
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}

		if (ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r') && !inSingle && !inDouble {
			flush()
			continue
		}

		buf.WriteByte(ch)
	}

	if escaped {
		return nil, fmt.Errorf("escape pendente no fim dos argumentos (terminou com '\\')")
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("aspas não balanceadas nos argumentos")
	}

	flush()
	return args, nil
}

// trimTrailingBackslashesOutsideQuotes remove barras invertidas finais (\\) que ficarem
// NO FINAL DO TEXTO e que estejam fora de aspas.
// Isso evita o caso clássico em que o modelo termina uma linha com "\" e o parser entende como escape pendente.
// Retorna (novoTexto, mudou?).
func trimTrailingBackslashesOutsideQuotes(s string) (string, bool) {
	orig := s

	// Normaliza finais
	t := strings.TrimRight(s, " \t\r\n")
	if t == "" {
		return orig, false
	}

	// Para saber se o último "\" está fora de aspas, precisamos fazer um scan simples.
	// Vamos remover "\" finais repetidos enquanto:
	// - o texto termina com "\"
	// - e esse "\" está fora de aspas (single/double)
	for {
		t2 := strings.TrimRight(t, " \t\r\n")
		if !strings.HasSuffix(t2, `\`) {
			break
		}

		// Verifica se o "\" final está fora de aspas
		if !isLastBackslashOutsideQuotes(t2) {
			// se está dentro de aspas, não mexe (é conteúdo)
			break
		}

		// remove a "\" final
		t2 = strings.TrimSuffix(t2, `\`)
		t = strings.TrimRight(t2, " \t\r\n")
	}

	if t == strings.TrimRight(orig, " \t\r\n") {
		return orig, false
	}

	// preserva a parte original "antes" (mas retornamos trimmed, pois era um erro estrutural)
	return t, true
}

// isLastBackslashOutsideQuotes detecta se o último caractere "\" está fora de aspas.
// Supõe que s já termina com "\".
func isLastBackslashOutsideQuotes(s string) bool {
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && !inSingle {
			// escape em modo normal/aspas duplas
			escaped = true
			continue
		}

		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
	}

	// Se termina com "\" e estamos fora de aspas, então é o caso problemático.
	return !inSingle && !inDouble
}

// extractXMLTagContent extrai o conteúdo de <tag>...</tag> (case-insensitive).
// Retorna ("", false) se não existir.
func extractXMLTagContent(s, tag string) (string, bool) {
	pat := fmt.Sprintf(`(?is)<%s>\s*(.*?)\s*</%s>`, regexp.QuoteMeta(tag), regexp.QuoteMeta(tag))
	re := regexp.MustCompile(pat)
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// stripAgentCallTags remove tags <agent_call .../> e <agent_call ...>...</agent_call> do texto.
var agentCallTagRe = regexp.MustCompile(`(?is)<agent_call\b[^>]*/\s*>|<agent_call\b[^>]*>.*?</agent_call>`)

func stripAgentCallTags(s string) string {
	return agentCallTagRe.ReplaceAllString(s, "")
}

// stripToolCallTags remove tags <tool_call .../> e <tool_call ...>...</tool_call> do texto.
var toolCallTagRe = regexp.MustCompile(`(?is)<tool_call\b[^>]*/\s*>|<tool_call\b[^>]*>.*?</tool_call>`)

func stripToolCallTags(s string) string {
	return toolCallTagRe.ReplaceAllString(s, "")
}

// stripXMLTagBlock remove completamente o bloco <tag>...</tag> do texto.
func stripXMLTagBlock(s, tag string) string {
	pat := fmt.Sprintf(`(?is)<%s>\s*.*?\s*</%s>`, regexp.QuoteMeta(tag), regexp.QuoteMeta(tag))
	re := regexp.MustCompile(pat)
	return re.ReplaceAllString(s, "")
}

// normalizeShellLineContinuations lida com quebras de linha escapadas (\ + Enter).
// - Fora de aspas: Substitui por espaço (para separar argumentos).
// - Dentro de aspas: Remove a sequência (para unir a string, igual ao bash).
func normalizeShellLineContinuations(input string) string {
	var result strings.Builder
	chars := []rune(input)
	length := len(chars)

	inDoubleQuote := false
	inSingleQuote := false

	for i := 0; i < length; i++ {
		char := chars[i]

		if char == '\\' {
			// Verifica se é continuação de linha (\ seguido de newline)
			j := i + 1
			// Pula espaços em branco opcionais entre a barra e o enter
			for j < length && (chars[j] == ' ' || chars[j] == '\t' || chars[j] == '\r') {
				j++
			}

			if j < length && chars[j] == '\n' {
				// É UMA CONTINUAÇÃO DE LINHA (\ + Enter)

				if !inDoubleQuote && !inSingleQuote {
					// Caso 1: Fora de aspas (ex: exec --cmd \ \n echo)
					// Substitui por espaço para não colar os argumentos
					result.WriteRune(' ')
				}
				// Caso 2: Dentro de aspas (ex: --content "\ \n code")
				// Não fazemos nada (não escrevemos espaço), efetivamente removendo
				// a barra e o enter, unindo o conteúdo limpo.

				i = j // Avança o índice para pular a barra e o enter
				continue
			}

			// Não é quebra de linha: é uma barra literal ou escape
			result.WriteRune(char)

			// Se for um escape de aspa (ex: \" ou \'), consumimos o próximo char
			// para não confundir a máquina de estados das aspas.
			if i+1 < length {
				nextChar := chars[i+1]
				if (inDoubleQuote && nextChar == '"') || (inSingleQuote && nextChar == '\'') {
					result.WriteRune(nextChar)
					i++
				}
			}
			continue
		}

		// Alternar estado das aspas
		if char == '"' && !inSingleQuote {
			inDoubleQuote = !inDoubleQuote
		} else if char == '\'' && !inDoubleQuote {
			inSingleQuote = !inSingleQuote
		}

		result.WriteRune(char)
	}
	return result.String()
}
