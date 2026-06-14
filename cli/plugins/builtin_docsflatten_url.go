/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @docs-flatten "url" source — bounded web crawler.
 *
 * Flattens a rendered documentation WEBSITE (plain HTML, no Markdown repo)
 * into the same docsFlattenChunk JSONL corpus cli/ctxmgr ingests for
 * /context --mode knowledge. This completes the autonomous docs pipeline:
 * the agent can point @docs-flatten at a docs site and produce a knowledge
 * base without a Markdown source.
 *
 * The crawl is deliberately bounded and corporate-friendly:
 *   - all fetches go through the SHARED webGet client (proxy/SSRF/TLS aware);
 *     never a bare http.Client. URLs are validated with validateWebTarget.
 *   - bounded BFS: MaxPages and MaxDepth cap the walk; per-page bodies are
 *     capped via io.LimitReader.
 *   - same-host by default so a crawl stays on one docs site.
 *   - NO SILENT TRUNCATION: when a cap stops the walk while links remained,
 *     the summary and the emit log say so — a silent cap reads as "covered
 *     everything".
 *
 * HTML→text reuses extractText (the @webfetch cleaner); chunking reuses
 * chunkMarkdown; rendering reuses renderDocsFlatten — the same internals the
 * local/git sources use, so the wire contract is identical.
 */
package plugins

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// docsFlattenMaxBodyBytes caps each crawled page body before HTML stripping,
// so one pathological page can't blow up memory. 4MB is generous for prose
// documentation while still bounded.
const docsFlattenMaxBodyBytes = 4 * 1024 * 1024

// docsFlattenAssetExts are link targets we never enqueue: binary assets and
// stylesheets/scripts carry no documentation prose.
var docsFlattenAssetExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".webp": true, ".ico": true, ".css": true, ".js": true, ".mjs": true,
	".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".woff": true,
	".woff2": true, ".ttf": true, ".eot": true, ".mp4": true, ".webm": true,
	".mp3": true, ".wav": true, ".json": true, ".xml": true,
}

// docsFlattenCrawlItem is one node in the BFS frontier.
type docsFlattenCrawlItem struct {
	url   string
	depth int
}

// executeURL runs the web-crawl source end to end: crawl → render → write or
// return, mirroring the local/git path's output contract.
func (p *BuiltinDocsFlattenPlugin) executeURL(ctx context.Context, cfg docsFlattenArgs, emit func(string)) (string, error) {
	seedHost := hostOf(cfg.URL)

	chunks, pages, capped, err := crawlDocsFlatten(ctx, cfg, emit)
	if err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}
	if len(chunks) == 0 {
		return fmt.Sprintf("@docs-flatten: crawled %s but extracted no text from %d page(s)", cfg.URL, pages), nil
	}

	rendered, err := renderDocsFlatten(chunks, cfg.Format)
	if err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}

	capNote := ""
	if capped {
		capNote = fmt.Sprintf(" (stopped at the maxPages=%d/maxDepth=%d limit — more links remained; raise the limits to cover them)", cfg.MaxPages, cfg.MaxDepth)
	}

	if cfg.Output == "" {
		if capNote != "" {
			return rendered + "\n# note:" + capNote, nil
		}
		return rendered, nil
	}
	if err := writeDocsFlattenOutput(cfg.Output, rendered); err != nil {
		return "", fmt.Errorf("@docs-flatten: %w", err)
	}
	summary := fmt.Sprintf("@docs-flatten: %d chunks from %d page(s) of %s → %s (format=%s, %s)",
		len(chunks), pages, seedHost, cfg.Output, cfg.Format, humanByteSize(len(rendered)))
	summary += fmt.Sprintf("\nsource: %s (crawl maxPages=%d maxDepth=%d sameHost=%t)", cfg.URL, cfg.MaxPages, cfg.MaxDepth, cfg.SameHost)
	if capNote != "" {
		summary += "\nNOTE:" + capNote
	}
	if cfg.Format == "jsonl" {
		summary += fmt.Sprintf("\nready for: /context create <name> %s --mode knowledge", cfg.Output)
	}
	return summary, nil
}

// crawlDocsFlatten performs a bounded BFS crawl from cfg.URL, turning each
// page into docsFlattenChunks. It returns the chunks, the number of pages
// actually fetched, and whether a cap (MaxPages/MaxDepth) cut the walk short
// while links still remained (so the caller can surface a non-silent note).
func crawlDocsFlatten(ctx context.Context, cfg docsFlattenArgs, emit func(string)) ([]docsFlattenChunk, int, bool, error) {
	seedURL, err := validateWebTarget(cfg.URL)
	if err != nil {
		return nil, 0, false, fmt.Errorf("invalid seed url %q: %w", cfg.URL, err)
	}
	seedHost := hostOf(seedURL)

	visited := make(map[string]bool)
	queue := []docsFlattenCrawlItem{{url: seedURL, depth: 0}}
	visited[normalizeDocsFlattenURL(seedURL)] = true

	var chunks []docsFlattenChunk
	pages := 0
	chunkIndex := 1
	capped := false

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, 0, false, err
		}
		if pages >= cfg.MaxPages {
			// Anything still queued is uncrawled because of the page cap.
			if len(queue) > 0 {
				capped = true
			}
			break
		}

		item := queue[0]
		queue = queue[1:]

		pageText, pageTitle, links, ok := fetchDocsFlattenPage(ctx, item.url, emit)
		if !ok {
			continue
		}
		pages++
		emit(fmt.Sprintf("crawled %s (%d/%d)", item.url, pages, cfg.MaxPages))

		for _, c := range chunkMarkdown(pageText, cfg.MaxChars) {
			chunks = append(chunks, docsFlattenChunk{
				ID:        fmt.Sprintf("%s#%04d", seedHost, chunkIndex),
				Source:    item.url,
				Title:     pageTitle,
				Content:   c,
				ChunkSize: len(c),
				RepoURL:   cfg.URL,
			})
			chunkIndex++
		}

		// Enqueue children unless we've reached the depth limit.
		if item.depth >= cfg.MaxDepth {
			if len(links) > 0 {
				// Links exist below the depth horizon but won't be followed.
				capped = true
			}
			continue
		}
		for _, link := range links {
			abs := resolveDocsFlattenLink(item.url, link)
			if abs == "" {
				continue
			}
			if cfg.SameHost && hostOf(abs) != seedHost {
				continue
			}
			key := normalizeDocsFlattenURL(abs)
			if visited[key] {
				continue
			}
			visited[key] = true
			queue = append(queue, docsFlattenCrawlItem{url: abs, depth: item.depth + 1})
		}
	}

	return chunks, pages, capped, nil
}

// fetchDocsFlattenPage GETs one page through the shared SSRF/proxy-aware
// client, returning the cleaned text, a title and the raw <a href> targets.
// ok is false (and the page skipped) on a validation error, transport error
// or non-200 status — a single bad link must not abort the whole crawl.
func fetchDocsFlattenPage(ctx context.Context, pageURL string, emit func(string)) (text, title string, links []string, ok bool) {
	safeURL, err := validateWebTarget(pageURL)
	if err != nil {
		emit(fmt.Sprintf("skipping %s: %v", pageURL, err))
		return "", "", nil, false
	}

	resp, err := webGet(ctx, safeURL, map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		emit(fmt.Sprintf("skipping %s: %v", pageURL, err))
		return "", "", nil, false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 {
		emit(fmt.Sprintf("skipping %s: HTTP %d", pageURL, resp.StatusCode))
		return "", "", nil, false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, docsFlattenMaxBodyBytes))
	if err != nil {
		emit(fmt.Sprintf("skipping %s: reading body: %v", pageURL, err))
		return "", "", nil, false
	}

	htmlContent := string(body)
	text = extractText(htmlContent)
	if strings.TrimSpace(text) == "" {
		return "", "", nil, false
	}
	title, links = parseDocsFlattenHTML(htmlContent)
	if title == "" {
		title = firstDocsFlattenHeading(text)
	}
	return text, title, links, true
}

// parseDocsFlattenHTML extracts the document <title> and every <a href> from
// HTML. It uses golang.org/x/net/html (already a webfetch dependency); a parse
// failure yields empty results rather than aborting.
func parseDocsFlattenHTML(htmlContent string) (title string, links []string) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", nil
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if title == "" {
					if t := strings.TrimSpace(textContent(n)); t != "" {
						title = t
					}
				}
			case "a":
				if href := strings.TrimSpace(getAttr(n, "href")); href != "" {
					links = append(links, href)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return title, links
}

// firstDocsFlattenHeading returns the first non-empty line of cleaned text as
// a fallback page title when the document has no <title>.
func firstDocsFlattenHeading(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 120 {
				t = t[:120]
			}
			return t
		}
	}
	return ""
}

// resolveDocsFlattenLink resolves href against the page's base URL and filters
// out non-crawlable targets: non-http(s) schemes (mailto:, javascript:, tel:),
// pure fragments, and obvious asset extensions. Returns "" to skip.
func resolveDocsFlattenLink(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	lower := strings.ToLower(href)
	for _, scheme := range []string{"mailto:", "javascript:", "tel:", "data:"} {
		if strings.HasPrefix(lower, scheme) {
			return ""
		}
	}

	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(href)
	if err != nil {
		return ""
	}
	abs := baseURL.ResolveReference(ref)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	if isDocsFlattenAsset(abs.Path) {
		return ""
	}
	abs.Fragment = ""
	return abs.String()
}

// isDocsFlattenAsset reports whether the URL path ends in a known asset
// extension we never want to enqueue.
func isDocsFlattenAsset(path string) bool {
	dot := strings.LastIndex(path, ".")
	if dot < 0 {
		return false
	}
	slash := strings.LastIndex(path, "/")
	if dot < slash {
		return false // the dot is in a directory segment, not an extension
	}
	return docsFlattenAssetExts[strings.ToLower(path[dot:])]
}

// normalizeDocsFlattenURL produces the dedup key for a URL: scheme+host+path
// with the fragment stripped and a trailing slash removed (so "/a" and "/a/"
// and "/a#frag" collapse to one visit). The query string is preserved because
// some docs sites route on it.
func normalizeDocsFlattenURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		path = "/"
	}
	u.Path = path
	return strings.ToLower(u.Scheme+"://"+u.Host) + u.Path + querySuffix(u)
}

// querySuffix returns "?<rawquery>" when a query is present, else "".
func querySuffix(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

// hostOf returns the lowercased host (including an explicit :port) of a URL,
// or "" when unparseable. The port is part of the comparison so a docs site
// served on :8080 is not confused with a different service on :443 of the
// same hostname — same-origin, not just same-hostname.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}
