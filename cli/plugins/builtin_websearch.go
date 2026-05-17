package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// SearchProvider identifies a backend. Only backends that don't require
// a third-party API key are supported — by design, to keep chatcli usable
// in corporate environments without credential provisioning friction.
type SearchProvider string

const (
	ProviderAuto       SearchProvider = "auto"
	ProviderSearXNG    SearchProvider = "searxng"
	ProviderDuckDuckGo SearchProvider = "duckduckgo"
)

// KnownSearchProviders is the canonical list for the /websearch CLI
// command. Default order: DuckDuckGo first (zero config, always available),
// SearxNG as secondary (used when the user has an instance configured).
var KnownSearchProviders = []SearchProvider{
	ProviderDuckDuckGo, ProviderSearXNG,
}

// BuiltinWebSearchPlugin provides web search with a pluggable backend chain.
//
// Default order (CHATCLI_WEBSEARCH_PROVIDER unset or "auto"):
//  1. DuckDuckGo HTML — zero-config default
//  2. SearxNG self-hosted (SEARXNG_URL) — used as fallback when configured
//
// Set CHATCLI_WEBSEARCH_PROVIDER=searxng to prefer SearxNG over DuckDuckGo.
// On failure or empty results the chain falls through to the next backend.
type BuiltinWebSearchPlugin struct{}

func NewBuiltinWebSearchPlugin() *BuiltinWebSearchPlugin {
	return &BuiltinWebSearchPlugin{}
}

func (p *BuiltinWebSearchPlugin) Name() string        { return "@websearch" }
func (p *BuiltinWebSearchPlugin) Description() string { return "Searches the web and returns results" }
func (p *BuiltinWebSearchPlugin) Usage() string       { return "@websearch <query>" }
func (p *BuiltinWebSearchPlugin) Version() string     { return "2.0.0" }
func (p *BuiltinWebSearchPlugin) Path() string        { return "[builtin]" }

func (p *BuiltinWebSearchPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON or positional",
		"subcommands": []map[string]interface{}{
			{
				"name":        "search",
				"description": "Searches the web and returns top results with titles, URLs, and snippets",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "description": "Search query", "required": true},
					{"name": "maxResults", "type": "integer", "description": "Maximum number of results", "default": "10"},
				},
				"examples": []string{
					`{"cmd":"search","args":{"query":"golang context best practices"}}`,
					`search --query "golang context best practices"`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

func (p *BuiltinWebSearchPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

func (p *BuiltinWebSearchPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("query required. Usage: @websearch search --query <query>")
	}

	var query string
	maxResults := 10

	// Try JSON args first
	if len(args) == 1 {
		var jsonArgs map[string]interface{}
		if err := json.Unmarshal([]byte(args[0]), &jsonArgs); err == nil {
			if cmd, ok := jsonArgs["cmd"].(string); ok && cmd == "search" {
				// Format: {"cmd":"search","args":{"query":"..."}}
				if a, ok := jsonArgs["args"].(map[string]interface{}); ok {
					if q, ok := a["query"].(string); ok {
						query = q
					}
					if m, ok := a["maxResults"].(float64); ok {
						maxResults = int(m)
					}
				}
			} else if q, ok := jsonArgs["query"].(string); ok && q != "" {
				// Flat format from native tool calling: {"query":"...","max_results":10}
				query = q
				if m, ok := jsonArgs["max_results"].(float64); ok && m > 0 {
					maxResults = int(m)
				}
				if m, ok := jsonArgs["maxResults"].(float64); ok && m > 0 {
					maxResults = int(m)
				}
			}
		}
	}

	// Positional args fallback
	if query == "" {
		subcmd := args[0]
		if subcmd == "search" && len(args) > 1 {
			var queryParts []string
			for i := 1; i < len(args); i++ {
				switch args[i] {
				case "--query":
					if i+1 < len(args) {
						query = args[i+1]
						i++
					}
				case "--maxResults":
					if i+1 < len(args) {
						_, _ = fmt.Sscanf(args[i+1], "%d", &maxResults)
						i++
					}
				default:
					queryParts = append(queryParts, args[i])
				}
			}
			if query == "" && len(queryParts) > 0 {
				query = strings.Join(queryParts, " ")
			}
		} else {
			// Simple: @websearch golang context
			query = strings.Join(args, " ")
		}
	}

	if query == "" {
		return "", fmt.Errorf("query required")
	}

	chain := SelectSearchChain()
	var (
		results  []searchResult
		provider SearchProvider
		lastErr  error
	)

	for i, p := range chain {
		if onOutput != nil {
			onOutput(fmt.Sprintf("Searching %s: %s...", p.name, query))
		}
		res, err := p.search(ctx, query, maxResults)
		if err == nil && len(res) > 0 {
			results = res
			provider = p.name
			lastErr = nil
			break
		}
		lastErr = err
		if onOutput != nil && i < len(chain)-1 {
			if err != nil {
				onOutput(fmt.Sprintf("%s failed (%v), falling back to %s...", p.name, err, chain[i+1].name))
			} else {
				onOutput(fmt.Sprintf("%s returned no results, falling back to %s...", p.name, chain[i+1].name))
			}
		}
	}

	if len(results) == 0 {
		if lastErr != nil {
			return "", fmt.Errorf("all search backends failed; last error: %w", lastErr)
		}
		return "No results found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Search results for: %q (via %s)\n\n", query, provider))
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("   URL: %s\n", r.URL))
		if r.Snippet != "" {
			sb.WriteString(fmt.Sprintf("   %s\n", r.Snippet))
		}
		sb.WriteString("\n")

		if onOutput != nil {
			onOutput(fmt.Sprintf("%d. %s - %s", i+1, r.Title, r.URL))
		}
	}

	return sb.String(), nil
}

type searchResult struct {
	Title   string
	URL     string
	Snippet string
}

// --- Provider chain selection ---

// providerEntry binds a provider name to its search function.
type providerEntry struct {
	name   SearchProvider
	search func(ctx context.Context, query string, max int) ([]searchResult, error)
}

// SelectSearchChain returns the ordered list of providers to try.
// An unset override → auto mode (DDG first → SearxNG if configured).
// An explicit override moves the named provider to the front and keeps the
// rest as fallbacks — so even a forced choice degrades gracefully.
func SelectSearchChain() []providerEntry {
	return selectSearchChainFromEnv(
		strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_WEBSEARCH_PROVIDER"))),
		strings.TrimSpace(os.Getenv("SEARXNG_URL")),
	)
}

// SelectSearchChainNames returns just the provider names in chain order.
// Exposed for display layers (e.g. /websearch status) that shouldn't touch
// the internal provider-entry struct.
func SelectSearchChainNames() []SearchProvider {
	chain := SelectSearchChain()
	out := make([]SearchProvider, 0, len(chain))
	for _, p := range chain {
		out = append(out, p.name)
	}
	return out
}

// selectSearchChainFromEnv is the pure-logic core, unit-testable without
// touching process environment state.
func selectSearchChainFromEnv(override, searxngURL string) []providerEntry {
	var chain []providerEntry

	addSearxNG := func() {
		if searxngURL != "" {
			chain = append(chain, providerEntry{
				name: ProviderSearXNG,
				search: func(ctx context.Context, q string, m int) ([]searchResult, error) {
					return searchSearxNG(ctx, q, m, searxngURL)
				},
			})
		}
	}
	addDDG := func() {
		chain = append(chain, providerEntry{name: ProviderDuckDuckGo, search: searchDuckDuckGo})
	}

	switch SearchProvider(override) {
	case ProviderSearXNG:
		addSearxNG()
		addDDG()
	case ProviderDuckDuckGo:
		addDDG()
		addSearxNG()
	default:
		// "", "auto", or anything unrecognized → default order (DDG first).
		addDDG()
		addSearxNG()
	}
	return chain
}

// --- SearxNG self-hosted ---

// searxngResponse matches the JSON output of SearxNG's /search?format=json.
// The instance must have JSON enabled in settings.yml:
//
//	search:
//	  formats: [html, json]
type searxngResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

// searchSearxNG queries a self-hosted SearxNG instance using its JSON API.
// baseURL must point to the instance root (e.g. https://searx.internal.corp).
// The function trims trailing slashes so either form is accepted.
func searchSearxNG(ctx context.Context, query string, maxResults int, baseURL string) ([]searchResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	baseURL = strings.TrimRight(baseURL, "/")
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")

	endpoint := baseURL + "/search?" + params.Encode()

	req, err := http.NewRequestWithContext(reqCtx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating SearxNG request: %w", err)
	}
	req.Header.Set("User-Agent", "chatcli-websearch/2.0")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SearxNG request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading SearxNG response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SearxNG returned %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	// If the instance hasn't enabled JSON, we'll get HTML back. Give a
	// specific, actionable error instead of a cryptic decode failure.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "application/json") {
		return nil, fmt.Errorf("SearxNG did not return JSON (Content-Type=%q). Enable JSON in settings.yml: search.formats: [html, json]", ct)
	}

	var sxResp searxngResponse
	if err := json.Unmarshal(body, &sxResp); err != nil {
		return nil, fmt.Errorf("parsing SearxNG response: %w", err)
	}

	limit := maxResults
	if limit <= 0 || limit > len(sxResp.Results) {
		limit = len(sxResp.Results)
	}
	out := make([]searchResult, 0, limit)
	for i := 0; i < limit; i++ {
		r := sxResp.Results[i]
		out = append(out, searchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}
	return out, nil
}

// --- DuckDuckGo HTML scraping (fallback) ---

// searchDuckDuckGo uses DuckDuckGo's HTML-only interface to get search results.
// No API key required. DDG occasionally serves anti-bot interstitials — this
// is why it's the fallback, not the default.
func searchDuckDuckGo(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(reqCtx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "chatcli-websearch/2.0 (compatible)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	return parseDDGResults(string(body), maxResults), nil
}

// parseDDGResults extracts search results from DuckDuckGo HTML response
// using a proper HTML parser for robustness against layout changes.
func parseDDGResults(htmlBody string, maxResults int) []searchResult {
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

		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__body") {
			r := extractResultFromBody(n)
			if r.Title != "" && r.URL != "" {
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

// extractResultFromBody extracts title, URL, and snippet from a result__body div.
func extractResultFromBody(body *html.Node) searchResult {
	var r searchResult

	var extract func(*html.Node)
	extract = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if hasClass(n, "result__a") {
				r.URL = cleanDDGURL(getAttr(n, "href"))
				r.Title = textContent(n)
			} else if hasClass(n, "result__snippet") {
				r.Snippet = textContent(n)
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extract(c)
		}
	}

	extract(body)
	return r
}

// --- HTML helpers ---

func hasClass(n *html.Node, class string) bool {
	for _, attr := range n.Attr {
		if attr.Key == "class" {
			for _, c := range strings.Fields(attr.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return strings.TrimSpace(sb.String())
}

// cleanDDGURL extracts the actual URL from DuckDuckGo's redirect URL.
func cleanDDGURL(rawURL string) string {
	if strings.Contains(rawURL, "uddg=") {
		if parts := strings.SplitN(rawURL, "uddg=", 2); len(parts) == 2 {
			decoded, err := url.QueryUnescape(parts[1])
			if err == nil {
				if idx := strings.Index(decoded, "&"); idx > 0 {
					decoded = decoded[:idx]
				}
				return decoded
			}
		}
	}
	return rawURL
}

// truncate returns the first n characters of a string.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
