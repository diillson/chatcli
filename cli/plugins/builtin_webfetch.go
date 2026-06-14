package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Inline-size budget for web_fetch: pages bigger than this (AFTER line
// filters) are considered too large to paste into the LLM context in
// one shot. When the caller hasn't asked for any filter/range AND no
// save_to_file was requested, the plugin ESCALATES to auto-save — the
// full body is written to the session scratch dir and a short preview
// (plus the absolute path) is returned, so the model can read_file a
// specific range instead of paying for an entire page every time.
//
// 20_000 chars ≈ 5k tokens with standard BPE — roughly the point where
// one fetch alone starts dwarfing the system prompt. The knob is
// overridable via CHATCLI_WEBFETCH_AUTOSAVE_BYTES in case a user wants
// looser or tighter behavior.
const (
	defaultWebFetchMaxLength    = 20_000
	defaultWebFetchAutoSaveSize = 10_000
)

// browserUserAgent mimics a mainstream desktop browser. Many docs/CDN
// providers (Mintlify, Cloudflare-fronted sites, etc.) serve a 403 or a
// JS challenge to anything that self-identifies as a bot/library. Using a
// common browser UA — together with browser-like Accept/Accept-Language
// headers — lets these pages return real HTML instead of a block page.
// Shared across the webfetch and websearch builtins. A pinned Chrome UA
// eventually ages out; CHATCLI_WEBFETCH_USER_AGENT lets users override it
// without a rebuild (see browserUA).
const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

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
func (p *BuiltinWebFetchPlugin) Version() string { return "1.2.0" }
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
					{"name": "max_length", "type": "integer", "description": "Max returned characters (default 20000). Output beyond this is truncated (or saved if save_to_file=true). Bodies larger than ~10KB without a filter are auto-saved to the session scratch dir and a preview is returned.", "default": "20000"},
					{"name": "filter", "type": "string", "description": "Keep only lines matching this regex (Go regexp syntax). Useful for large endpoints like Prometheus /metrics — e.g. filter='^chatcli_'."},
					{"name": "exclude", "type": "string", "description": "Drop lines matching this regex. Applied AFTER filter."},
					{"name": "from_line", "type": "integer", "description": "Start at this line (1-based, inclusive). Applied after filter/exclude."},
					{"name": "to_line", "type": "integer", "description": "End at this line (1-based, inclusive). Applied after filter/exclude."},
					{"name": "save_to_file", "type": "boolean", "description": "Save the full (pre-truncation) content to the session scratch dir and return a preview + absolute path. Use when you want to analyze later with read_file."},
					{"name": "save_path", "type": "string", "description": "If save_to_file is true, override the generated filename (will be placed under CHATCLI_AGENT_TMPDIR)."},
					{"name": "render", "type": "boolean", "description": "Force headless-browser rendering for pages that build their content with JavaScript (SPAs, dynamic tables). By default rendering happens automatically when the static HTML looks like a JS shell and a Chrome/Chromium is available. Use render=true when a page came back empty or render=false to stay static.", "default": "auto"},
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
	// Render forces ("always") or suppresses ("never") the headless-browser
	// escalation for this call; empty defers to CHATCLI_WEBFETCH_RENDER.
	Render string
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
		parsed.MaxLength = defaultWebFetchMaxLength
	}

	if onOutput != nil {
		onOutput(fmt.Sprintf("Fetching %s...", parsed.URL))
	}

	// Create HTTP request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// SSRF guard: validate (and canonicalize) the target before it reaches the
	// HTTP client. Blocks cloud-metadata/link-local always; private/loopback
	// only when CHATCLI_WEBFETCH_BLOCK_PRIVATE is set.
	safeURL, err := validateWebTarget(parsed.URL)
	if err != nil {
		return "", fmt.Errorf("refusing to fetch %q: %w", parsed.URL, err)
	}

	// webGet sends a browser UA by default (avoids CDN bot-blocks) and
	// auto-retries with a neutral tool UA on a 401/407 gateway challenge —
	// the behavior of TLS-intercepting corporate proxies toward "browsers".
	resp, err := webGet(reqCtx, safeURL, map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	})
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
		fullContent = p.extractWithRenderEscalation(reqCtx, parsed, safeURL, fullContent, onOutput)
	}

	// Apply line-level filters before truncation so the final output keeps
	// the most relevant rows instead of whatever happens to fit in N chars.
	filtered, filterErr := applyLineFilters(fullContent, parsed.Filter, parsed.Exclude, parsed.FromLine, parsed.ToLine)
	if filterErr != nil {
		return "", filterErr
	}

	// Auto-save escalation: if the caller did not explicitly ask for
	// save_to_file, did not supply any line filter/range, and the body
	// weighs more than the inline budget, force save_to_file anyway.
	// Without this, a single naive @webfetch to a wiki page dumps 50 KB
	// of HTML-stripped text straight into the LLM context — exactly the
	// "23k tokens on a trivial query" symptom we're fighting.
	autoSaveSize := webFetchAutoSaveThreshold()
	autoSaved := false
	if !parsed.SaveToFile &&
		parsed.Filter == "" && parsed.Exclude == "" &&
		parsed.FromLine <= 0 && parsed.ToLine <= 0 &&
		len(fullContent) > autoSaveSize {
		parsed.SaveToFile = true
		autoSaved = true
	}

	// Persist full *pre-filter* content to the session scratch dir when asked.
	// This gives the agent the option to re-slice later via read_file without
	// re-fetching. We write the unfiltered text so the full data is available.
	savedPath, err := saveFetchToScratch(parsed, fullContent)
	if err != nil {
		return "", err
	}

	output := buildFetchOutput(parsed, filtered, fullContent, savedPath, autoSaved)

	if onOutput != nil {
		for _, line := range strings.Split(output, "\n") {
			if strings.TrimSpace(line) != "" {
				onOutput(line)
			}
		}
	}

	return output, nil
}

// saveFetchToScratch writes fullContent to the session scratch dir when
// parsed.SaveToFile is set, returning the absolute path written (empty
// when no save was requested). All writes are confined to the scratch dir:
// only the base name of the caller-supplied save_path is honored, and the
// resolved path is re-checked against scratch as defense-in-depth.
func saveFetchToScratch(parsed fetchArgs, fullContent string) (string, error) {
	if !parsed.SaveToFile {
		return "", nil
	}
	scratch := os.Getenv("CHATCLI_AGENT_TMPDIR")
	if scratch == "" {
		scratch = os.TempDir()
	}
	// Take only the base name so we can't be talked into writing /etc/passwd
	// via an absolute path — matches gosec G703 guidance and how the coder
	// engine validates agent paths.
	baseName := filepath.Base(strings.TrimSpace(parsed.SavePath))
	if baseName == "" || baseName == "." || baseName == string(filepath.Separator) {
		baseName = fmt.Sprintf("webfetch_%d.txt", time.Now().UnixNano())
	}
	// Clean collapses any surviving ../ segments introduced by exotic
	// basenames on platforms where Base keeps them.
	cleaned := filepath.Clean(filepath.Join(scratch, baseName))
	absScratch, _ := filepath.Abs(scratch)
	absCleaned, _ := filepath.Abs(cleaned)
	if !strings.HasPrefix(absCleaned, absScratch+string(filepath.Separator)) && absCleaned != absScratch {
		return "", fmt.Errorf("save_path %q escapes the session scratch directory", parsed.SavePath)
	}
	if err := os.WriteFile(cleaned, []byte(fullContent), 0o600); err != nil { //nolint:gosec // path confined to scratch dir above
		return "", fmt.Errorf("saving response to %s: %w", cleaned, err)
	}
	return cleaned, nil
}

// buildFetchOutput assembles the final string returned to the agent:
// applies the auto-save preview cap and max_length truncation, then
// prepends a pointer to the on-disk copy when one was written.
func buildFetchOutput(parsed fetchArgs, filtered, fullContent, savedPath string, autoSaved bool) string {
	output := filtered

	// When we auto-saved to disk, ship only a compact preview back — full
	// body is on disk and the model has been told where. Keeping the inline
	// preview small is THE point of auto-save: otherwise we pay twice
	// (inline bytes + disk) for the same content.
	if autoSaved {
		previewSize := parsed.MaxLength / 4
		if previewSize < 2000 {
			previewSize = 2000
		}
		if len(output) > previewSize {
			output = output[:previewSize] + "\n...(auto-truncated — full body saved to disk)"
		}
	}
	truncated := false
	if len(output) > parsed.MaxLength {
		output = output[:parsed.MaxLength] + "\n...(truncated)"
		truncated = true
	}

	if savedPath == "" {
		return output
	}

	var prefix string
	switch {
	case autoSaved:
		prefix = fmt.Sprintf("[auto-saved: response was %d bytes — too large to inline. Full body is at %s. Preview below; use read_file with start/end or rerun with filter/from_line/to_line for specific ranges.]\n\n",
			len(fullContent), savedPath)
	case truncated:
		prefix = fmt.Sprintf("[response truncated inline at %d chars; full response saved to %s — %d bytes. Use read_file with start/end to examine specific ranges.]\n\n",
			parsed.MaxLength, savedPath, len(fullContent))
	default:
		prefix = fmt.Sprintf("[full response saved to %s — %d bytes. Use read_file with start/end to examine specific ranges.]\n\n",
			savedPath, len(fullContent))
	}
	return prefix + output
}

// parseFetchArgs accepts either a single JSON arg or positional --flag args.
func parseFetchArgs(args []string) (fetchArgs, error) {
	out := fetchArgs{MaxLength: defaultWebFetchMaxLength}

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

	parsePositionalFetchArgs(args, start, &out)
	return out, nil
}

// parsePositionalFetchArgs walks the --flag value pairs (snake_case and
// kebab-case accepted) starting at index start, mutating out in place. A
// bare http(s) token is treated as an implicit URL when none was set.
func parsePositionalFetchArgs(args []string, start int, out *fetchArgs) {
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
		case a == "--render":
			out.Render = renderModeAlways
		case a == "--no_render" || a == "--no-render":
			out.Render = renderModeNever
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
	// render accepts a boolean (true → force, false → suppress) and the
	// string forms "always"/"never"/"auto" for symmetry with the env knob.
	if v, ok := m["render"].(bool); ok {
		if v {
			out.Render = renderModeAlways
		} else {
			out.Render = renderModeNever
		}
	}
	if v, ok := m["render"].(string); ok {
		out.Render = strings.ToLower(strings.TrimSpace(v))
	}
}

// webFetchAutoSaveThreshold returns the byte threshold above which an
// unfiltered fetch triggers the auto-save-to-disk escalation. Honors
// CHATCLI_WEBFETCH_AUTOSAVE_BYTES (power-user override); any non-parsable
// value falls back to defaultWebFetchAutoSaveSize.
func webFetchAutoSaveThreshold() int {
	if v := os.Getenv("CHATCLI_WEBFETCH_AUTOSAVE_BYTES"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return defaultWebFetchAutoSaveSize
}

// browserUA returns the User-Agent to send on outbound page/search
// requests. Honors CHATCLI_WEBFETCH_USER_AGENT (power-user override, e.g.
// to track a newer Chrome or impersonate a different browser); a blank or
// whitespace-only value falls back to the pinned browserUserAgent.
func browserUA() string {
	if v := strings.TrimSpace(os.Getenv("CHATCLI_WEBFETCH_USER_AGENT")); v != "" {
		return v
	}
	return browserUserAgent
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
