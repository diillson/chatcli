package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// BuiltinWebSearchPlugin provides web search functionality using DuckDuckGo.
type BuiltinWebSearchPlugin struct{}

func NewBuiltinWebSearchPlugin() *BuiltinWebSearchPlugin {
	return &BuiltinWebSearchPlugin{}
}

func (p *BuiltinWebSearchPlugin) Name() string        { return "@websearch" }
func (p *BuiltinWebSearchPlugin) Description() string { return "Searches the web and returns results" }
func (p *BuiltinWebSearchPlugin) Usage() string       { return "@websearch <query>" }
func (p *BuiltinWebSearchPlugin) Version() string     { return "1.0.0" }
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
				if a, ok := jsonArgs["args"].(map[string]interface{}); ok {
					if q, ok := a["query"].(string); ok {
						query = q
					}
					if m, ok := a["maxResults"].(float64); ok {
						maxResults = int(m)
					}
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

	if onOutput != nil {
		onOutput(fmt.Sprintf("Searching: %s...", query))
	}

	results, err := searchDuckDuckGo(ctx, query, maxResults)
	if err != nil {
		return "", err
	}

	if len(results) == 0 {
		return "No results found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Search results for: %q\n\n", query))
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

// searchDuckDuckGo uses DuckDuckGo's HTML-only interface to get search results.
func searchDuckDuckGo(ctx context.Context, query string, maxResults int) ([]searchResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(reqCtx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "chatcli-websearch/1.0 (compatible)")

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

	// Walk the DOM tree looking for result containers
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= maxResults {
			return
		}

		// Look for result link divs: <div class="result__body">
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__body") {
			r := extractResultFromBody(n)
			if r.Title != "" && r.URL != "" {
				results = append(results, r)
			}
			return // Don't recurse into result body children again
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
				// Title link
				r.URL = cleanDDGURL(getAttr(n, "href"))
				r.Title = textContent(n)
			} else if hasClass(n, "result__snippet") {
				// Snippet link
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

// hasClass checks if an HTML node has a specific CSS class.
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

// getAttr gets an attribute value from an HTML node.
func getAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

// textContent extracts all text from an HTML node and its children.
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
				// Remove trailing &rut=...
				if idx := strings.Index(decoded, "&"); idx > 0 {
					decoded = decoded[:idx]
				}
				return decoded
			}
		}
	}
	return rawURL
}
