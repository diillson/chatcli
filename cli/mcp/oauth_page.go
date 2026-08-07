/*
 * ChatCLI - MCP OAuth loopback callback pages
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The browser lands here at the end of the authorization flow, so this page is
 * the only chatcli UI a user sees outside the terminal. It is deliberately
 * self-contained: the loopback server serves it under a strict CSP with no
 * network access, so every style is inline and there is not a single external
 * asset, script or font to fetch.
 */
package mcp

import (
	"fmt"
	"html/template"
	"strings"
)

// callbackCSP is the response policy for the callback pages. Nothing loads
// from anywhere; inline styles are the single concession, which is what lets
// the page be presentable without a script or an external stylesheet.
const callbackCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'"

// callbackTone selects the accent of a callback page.
type callbackTone string

const (
	toneSuccess callbackTone = "success"
	toneFailure callbackTone = "failure"
)

// callbackPageTemplate renders the loopback callback. Colors follow the
// viewer's system theme, and the layout stays centered and readable on a phone
// as easily as on a desktop — the authorization may well finish on either.
var callbackPageTemplate = template.Must(template.New("callback").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · ChatCLI</title>
<style>
  :root {
    color-scheme: light dark;
    --bg: #f6f7f9; --card: #ffffff; --fg: #101418; --muted: #5b6672;
    --line: #e3e7ec; --accent: #10893e; --shadow: rgba(16, 20, 24, .08);
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0e1116; --card: #161b22; --fg: #e6edf3; --muted: #9aa7b4;
      --line: #263140; --accent: #3fb950; --shadow: rgba(0, 0, 0, .4);
    }
  }
  .failure { --accent: #d1242f; }
  @media (prefers-color-scheme: dark) { .failure { --accent: #f85149; } }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: flex; align-items: center;
    justify-content: center; padding: 24px; background: var(--bg); color: var(--fg);
    font: 15px/1.55 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto,
          "Helvetica Neue", Arial, sans-serif;
  }
  .card {
    width: 100%; max-width: 460px; background: var(--card); border: 1px solid var(--line);
    border-radius: 14px; padding: 32px; text-align: center;
    box-shadow: 0 10px 30px var(--shadow);
  }
  .badge {
    width: 56px; height: 56px; margin: 0 auto 18px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    font-size: 27px; line-height: 1; color: #fff; background: var(--accent);
  }
  h1 { margin: 0 0 10px; font-size: 20px; font-weight: 620; letter-spacing: -.01em; }
  p { margin: 0; color: var(--muted); }
  .detail { margin-top: 10px; font-size: 13.5px; }
  .foot {
    margin-top: 26px; padding-top: 16px; border-top: 1px solid var(--line);
    color: var(--muted); font-size: 12px; letter-spacing: .04em; text-transform: uppercase;
  }
  code {
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
    font-size: 12.5px; background: var(--bg); border: 1px solid var(--line);
    border-radius: 6px; padding: 1px 6px; color: var(--fg);
  }
</style>
</head>
<body>
  <main class="card {{.ToneClass}}">
    <div class="badge" aria-hidden="true">{{.Glyph}}</div>
    <h1>{{.Title}}</h1>
    <p>{{.Message}}</p>
    <p class="detail">{{.Detail}}</p>
    <div class="foot">ChatCLI</div>
  </main>
</body>
</html>
`))

// callbackPage renders one callback page. All values are injected through
// html/template, so a provider-supplied error string can never inject markup.
func callbackPage(tone callbackTone, title, message, detail string) string {
	glyph, toneClass := "✓", ""
	if tone == toneFailure {
		glyph, toneClass = "!", "failure"
	}
	var sb strings.Builder
	data := struct {
		Title, Message, Detail, Glyph, ToneClass string
	}{
		Title:     strings.TrimSpace(title),
		Message:   strings.TrimSpace(message),
		Detail:    strings.TrimSpace(detail),
		Glyph:     glyph,
		ToneClass: toneClass,
	}
	if err := callbackPageTemplate.Execute(&sb, data); err != nil {
		// A template that cannot render its own literal is a programming error,
		// not a runtime condition; degrade to plain text rather than serve an
		// empty body to a user who just finished authorizing.
		return fmt.Sprintf("<!doctype html><meta charset=\"utf-8\"><title>ChatCLI</title><h1>%s</h1><p>%s</p>",
			template.HTMLEscapeString(title), template.HTMLEscapeString(message))
	}
	return sb.String()
}
