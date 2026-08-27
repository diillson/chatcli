/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Renderização do relatório de versão — usada pelo /version, pela slash
 * tool equivalente e pela flag -v/--version do binário. Substitui a saída
 * antiga (hardcoded em PT, sem menção ao /update e cega ao canal de
 * instalação) por um card no tratamento sóbrio do welcome: régua titulada,
 * grid de indentação 2, canal detectado, convite ao /update e as novidades
 * da release quando há atualização disponível.
 */
package cli

import (
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/ui/kit"
	"github.com/diillson/chatcli/ui/theme"
	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
)

// maxReleaseNoteBullets limita a seção de novidades — o resto fica a um
// clique de distância na página da release.
const maxReleaseNoteBullets = 6

// FormatVersionReport renderiza o card completo de versão. Exportada para o
// main (flag -v/--version) compartilhar exatamente a mesma saída do /version.
func FormatVersionReport(rep version.Report) string {
	anchor := screenWidth()
	info := detectInstallFn()

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(kit.RuleHeader(" "+kit.Bold(i18n.T("version.card.title"), theme.RoleHeader)+" ", "", anchor))
	b.WriteString("\n\n")

	labels := []string{
		i18n.T("version.card.installed"),
		i18n.T("version.card.commit"),
		i18n.T("version.card.channel"),
		i18n.T("version.card.latest"),
	}
	width := 0
	for _, l := range labels {
		if n := kit.VisibleLen(l); n > width {
			width = n
		}
	}
	row := func(label, value string) {
		pad := strings.Repeat(" ", width-kit.VisibleLen(label)+2)
		b.WriteString("  " + colorize(label, ColorCyan) + pad + value + "\n")
	}

	row(labels[0], colorize(installedDisplay(rep.Current.Version), ColorLime+ColorBold))
	if build := buildStamp(rep.Current); build != "" {
		row(labels[1], colorize(build, ColorGray))
	}
	row(labels[2], colorize(methodLabel(info.Method), ColorGray))

	wrapped := func(msg string) {
		b.WriteString("\n")
		for _, ln := range kit.WrapText(msg, anchor-2) {
			b.WriteString("  " + colorize(ln, ColorYellow) + "\n")
		}
	}
	switch {
	case rep.CheckErr != nil:
		wrapped("⚠ " + i18n.T("update.err.check", rep.CheckErr.Error()))
	case rep.Latest == "":
		wrapped(i18n.T("update.check_disabled"))
	default:
		row(labels[3], colorize("v"+rep.Latest, ColorGray))
		b.WriteString("\n")
		if rep.NeedsUpdate {
			b.WriteString(renderUpdateInvite(rep.Latest))
			b.WriteString(renderReleaseNotes(rep, anchor))
		} else {
			b.WriteString("  " + colorize("✓ "+i18n.T("update.uptodate"), ColorGreen) + "\n")
		}
	}
	return b.String()
}

// renderUpdateInvite é o convite de atualização compartilhado pelo /version e
// pela welcome screen: a linha amarela com a versão nova e o /update, mais o
// comando do canal de instalação detectado — o usuário sabe na hora COMO
// atualizar, não só que existe versão nova.
func renderUpdateInvite(latest string) string {
	var b strings.Builder
	b.WriteString("  " + colorize(i18n.T("welcome.update.available", latest), ColorYellow) + "\n")
	if argv := update.CommandForVersion(detectInstallFn().Method, latest); argv != nil {
		b.WriteString("    " + colorize(i18n.T("version.card.channel_cmd", strings.Join(argv, " ")), ColorGray) + "\n")
	}
	return b.String()
}

// renderReleaseNotes monta a seção "novidades" a partir do corpo markdown da
// release. Vazio quando não há notas — chamadores concatenam sem checar.
func renderReleaseNotes(rep version.Report, anchor int) string {
	notes, more := version.ReleaseHighlights(rep.Notes, maxReleaseNoteBullets)
	if len(notes) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(kit.RuleHeader(" "+kit.Bold(i18n.T("version.notes.title", rep.Latest), theme.RoleHeader)+" ", "", anchor))
	b.WriteString("\n\n")

	section := ""
	for _, n := range notes {
		if n.Section != "" && n.Section != section {
			section = n.Section
			b.WriteString("  " + colorize(section, ColorCyan) + "\n")
		}
		lines := kit.WrapText(n.Text, anchor-6)
		for i, ln := range lines {
			if i == 0 {
				b.WriteString("    " + colorize("• ", ColorLime) + colorize(ln, ColorGray) + "\n")
			} else {
				b.WriteString("      " + colorize(ln, ColorGray) + "\n")
			}
		}
	}
	if more > 0 {
		b.WriteString("\n  " + colorize(i18n.T("version.notes.more", more, releasePageURL(rep)), ColorGray) + "\n")
	} else {
		b.WriteString("\n  " + colorize(releasePageURL(rep), ColorGray) + "\n")
	}
	return b.String()
}

// releasePageURL resolve o link exibido ao pé das novidades.
func releasePageURL(rep version.Report) string {
	if rep.ReleaseURL != "" {
		return rep.ReleaseURL
	}
	return "https://github.com/diillson/chatcli/releases/latest"
}

// installedDisplay traduz os sentinelas de build para algo legível; versões
// reais ganham o prefixo v canônico.
func installedDisplay(v string) string {
	switch v {
	case "", "unknown":
		return i18n.T("version.card.value_unknown")
	case "dev":
		return i18n.T("version.card.value_dev")
	default:
		return "v" + strings.TrimPrefix(v, "v")
	}
}

// buildStamp compõe "commit · data" com o que estiver disponível; vazio
// quando o build não carrega carimbo nenhum.
func buildStamp(cur version.VersionInfo) string {
	var parts []string
	if cur.CommitHash != "" && cur.CommitHash != "unknown" {
		parts = append(parts, cur.CommitHash)
	}
	if cur.BuildDate != "" && cur.BuildDate != "unknown" {
		parts = append(parts, cur.BuildDate)
	}
	return strings.Join(parts, " · ")
}
