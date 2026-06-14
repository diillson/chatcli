/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinWikipediaPlugin — @wikipedia as a native ReAct tool.
 *
 * Searches Wikipedia for matching article titles, or reads the plain-text
 * intro (extract) of a specific article. Keyless: it uses the public MediaWiki
 * action API, no account or token required. A natural companion to the
 * @websearch / @webfetch / @knowledge builtins for quick factual lookups.
 *
 * Ported from plugins-examples/chatcli-wikipedia with the fixes the external
 * version lacked:
 *   - Returns errors instead of os.Exit, so it composes as a builtin tool.
 *   - Configurable language edition (lang=pt|en|es|…) instead of being hard-
 *     wired to en.wikipedia.org — so a pt-BR user gets pt-BR articles.
 *   - Typed JSON decoding instead of the fragile map[string]interface{} +
 *     unchecked type assertions (which panicked on unexpected shapes).
 *   - Uses the shared proxy/SSRF/TLS-trust-aware HTTP client, so it works
 *     behind a corporate proxy and honors CHATCLI_CA_BUNDLE.
 *
 * The external plugins-examples/chatcli-wikipedia remains as a plugin-authoring
 * example.
 */
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

	"github.com/diillson/chatcli/i18n"
)

// wikipediaHTTPClient is the HTTP client used for Wikipedia queries.
// Overridable in tests. It shares the proxy-aware transport so lookups work
// behind a corporate proxy and honor the global TLS trust overrides, like the
// @webfetch/@websearch/@osv builtins (see builtin_web_httpclient.go).
var wikipediaHTTPClient = &http.Client{Timeout: 15 * time.Second, Transport: &proxyAuthTransport{base: newWebTransport()}}

// wikipediaBaseURL builds the MediaWiki action API endpoint for a language
// edition. Split out so tests can point it at a local server.
var wikipediaBaseURL = func(lang string) string {
	return fmt.Sprintf("https://%s.wikipedia.org/w/api.php", lang)
}

// wikipediaUserAgent identifies the client per the Wikimedia API etiquette
// (a real contact/project URL is expected).
const wikipediaUserAgent = "ChatCLI/1.0 (+https://github.com/diillson/chatcli)"

// wikipediaDefaultLang is the fallback language edition.
const wikipediaDefaultLang = "en"

// wikipediaSearchLimit caps how many titles a search returns.
const wikipediaSearchLimit = 5

// wikipediaArgs is the typed view of @wikipedia's JSON input.
type wikipediaArgs struct {
	Query string // search term
	Read  string // exact title to read (mutually informs the mode)
	Lang  string // wiki language edition (en, pt, es, …)
}

// BuiltinWikipediaPlugin is the @wikipedia tool.
type BuiltinWikipediaPlugin struct{}

// NewBuiltinWikipediaPlugin returns a ready-to-register plugin.
func NewBuiltinWikipediaPlugin() *BuiltinWikipediaPlugin { return &BuiltinWikipediaPlugin{} }

// Name returns "@wikipedia".
func (*BuiltinWikipediaPlugin) Name() string { return "@wikipedia" }

// Description surfaces the tool in the catalog.
func (*BuiltinWikipediaPlugin) Description() string {
	return i18n.T("plugins.wikipedia.description")
}

// IsReadOnly reports true: lookups never mutate anything.
func (*BuiltinWikipediaPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: each call is an independent read-only query.
func (*BuiltinWikipediaPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the query/title in the spinner.
func (*BuiltinWikipediaPlugin) DescribeCall(args []string) string {
	if t := extractStringArg(args, "read", "title"); t != "" {
		return i18n.T("plugins.wikipedia.describe_read", t)
	}
	q := extractStringArg(args, "query", "q", "search", "term")
	if q == "" {
		return i18n.T("plugins.wikipedia.description")
	}
	return i18n.T("plugins.wikipedia.describe_search", q)
}

// Usage explains the canonical invocation.
func (*BuiltinWikipediaPlugin) Usage() string {
	return `<tool_call name="@wikipedia" args='{"query":"Alan Turing"}' />

Flags (flat JSON or {"cmd":"...","args":{...}} envelope):
  query   search term — returns up to 5 matching article titles
  read    exact article title — returns its plain-text intro/summary
  lang    Wikipedia language edition (default: en; e.g. pt, es, fr, de)

Provide either query (to search) or read (to fetch a known article). Typical
flow: search for the term, then read the exact title from the results.`
}

// Version is semver. 2.x marks the builtin port of the 1.x external example.
func (*BuiltinWikipediaPlugin) Version() string { return "2.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinWikipediaPlugin) Path() string { return "" }

// Schema describes the tool for the LLM catalog.
func (*BuiltinWikipediaPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "flat JSON preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "search",
				"description": "Search Wikipedia for article titles matching a term. Returns up to 5 exact titles to pass to read.",
				"flags": []map[string]interface{}{
					{"name": "query", "type": "string", "required": true, "description": "Search term, e.g. 'Alan Turing'."},
					{"name": "lang", "type": "string", "description": "Wikipedia language edition (default: en; e.g. pt, es)."},
				},
				"examples": []string{`{"query":"Alan Turing"}`, `{"query":"computador quântico","lang":"pt"}`},
			},
			{
				"name":        "read",
				"description": "Read the plain-text intro/summary of a specific Wikipedia article by its exact title.",
				"flags": []map[string]interface{}{
					{"name": "read", "type": "string", "required": true, "description": "Exact article title, e.g. 'Alan Turing'."},
					{"name": "lang", "type": "string", "description": "Wikipedia language edition (default: en; e.g. pt, es)."},
				},
				"examples": []string{`{"read":"Alan Turing"}`, `{"read":"Computação quântica","lang":"pt"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches to search or read.
func (p *BuiltinWikipediaPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream runs the lookup. The streaming callback is unused — a
// Wikipedia call is a single short request — but the signature satisfies the
// Plugin contract.
func (p *BuiltinWikipediaPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	cfg, err := parseWikipediaArgs(args)
	if err != nil {
		return "", fmt.Errorf("@wikipedia: %w", err)
	}
	if cfg.Read != "" {
		out, rerr := wikipediaRead(ctx, cfg.Lang, cfg.Read)
		if rerr != nil {
			return "", fmt.Errorf("@wikipedia: %w", rerr)
		}
		return out, nil
	}
	out, serr := wikipediaSearch(ctx, cfg.Lang, cfg.Query)
	if serr != nil {
		return "", fmt.Errorf("@wikipedia: %w", serr)
	}
	return out, nil
}

// parseWikipediaArgs supports flat JSON, the {"cmd","args"} envelope and
// --flag argv form. A bare positional is treated as the search query.
func parseWikipediaArgs(args []string) (wikipediaArgs, error) {
	out := wikipediaArgs{Lang: wikipediaDefaultLang}
	payload := strings.TrimSpace(strings.Join(args, " "))
	var raw map[string]json.RawMessage

	switch {
	case strings.HasPrefix(payload, "{"):
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return out, fmt.Errorf("malformed JSON args: %w", err)
		}
		if inner, ok := raw["args"]; ok {
			var innerMap map[string]json.RawMessage
			if err := json.Unmarshal(inner, &innerMap); err == nil {
				raw = innerMap
			}
		}
	default:
		// Bare argv: treat --read/--lang flags, else join the rest as a query.
		if t := stringFromFlagArgs(args, []string{"read", "title"}); t != "" {
			out.Read = t
		}
		if l := stringFromFlagArgs(args, []string{"lang", "language"}); l != "" {
			out.Lang = l
		}
		if out.Read == "" {
			var terms []string
			for _, a := range args {
				if strings.HasPrefix(a, "--") {
					continue
				}
				terms = append(terms, a)
			}
			out.Query = strings.TrimSpace(strings.Join(terms, " "))
		}
		return finalizeWikipediaArgs(out)
	}

	out.Read = strings.TrimSpace(jsonString(raw, "read", "title", "article"))
	out.Query = strings.TrimSpace(jsonString(raw, "query", "q", "search", "term"))
	if l := strings.TrimSpace(jsonString(raw, "lang", "language")); l != "" {
		out.Lang = l
	}
	return finalizeWikipediaArgs(out)
}

// finalizeWikipediaArgs validates and normalizes the parsed args.
func finalizeWikipediaArgs(out wikipediaArgs) (wikipediaArgs, error) {
	out.Lang = normalizeWikipediaLang(out.Lang)
	if out.Read == "" && out.Query == "" {
		return out, fmt.Errorf(`provide "query" (to search) or "read" (to fetch an article)`)
	}
	return out, nil
}

// normalizeWikipediaLang reduces a language value to the lowercase subdomain
// label Wikipedia uses (pt-BR → pt) and rejects anything non-alphabetic to
// keep the host well-formed.
func normalizeWikipediaLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return wikipediaDefaultLang
		}
	}
	if lang == "" {
		return wikipediaDefaultLang
	}
	return lang
}

// wikipediaSearch returns up to wikipediaSearchLimit matching titles via the
// opensearch action.
func wikipediaSearch(ctx context.Context, lang, term string) (string, error) {
	params := url.Values{}
	params.Set("action", "opensearch")
	params.Set("search", term)
	params.Set("limit", fmt.Sprintf("%d", wikipediaSearchLimit))
	params.Set("namespace", "0")
	params.Set("format", "json")

	body, err := wikipediaGet(ctx, lang, params)
	if err != nil {
		return "", err
	}

	// opensearch returns a heterogeneous array: [term, [titles], [descs], [urls]].
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("decoding search response: %w", err)
	}
	if len(raw) < 2 {
		return i18n.T("plugins.wikipedia.no_results", term), nil
	}
	var titles []string
	if err := json.Unmarshal(raw[1], &titles); err != nil {
		return "", fmt.Errorf("decoding search titles: %w", err)
	}
	if len(titles) == 0 {
		return i18n.T("plugins.wikipedia.no_results", term), nil
	}

	var b strings.Builder
	b.WriteString(i18n.T("plugins.wikipedia.results_header"))
	b.WriteByte('\n')
	for i, t := range titles {
		fmt.Fprintf(&b, "%d. %s\n", i+1, t)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// wikipediaRead returns the plain-text intro/summary of an exact-title article.
func wikipediaRead(ctx context.Context, lang, title string) (string, error) {
	title = strings.Trim(title, `"`)
	params := url.Values{}
	params.Set("action", "query")
	params.Set("format", "json")
	params.Set("titles", title)
	params.Set("prop", "extracts")
	params.Set("exintro", "true")
	params.Set("explaintext", "true")
	params.Set("redirects", "1")

	body, err := wikipediaGet(ctx, lang, params)
	if err != nil {
		return "", err
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
		return "", fmt.Errorf("decoding article response: %w", err)
	}
	for _, page := range parsed.Query.Pages {
		if strings.TrimSpace(page.Extract) != "" {
			return fmt.Sprintf("# %s\n\n%s", page.Title, page.Extract), nil
		}
	}
	return i18n.T("plugins.wikipedia.no_article", title), nil
}

// wikipediaGet performs a GET against the MediaWiki API for the given language
// edition and query parameters, returning the raw body.
//
// gosec G704: the URL host is a fixed *.wikipedia.org built from a validated
// language label (normalizeWikipediaLang restricts it to [a-z]); the shared
// ssrfDialControl still refuses metadata/link-local. Annotated to match the
// repo convention for the web builtins.
func wikipediaGet(ctx context.Context, lang string, params url.Values) ([]byte, error) {
	endpoint := wikipediaBaseURL(lang) + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil) //#nosec G704 -- fixed *.wikipedia.org host from validated [a-z] lang label; ssrfDialControl refuses metadata/link-local
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", wikipediaUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := wikipediaHTTPClient.Do(req) //#nosec G704 -- see above
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Wikipedia API returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
