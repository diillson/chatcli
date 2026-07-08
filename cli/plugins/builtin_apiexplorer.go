/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * @api-explorer — end-to-end API reconnaissance for the agent.
 *
 * Point it at a base URL and it maps the API from edge to edge without the
 * user hand-feeding paths. Discovery is multi-vector, not just a guess at
 * /openapi.json:
 *   - ~20 well-known spec locations (JSON and YAML), probed concurrently.
 *   - The spec URL embedded inside a rendered Swagger-UI / ReDoc / RapiDoc /
 *     Stoplight docs page (the common case where the machine spec lives at a
 *     non-standard path only the HTML knows).
 *   - /.well-known manifests: openapi.json, ai-plugin.json (LLM plugins),
 *     openid-configuration / oauth-authorization-server (the real auth model).
 *   - Operational surface: health/readiness/metrics endpoints and Spring Boot
 *     Actuator (/actuator/mappings enumerates every route a Spring app serves).
 *   - API version roots (/v1../v3, /api/v1..) and a GraphQL endpoint probe.
 *
 * The spec, once found, is parsed with a real $ref resolver (see the model
 * file), so an endpoint deep-dive shows the actual model fields, enums and
 * constraints. A dedicated `security` subcommand reports the TLS/security
 * header posture, a live CORS preflight analysis, the auth challenge, cookie
 * flags and framework fingerprint.
 *
 * Read-only by construction: only GET/HEAD/OPTIONS plus the GraphQL
 * introspection query (a pure read) are ever issued, so it fans out
 * concurrently and never triggers the confirmation policy. Self-contained: every
 * request flows through the shared hardened client — corporate-proxy tolerant,
 * TLS-trust aware, SSRF-guarded (cloud metadata and link-local always refused)
 * with every redirect hop re-validated.
 */
package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"gopkg.in/yaml.v3"
)

const (
	apiExplorerRequestTimeout = 20 * time.Second
	apiExplorerMaxSpecBody    = 8 << 20 // 8MiB — large specs are common
	apiExplorerMaxHTMLBody    = 3 << 20 // 3MiB of HTML scanned for a spec URL
	apiExplorerMaxRendered    = 96 * 1024

	apiExplorerProbeConcurrency    = 8
	apiExplorerDiscoverEndpointCap = 80
	apiExplorerSpecEndpointCap     = 400
)

// BuiltinAPIExplorerPlugin is the @api-explorer tool.
type BuiltinAPIExplorerPlugin struct{}

// NewBuiltinAPIExplorerPlugin returns a ready-to-register plugin.
func NewBuiltinAPIExplorerPlugin() *BuiltinAPIExplorerPlugin { return &BuiltinAPIExplorerPlugin{} }

// Name returns "@api-explorer".
func (*BuiltinAPIExplorerPlugin) Name() string { return "@api-explorer" }

// Description surfaces the tool in the catalog. First sentence is the index
// line, so it leads with the headline capability.
func (*BuiltinAPIExplorerPlugin) Description() string {
	return i18n.T("plugins.apiexplorer.description")
}

// Usage explains the canonical invocation forms.
func (*BuiltinAPIExplorerPlugin) Usage() string {
	return `<tool_call name="@api-explorer" args='{"cmd":"discover","args":{"url":"https://api.example.com"}}' />

Subcommands (cmd + args):
  discover {url, format?}                 multi-vector recon from a base URL: find the spec
                                          (well-known paths, Swagger-UI/ReDoc HTML, /.well-known),
                                          fingerprint tech/auth, probe OIDC, Actuator, health,
                                          versions and GraphQL, then list endpoints
  spec     {url, filter?, tag?, limit?, format?}  fetch & parse an OpenAPI/Swagger spec
                                          (JSON or YAML) into a full endpoint catalog
  endpoint {url, path, method?}           deep-dive one operation with $ref-resolved schemas:
                                          every parameter, header, request/response model, security
  security {url}                          security posture: TLS/security headers, live CORS
                                          preflight, auth challenge, cookie flags, OIDC, framework
  graphql  {url}                          run a GraphQL schema introspection query

format: "markdown" (default) or "json" (machine-readable inventory) for discover/spec.
Read-only: only GET/HEAD/OPTIONS and the GraphQL introspection query are issued.`
}

// Version is semver; bumped when the surface changes.
func (*BuiltinAPIExplorerPlugin) Version() string { return "2.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinAPIExplorerPlugin) Path() string { return "" }

// Schema exposes the structured description the agent prompt builder renders.
func (*BuiltinAPIExplorerPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred; argv form also accepted",
		"subcommands": []map[string]interface{}{
			{
				"name":        "discover",
				"description": "Multi-vector end-to-end recon from a base URL: auto-find the OpenAPI/Swagger spec across ~20 well-known locations AND inside rendered Swagger-UI/ReDoc/RapiDoc/Stoplight docs pages AND /.well-known manifests; fingerprint server tech and auth; probe OIDC discovery, Spring Boot Actuator, health/metrics endpoints, API version roots and a GraphQL endpoint; then list the endpoints. Start here.",
				"flags": []map[string]interface{}{
					{"name": "url", "description": "API base URL (or a direct spec/docs URL)", "type": "string", "required": true},
					{"name": "format", "description": "markdown (default) or json", "type": "string", "default": "markdown"},
				},
				"examples": []string{
					`{"cmd":"discover","args":{"url":"https://api.example.com"}}`,
					`{"cmd":"discover","args":{"url":"https://petstore3.swagger.io/api/v3","format":"json"}}`,
				},
			},
			{
				"name":        "spec",
				"description": "Fetch and fully parse a known OpenAPI 2.0/3.x spec (JSON or YAML) into a catalog of every path, method, parameter, model and security scheme, grouped by tag. Filter by path substring or tag.",
				"flags": []map[string]interface{}{
					{"name": "url", "description": "spec URL, docs page URL, or a base URL to auto-locate it", "type": "string", "required": true},
					{"name": "filter", "description": "only show paths containing this substring", "type": "string"},
					{"name": "tag", "description": "only show operations with this tag", "type": "string"},
					{"name": "limit", "description": "max endpoints to list", "type": "int", "default": "400"},
					{"name": "format", "description": "markdown (default) or json", "type": "string", "default": "markdown"},
				},
				"examples": []string{
					`{"cmd":"spec","args":{"url":"https://petstore3.swagger.io/api/v3/openapi.json"}}`,
					`{"cmd":"spec","args":{"url":"https://api.example.com","filter":"/users","format":"json"}}`,
				},
			},
			{
				"name":        "endpoint",
				"description": "Deep-dive a single operation with $ref-resolved schemas: every path/query/header/cookie parameter with type, constraints and required flag; the request body model expanded to its fields; response models per status code; and the security it requires.",
				"flags": []map[string]interface{}{
					{"name": "url", "description": "spec URL, docs page URL, or a base URL to auto-locate it", "type": "string", "required": true},
					{"name": "path", "description": "the API path, e.g. /pet/{petId}", "type": "string", "required": true},
					{"name": "method", "description": "HTTP method (default: all methods on the path)", "type": "string"},
				},
				"examples": []string{
					`{"cmd":"endpoint","args":{"url":"https://petstore3.swagger.io/api/v3/openapi.json","path":"/pet/{petId}","method":"get"}}`,
				},
			},
			{
				"name":        "security",
				"description": "Security posture report for an endpoint: HTTPS/TLS, security response headers (HSTS, CSP, X-Frame-Options, …) present vs missing, a LIVE CORS preflight analysis (allowed origins/methods/credentials, flagged if permissive), the auth challenge, Set-Cookie flags (Secure/HttpOnly/SameSite), OIDC discovery and framework fingerprint.",
				"flags": []map[string]interface{}{
					{"name": "url", "description": "the URL to assess", "type": "string", "required": true},
				},
				"examples": []string{`{"cmd":"security","args":{"url":"https://api.example.com"}}`},
			},
			{
				"name":        "graphql",
				"description": "Run a GraphQL schema introspection query against a GraphQL endpoint and summarize the types, queries, mutations, subscriptions and their arguments. A pure read (mutates nothing).",
				"flags": []map[string]interface{}{
					{"name": "url", "description": "the GraphQL endpoint URL, e.g. https://api.example.com/graphql", "type": "string", "required": true},
				},
				"examples": []string{`{"cmd":"graphql","args":{"url":"https://api.example.com/graphql"}}`},
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// apiExplorerArgs is the typed view of @api-explorer's JSON input.
type apiExplorerArgs struct {
	Cmd    string
	URL    string
	Path   string
	Method string
	Filter string
	Tag    string
	Limit  int
	Format string
}

// Execute implements the plugin contract.
func (p *BuiltinAPIExplorerPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream parses args and dispatches to the requested subcommand.
func (p *BuiltinAPIExplorerPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@api-explorer: empty args. Example: <tool_call name="@api-explorer" args='{"cmd":"discover","args":{"url":"https://api.example.com"}}' />`)
	}
	in, err := parseAPIExplorerArgs(args)
	if err != nil {
		return "", fmt.Errorf("@api-explorer: %w", err)
	}

	var out string
	switch in.Cmd {
	case "discover", "":
		out, err = apiExplorerDiscover(ctx, in.URL, in.Format)
	case "spec":
		out, err = apiExplorerSpec(ctx, in.URL, in.Filter, in.Tag, in.Limit, in.Format)
	case "endpoint":
		out, err = apiExplorerEndpoint(ctx, in.URL, in.Path, in.Method)
	case "security":
		out, err = apiExplorerSecurity(ctx, in.URL)
	case "graphql":
		out, err = apiExplorerGraphQL(ctx, in.URL)
	default:
		return "", fmt.Errorf("@api-explorer: unknown cmd %q (valid: discover|spec|endpoint|security|graphql)", in.Cmd)
	}
	if err != nil {
		return "", fmt.Errorf("@api-explorer: %w", err)
	}
	return boundRuneSafe(out, apiExplorerMaxRendered), nil
}

// parseAPIExplorerArgs supports the {"cmd","args"} envelope, flat JSON, and
// --flag argv form (the agent flattener delivers the envelope as argv).
func parseAPIExplorerArgs(args []string) (apiExplorerArgs, error) {
	out := apiExplorerArgs{}
	payload := strings.TrimSpace(strings.Join(args, " "))

	if strings.HasPrefix(payload, "{") {
		var top map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &top); err != nil {
			return out, fmt.Errorf(`parse envelope: %w. Expected {"cmd":"discover","args":{"url":"..."}}`, err)
		}
		out.Cmd = strings.ToLower(strings.TrimSpace(jsonString(top, "cmd", "command")))
		raw := top
		if inner, ok := top["args"]; ok {
			var innerMap map[string]json.RawMessage
			if err := json.Unmarshal(inner, &innerMap); err == nil {
				raw = innerMap
			}
		}
		out.URL = strings.TrimSpace(jsonString(raw, "url", "uri", "base", "base_url", "endpoint"))
		out.Path = strings.TrimSpace(jsonString(raw, "path", "route"))
		out.Method = strings.ToUpper(strings.TrimSpace(jsonString(raw, "method", "verb")))
		out.Filter = strings.TrimSpace(jsonString(raw, "filter", "contains", "grep"))
		out.Tag = strings.TrimSpace(jsonString(raw, "tag"))
		out.Format = strings.ToLower(strings.TrimSpace(jsonString(raw, "format", "output")))
		out.Limit = jsonInt(raw, "limit", "max")
	} else {
		for _, a := range args {
			a = strings.ToLower(strings.TrimSpace(a))
			if a == "" || strings.HasPrefix(a, "-") {
				continue
			}
			if a == "discover" || a == "spec" || a == "endpoint" || a == "security" || a == "graphql" {
				out.Cmd = a
			}
			break
		}
		out.URL = stringFromFlagArgs(args, []string{"url", "uri", "base", "base_url", "endpoint"})
		out.Path = stringFromFlagArgs(args, []string{"path", "route"})
		out.Method = strings.ToUpper(stringFromFlagArgs(args, []string{"method", "verb"}))
		out.Filter = stringFromFlagArgs(args, []string{"filter", "contains", "grep"})
		out.Tag = stringFromFlagArgs(args, []string{"tag"})
		out.Format = strings.ToLower(stringFromFlagArgs(args, []string{"format", "output"}))
		if out.URL == "" {
			for _, a := range args {
				a = strings.TrimSpace(a)
				if strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
					out.URL = a
					break
				}
			}
		}
	}

	if out.URL == "" {
		return out, errors.New(`"url" is required`)
	}
	if out.Cmd == "" {
		out.Cmd = "discover"
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// discover — the headline flow.
// ---------------------------------------------------------------------------

// reconResult is the structured output of a discovery sweep, rendered as
// markdown or JSON.
type reconResult struct {
	BaseURL       string      `json:"baseURL"`
	BaseStatus    int         `json:"baseStatus"`
	Tech          []string    `json:"tech,omitempty"`
	AuthChallenge string      `json:"authChallenge,omitempty"`
	RateLimit     string      `json:"rateLimit,omitempty"`
	CORS          string      `json:"cors,omitempty"`
	OIDC          *oidcConfig `json:"oidc,omitempty"`
	SpecURL       string      `json:"specURL,omitempty"`
	SpecFormat    string      `json:"specFormat,omitempty"`
	SpecVia       string      `json:"specVia,omitempty"`
	EndpointCount int         `json:"endpointCount"`
	GraphQLURL    string      `json:"graphqlURL,omitempty"`
	Operational   []opHit     `json:"operational,omitempty"`
	Versions      []string    `json:"versions,omitempty"`
	Interesting   []string    `json:"interesting,omitempty"`
	Probed        int         `json:"probed"`

	doc *oasDoc // not serialized; used for the markdown endpoint listing
}

type opHit struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status int    `json:"status"`
	Note   string `json:"note,omitempty"`
}

func apiExplorerDiscover(ctx context.Context, base, format string) (string, error) {
	safeBase, err := validateWebTarget(base)
	if err != nil {
		return "", fmt.Errorf("refusing %q: %w", base, err)
	}
	r := runRecon(ctx, safeBase)
	if format == "json" {
		b, _ := json.MarshalIndent(r, "", "  ")
		return string(b), nil
	}
	return renderReconMarkdown(r), nil
}

// runRecon performs the full multi-vector discovery sweep.
func runRecon(ctx context.Context, safeBase string) *reconResult {
	r := &reconResult{BaseURL: safeBase}

	// 1. Base fetch — fingerprint + possible spec + hypermedia.
	baseStatus, baseHeader, baseBody, berr := apiExplorerGet(ctx, safeBase)
	var specHits []specHit
	if berr == nil {
		r.BaseStatus = baseStatus
		r.Tech = fingerprintTech(baseHeader)
		r.AuthChallenge = detectAuthChallenge(baseHeader)
		r.RateLimit = rateLimitSignal(baseHeader)
		r.CORS = corsSummary(baseHeader)
		if baseStatus == http.StatusOK {
			if doc, format, ok := tryParseOAS(baseBody, baseHeader.Get("Content-Type")); ok {
				specHits = append(specHits, specHit{safeBase, doc, format, "base URL"})
			}
		}
	}

	// 2. Concurrent probes.
	var (
		mu    sync.Mutex
		codes = map[string]int{}
	)
	addHit := func(h specHit) { mu.Lock(); specHits = append(specHits, h); mu.Unlock() }

	// 2a. Well-known spec locations.
	specURLs := specCandidateURLs(safeBase)
	r.Probed = len(specURLs)
	runBounded(ctx, specURLs, apiExplorerProbeConcurrency, func(ctx context.Context, u string) {
		status, header, body, err := apiExplorerGet(ctx, u)
		if err != nil {
			return
		}
		mu.Lock()
		codes[u] = status
		mu.Unlock()
		if status == http.StatusOK {
			if doc, format, ok := tryParseOAS(body, header.Get("Content-Type")); ok {
				addHit(specHit{u, doc, format, "well-known path"})
			}
		}
	})

	// 2b. Extract a spec URL from rendered docs pages (Swagger-UI/ReDoc/…).
	if len(specHits) == 0 {
		for _, docURL := range docUICandidateURLs(safeBase) {
			status, header, body, err := apiExplorerGet(ctx, docURL)
			if err != nil || status != http.StatusOK {
				continue
			}
			if !strings.Contains(strings.ToLower(header.Get("Content-Type")), "html") {
				continue
			}
			for _, cand := range extractSpecURLsFromHTML(string(body), docURL) {
				st, hd, bd, gerr := apiExplorerGet(ctx, cand)
				if gerr != nil || st != http.StatusOK {
					continue
				}
				if doc, format, ok := tryParseOAS(bd, hd.Get("Content-Type")); ok {
					addHit(specHit{cand, doc, format, "embedded in " + docURL})
					break
				}
			}
			if len(specHits) > 0 {
				break
			}
		}
	}

	// 2c. /.well-known manifests (OIDC + ai-plugin) — run regardless.
	origin := originOf(safeBase)
	r.OIDC = fetchOIDC(ctx, origin)
	if len(specHits) == 0 {
		if specURL := fetchAIPluginSpecURL(ctx, origin); specURL != "" {
			if st, hd, bd, gerr := apiExplorerGet(ctx, specURL); gerr == nil && st == http.StatusOK {
				if doc, format, ok := tryParseOAS(bd, hd.Get("Content-Type")); ok {
					addHit(specHit{specURL, doc, format, "ai-plugin.json manifest"})
				}
			}
		}
	}

	// 2d. Operational + Actuator surface.
	r.Operational = probeOperational(ctx, origin, safeBase)
	// 2e. Version roots.
	r.Versions = probeVersions(ctx, origin)
	// 2f. GraphQL endpoint probe (GET only — introspection stays in `graphql`).
	r.GraphQLURL = probeGraphQL(ctx, origin, safeBase)

	// 3. Pick the richest spec.
	var best *specHit
	for i := range specHits {
		if best == nil || len(specHits[i].doc.Paths) > len(best.doc.Paths) {
			best = &specHits[i]
		}
	}
	if best != nil {
		r.SpecURL = best.url
		r.SpecFormat = best.format
		r.SpecVia = best.via
		r.doc = best.doc
		r.EndpointCount = countEndpoints(best.doc)
	}

	// 4. Candidates worth a manual look (gated specs / doc UIs).
	for _, u := range specURLs {
		switch codes[u] {
		case http.StatusUnauthorized, http.StatusForbidden:
			r.Interesting = append(r.Interesting, fmt.Sprintf("%d %s", codes[u], u))
		}
	}
	sort.Strings(r.Interesting)
	return r
}

type specHit struct {
	url    string
	doc    *oasDoc
	format string
	via    string
}

func renderReconMarkdown(r *reconResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# API reconnaissance: %s\n\n", r.BaseURL)

	fmt.Fprintf(&b, "Base responded HTTP %d.\n", r.BaseStatus)
	for _, t := range r.Tech {
		fmt.Fprintf(&b, "- %s\n", t)
	}
	if r.RateLimit != "" {
		fmt.Fprintf(&b, "- Rate limiting: %s\n", r.RateLimit)
	}
	if r.CORS != "" {
		fmt.Fprintf(&b, "- CORS: %s\n", r.CORS)
	}
	if r.AuthChallenge != "" {
		fmt.Fprintf(&b, "- Auth challenge: %s\n", r.AuthChallenge)
	}
	b.WriteString("\n")

	if r.OIDC != nil {
		b.WriteString(renderOIDC(r.OIDC))
		b.WriteString("\n")
	}
	if len(r.Versions) > 0 {
		fmt.Fprintf(&b, "**API version roots detected:** %s\n\n", strings.Join(r.Versions, ", "))
	}
	if r.GraphQLURL != "" {
		fmt.Fprintf(&b, "**GraphQL endpoint detected:** %s — run `graphql` for its schema.\n\n", r.GraphQLURL)
	}
	if len(r.Operational) > 0 {
		b.WriteString("## Operational endpoints\n\n")
		for _, o := range r.Operational {
			line := fmt.Sprintf("- `%d` %s (%s)", o.Status, o.Name, o.URL)
			if o.Note != "" {
				line += " — " + o.Note
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	if r.doc != nil {
		b.WriteString("## Specification found\n\n")
		fmt.Fprintf(&b, "**%s** at %s _(via %s)_\n\n", r.SpecFormat, r.SpecURL, r.SpecVia)
		b.WriteString(renderSpecSummary(r.doc, "", "", apiExplorerDiscoverEndpointCap))
		b.WriteString("\n→ Use `spec` (with filter/tag) for the full catalog, or `endpoint` to deep-dive one operation.\n")
		return b.String()
	}

	b.WriteString("## No OpenAPI/Swagger spec found at the usual locations\n\n")
	if len(r.Interesting) > 0 {
		b.WriteString("Candidates that responded but were gated (worth a closer look):\n")
		for _, c := range r.Interesting {
			b.WriteString("  " + c + "\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Probed %d well-known spec locations plus docs pages and /.well-known manifests; none yielded a parseable spec.\n", r.Probed)
	if r.GraphQLURL != "" {
		b.WriteString("→ A GraphQL endpoint was detected — try the `graphql` cmd.\n")
	} else {
		b.WriteString("→ Inspect the base response or a known doc URL directly with @http, or run `security` for the auth/headers posture.\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// spec / endpoint.
// ---------------------------------------------------------------------------

func apiExplorerSpec(ctx context.Context, target, filter, tag string, limit int, format string) (string, error) {
	doc, specFormat, specURL, err := locateSpec(ctx, target)
	if err != nil {
		return "", err
	}
	if limit <= 0 || limit > apiExplorerSpecEndpointCap {
		limit = apiExplorerSpecEndpointCap
	}
	if format == "json" {
		return renderSpecJSON(doc, specFormat, specURL, filter, tag, limit), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n**%s** at %s\n\n", specTitle(doc), specFormat, specURL)
	b.WriteString(renderSpecSummary(doc, filter, tag, limit))
	return b.String(), nil
}

func apiExplorerEndpoint(ctx context.Context, target, path, method string) (string, error) {
	if path == "" {
		return "", errors.New(`"path" is required for the endpoint cmd`)
	}
	doc, format, specURL, err := locateSpec(ctx, target)
	if err != nil {
		return "", err
	}
	item, ok := doc.Paths[path]
	if !ok {
		item, ok = doc.Paths[strings.TrimRight(path, "/")]
	}
	if !ok {
		return "", fmt.Errorf("path %q not found in the spec (%d paths available; use the spec cmd to list them)", path, len(doc.Paths))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n_%s at %s_\n\n", path, format, specURL)
	for _, mo := range item.operations() {
		if method != "" && !strings.EqualFold(mo.Method, method) {
			continue
		}
		b.WriteString(renderOperationDetail(doc, path, mo.Method, mo.Op, item.Parameters))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ---------------------------------------------------------------------------
// Spec location + parsing.
// ---------------------------------------------------------------------------

func locateSpec(ctx context.Context, target string) (*oasDoc, string, string, error) {
	safe, err := validateWebTarget(target)
	if err != nil {
		return nil, "", "", fmt.Errorf("refusing %q: %w", target, err)
	}
	// Direct spec?
	if status, header, body, gerr := apiExplorerGet(ctx, safe); gerr == nil && status == http.StatusOK {
		if doc, format, ok := tryParseOAS(body, header.Get("Content-Type")); ok {
			return doc, format, safe, nil
		}
		// Maybe a docs page embedding the spec URL.
		if strings.Contains(strings.ToLower(header.Get("Content-Type")), "html") {
			for _, cand := range extractSpecURLsFromHTML(string(body), safe) {
				if st, hd, bd, e := apiExplorerGet(ctx, cand); e == nil && st == http.StatusOK {
					if doc, format, ok := tryParseOAS(bd, hd.Get("Content-Type")); ok {
						return doc, format, cand, nil
					}
				}
			}
		}
	}
	// Sweep well-known locations.
	for _, u := range specCandidateURLs(safe) {
		if st, hd, bd, gerr := apiExplorerGet(ctx, u); gerr == nil && st == http.StatusOK {
			if doc, format, ok := tryParseOAS(bd, hd.Get("Content-Type")); ok {
				return doc, format, u, nil
			}
		}
	}
	return nil, "", "", fmt.Errorf("no OpenAPI/Swagger spec found at %s or its well-known locations", safe)
}

// tryParseOAS attempts to decode a candidate body as OpenAPI 2.0/3.x in JSON or
// YAML, returning ok=false for anything that is not recognizably a spec.
func tryParseOAS(raw []byte, contentType string) (*oasDoc, string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] == '<' { // HTML/XML is not a spec
		return nil, "", false
	}
	var doc oasDoc
	isJSON := trimmed[0] == '{' || strings.Contains(strings.ToLower(contentType), "json")
	if isJSON {
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, "", false
		}
	} else {
		if err := yaml.Unmarshal(trimmed, &doc); err != nil {
			return nil, "", false
		}
	}
	switch {
	case doc.OpenAPI != "":
		return &doc, "OpenAPI " + doc.OpenAPI, true
	case doc.Swagger != "":
		return &doc, "Swagger " + doc.Swagger, true
	case len(doc.Paths) > 0:
		return &doc, "OpenAPI (unversioned)", true
	}
	return nil, "", false
}

func countEndpoints(doc *oasDoc) int {
	n := 0
	for _, item := range doc.Paths {
		n += len(item.operations())
	}
	return n
}

// ---------------------------------------------------------------------------
// HTTP.
// ---------------------------------------------------------------------------

func apiExplorerGet(ctx context.Context, rawURL string) (int, http.Header, []byte, error) {
	safe, err := validateWebTarget(rawURL)
	if err != nil {
		return 0, nil, nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, apiExplorerRequestTimeout)
	defer cancel()
	resp, err := webGet(reqCtx, safe, map[string]string{"Accept": "application/json, application/yaml, text/yaml, text/html, */*"})
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, apiExplorerMaxSpecBody))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

// apiExplorerRequest issues a bounded request with a specific method and headers
// (for OPTIONS/HEAD probes). Read-only methods only. It drains and closes the
// body here and returns just the status and headers, so callers never hold an
// unclosed response.
func apiExplorerRequest(ctx context.Context, method, rawURL string, headers map[string]string) (int, http.Header, error) {
	safe, err := validateWebTarget(rawURL)
	if err != nil {
		return 0, nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, apiExplorerRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, safe, nil) //#nosec G704 -- URL validated by validateWebTarget + ssrfDialControl (metadata/link-local refused, redirects re-validated)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", fallbackUserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := webHTTPClient().Do(req) //#nosec G704 -- see annotation above
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192))
	return resp.StatusCode, resp.Header, nil
}

func runBounded(ctx context.Context, items []string, concurrency int, fn func(context.Context, string)) {
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, it := range items {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(ctx, u)
		}(it)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Candidate URL sets.
// ---------------------------------------------------------------------------

func specCandidateURLs(base string) []string {
	paths := []string{
		"/openapi.json", "/openapi.yaml", "/openapi.yml", "/openapi",
		"/swagger.json", "/swagger.yaml", "/swagger/v1/swagger.json", "/swagger/v2/swagger.json",
		"/v3/api-docs", "/v3/api-docs/swagger-config", "/v2/api-docs", "/api-docs", "/api-docs.json",
		"/api/openapi.json", "/api/swagger.json", "/api/v1/openapi.json", "/api/docs/openapi.json",
		"/.well-known/openapi.json", "/docs/openapi.json", "/redoc/openapi.json", "/spec/openapi.json",
	}
	return joinCandidates(base, paths)
}

func docUICandidateURLs(base string) []string {
	paths := []string{
		"/", "/docs", "/swagger", "/swagger-ui", "/swagger-ui.html", "/swagger-ui/index.html",
		"/redoc", "/api-docs", "/api/docs", "/documentation", "/developer", "/reference",
	}
	return joinCandidates(base, paths)
}

// joinCandidates expands paths against both the origin root and the base path,
// de-duplicated in stable order.
func joinCandidates(base string, paths []string) []string {
	u, err := url.Parse(base)
	if err != nil {
		return nil
	}
	origin := u.Scheme + "://" + u.Host
	basePrefix := origin + strings.TrimRight(u.Path, "/")
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if c == "" || seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
	}
	for _, p := range paths {
		add(origin + p)
		if basePrefix != origin {
			add(basePrefix + p)
		}
	}
	return out
}

func originOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Scheme + "://" + u.Host
}

// ---------------------------------------------------------------------------
// HTML spec-URL extraction (Swagger-UI / ReDoc / RapiDoc / Stoplight).
// ---------------------------------------------------------------------------

var htmlSpecPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)spec-?url\s*=\s*["']([^"']+)["']`),                              // <redoc spec-url="...">, rapi-doc spec-url
	regexp.MustCompile(`(?i)apiDescriptionUrl\s*=\s*["']([^"']+)["']`),                      // Stoplight Elements
	regexp.MustCompile(`(?i)["']?url["']?\s*:\s*["']([^"']+\.(?:json|yaml|yml)[^"']*)["']`), // SwaggerUIBundle({url:"..."})
	regexp.MustCompile(`(?i)configUrl\s*:\s*["']([^"']+)["']`),                              // Swagger UI configUrl
	regexp.MustCompile(`(?i)href\s*=\s*["']([^"']*(?:openapi|swagger)[^"']*\.(?:json|yaml|yml))["']`),
}

// extractSpecURLsFromHTML scans a docs page for the machine spec URL and
// resolves each candidate to an absolute URL against the page URL.
func extractSpecURLsFromHTML(html, pageURL string) []string {
	if len(html) > apiExplorerMaxHTMLBody {
		html = html[:apiExplorerMaxHTMLBody]
	}
	base, _ := url.Parse(pageURL)
	seen := map[string]bool{}
	var out []string
	for _, re := range htmlSpecPatterns {
		for _, m := range re.FindAllStringSubmatch(html, 8) {
			if len(m) < 2 {
				continue
			}
			raw := strings.TrimSpace(m[1])
			if raw == "" || strings.HasPrefix(raw, "data:") || strings.HasPrefix(raw, "#") {
				continue
			}
			abs := raw
			if base != nil {
				if ref, err := url.Parse(raw); err == nil {
					abs = base.ResolveReference(ref).String()
				}
			}
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// /.well-known manifests.
// ---------------------------------------------------------------------------

type oidcConfig struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ScopesSupported       []string `json:"scopes_supported"`
	GrantTypesSupported   []string `json:"grant_types_supported"`
}

func fetchOIDC(ctx context.Context, origin string) *oidcConfig {
	for _, p := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server"} {
		status, _, body, err := apiExplorerGet(ctx, origin+p)
		if err != nil || status != http.StatusOK {
			continue
		}
		var cfg oidcConfig
		if json.Unmarshal(bytes.TrimSpace(body), &cfg) == nil && cfg.Issuer != "" {
			return &cfg
		}
	}
	return nil
}

func renderOIDC(c *oidcConfig) string {
	var b strings.Builder
	b.WriteString("## OpenID Connect / OAuth2 discovery\n\n")
	fmt.Fprintf(&b, "- Issuer: %s\n", c.Issuer)
	if c.AuthorizationEndpoint != "" {
		fmt.Fprintf(&b, "- Authorization endpoint: %s\n", c.AuthorizationEndpoint)
	}
	if c.TokenEndpoint != "" {
		fmt.Fprintf(&b, "- Token endpoint: %s\n", c.TokenEndpoint)
	}
	if c.UserinfoEndpoint != "" {
		fmt.Fprintf(&b, "- Userinfo endpoint: %s\n", c.UserinfoEndpoint)
	}
	if len(c.GrantTypesSupported) > 0 {
		fmt.Fprintf(&b, "- Grant types: %s\n", strings.Join(c.GrantTypesSupported, ", "))
	}
	if len(c.ScopesSupported) > 0 {
		fmt.Fprintf(&b, "- Scopes: %s\n", strings.Join(clip(c.ScopesSupported, 20), ", "))
	}
	return b.String()
}

func fetchAIPluginSpecURL(ctx context.Context, origin string) string {
	status, _, body, err := apiExplorerGet(ctx, origin+"/.well-known/ai-plugin.json")
	if err != nil || status != http.StatusOK {
		return ""
	}
	var manifest struct {
		API struct {
			URL string `json:"url"`
		} `json:"api"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &manifest) == nil {
		return manifest.API.URL
	}
	return ""
}

// ---------------------------------------------------------------------------
// Operational / version / GraphQL probes.
// ---------------------------------------------------------------------------

func probeOperational(ctx context.Context, origin, base string) []opHit {
	type probe struct{ name, path, note string }
	probes := []probe{
		{"health", "/health", ""}, {"health", "/healthz", ""}, {"readiness", "/readyz", ""},
		{"liveness", "/livez", ""}, {"ping", "/ping", ""}, {"status", "/status", ""},
		{"version", "/version", ""}, {"info", "/info", ""},
		{"metrics", "/metrics", "Prometheus metrics may reveal route names via labels"},
		{"actuator", "/actuator", "Spring Boot Actuator"},
		{"actuator-health", "/actuator/health", "Spring Boot Actuator"},
		{"actuator-mappings", "/actuator/mappings", "enumerates EVERY Spring route — inspect directly"},
		{"actuator-env", "/actuator/env", "Spring Actuator — may leak config"},
	}
	urls := map[string]probe{}
	var list []string
	seen := map[string]bool{}
	for _, pr := range probes {
		for _, root := range dedupeRoots(origin, base) {
			u := root + pr.path
			if !seen[u] {
				seen[u] = true
				urls[u] = pr
				list = append(list, u)
			}
		}
	}
	var (
		mu   sync.Mutex
		hits []opHit
	)
	runBounded(ctx, list, apiExplorerProbeConcurrency, func(ctx context.Context, u string) {
		status, _, _, err := apiExplorerGet(ctx, u)
		if err != nil || status == http.StatusNotFound || status >= 500 {
			return
		}
		pr := urls[u]
		mu.Lock()
		hits = append(hits, opHit{Name: pr.name, URL: u, Status: status, Note: pr.note})
		mu.Unlock()
	})
	sort.Slice(hits, func(i, j int) bool { return hits[i].URL < hits[j].URL })
	return hits
}

func probeVersions(ctx context.Context, origin string) []string {
	paths := []string{"/v1", "/v2", "/v3", "/api/v1", "/api/v2", "/api/v3", "/api"}
	var (
		mu   sync.Mutex
		hits []string
	)
	urls := make([]string, len(paths))
	for i, p := range paths {
		urls[i] = origin + p
	}
	runBounded(ctx, urls, apiExplorerProbeConcurrency, func(ctx context.Context, u string) {
		status, _, _, err := apiExplorerGet(ctx, u)
		// A version root that exists usually answers 200/401/403/405 — not 404.
		if err != nil || status == http.StatusNotFound || status >= 500 {
			return
		}
		mu.Lock()
		hits = append(hits, strings.TrimPrefix(u, origin))
		mu.Unlock()
	})
	sort.Strings(hits)
	return hits
}

// probeGraphQL looks for a GraphQL endpoint using GET only (a POST introspection
// is reserved for the explicit graphql cmd). Most servers answer GET with 200
// (GraphiQL) or 400/405 ("must POST") — either confirms presence.
func probeGraphQL(ctx context.Context, origin, base string) string {
	paths := []string{"/graphql", "/api/graphql", "/graphql/v1", "/query", "/v1/graphql"}
	for _, root := range dedupeRoots(origin, base) {
		for _, p := range paths {
			u := root + p
			status, header, _, err := apiExplorerGet(ctx, u)
			if err != nil {
				continue
			}
			ct := strings.ToLower(header.Get("Content-Type"))
			if status == http.StatusOK && (strings.Contains(ct, "html") || strings.Contains(ct, "json")) {
				return u
			}
			if status == http.StatusBadRequest || status == http.StatusMethodNotAllowed {
				return u // "must POST a query" — a GraphQL endpoint
			}
		}
	}
	return ""
}

func dedupeRoots(origin, base string) []string {
	basePrefix := strings.TrimRight(base, "/")
	if basePrefix == origin || basePrefix == "" {
		return []string{origin}
	}
	return []string{origin, basePrefix}
}

// ---------------------------------------------------------------------------
// Header fingerprinting.
// ---------------------------------------------------------------------------

func fingerprintTech(h http.Header) []string {
	if h == nil {
		return nil
	}
	var lines []string
	addFirst := func(label string, names ...string) {
		for _, n := range names {
			if v := strings.TrimSpace(h.Get(n)); v != "" {
				lines = append(lines, fmt.Sprintf("%s: %s", label, v))
				return
			}
		}
	}
	addFirst("Server", "Server")
	addFirst("Powered-by", "X-Powered-By")
	addFirst("Framework", "X-AspNet-Version", "X-AspNetMvc-Version", "X-Runtime", "X-Rails-Version")
	addFirst("Gateway/CDN", "Via", "CF-Ray", "X-Vercel-Id", "X-Amz-Cf-Id", "X-Kong-Upstream-Latency", "X-Envoy-Upstream-Service-Time", "X-Served-By")
	if fw := frameworkFromCookies(h); fw != "" {
		lines = append(lines, "Framework (cookie): "+fw)
	}
	addFirst("Content-Type", "Content-Type")
	return lines
}

// frameworkFromCookies infers the server stack from session-cookie names.
func frameworkFromCookies(h http.Header) string {
	var found []string
	for _, c := range h.Values("Set-Cookie") {
		name := strings.ToLower(strings.TrimSpace(strings.SplitN(c, "=", 2)[0]))
		switch {
		case strings.HasPrefix(name, "jsessionid"):
			found = append(found, "Java/Servlet")
		case strings.HasPrefix(name, "phpsessid"):
			found = append(found, "PHP")
		case strings.HasPrefix(name, "asp.net") || strings.HasPrefix(name, ".aspnetcore"):
			found = append(found, "ASP.NET")
		case strings.HasPrefix(name, "connect.sid"):
			found = append(found, "Node/Express")
		case strings.HasPrefix(name, "laravel_session") || name == "xsrf-token":
			found = append(found, "Laravel")
		case strings.HasPrefix(name, "_rails") || strings.Contains(name, "_session_id"):
			found = append(found, "Ruby on Rails")
		case name == "csrftoken" || name == "sessionid":
			found = append(found, "Django")
		}
	}
	return strings.Join(dedupeStrings(found), ", ")
}

func rateLimitSignal(h http.Header) string {
	for _, n := range []string{"X-RateLimit-Limit", "RateLimit-Limit", "X-Rate-Limit-Limit"} {
		if v := strings.TrimSpace(h.Get(n)); v != "" {
			rem := ""
			for _, r := range []string{"X-RateLimit-Remaining", "RateLimit-Remaining"} {
				if rv := strings.TrimSpace(h.Get(r)); rv != "" {
					rem = ", remaining " + rv
					break
				}
			}
			return "limit " + v + rem
		}
	}
	if ra := strings.TrimSpace(h.Get("Retry-After")); ra != "" {
		return "Retry-After " + ra
	}
	return ""
}

func corsSummary(h http.Header) string {
	origin := strings.TrimSpace(h.Get("Access-Control-Allow-Origin"))
	if origin == "" {
		return ""
	}
	parts := []string{"origin " + origin}
	if m := strings.TrimSpace(h.Get("Access-Control-Allow-Methods")); m != "" {
		parts = append(parts, "methods "+m)
	}
	if strings.EqualFold(h.Get("Access-Control-Allow-Credentials"), "true") {
		parts = append(parts, "credentials allowed")
	}
	return strings.Join(parts, ", ")
}

func detectAuthChallenge(h http.Header) string {
	if h == nil {
		return ""
	}
	return strings.TrimSpace(strings.Join(h.Values("WWW-Authenticate"), ", "))
}

// ---------------------------------------------------------------------------
// Small utilities.
// ---------------------------------------------------------------------------

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func clip(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return append(in[:n:n], "…")
}
