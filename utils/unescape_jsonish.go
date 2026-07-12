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

// RepairJSONInvalidEscapes conserta texto JSON-ish em que barras invertidas
// não foram escapadas — o caso clássico é o LLM embutir um path Windows
// ("C:\Users\x") numa string JSON, produzindo escapes inválidos como \U.
//
// A decisão é POR LITERAL DE STRING, tudo-ou-nada: uma string que contém
// qualquer escape inválido é "suja" — o emissor claramente não estava
// escapando — e então TODAS as suas barras são tratadas como literais de
// path e duplicadas, inclusive as que por coincidência formariam escapes
// válidos (\b em "\builder", \t em "\temp\Users"). Preservam-se apenas \"
// (aspas são ilegais em nomes de arquivo Windows, logo é um escape real) e
// \\ (já escapada). Strings totalmente válidas ficam intactas, então um
// "\n" legítimo numa chave irmã sobrevive ao reparo da chave suja.
//
// Retorna o input inalterado quando não há nada a reparar. Limitação
// inerente: uma string SÓ com escapes coincidentemente válidos (ex.:
// "C:\temp") é JSON válido e nunca chega a esta função — o erro segue para o
// chamador, que devolve o problema ao modelo.
func RepairJSONInvalidEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	changed := false

	i := 0
	for i < len(s) {
		c := s[i]
		b.WriteByte(c)
		i++
		if c != '"' {
			continue
		}

		content, end := scanJSONStringLiteral(s, i)
		if hasInvalidJSONEscape(content, end == len(s)) {
			b.WriteString(doublePathBackslashes(content))
			changed = true
		} else {
			b.WriteString(content)
		}
		i = end
		if i < len(s) { // closing quote
			b.WriteByte('"')
			i++
		}
	}

	if !changed {
		return s
	}
	return b.String()
}

// scanJSONStringLiteral returns the content of the string literal starting at
// s[start] (start points just past the opening quote) and the index of its
// closing quote — or len(s) when the literal is unterminated.
func scanJSONStringLiteral(s string, start int) (content string, end int) {
	j := start
	for j < len(s) {
		switch s[j] {
		case '\\':
			j += 2 // skip the escaped character (whatever it is)
			if j > len(s) {
				j = len(s)
			}
		case '"':
			return s[start:j], j
		default:
			j++
		}
	}
	return s[start:], len(s)
}

// hasInvalidJSONEscape reports whether the string-literal content carries any
// escape sequence the JSON grammar rejects. unterminated marks a literal that
// ran off the end of the input (always in need of repair).
func hasInvalidJSONEscape(content string, unterminated bool) bool {
	if unterminated {
		return true
	}
	isHex := func(c byte) bool {
		return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
	}
	for k := 0; k < len(content); k++ {
		if content[k] != '\\' {
			continue
		}
		if k+1 >= len(content) {
			return true
		}
		next := content[k+1]
		switch {
		case next == 'u':
			if k+5 >= len(content) || !isHex(content[k+2]) || !isHex(content[k+3]) || !isHex(content[k+4]) || !isHex(content[k+5]) {
				return true
			}
			k += 5
		case strings.IndexByte(`"\/bfnrt`, next) >= 0:
			k++
		default:
			return true
		}
	}
	return false
}

// doublePathBackslashes rewrites a dirty string literal treating every
// backslash as a literal path separator, preserving only \" (a real quote
// escape — quotes cannot appear in Windows file names) and \\ (already
// escaped).
func doublePathBackslashes(content string) string {
	var b strings.Builder
	b.Grow(len(content) + 8)
	for k := 0; k < len(content); k++ {
		c := content[k]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if k+1 < len(content) && (content[k+1] == '"' || content[k+1] == '\\') {
			b.WriteByte('\\')
			b.WriteByte(content[k+1])
			k++
			continue
		}
		b.WriteString(`\\`)
	}
	return b.String()
}
