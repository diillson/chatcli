/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * dom.go — how the session reads and addresses the page.
 *
 * One JavaScript prelude gives every action the same view of the document:
 * elements are found across the top document, every open shadow root and
 * every same-origin iframe, and their geometry is translated into viewport
 * coordinates of the top frame so real mouse events land on them. The page
 * scan behind snapshot/find/text stamps interactive elements with
 * data-chatcli-ref (the [n] refs click/type take), labels them the way an
 * accessibility tree would (aria-label, <label for>, placeholder, alt…),
 * reports their state (value, checked, disabled, expanded…) and never shows
 * the value of a password field.
 */
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/pkg/textfold"
)

// domPreludeJS defines __cc, the DOM helper every action script uses.
// roots(): the top document, open shadow roots and same-origin iframe
// documents, in document order. all(sel)/one(sel): querySelectorAll across
// those roots. rect(el): bounding box in top-frame viewport coordinates.
// visible(el): painted with a non-empty box. frameOf(el): the iframe
// element hosting el's document ("" for the top document).
const domPreludeJS = `const __cc = (() => {
  const roots = (root, out) => {
    out.push(root);
    const all = root.querySelectorAll('*');
    for (const el of all) {
      if (el.shadowRoot) roots(el.shadowRoot, out);
      if (el.tagName === 'IFRAME' || el.tagName === 'FRAME') {
        try { const d = el.contentDocument; if (d && d.documentElement) roots(d, out); } catch (e) {}
      }
    }
    return out;
  };
  const all = (sel) => {
    const out = [];
    for (const r of roots(document, [])) { try { out.push(...r.querySelectorAll(sel)); } catch (e) {} }
    return out;
  };
  const one = (sel) => { const a = all(sel); return a.length ? a[0] : null; };
  const rect = (el) => {
    const r = el.getBoundingClientRect();
    let x = r.left, y = r.top;
    let win = el.ownerDocument.defaultView;
    while (win && win !== window && win.frameElement) {
      const fr = win.frameElement.getBoundingClientRect();
      x += fr.left; y += fr.top;
      win = win.parent;
    }
    return {x: x, y: y, w: r.width, h: r.height};
  };
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    if (!(r.width > 0 && r.height > 0)) return false;
    const cs = el.ownerDocument.defaultView.getComputedStyle(el);
    return cs.visibility !== 'hidden' && cs.display !== 'none';
  };
  const frameOf = (el) => {
    const win = el.ownerDocument.defaultView;
    if (!win || win === window || !win.frameElement) return '';
    const f = win.frameElement;
    return (f.getAttribute('src') || f.id || f.name || 'iframe').slice(0, 80);
  };
  return {roots, all, one, rect, visible, frameOf};
})();
`

// domScript wraps body in a function scope that defines __cc, so the helper
// never lands in the page's global scope (a top-level const would make the
// next evaluation fail with "already been declared").
func domScript(body string) string {
	return "(() => {\n" + domPreludeJS + "\nreturn (" + body + ");\n})()"
}

// interactiveSelector lists what counts as an interactive element: native
// controls, ARIA widgets, editable regions and the explicit focus/click
// hooks sites hang on plain divs.
const interactiveSelector = `a[href], button, input, select, textarea, summary, [role="button"], [role="link"], [role="tab"], [role="menuitem"], [role="menuitemcheckbox"], [role="menuitemradio"], [role="checkbox"], [role="radio"], [role="switch"], [role="option"], [role="combobox"], [role="textbox"], [role="searchbox"], [role="slider"], [contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"], [onclick], [tabindex="0"]`

// pageScanJS is the scan behind snapshot, find and text. It takes a JSON
// options object (maxItems, query) and returns the page identity, the
// stamped interactive listing, the visible text (iframes appended), the
// cross-origin frames it could not read, the find matches and the bot
// challenge markers detectChallenge evaluates.
const pageScanJS = `(() => {
  const opts = %s;
  const clean = (s) => (s || '').replace(/\s+/g, ' ').trim();
  const labelOf = (el) => {
    const doc = el.ownerDocument;
    let t = clean(el.getAttribute('aria-label'));
    if (!t) {
      const ids = el.getAttribute('aria-labelledby');
      if (ids) t = clean(ids.split(/\s+/).map(id => { const n = doc.getElementById(id); return n ? n.textContent : ''; }).join(' '));
    }
    if (!t && el.labels && el.labels.length) t = clean(Array.from(el.labels).map(l => l.textContent).join(' '));
    if (!t && el.id && window.CSS && CSS.escape) { const l = doc.querySelector('label[for="' + CSS.escape(el.id) + '"]'); if (l) t = clean(l.textContent); }
    if (!t) t = clean(el.innerText);
    if (!t) t = clean(el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('alt'));
    if (!t) { const img = el.querySelector('img[alt], svg title'); if (img) t = clean(img.getAttribute('alt') || img.textContent); }
    if (!t && el.tagName === 'INPUT' && ['submit', 'button', 'reset'].includes((el.type || '').toLowerCase())) t = clean(el.value);
    if (!t) t = clean(el.getAttribute('name') || el.id);
    return t.slice(0, 80);
  };
  const quote = (s) => JSON.stringify(clean(s).slice(0, 60));
  const stateOf = (el) => {
    const tag = el.tagName.toLowerCase();
    const parts = [];
    if (tag === 'input') {
      const type = (el.type || 'text').toLowerCase();
      parts.push('type=' + type);
      if (type === 'checkbox' || type === 'radio') parts.push(el.checked ? 'checked' : 'unchecked');
      else if (type === 'password') { if (el.value) parts.push('value=<hidden>'); }
      else if (!['submit', 'button', 'reset', 'file', 'image', 'hidden'].includes(type) && el.value) parts.push('value=' + quote(el.value));
    } else if (tag === 'textarea') {
      if (el.value) parts.push('value=' + quote(el.value));
    } else if (tag === 'select') {
      const o = el.options[el.selectedIndex];
      if (o) parts.push('selected=' + quote(o.text));
    } else if (el.isContentEditable) {
      parts.push('editable');
      const v = clean(el.innerText);
      if (v) parts.push('value=' + quote(v));
    }
    const role = el.getAttribute('role');
    if (role && role !== tag) parts.push('role=' + role);
    if (el.disabled || el.getAttribute('aria-disabled') === 'true') parts.push('disabled');
    if (el.required) parts.push('required');
    const ac = el.getAttribute('aria-checked');
    if (ac !== null && tag !== 'input') parts.push(ac === 'true' ? 'checked' : 'unchecked');
    const ae = el.getAttribute('aria-expanded');
    if (ae !== null) parts.push(ae === 'true' ? 'expanded' : 'collapsed');
    if (el.getAttribute('aria-selected') === 'true') parts.push('selected');
    const fr = __cc.frameOf(el);
    if (fr) parts.push('in iframe ' + JSON.stringify(fr));
    if (tag === 'a') parts.push('-> ' + (el.getAttribute('href') || '').slice(0, 120));
    return parts;
  };
  const els = __cc.all(%s).filter(__cc.visible);
  const query = (opts.query || '').toLowerCase();
  const items = [];
  const matches = [];
  els.forEach((el, i) => {
    el.setAttribute('data-chatcli-ref', String(i + 1));
    if (i >= opts.maxItems && !query) return;
    const tag = el.tagName.toLowerCase();
    const parts = stateOf(el);
    const line = '[' + (i + 1) + '] <' + tag + '> ' + labelOf(el) + (parts.length ? ' ' + parts.join(' ') : '');
    if (i < opts.maxItems) items.push(line);
    if (query && line.toLowerCase().includes(query)) matches.push(line);
  });
  const cross = [];
  for (const f of __cc.all('iframe, frame')) {
    let ok = false;
    try { ok = !!(f.contentDocument && f.contentDocument.documentElement); } catch (e) {}
    if (!ok && __cc.visible(f)) cross.push((f.getAttribute('src') || f.getAttribute('title') || f.id || 'iframe').slice(0, 120));
  }
  let text = (document.body ? document.body.innerText : '') || '';
  for (const r of __cc.roots(document, [])) {
    if (r === document || !r.body || !r.defaultView || !r.defaultView.frameElement) continue;
    const t = (r.body.innerText || '').trim();
    if (t) text += '\n\n--- iframe ' + JSON.stringify(__cc.frameOf(r.body)) + ' ---\n' + t;
  }
  text = text.replace(/\n{3,}/g, '\n\n');
  const has = (sel) => { try { return !!document.querySelector(sel); } catch (e) { return false; } };
  const markers = {
    cloudflare: has('#challenge-form, #challenge-running, #cf-challenge-running, .cf-browser-verification, #turnstile-wrapper, script[src*="challenges.cloudflare.com"], #cf-wrapper #cf-error-details'),
    recaptcha: has('.g-recaptcha, iframe[src*="recaptcha"]'),
    hcaptcha: has('.h-captcha, iframe[src*="hcaptcha.com"]'),
    perimeterx: has('#px-captcha, script[src*="px-cdn.net"], script[src*="px-cloud.net"]'),
    datadome: has('iframe[src*="captcha-delivery.com"], script[src*="captcha-delivery.com"], #datadome-captcha'),
    imperva: has('iframe[src*="_Incapsula_Resource"], #main-iframe[src*="Incapsula"]'),
    akamai: has('script[src*="/_sec/cp_challenge/"], #sec-cpt-if'),
    awswaf: has('#captcha-container script[src*="awswaf"], script[src*="captcha.awswaf.com"]')
  };
  return JSON.stringify({
    title: document.title,
    url: location.href,
    interactive: items,
    more: Math.max(0, els.length - opts.maxItems),
    crossFrames: cross.slice(0, 10),
    matches: matches.slice(0, opts.maxMatches || 40),
    text: text,
    markers: markers
  });
})()`

// pageScan is the parsed result of pageScanJS.
type pageScan struct {
	Title       string          `json:"title"`
	URL         string          `json:"url"`
	Interactive []string        `json:"interactive"`
	More        int             `json:"more"`
	CrossFrames []string        `json:"crossFrames"`
	Matches     []string        `json:"matches"`
	Text        string          `json:"text"`
	Markers     map[string]bool `json:"markers"`
}

// maxSnapshotItems bounds the interactive listing of one snapshot.
const maxSnapshotItems = 150

// scanPage runs pageScanJS with the given options.
func (s *Session) scanPage(ctx context.Context, maxItems int, query string) (*pageScan, error) {
	if maxItems <= 0 {
		maxItems = maxSnapshotItems
	}
	opts, _ := json.Marshal(map[string]interface{}{"maxItems": maxItems, "query": query, "maxMatches": 40})
	selJSON, _ := json.Marshal(interactiveSelector)
	raw, err := s.evalRaw(ctx, domScript(fmt.Sprintf(pageScanJS, opts, selJSON)))
	if err != nil {
		return nil, err
	}
	var scan pageScan
	if err := json.Unmarshal([]byte(raw), &scan); err != nil {
		return nil, fmt.Errorf("parse page scan: %w", err)
	}
	return &scan, nil
}

// Model-facing snapshot fragments.
const (
	snapMoreFmt        = "… %d more interactive element(s) not listed — `find` locates one by its text, `snapshot --max` does not raise this cap\n"
	snapCrossFramesFmt = "\nCross-origin iframe(s) on the page (their content is not readable from here; a captcha or embedded login lives there — use `show` for the user): %s\n"
	snapTextTruncFmt   = "\n… (page text truncated at %d bytes — `text --offset %d` continues, `find` searches the whole page)"
	snapChallengeFmt   = "⚠ Bot challenge detected (%s): this page is the interstitial, not the content you asked for. Automation cannot pass it — `show` the window so the user solves it, then `wait --unblocked` (the URL usually does not change) and read the page again.\n\n"
	textRangeFmt       = "\n[text bytes %d–%d of %d; `--offset %d` continues]"
	textRangeEndFmt    = "\n[text bytes %d–%d of %d; end of page]"
	findHeaderFmt      = "Page: %s\nURL: %s\n\n"
	findNoMatchFmt     = "No element or text matches %q on this page (%d interactive elements, %d bytes of text). Try a shorter or different word; `text` reads the page in full."
	findElementsFmt    = "Interactive elements matching %q (use the [n] ref with click/type):\n"
	findTextFmt        = "\nText matches for %q (%d shown of %d):\n"
)

// renderSnapshot formats a scan as the model-facing snapshot: challenge
// warning, identity, interactive listing, cross-origin frames and the
// visible text window [offset, offset+maxBytes).
func renderSnapshot(scan *pageScan, maxBytes, offset int) string {
	if maxBytes <= 0 {
		maxBytes = defaultSnapshotBytes
	}
	var b strings.Builder
	if vendor := detectChallenge(scan); vendor != "" {
		fmt.Fprintf(&b, snapChallengeFmt, vendor)
	}
	fmt.Fprintf(&b, "Page: %s\nURL: %s\n", scan.Title, scan.URL)
	if len(scan.Interactive) > 0 {
		b.WriteString("\nInteractive elements (use the [n] ref with click/type):\n")
		for _, it := range scan.Interactive {
			b.WriteString(it)
			b.WriteString("\n")
		}
		if scan.More > 0 {
			fmt.Fprintf(&b, snapMoreFmt, scan.More)
		}
	}
	if len(scan.CrossFrames) > 0 {
		fmt.Fprintf(&b, snapCrossFramesFmt, strings.Join(scan.CrossFrames, ", "))
	}
	text := strings.TrimSpace(scan.Text)
	window, _, end, total := textWindow(text, offset, maxBytes)
	if window != "" {
		b.WriteString("\nPage text:\n")
		b.WriteString(window)
		if end < total {
			fmt.Fprintf(&b, snapTextTruncFmt, maxBytes, end)
		}
	}
	return b.String()
}

// textWindow returns the [offset, offset+maxBytes) slice of text aligned to
// rune boundaries, with the effective start, end and total byte counts.
func textWindow(text string, offset, maxBytes int) (window string, start, end, total int) {
	total = len(text)
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	for offset > 0 && offset < total && !utf8.RuneStart(text[offset]) {
		offset++
	}
	rest := text[offset:]
	if maxBytes > 0 && len(rest) > maxBytes {
		rest = textfold.Cut(rest, maxBytes)
	}
	return rest, offset, offset + len(rest), total
}

// Text returns the visible text of the page (or of one element when target
// is set) from byte offset, at most maxBytes (0 = default), with a range
// footer that tells the model how to continue.
func (s *Session) Text(ctx context.Context, target string, offset, maxBytes int) (string, error) {
	span := s.beginAction(ctx, "text")
	v, err := s.doText(ctx, target, offset, maxBytes)
	span.EndErr(err)
	return v, err
}

// doText is Text under its dashboard node.
func (s *Session) doText(ctx context.Context, target string, offset, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = defaultSnapshotBytes
	}
	var text string
	if strings.TrimSpace(target) == "" {
		scan, err := s.scanPage(ctx, 1, "")
		if err != nil {
			return "", err
		}
		text = scan.Text
	} else {
		selJSON, _ := json.Marshal(resolveSelector(target))
		out, err := s.evalRaw(ctx, domScript(fmt.Sprintf(`(() => { const el = __cc.one(%s); if (!el) return 'NOTFOUND'; return 'OK' + (el.innerText || el.textContent || '').replace(/\n{3,}/g, '\n\n'); })()`, selJSON)))
		if err != nil {
			return "", err
		}
		if out == "NOTFOUND" {
			return "", fmt.Errorf(errNoElementFmt, target)
		}
		text = strings.TrimPrefix(out, "OK")
	}
	text = strings.TrimSpace(text)
	window, start, end, total := textWindow(text, offset, maxBytes)
	if end < total {
		return window + fmt.Sprintf(textRangeFmt, start, end, total, end), nil
	}
	return window + fmt.Sprintf(textRangeEndFmt, start, end, total), nil
}

// errNoElementFmt is the shared "no such element" error.
const errNoElementFmt = "no element matches %q — run snapshot first and use one of its [n] refs"

// Find searches the page for query (case-insensitive): interactive elements
// whose listing line contains it, stamped with fresh refs, and the places it
// occurs in the visible text with surrounding context.
func (s *Session) Find(ctx context.Context, query string, maxMatches int) (string, error) {
	span := s.beginAction(ctx, "find")
	v, err := s.doFind(ctx, query, maxMatches)
	span.EndErr(err)
	return v, err
}

// doFind is Find under its dashboard node.
func (s *Session) doFind(ctx context.Context, query string, maxMatches int) (string, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return "", fmt.Errorf("find: empty query")
	}
	if maxMatches <= 0 {
		maxMatches = 20
	}
	scan, err := s.scanPage(ctx, maxSnapshotItems, q)
	if err != nil {
		return "", err
	}
	snippets, totalHits := textMatches(scan.Text, q, maxMatches)
	var b strings.Builder
	if vendor := detectChallenge(scan); vendor != "" {
		fmt.Fprintf(&b, snapChallengeFmt, vendor)
	}
	fmt.Fprintf(&b, findHeaderFmt, scan.Title, scan.URL)
	if len(scan.Matches) == 0 && totalHits == 0 {
		fmt.Fprintf(&b, findNoMatchFmt, q, len(scan.Interactive)+scan.More, len(scan.Text))
		return b.String(), nil
	}
	if len(scan.Matches) > 0 {
		fmt.Fprintf(&b, findElementsFmt, q)
		for _, m := range scan.Matches {
			b.WriteString(m)
			b.WriteString("\n")
		}
	}
	if totalHits > 0 {
		fmt.Fprintf(&b, findTextFmt, q, len(snippets), totalHits)
		for _, sn := range snippets {
			b.WriteString(sn)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// textMatches finds every case-insensitive occurrence of q in text and
// returns up to max snippets "…context [match] context… (at byte N)" plus
// the total count.
func textMatches(text, q string, maxSnippets int) (snippets []string, total int) {
	const around = 90
	pos := 0
	for {
		start, end := textfold.IndexIgnoreCase(text[pos:], q)
		if start < 0 {
			break
		}
		start, end = pos+start, pos+end
		total++
		if len(snippets) < maxSnippets {
			from := start - around
			if from < 0 {
				from = 0
			}
			for from > 0 && !utf8.RuneStart(text[from]) {
				from--
			}
			to := end + around
			if to > len(text) {
				to = len(text)
			}
			for to < len(text) && !utf8.RuneStart(text[to]) {
				to++
			}
			snip := strings.ReplaceAll(text[from:start], "\n", " ") + "[" + text[start:end] + "]" + strings.ReplaceAll(text[end:to], "\n", " ")
			snippets = append(snippets, fmt.Sprintf("…%s… (at byte %d)", strings.TrimSpace(snip), start))
		}
		pos = end
		if pos >= len(text) {
			break
		}
	}
	return snippets, total
}

// challengeVendors maps a marker key onto the vendor name the model sees.
var challengeVendors = map[string]string{
	"cloudflare": "Cloudflare",
	"perimeterx": "PerimeterX/HUMAN",
	"datadome":   "DataDome",
	"imperva":    "Imperva/Incapsula",
	"akamai":     "Akamai Bot Manager",
	"awswaf":     "AWS WAF",
	"recaptcha":  "reCAPTCHA",
	"hcaptcha":   "hCaptcha",
}

// interstitialTitles are page titles that are the challenge itself.
var interstitialTitles = []string{
	"just a moment", "um moment", "un moment", "einen moment", "attention required",
	"verificando se você é humano", "confirme que você é humano", "verifique que você é humano",
	"access denied", "pardon our interruption", "verify you are human", "verifying you are human",
	"are you a robot", "bot verification", "please verify", "security check", "ddos-guard",
	"human verification", "request blocked", "403 forbidden", "radware bot manager",
}

// interstitialTexts are phrases the body of a challenge page carries.
var interstitialTexts = []string{
	"checking your browser", "verify you are human", "verifying you are human", "needs to review the security of your connection",
	"performance & security by cloudflare", "press & hold", "access to this page has been denied",
	"incapsula incident id", "reference #", "enable javascript and cookies to continue",
	"please complete the security check", "ddos protection by", "confirm you are a human",
	"we need to verify that you are not a robot", "unusual traffic from your computer network",
	"confirme que você é humano", "verificando se você é humano", "verifique que você é humano",
	"revisar a segurança da sua conexão", "ative o javascript e os cookies",
}

// challengeTextBudget is how much of the page text is examined for the
// interstitial phrases: a real article may quote them; an interstitial is
// short.
const challengeTextBudget = 3000

// detectChallenge reports the vendor of a bot challenge the page is showing
// instead of its content, or "" when the page looks like real content. A
// vendor marker or an interstitial title is decisive; a captcha widget
// alone counts only on a page with almost no other text (a captcha on a
// normal form is just a form field).
func detectChallenge(scan *pageScan) string {
	if scan == nil {
		return ""
	}
	title := strings.ToLower(strings.TrimSpace(scan.Title))
	text := strings.ToLower(scan.Text)
	short := len(strings.TrimSpace(scan.Text)) < challengeTextBudget
	head := text
	if len(head) > challengeTextBudget {
		head = head[:challengeTextBudget]
	}
	titled := false
	for _, t := range interstitialTitles {
		if strings.Contains(title, t) {
			titled = true
			break
		}
	}
	phrased := false
	for _, p := range interstitialTexts {
		if strings.Contains(head, p) {
			phrased = true
			break
		}
	}
	keys := make([]string, 0, len(scan.Markers))
	for k, on := range scan.Markers {
		if on {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch k {
		case "recaptcha", "hcaptcha":
			if short || titled {
				return challengeVendors[k]
			}
		default:
			if short || titled || phrased {
				return challengeVendors[k]
			}
		}
	}
	if titled && (short || phrased) {
		return "access check"
	}
	if phrased && short {
		return "bot verification"
	}
	return ""
}

// ClickOptions tunes a click: how many times (2 = double, 3 = triple, the
// way text gets selected) and with which mouse button.
type ClickOptions struct {
	Count  int
	Button string // left (default), right, middle
}

// clickTargetJS locates the element, scrolls it into view and hit-tests its
// center: when the center is painted by the element itself (or a child, or
// the iframe hosting it) real mouse events will land on it; otherwise it is
// covered and only a programmatic click reaches it.
const clickTargetJS = `(() => {
  const el = __cc.one(%s);
  if (!el) return 'NOTFOUND';
  el.scrollIntoView({block: 'center', inline: 'center'});
  const r = __cc.rect(el);
  const x = r.x + r.w / 2, y = r.y + r.h / 2;
  const inView = r.w > 0 && r.h > 0 && x >= 0 && y >= 0 && x <= window.innerWidth && y <= window.innerHeight;
  let hit = null;
  if (inView) {
    let doc = document, p = document.elementFromPoint(x, y);
    while (p && (p.tagName === 'IFRAME' || p.tagName === 'FRAME')) {
      let d = null; try { d = p.contentDocument; } catch (e) {}
      if (!d) break;
      const fr = p.getBoundingClientRect();
      doc = d; p = d.elementFromPoint(x - fr.left, y - fr.top);
    }
    hit = p;
  }
  const ok = hit && (hit === el || el.contains(hit) || (hit.shadowRoot && hit.shadowRoot.contains(el)) || (el.shadowRoot && el.shadowRoot.contains(hit)) || (hit.getRootNode && hit.getRootNode().host && el.contains(hit.getRootNode().host)));
  if (ok) return JSON.stringify({mode: 'mouse', x: x, y: y});
  const cover = hit ? ('<' + hit.tagName.toLowerCase() + (hit.id ? '#' + hit.id : '') + (hit.className && typeof hit.className === 'string' ? '.' + hit.className.trim().split(/\s+/).slice(0, 2).join('.') : '') + '>') : 'nothing';
  el.click();
  return JSON.stringify({mode: 'js', cover: cover});
})()`

// clickResult is what clickTargetJS reports.
type clickResult struct {
	Mode  string  `json:"mode"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Cover string  `json:"cover"`
}

// ClickWith clicks the element with real mouse events at its center (the
// page sees mousedown/mouseup/click like a person's click, with the
// requested count and button); when the center is covered by another
// element the click is dispatched programmatically instead and the
// returned note says by what.
func (s *Session) ClickWith(ctx context.Context, target string, opts ClickOptions) (note string, err error) {
	span := s.beginAction(ctx, "click")
	note, err = s.doClickWith(ctx, target, opts)
	span.EndErr(err)
	return note, err
}

// doClickWith is ClickWith under its dashboard node.
func (s *Session) doClickWith(ctx context.Context, target string, opts ClickOptions) (string, error) {
	selJSON, _ := json.Marshal(resolveSelector(target))
	out, err := s.evalRaw(ctx, domScript(fmt.Sprintf(clickTargetJS, selJSON)))
	if err != nil {
		return "", err
	}
	if strings.Contains(out, "NOTFOUND") {
		return "", fmt.Errorf(errNoElementFmt, target)
	}
	var res clickResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return "", fmt.Errorf("parse click result: %w", err)
	}
	if res.Mode != "mouse" {
		if opts.Count > 1 || (opts.Button != "" && opts.Button != "left") {
			return "", fmt.Errorf("the center of %q is covered by %s, so a %s click cannot reach it; scroll or close what covers it first", target, res.Cover, describeClick(opts))
		}
		return fmt.Sprintf("clicked %s programmatically — its center is covered by %s", target, res.Cover), nil
	}
	count := opts.Count
	if count <= 0 {
		count = 1
	}
	button := strings.ToLower(strings.TrimSpace(opts.Button))
	if button == "" {
		button = "left"
	}
	switch button {
	case "left", "right", "middle":
	default:
		return "", fmt.Errorf("click: unknown mouse button %q (left, right or middle)", opts.Button)
	}
	if _, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{"type": "mouseMoved", "x": res.X, "y": res.Y}); err != nil {
		return "", err
	}
	for i := 1; i <= count; i++ {
		for _, kind := range []string{"mousePressed", "mouseReleased"} {
			if _, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
				"type": kind, "x": res.X, "y": res.Y, "button": button, "clickCount": i,
			}); err != nil {
				return "", err
			}
		}
	}
	return "", nil
}

// describeClick names a click for error messages.
func describeClick(opts ClickOptions) string {
	var parts []string
	switch opts.Count {
	case 2:
		parts = append(parts, "double")
	case 3:
		parts = append(parts, "triple")
	}
	if opts.Button != "" && opts.Button != "left" {
		parts = append(parts, opts.Button+"-button")
	}
	if len(parts) == 0 {
		return "mouse"
	}
	return strings.Join(parts, " ")
}

// typeTargetJS resolves the element and reports how to type into it: a
// value-bearing control (input/textarea, value set through the native
// setter so frameworks see the change), an editable region (contenteditable
// or role=textbox, text inserted through the browser's input pipeline so
// editors see beforeinput/input like a keystroke) — and whether it is a
// password field, so the caller never echoes what was typed.
const typeTargetJS = `(() => {
  const el = __cc.one(%s);
  if (!el) return 'NOTFOUND';
  const text = %s, append = %t, submit = %t;
  el.scrollIntoView({block: 'center'});
  el.focus();
  const tag = el.tagName.toLowerCase();
  const password = tag === 'input' && (el.type || '').toLowerCase() === 'password';
  if (tag === 'input' || tag === 'textarea') {
    const proto = tag === 'input' ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value');
    const value = append ? (el.value || '') + text : text;
    if (setter && setter.set) { setter.set.call(el, value); } else { el.value = value; }
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
    if (submit) {
      el.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', keyCode: 13, bubbles: true}));
      if (el.form) { el.form.requestSubmit ? el.form.requestSubmit() : el.form.submit(); }
    }
    return JSON.stringify({kind: 'value', password: password});
  }
  if (!append) {
    const win = el.ownerDocument.defaultView;
    const sel = win.getSelection();
    if (sel) { sel.selectAllChildren(el); }
  } else {
    const win = el.ownerDocument.defaultView;
    const sel = win.getSelection();
    if (sel) { sel.selectAllChildren(el); sel.collapseToEnd(); }
  }
  return JSON.stringify({kind: 'editable', password: false});
})()`

// typeResult is what typeTargetJS reports.
type typeResult struct {
	Kind     string `json:"kind"`
	Password bool   `json:"password"`
}

// TypeWith types text into the element: inputs and textareas get their
// value set (replaced, or appended with append), editable regions get the
// text inserted through the browser's input pipeline. submit presses Enter
// (and submits an input's form). It reports whether the field was a
// password field so the caller keeps the text out of the transcript.
func (s *Session) TypeWith(ctx context.Context, target, text string, appendText, submit bool) (password bool, err error) {
	span := s.beginAction(ctx, "type")
	password, err = s.doTypeWith(ctx, target, text, appendText, submit)
	span.EndErr(err)
	return password, err
}

// doTypeWith is TypeWith under its dashboard node.
func (s *Session) doTypeWith(ctx context.Context, target, text string, appendText, submit bool) (bool, error) {
	selJSON, _ := json.Marshal(resolveSelector(target))
	textJSON, _ := json.Marshal(text)
	out, err := s.evalRaw(ctx, domScript(fmt.Sprintf(typeTargetJS, selJSON, textJSON, appendText, submit)))
	if err != nil {
		return false, err
	}
	if strings.Contains(out, "NOTFOUND") {
		return false, fmt.Errorf(errNoElementFmt, target)
	}
	var res typeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return false, fmt.Errorf("parse type result: %w", err)
	}
	if res.Kind == "value" {
		return res.Password, nil
	}
	if text != "" {
		if _, err := s.call(ctx, "Input.insertText", map[string]interface{}{"text": text}); err != nil {
			return false, err
		}
	}
	if submit {
		if err := s.doPress(ctx, "Enter"); err != nil {
			return false, err
		}
	}
	return false, nil
}

// clearTargetJS empties an input, textarea or editable region with the
// events frameworks listen to.
const clearTargetJS = `(() => {
  const el = __cc.one(%s);
  if (!el) return 'NOTFOUND';
  el.scrollIntoView({block: 'center'});
  el.focus();
  const tag = el.tagName.toLowerCase();
  if (tag === 'input' || tag === 'textarea') {
    const proto = tag === 'input' ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(proto, 'value');
    if (setter && setter.set) { setter.set.call(el, ''); } else { el.value = ''; }
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
    return 'OK';
  }
  if (tag === 'select') return 'NOTEDITABLE';
  const doc = el.ownerDocument;
  const sel = doc.defaultView.getSelection();
  if (sel) sel.selectAllChildren(el);
  if (!doc.execCommand('delete')) { el.textContent = ''; el.dispatchEvent(new Event('input', {bubbles: true})); }
  return 'OK';
})()`

// Clear empties an input, textarea or editable region.
func (s *Session) Clear(ctx context.Context, target string) error {
	span := s.beginAction(ctx, "clear")
	err := s.doClear(ctx, target)
	span.EndErr(err)
	return err
}

// doClear is Clear under its dashboard node.
func (s *Session) doClear(ctx context.Context, target string) error {
	selJSON, _ := json.Marshal(resolveSelector(target))
	out, err := s.evalRaw(ctx, domScript(fmt.Sprintf(clearTargetJS, selJSON)))
	if err != nil {
		return err
	}
	switch out {
	case "NOTFOUND":
		return fmt.Errorf(errNoElementFmt, target)
	case "NOTEDITABLE":
		return fmt.Errorf("%q is a <select>; use select to change its option", target)
	}
	return nil
}
