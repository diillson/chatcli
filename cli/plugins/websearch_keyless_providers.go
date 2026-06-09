/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Additional keyless @websearch backends: Brave Search and Mojeek.
 *
 * Both follow the same posture as the DuckDuckGo provider — public HTML
 * results page, browser-like request, proper DOM parsing, zero API keys —
 * and exist to give the fallback chain independent indexes: Brave and
 * Mojeek run their own crawlers, so a DDG outage or bot-block does not
 * take @websearch down with it. Either backend failing (403 interstitial,
 * layout drift, network block) simply falls through to the next link in
 * the chain.
 *
 * Parsing strategy favors semantic, hash-free hooks: Brave's SERP is a
 * Svelte app with content-hashed class names, but every organic result
 * carries data-type="web"; Mojeek's classic markup nests results under
 * <ul class="results-standard">. Class tokens like "title" and "s" are
 * matched as whole tokens via hasClass, never as hashed substrings.
 */
package plugins

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	braveSearchEndpoint  = "https://search.brave.com/search?q="
	mojeekSearchEndpoint = "https://www.mojeek.com/search?q="

	// keylessSearchTimeout matches the DuckDuckGo provider budget.
	keylessSearchTimeout = 15 * time.Second
	// keylessSearchBodyCap bounds the SERP body read, mirroring DDG.
	keylessSearchBodyCap = 2 * 1024 * 1024
	// braveSnippetFloor: the snippet is the longest free text node in a
	// result block; below this length it is favicon alt-text or chrome.
	braveSnippetFloor = 40
)

// fetchSearchHTML centralizes the request path shared by the keyless
// scraping providers: SSRF validation, browser-like headers, body cap.
func fetchSearchHTML(ctx context.Context, endpoint, query string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, keylessSearchTimeout)
	defer cancel()

	safeURL, err := validateWebTarget(endpoint + url.QueryEscape(query))
	if err != nil {
		return "", fmt.Errorf("refusing search request: %w", err)
	}
	resp, err := webGet(reqCtx, safeURL, map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	})
	if err != nil {
		return "", fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("search returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, keylessSearchBodyCap))
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	return string(body), nil
}

// ── Brave Search ────────────────────────────────────────────────────

// searchBrave scrapes Brave Search's HTML results. Brave operates an
// independent index, which makes it a genuinely diverse fallback rather
// than another view over the same upstream.
func searchBrave(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	body, err := fetchSearchHTML(ctx, braveSearchEndpoint, query)
	if err != nil {
		return nil, err
	}
	return parseBraveResults(body, maxResults), nil
}

// parseBraveResults extracts organic results from a Brave SERP. Organic
// hits are the elements carrying data-type="web" — the only stable,
// hash-free marker in Brave's Svelte markup.
func parseBraveResults(htmlBody string, maxResults int) []searchResult {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return nil
	}

	var results []searchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}
		if n.Type == html.ElementNode && attrValue(n, "data-type") == "web" {
			if r := extractBraveResult(n); r.Title != "" && r.URL != "" {
				results = append(results, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return results
}

// extractBraveResult pulls URL, title and snippet out of one organic
// result block. The URL is the first external anchor; the title is the
// element carrying the "title" class token; the snippet is the longest
// free-standing text node that is not the title itself.
func extractBraveResult(block *html.Node) searchResult {
	var r searchResult
	var snippet string

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if r.URL == "" && n.Data == "a" {
				if href := attrValue(n, "href"); strings.HasPrefix(href, "http") {
					r.URL = href
				}
			}
			if r.Title == "" && hasClass(n, "title") {
				r.Title = strings.TrimSpace(textContent(n))
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); len(t) > len(snippet) && len(t) >= braveSnippetFloor {
				snippet = t
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(block)

	if snippet != r.Title {
		r.Snippet = snippet
	}
	return r
}

// ── Mojeek ──────────────────────────────────────────────────────────

// searchMojeek scrapes Mojeek's classic HTML results page. Mojeek also
// runs its own independent crawler; some networks receive a 403
// interstitial for automated traffic, in which case the chain simply
// falls through to the next provider.
func searchMojeek(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	body, err := fetchSearchHTML(ctx, mojeekSearchEndpoint, query)
	if err != nil {
		return nil, err
	}
	return parseMojeekResults(body, maxResults), nil
}

// parseMojeekResults extracts results from Mojeek markup: list items under
// <ul class="results-standard">, each holding an <h2><a href> title link
// and a <p class="s"> snippet.
func parseMojeekResults(htmlBody string, maxResults int) []searchResult {
	doc, err := html.Parse(strings.NewReader(htmlBody))
	if err != nil {
		return nil
	}

	var results []searchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}
		if n.Type == html.ElementNode && n.Data == "ul" && hasClass(n, "results-standard") {
			for li := n.FirstChild; li != nil && len(results) < maxResults; li = li.NextSibling {
				if li.Type != html.ElementNode || li.Data != "li" {
					continue
				}
				if r := extractMojeekResult(li); r.Title != "" && r.URL != "" {
					results = append(results, r)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return results
}

// extractMojeekResult pulls one result out of a results-standard <li>.
func extractMojeekResult(li *html.Node) searchResult {
	var r searchResult

	var walk func(*html.Node, bool)
	walk = func(n *html.Node, underH2 bool) {
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "h2":
				underH2 = true
			case n.Data == "a" && underH2 && r.URL == "":
				if href := attrValue(n, "href"); strings.HasPrefix(href, "http") {
					r.URL = href
					r.Title = strings.TrimSpace(textContent(n))
				}
			case n.Data == "p" && hasClass(n, "s") && r.Snippet == "":
				r.Snippet = strings.TrimSpace(textContent(n))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, underH2)
		}
	}
	walk(li, false)
	return r
}

// attrValue returns the value of attribute key on n, or "".
func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
