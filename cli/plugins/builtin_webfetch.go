package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	return "Fetches content from a URL and returns the text (filtering + save-to-file supported)"
}
func (p *BuiltinWebFetchPlugin) Usage() string   { return "@webfetch <url>" }
func (p *BuiltinWebFetchPlugin) Version() string { return "1.1.0" }
func (p *BuiltinWebFetchPlugin) Path() string    { return "[builtin]" }

func (p *BuiltinWebFetchPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON or positional",
		"subcommands": []map[string]interface{}{
			{
				"name":        "fetch",
				"description": "Fetches a web page and returns its text content (HTML stripped). Supports line-level regex filtering and persistence to the session scratch dir for large payloads.",
				"flags": []map[string]interface{}{
					{"name": "url", "type": "string", "description": "URL to fetch", "required": true},
					{"name": "raw", "type": "boolean", "description": "Return raw HTML instead of text", "default": "false"},
					{"name": "max_length", "type": "integer", "description": "Max returned characters (default 50000). Output beyond this is truncated (or saved if save_to_file=true).", "default": "50000"},
					{"name": "filter", "type": "string", "description": "Keep only lines matching this regex (Go regexp syntax). Useful for large endpoints like Prometheus /metrics — e.g. filter='^chatcli_'."},
					{"name": "exclude", "type": "string", "description": "Drop lines matching this regex. Applied AFTER filter."},
					{"name": "from_line", "type": "integer", "description": "Start at this line (1-based, inclusive). Applied after filter/exclude."},
					{"name": "to_line", "type": "integer", "description": "End at this line (1-based, inclusive). Applied after filter/exclude."},
					{"name": "save_to_file", "type": "boolean", "description": "Save the full (pre-truncation) content to the session scratch dir and return a preview + absolute path. Use when you want to analyze later with read_file."},
					{"name": "save_path", "type": "string", "description": "If save_to_file is true, override the generated filename (will be placed under CHATCLI_AGENT_TMPDIR)."},
				},
				"examples": []string{
					`{"cmd":"fetch","args":{"url":"http://svc/metrics","filter":"^chatcli_"}}`,
					`{"cmd":"fetch","args":{"url":"http://svc/metrics","save_to_file":true}}`,
					`fetch --url https://example.com --from_line 1 --to_line 200`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// fetchArgs mirrors the native tool call shape for easier parsing.
type fetchArgs struct {
	URL        string
	Raw        bool
	MaxLength  int
	Filter     string
	Exclude    string
	FromLine   int
	ToLine     int
	SaveToFile bool
	SavePath   string
}

func (p *BuiltinWebFetchPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

func (p *BuiltinWebFetchPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("url required. Usage: @webfetch fetch --url <URL>")
	}

	parsed, err := parseFetchArgs(args)
	if err != nil {
		return "", err
	}
	if parsed.URL == "" {
		return "", fmt.Errorf("url required")
	}
	if parsed.MaxLength <= 0 {
		parsed.MaxLength = 50000
	}

	if onOutput != nil {
		onOutput(fmt.Sprintf("Fetching %s...", parsed.URL))
	}

	// Create HTTP request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", parsed.URL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "chatcli-webfetch/1.1 (compatible; bot)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,*/*")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	// Read body with hard cap of 10MB to avoid memory blowup.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}

	fullContent := string(body)
	if !parsed.Raw {
		fullContent = extractText(fullContent)
	}

	// Apply line-level filters before truncation so the final output keeps
	// the most relevant rows instead of whatever happens to fit in N chars.
	filtered, filterErr := applyLineFilters(fullContent, parsed.Filter, parsed.Exclude, parsed.FromLine, parsed.ToLine)
	if filterErr != nil {
		return "", filterErr
	}

	// Persist full *pre-filter* content to the session scratch dir when asked.
	// This gives the agent the option to re-slice later via read_file without
	// re-fetching. We write the unfiltered text so the full data is available.
	var savedPath string
	if parsed.SaveToFile {
		scratch := os.Getenv("CHATCLI_AGENT_TMPDIR")
		if scratch == "" {
			scratch = os.TempDir()
		}
		// Always keep writes inside the scratch dir. Take only the base name
		// of whatever the caller supplied so we can't be talked into writing
		// /etc/passwd via an absolute path — this matches gosec G703
		// guidance and is consistent with how the coder engine validates
		// paths for the agent.
		baseName := filepath.Base(strings.TrimSpace(parsed.SavePath))
		if baseName == "" || baseName == "." || baseName == string(filepath.Separator) {
			baseName = fmt.Sprintf("webfetch_%d.txt", time.Now().UnixNano())
		}
		target := filepath.Join(scratch, baseName)
		// Defence-in-depth: resolve and double-check we stayed under scratch
		// (Clean collapses any surviving ../ segments introduced by exotic
		// basenames on platforms where Base keeps them).
		cleaned := filepath.Clean(target)
		absScratch, _ := filepath.Abs(scratch)
		absCleaned, _ := filepath.Abs(cleaned)
		if !strings.HasPrefix(absCleaned, absScratch+string(filepath.Separator)) && absCleaned != absScratch {
			return "", fmt.Errorf("save_path %q escapes the session scratch directory", parsed.SavePath)
		}
		if err := os.WriteFile(cleaned, []byte(fullContent), 0o600); err != nil { //nolint:gosec // path confined to scratch dir above
			return "", fmt.Errorf("saving response to %s: %w", cleaned, err)
		}
		savedPath = cleaned
	}

	// Final output returned to the agent. Apply max_length as the last step.
	output := filtered
	truncated := false
	if len(output) > parsed.MaxLength {
		output = output[:parsed.MaxLength] + "\n...(truncated)"
		truncated = true
	}

	// If we saved the full body to disk, tell the agent exactly where.
	if savedPath != "" {
		prefix := fmt.Sprintf("[full response saved to %s — %d bytes. Use read_file with start/end to examine specific ranges.]\n\n", savedPath, len(fullContent))
		if truncated {
			prefix = fmt.Sprintf("[response truncated inline at %d chars; full response saved to %s — %d bytes. Use read_file with start/end to examine specific ranges.]\n\n", parsed.MaxLength, savedPath, len(fullContent))
		}
		output = prefix + output
	}

	if onOutput != nil {
		for _, line := range strings.Split(output, "\n") {
			if strings.TrimSpace(line) != "" {
				onOutput(line)
			}
		}
	}

	return output, nil
}

// parseFetchArgs accepts either a single JSON arg or positional --flag args.
func parseFetchArgs(args []string) (fetchArgs, error) {
	out := fetchArgs{MaxLength: 50000}

	// Single JSON-arg form (native tool call).
	if len(args) == 1 {
		raw := args[0]
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			// Two shapes supported:
			//   {"cmd":"fetch","args":{"url":"..."}}
			//   {"url":"...","filter":"..."}
			if cmd, ok := m["cmd"].(string); ok && cmd == "fetch" {
				if inner, ok := m["args"].(map[string]interface{}); ok {
					m = inner
				}
			}
			mapToFetchArgs(m, &out)
			return out, nil
		}
	}

	// Positional flags.
	subcmd := args[0]
	start := 0
	if subcmd == "fetch" {
		start = 1
	} else if strings.HasPrefix(subcmd, "http") {
		out.URL = subcmd
		start = 1
	}

	for i := start; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--url" && i+1 < len(args):
			out.URL = args[i+1]
			i++
		case a == "--raw":
			out.Raw = true
		case a == "--max_length" || a == "--maxLength" || a == "--max-length":
			if i+1 < len(args) {
				_, _ = fmt.Sscanf(args[i+1], "%d", &out.MaxLength)
				i++
			}
		case a == "--filter":
			if i+1 < len(args) {
				out.Filter = args[i+1]
				i++
			}
		case a == "--exclude":
			if i+1 < len(args) {
				out.Exclude = args[i+1]
				i++
			}
		case a == "--from_line" || a == "--from-line":
			if i+1 < len(args) {
				_, _ = fmt.Sscanf(args[i+1], "%d", &out.FromLine)
				i++
			}
		case a == "--to_line" || a == "--to-line":
			if i+1 < len(args) {
				_, _ = fmt.Sscanf(args[i+1], "%d", &out.ToLine)
				i++
			}
		case a == "--save_to_file" || a == "--save-to-file":
			out.SaveToFile = true
		case a == "--save_path" || a == "--save-path":
			if i+1 < len(args) {
				out.SavePath = args[i+1]
				i++
			}
		default:
			// Implicit URL as positional.
			if out.URL == "" && strings.HasPrefix(a, "http") {
				out.URL = a
			}
		}
	}
	return out, nil
}

func mapToFetchArgs(m map[string]interface{}, out *fetchArgs) {
	if v, ok := m["url"].(string); ok {
		out.URL = v
	}
	if v, ok := m["raw"].(bool); ok {
		out.Raw = v
	}
	// Accept both camelCase and snake_case (LLMs are inconsistent).
	for _, key := range []string{"max_length", "maxLength"} {
		if v, ok := m[key].(float64); ok && v > 0 {
			out.MaxLength = int(v)
		}
	}
	if v, ok := m["filter"].(string); ok {
		out.Filter = v
	}
	if v, ok := m["exclude"].(string); ok {
		out.Exclude = v
	}
	for _, key := range []string{"from_line", "fromLine"} {
		if v, ok := m[key].(float64); ok && v > 0 {
			out.FromLine = int(v)
		}
	}
	for _, key := range []string{"to_line", "toLine"} {
		if v, ok := m[key].(float64); ok && v > 0 {
			out.ToLine = int(v)
		}
	}
	for _, key := range []string{"save_to_file", "saveToFile"} {
		if v, ok := m[key].(bool); ok {
			out.SaveToFile = v
		}
	}
	for _, key := range []string{"save_path", "savePath"} {
		if v, ok := m[key].(string); ok {
			out.SavePath = v
		}
	}
}

// applyLineFilters applies regex filter/exclude and line-range clipping.
// Returns the filtered text. An empty filter matches all lines; an empty
// exclude drops none.
func applyLineFilters(content, filter, exclude string, fromLine, toLine int) (string, error) {
	// Fast path: no line operations needed.
	if filter == "" && exclude == "" && fromLine <= 0 && toLine <= 0 {
		return content, nil
	}

	var keepRE, dropRE *regexp.Regexp
	var err error
	if filter != "" {
		keepRE, err = regexp.Compile(filter)
		if err != nil {
			return "", fmt.Errorf("invalid filter regex %q: %w", filter, err)
		}
	}
	if exclude != "" {
		dropRE, err = regexp.Compile(exclude)
		if err != nil {
			return "", fmt.Errorf("invalid exclude regex %q: %w", exclude, err)
		}
	}

	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if keepRE != nil && !keepRE.MatchString(line) {
			continue
		}
		if dropRE != nil && dropRE.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}

	// Apply line range AFTER filters so the agent's from/to is relative to
	// the filtered view (which is more useful for paging).
	if fromLine > 0 || toLine > 0 {
		start := 0
		if fromLine > 0 {
			start = fromLine - 1
		}
		if start > len(kept) {
			start = len(kept)
		}
		end := len(kept)
		if toLine > 0 && toLine < end {
			end = toLine
		}
		if end < start {
			end = start
		}
		kept = kept[start:end]
	}

	return strings.Join(kept, "\n"), nil
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
