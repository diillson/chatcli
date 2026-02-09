package utils

import (
	"strconv"
	"strings"
)

// MaybeUnescapeJSONishArgs tenta desfazer escape de JSON embutido em uma string
// quando o conteúdo parece ser um objeto/array JSON com aspas escapadas.
// Retorna (valor, true) se conseguiu unescape e o resultado parece JSON.
func MaybeUnescapeJSONishArgs(input string) (string, bool) {
	orig := strings.TrimSpace(input)
	if orig == "" {
		return input, false
	}

	cur := orig

	// Tenta remover aspas externas e/ou unescape múltiplo (até 2x)
	for i := 0; i < 2; i++ {
		if len(cur) >= 2 {
			if (cur[0] == '"' && cur[len(cur)-1] == '"') || (cur[0] == '\'' && cur[len(cur)-1] == '\'') {
				if unq, err := strconv.Unquote(cur); err == nil {
					cur = strings.TrimSpace(unq)
				}
			}
		}

		looksJSON := strings.HasPrefix(cur, "{") || strings.HasPrefix(cur, "[")
		if looksJSON && (strings.Contains(cur, "\\\"") || strings.Contains(cur, "\\'")) {
			if unq, err := strconv.Unquote(`"` + cur + `"`); err == nil {
				cur = strings.TrimSpace(unq)
				continue
			}
		}
		break
	}

	if strings.HasPrefix(cur, "{") || strings.HasPrefix(cur, "[") {
		if cur != orig {
			return cur, true
		}
	}

	return input, false
}
