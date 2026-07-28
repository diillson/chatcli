/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"regexp"
	"strings"
)

// ReleaseNote é um item das notas de release já limpo para exibição em
// terminal: o texto do bullet e a seção (Features, Bug Fixes, …) a que
// pertence, quando o corpo da release a declara.
type ReleaseNote struct {
	Section string
	Text    string
}

var (
	// [texto](url) → texto. O corpo gerado pelo release-please não usa
	// colchetes aninhados, então o casamento não-guloso é suficiente.
	mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// Referência de commit ao fim do bullet — "(abc1234)" — vira ruído no
	// terminal depois que o link é removido. Referências de PR "(#1234)"
	// são preservadas: apontam para algo que o usuário consegue buscar.
	trailingHashRe = regexp.MustCompile(`\s*\(([0-9a-f]{7,40})\)\s*$`)
)

// ReleaseHighlights extrai do corpo markdown de uma release (formato
// release-please/changelog) os bullets prontos para exibição, limitados a
// limit itens. Retorna também quantos bullets ficaram de fora, para o
// chamador oferecer o link da release completa. Função pura; corpo vazio ou
// sem bullets devolve lista vazia.
func ReleaseHighlights(body string, limit int) (notes []ReleaseNote, more int) {
	if limit <= 0 {
		return nil, 0
	}
	section := ""
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			heading := cleanReleaseLine(strings.TrimLeft(line, "# "))
			// O cabeçalho-título "1.162.0 (2026-07-25)" não é uma seção;
			// só rótulos textuais (Features, Bug Fixes, …) interessam.
			if heading != "" && !startsWithDigit(heading) {
				section = heading
			}
			continue
		}
		marker := ""
		for _, m := range []string{"* ", "- ", "+ "} {
			if strings.HasPrefix(line, m) {
				marker = m
				break
			}
		}
		if marker == "" {
			continue
		}
		text := cleanReleaseLine(strings.TrimPrefix(line, marker))
		if text == "" {
			continue
		}
		if len(notes) >= limit {
			more++
			continue
		}
		notes = append(notes, ReleaseNote{Section: section, Text: text})
	}
	return notes, more
}

// cleanReleaseLine remove a marcação markdown que não sobrevive bem em
// terminal: links viram só o texto, negrito perde os asteriscos e a
// referência de commit no fim do bullet é descartada.
func cleanReleaseLine(s string) string {
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "**", "")
	s = trailingHashRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

func startsWithDigit(s string) bool {
	return s != "" && s[0] >= '0' && s[0] <= '9'
}
