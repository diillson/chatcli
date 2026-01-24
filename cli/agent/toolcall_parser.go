package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// ToolCall representa uma invocação de ferramenta parseada do texto.
type ToolCall struct {
	Name string
	Args string
	Raw  string
}

// ParseToolCalls extrai tool calls do texto.
// Suporta:
//   - <tool_call name="@x" args="..." />
//   - <tool_call args="..." name="@x"></tool_call>
//   - args com aspas simples ou duplas
//   - args multiline
//   - múltiplos tool_calls no mesmo texto
func ParseToolCalls(text string) ([]ToolCall, error) {
	// Captura a tag de abertura <tool_call ...> e, se existir, permite um fechamento correspondente.
	// Group 1: atributos dentro da tag de abertura.
	re := regexp.MustCompile(`(?is)<tool_call\s+([^>]*?)(?:/?>)(?:.*?</tool_call>)?`)
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil, nil
	}

	var calls []ToolCall
	for _, m := range matches {
		full := text[m[0]:m[1]]
		attrText := text[m[2]:m[3]]

		name, err := extractAttr(attrText, "name")
		if err != nil {
			return nil, fmt.Errorf("tool_call sem atributo name válido: %w", err)
		}
		args, _ := extractAttr(attrText, "args") // args pode ser vazio

		calls = append(calls, ToolCall{
			Name: strings.TrimSpace(name),
			Args: args,
			Raw:  full,
		})
	}

	return calls, nil
}

func extractAttr(attrText, key string) (string, error) {
	// key="..." ou key='...'
	// Suporta aspas escapadas dentro do valor: "((?:[^"\\]|\\.)*)"
	pat := fmt.Sprintf(`(?is)\b%s\s*=\s*("((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')`, regexp.QuoteMeta(key))
	re := regexp.MustCompile(pat)

	sub := re.FindStringSubmatch(attrText)
	if len(sub) == 0 {
		return "", fmt.Errorf("atributo %q não encontrado", key)
	}

	// sub[2] => aspas duplas, sub[3] => aspas simples
	val := sub[3]
	if sub[2] != "" {
		val = sub[2]
	}

	// Normaliza: remove barra invertida seguida de quebra de linha (escape de multilinha)
	// Ex: "comando P\n        args" -> "comando args"
	reNorm := regexp.MustCompile(`\\[\s\n]+`)
	val = reNorm.ReplaceAllString(val, " ")

	return val, nil
}
