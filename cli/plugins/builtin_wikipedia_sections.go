/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * builtin_wikipedia_sections.go — reading past the intro. The extracts
 * API returns the whole article as plain text with "== Heading ==" lines;
 * one fetch yields the table of contents, a single section, or the capped
 * full text, so the model can read the one section that answers the
 * question instead of paying for the page.
 */
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/textfold"
)

// wikipediaSection is one heading of a plain-text extract.
type wikipediaSection struct {
	Index   int    // 1-based, in page order
	Level   int    // 2 for "== X ==", 3 for "=== X ===" …
	Heading string // trimmed heading text
	Body    string // text until the next heading of any level
}

// wikipediaFullExtract fetches the whole plain-text article.
func wikipediaFullExtract(ctx context.Context, lang, title string) (string, string, error) {
	params := url.Values{}
	params.Set("action", "query")
	params.Set("format", "json")
	params.Set("titles", title)
	params.Set("prop", "extracts")
	params.Set("explaintext", "true")
	params.Set("exlimit", "1")
	params.Set("redirects", "1")
	body, err := wikipediaGet(ctx, lang, params)
	if err != nil {
		return "", "", err
	}
	var parsed struct {
		Query struct {
			Pages map[string]struct {
				Title   string `json:"title"`
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", fmt.Errorf("decoding article response: %w", err)
	}
	for _, page := range parsed.Query.Pages {
		if strings.TrimSpace(page.Extract) != "" {
			return page.Title, page.Extract, nil
		}
	}
	return "", "", nil
}

// splitWikipediaSections parses "== Heading ==" lines. The text before the
// first heading is the intro and comes back separately.
func splitWikipediaSections(extract string) (intro string, sections []wikipediaSection) {
	var cur *wikipediaSection
	var introLines, bodyLines []string
	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(bodyLines, "\n"))
			sections = append(sections, *cur)
		}
		bodyLines = nil
	}
	for _, line := range strings.Split(extract, "\n") {
		t := strings.TrimSpace(line)
		if level, heading, ok := parseWikiHeading(t); ok {
			flush()
			cur = &wikipediaSection{Index: len(sections) + 1, Level: level, Heading: heading}
			continue
		}
		if cur == nil {
			introLines = append(introLines, line)
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	flush()
	return strings.TrimSpace(strings.Join(introLines, "\n")), sections
}

// parseWikiHeading recognizes "== X ==" (level = number of '=').
func parseWikiHeading(t string) (int, string, bool) {
	if len(t) < 5 || !strings.HasPrefix(t, "==") || !strings.HasSuffix(t, "==") {
		return 0, "", false
	}
	level := 0
	for level < len(t) && t[level] == '=' {
		level++
	}
	end := len(t)
	for end > 0 && t[end-1] == '=' {
		end--
	}
	if end <= level || level < 2 {
		return 0, "", false
	}
	heading := strings.TrimSpace(t[level:end])
	if heading == "" {
		return 0, "", false
	}
	return level, heading, true
}

// wikipediaSections lists the headings of an article.
func wikipediaSections(ctx context.Context, lang, title string) (string, error) {
	pageTitle, extract, err := wikipediaFullExtract(ctx, lang, title)
	if err != nil {
		return "", err
	}
	if extract == "" {
		return i18n.T("plugins.wikipedia.no_article", title), nil
	}
	intro, sections := splitWikipediaSections(extract)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %d section(s), %d chars total\n", pageTitle, len(sections), len(extract))
	fmt.Fprintf(&b, "intro: %d chars\n", len(intro))
	for _, s := range sections {
		fmt.Fprintf(&b, "%3d. %s%s (%d chars)\n", s.Index, strings.Repeat("  ", s.Level-2), s.Heading, len(s.Body))
	}
	b.WriteString(`Read one with {"read":"` + pageTitle + `","section":"<heading or index>"}.`)
	return b.String(), nil
}

// wikipediaReadBody returns one section (by heading or 1-based index) or
// the whole article, capped at maxChars with a note on what was cut.
func wikipediaReadBody(ctx context.Context, lang, title, section string, maxChars int) (string, error) {
	pageTitle, extract, err := wikipediaFullExtract(ctx, lang, title)
	if err != nil {
		return "", err
	}
	if extract == "" {
		return i18n.T("plugins.wikipedia.no_article", title), nil
	}
	if maxChars <= 0 {
		maxChars = wikipediaDefaultMaxChars
	}
	want := strings.TrimSpace(section)
	if want == "" {
		return capWikipediaText("# "+pageTitle, extract, maxChars), nil
	}
	_, sections := splitWikipediaSections(extract)
	if idx, convErr := strconv.Atoi(want); convErr == nil {
		if idx < 1 || idx > len(sections) {
			return "", fmt.Errorf("section %d is out of range: %s has %d sections (list them with sections:true)", idx, pageTitle, len(sections))
		}
		s := sections[idx-1]
		return capWikipediaText(fmt.Sprintf("# %s — %s", pageTitle, s.Heading), sectionWithChildren(sections, idx-1), maxChars), nil
	}
	for i, s := range sections {
		if strings.EqualFold(s.Heading, want) {
			return capWikipediaText(fmt.Sprintf("# %s — %s", pageTitle, s.Heading), sectionWithChildren(sections, i), maxChars), nil
		}
	}
	var near []string
	for _, s := range sections {
		if strings.Contains(strings.ToLower(s.Heading), strings.ToLower(want)) {
			near = append(near, s.Heading)
		}
	}
	if len(near) == 1 {
		for i, s := range sections {
			if s.Heading == near[0] {
				return capWikipediaText(fmt.Sprintf("# %s — %s", pageTitle, s.Heading), sectionWithChildren(sections, i), maxChars), nil
			}
		}
	}
	if len(near) > 1 {
		return "", fmt.Errorf("section %q matches several headings in %s: %s", want, pageTitle, strings.Join(near, "; "))
	}
	return "", fmt.Errorf("no section %q in %s (list them with sections:true)", want, pageTitle)
}

// sectionWithChildren returns a section's body followed by its deeper
// subsections, up to the next heading of the same or higher level.
func sectionWithChildren(sections []wikipediaSection, i int) string {
	var b strings.Builder
	b.WriteString(sections[i].Body)
	for j := i + 1; j < len(sections) && sections[j].Level > sections[i].Level; j++ {
		fmt.Fprintf(&b, "\n\n%s %s\n%s", strings.Repeat("#", sections[j].Level), sections[j].Heading, sections[j].Body)
	}
	return b.String()
}

// capWikipediaText prefixes a header and cuts the body at maxChars with a
// note that says so and how to see more.
func capWikipediaText(header, body string, maxChars int) string {
	body = strings.TrimSpace(body)
	if len(body) <= maxChars {
		return header + "\n\n" + body
	}
	cut := textfold.Cut(body, maxChars)
	return fmt.Sprintf("%s\n\n%s\n\n[... %d of %d chars shown; raise max_chars (up to %d) or read a single section via sections:true ...]", header, cut, len(cut), len(body), wikipediaMaxChars)
}
