package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// BuiltinWebFetchPlugin provides web page fetching functionality.
type BuiltinWebFetchPlugin struct{}

func NewBuiltinWebFetchPlugin() *BuiltinWebFetchPlugin {
	return &BuiltinWebFetchPlugin{}
}

func (p *BuiltinWebFetchPlugin) Name() string { return "@webfetch" }
func (p *BuiltinWebFetchPlugin) Description() string {
	return "Fetches content from a URL and returns the text"
}
func (p *BuiltinWebFetchPlugin) Usage() string   { return "@webfetch <url>" }
func (p *BuiltinWebFetchPlugin) Version() string { return "1.0.0" }
func (p *BuiltinWebFetchPlugin) Path() string    { return "[builtin]" }

func (p *BuiltinWebFetchPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON or positional",
		"subcommands": []map[string]interface{}{
			{
				"name":        "fetch",
				"description": "Fetches a web page and returns its text content (HTML stripped)",
				"flags": []map[string]interface{}{
					{"name": "url", "type": "string", "description": "URL to fetch", "required": true},
					{"name": "raw", "type": "boolean", "description": "Return raw HTML instead of text", "default": "false"},
					{"name": "maxLength", "type": "integer", "description": "Maximum content length in characters", "default": "50000"},
				},
				"examples": []string{
					`{"cmd":"fetch","args":{"url":"https://example.com"}}`,
					`fetch --url https://example.com`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

func (p *BuiltinWebFetchPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

func (p *BuiltinWebFetchPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("url required. Usage: @webfetch fetch --url <URL>")
	}

	// Parse args: either JSON or positional
	var url string
	var rawHTML bool
	maxLength := 50000

	// Try JSON args first
	if len(args) == 1 {
		var jsonArgs map[string]interface{}
		if err := json.Unmarshal([]byte(args[0]), &jsonArgs); err == nil {
			if cmd, ok := jsonArgs["cmd"].(string); ok && cmd == "fetch" {
				// Format: {"cmd":"fetch","args":{"url":"..."}}
				if a, ok := jsonArgs["args"].(map[string]interface{}); ok {
					if u, ok := a["url"].(string); ok {
						url = u
					}
					if r, ok := a["raw"].(bool); ok {
						rawHTML = r
					}
					if m, ok := a["maxLength"].(float64); ok {
						maxLength = int(m)
					}
				}
			} else if u, ok := jsonArgs["url"].(string); ok && u != "" {
				// Flat format from native tool calling: {"url":"...","raw":true}
				url = u
				if r, ok := jsonArgs["raw"].(bool); ok {
					rawHTML = r
				}
				if m, ok := jsonArgs["max_length"].(float64); ok && m > 0 {
					maxLength = int(m)
				}
				if m, ok := jsonArgs["maxLength"].(float64); ok && m > 0 {
					maxLength = int(m)
				}
			}
		}
	}

	// Positional args fallback
	if url == "" {
		subcmd := args[0]
		if subcmd == "fetch" && len(args) > 1 {
			for i := 1; i < len(args); i++ {
				switch args[i] {
				case "--url":
					if i+1 < len(args) {
						url = args[i+1]
						i++
					}
				case "--raw":
					rawHTML = true
				default:
					if url == "" && strings.HasPrefix(args[i], "http") {
						url = args[i]
					}
				}
			}
		} else if strings.HasPrefix(subcmd, "http") {
			url = subcmd
		}
	}

	if url == "" {
		return "", fmt.Errorf("url required")
	}

	if onOutput != nil {
		onOutput(fmt.Sprintf("Fetching %s...", url))
	}

	// Create HTTP request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "chatcli-webfetch/1.0 (compatible; bot)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,*/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	// Read body with size limit (10MB)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}

	content := string(body)

	if rawHTML {
		if len(content) > maxLength {
			content = content[:maxLength] + "\n...(truncated)"
		}
		return content, nil
	}

	// Extract text from HTML
	text := extractText(content)

	if len(text) > maxLength {
		text = text[:maxLength] + "\n...(truncated)"
	}

	if onOutput != nil {
		lines := strings.Split(text, "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				onOutput(line)
			}
		}
	}

	return text, nil
}

// extractText extracts readable text from HTML, removing scripts, styles, and tags.
func extractText(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		// Fallback: strip tags with regex
		return stripHTMLTags(htmlContent)
	}

	var sb strings.Builder
	var extractNode func(*html.Node)
	extractNode = func(n *html.Node) {
		if n.Type == html.ElementNode {
			// Skip script, style, head
			if n.Data == "script" || n.Data == "style" || n.Data == "head" || n.Data == "noscript" {
				return
			}
			// Add newlines for block elements
			if isBlockElement(n.Data) {
				sb.WriteString("\n")
			}
		}

		if n.Type == html.TextNode {
			text := strings.TrimSpace(n.Data)
			if text != "" {
				sb.WriteString(text + " ")
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			extractNode(c)
		}

		if n.Type == html.ElementNode && isBlockElement(n.Data) {
			sb.WriteString("\n")
		}
	}

	extractNode(doc)

	// Clean up excessive whitespace
	result := sb.String()
	re := regexp.MustCompile(`\n{3,}`)
	result = re.ReplaceAllString(result, "\n\n")
	return strings.TrimSpace(result)
}

func isBlockElement(tag string) bool {
	switch tag {
	case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "table", "tr", "br", "hr",
		"blockquote", "pre", "article", "section", "main",
		"header", "footer", "nav", "aside", "details":
		return true
	}
	return false
}

func stripHTMLTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(s, "")
}
