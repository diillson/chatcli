/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinRegistryTagsPlugin — @registry-tags as a native ReAct tool.
 *
 * Lists the tags published for a container image across public and private
 * OCI registries (Docker Hub, GCR, GHCR, Quay, ACR, Harbor, Artifactory, …).
 * It is keyless by default: credentials, when needed, come from the
 * environment (CHATCLI_REGISTRY_<HOST>_TOKEN, CHATCLI_REGISTRY_TOKEN, the
 * username/password pairs) or the standard docker config, exactly like
 * docker and crane — never from the model, whose inline credentials would
 * land in the transcript (still accepted for compatibility, but redacted
 * from every echoed line). No image is pulled — only the registry's
 * read-only tags API is queried — so it is safe, fast and side-effect free.
 *
 * Ported from plugins-examples/chatcli-dockerhub with the fixes the external
 * version lacked:
 *   - Returns errors instead of os.Exit, so it composes as a builtin tool.
 *   - Performs the OCI Bearer-token negotiation (the WWW-Authenticate dance),
 *     so anonymous registries that gate even public reads behind a token —
 *     GHCR, Quay, GCR — actually return tags instead of a bare 401.
 *   - Follows pagination (Docker Hub `next`, OCI `Link: rel=next`) up to a
 *     bounded cap and reports when the listing was truncated, instead of
 *     silently returning only the first page.
 *   - Uses the shared proxy/SSRF/TLS-trust-aware HTTP client, so it works
 *     behind a corporate proxy and honors CHATCLI_CA_BUNDLE just like the
 *     @webfetch / @websearch / @osv builtins.
 *
 * The external plugins-examples/chatcli-dockerhub remains as a plugin-authoring
 * example.
 */
package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// registryTagsHTTPClient is the HTTP client used for registry queries.
// Overridable in tests. It shares the proxy-aware transport so lookups
// authenticate against a corporate proxy and honor the global TLS trust
// overrides, just like @webfetch/@websearch/@osv (see builtin_web_httpclient.go).
var registryTagsHTTPClient = &http.Client{Timeout: 30 * time.Second, Transport: newWebRoundTripper(), CheckRedirect: validateRedirect}

// registryTagsMaxTags caps how many tags a single call returns. A busy
// repository (e.g. library/ubuntu) has thousands of tags; returning all of
// them would flood the model's context for no benefit. The cap is the default
// for the optional `limit` arg, which may lower it but not raise it past the
// hard ceiling below.
const (
	registryTagsDefaultLimit = 200
	registryTagsHardCap      = 1000
	registryTagsMaxPages     = 20
)

// registryTagsArgs is the typed view of @registry-tags' JSON input.
type registryTagsArgs struct {
	Image    string
	Registry string
	Username string
	Password string
	Token    string
	Limit    int
	// Sort orders the result before it is truncated to Limit:
	// newest (semver descending) | oldest (semver ascending) | name (lexical) |
	// pushed (registry push order, newest first). Empty = registry order.
	Sort string
	// Last is a shorthand: the N most recent tags (implies Sort=newest, Limit=N).
	Last int
	// AuthSource names where the credentials came from (inline, env,
	// docker-config, none) for the output line — never their values.
	AuthSource string
}

// hasInlineCreds reports whether the call itself carried credentials.
func (c registryTagsArgs) hasInlineCreds() bool {
	return c.Token != "" || c.Username != "" || c.Password != ""
}

// fetchBudget is how many tags to pull from the registry before sorting. When
// a sort is requested we must read past Limit — the newest/highest tags may sit
// at the END of a push-ordered listing — so we pull up to the hard cap and then
// sort + truncate. Without a sort, Limit is the stop and the first page wins.
func (c registryTagsArgs) fetchBudget() int {
	if c.Sort != "" {
		return registryTagsHardCap
	}
	return c.Limit
}

// BuiltinRegistryTagsPlugin is the @registry-tags tool.
type BuiltinRegistryTagsPlugin struct{}

// NewBuiltinRegistryTagsPlugin returns a ready-to-register plugin.
func NewBuiltinRegistryTagsPlugin() *BuiltinRegistryTagsPlugin { return &BuiltinRegistryTagsPlugin{} }

// Name returns "@registry-tags".
func (*BuiltinRegistryTagsPlugin) Name() string { return "@registry-tags" }

// Description surfaces the tool in the catalog.
func (*BuiltinRegistryTagsPlugin) Description() string {
	return i18n.T("plugins.registrytags.description")
}

// IsReadOnly reports true: listing tags never mutates anything.
func (*BuiltinRegistryTagsPlugin) IsReadOnly(_ []string) bool { return true }

// IsConcurrencySafe reports true: each call opens an independent HTTP
// connection to a read-only API.
func (*BuiltinRegistryTagsPlugin) IsConcurrencySafe(_ []string) bool { return true }

// DescribeCall surfaces the image being queried in the spinner. It goes
// through the real parser so a credential flag can never be mistaken for
// the image and echoed on screen.
func (*BuiltinRegistryTagsPlugin) DescribeCall(args []string) string {
	cfg, err := parseRegistryTagsArgs(args)
	if err != nil || cfg.Image == "" {
		return i18n.T("plugins.registrytags.description")
	}
	return i18n.T("plugins.registrytags.describe", cfg.Image)
}

// Usage explains the canonical invocation.
func (*BuiltinRegistryTagsPlugin) Usage() string {
	return `<tool_call name="@registry-tags" args='{"image":"redis"}' />

Flags (flat JSON or {"cmd":"tags","args":{...}} envelope):
  image     image reference (required), e.g. "redis", "library/nginx",
            "ghcr.io/cli/cli", "myreg.example.com/team/app". The registry is
            inferred from the reference; Docker Hub is the default.
  registry  override the registry base URL (e.g. https://harbor.example.com)
  limit     max tags to return (default 200, ceiling 1000)
  sort      newest | oldest | name | pushed — order before truncating. Use
            newest/last to surface recent tags on registries that list
            oldest-first (e.g. GHCR); the fetch reads all pages, then sorts.
  last      shorthand: the N most recent tags (sort=newest, limit=N)
  username / password / token
            DISCOURAGED — anything passed here lands in the transcript. Kept
            for backward compatibility only; the values are redacted from
            every echoed line. Prefer the environment or docker config.

Credentials are never required for public images. For private ones the tool
resolves them in this order and reports the winner as "auth: ..." (never the
value):
  1. inline username/password/token (discouraged, see above)
  2. per-host env  CHATCLI_REGISTRY_<HOST>_TOKEN or
                   CHATCLI_REGISTRY_<HOST>_USERNAME + _PASSWORD
                   (<HOST> = registry host upper-cased, non-alphanumerics -> _,
                   e.g. GHCR_IO, REGISTRY_EXAMPLE_COM_5000, DOCKER_IO for Hub)
  3. generic env   CHATCLI_REGISTRY_TOKEN, or CHATCLI_REGISTRY_USERNAME +
                   CHATCLI_REGISTRY_PASSWORD (legacy REGISTRY_* names also read)
  4. docker config $DOCKER_CONFIG/config.json or ~/.docker/config.json "auths"
                   (base64 user:pass or identitytoken). credsStore/credHelpers
                   are NOT invoked — such hosts are reported and need an env var.
  5. none (anonymous; public registries still work)
Ask the user to export the variable instead of pasting a secret into a call.`
}

// Version is semver. 2.x marks the builtin port of the 3.x external example
// (the version line is independent; the builtin is a fresh implementation).
func (*BuiltinRegistryTagsPlugin) Version() string { return "2.1.0" }

// Path is empty for builtin plugins.
func (*BuiltinRegistryTagsPlugin) Path() string { return "" }

// Schema describes the tool for the LLM catalog.
func (*BuiltinRegistryTagsPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "flat JSON preferred",
		"subcommands": []map[string]interface{}{
			{
				"name": "tags",
				"description": "List the tags published for a container image across public and private OCI registries " +
					"(Docker Hub, GCR, GHCR, Quay, ACR, Harbor, Artifactory). Keyless for public images. For private ones " +
					"credentials are resolved from the environment (CHATCLI_REGISTRY_<HOST>_TOKEN / CHATCLI_REGISTRY_TOKEN / " +
					"CHATCLI_REGISTRY_USERNAME+PASSWORD) or $DOCKER_CONFIG|~/.docker/config.json — DO NOT pass secrets in the " +
					"call; ask the user to export them. The output reports which source was used (auth: ...). No image is pulled. " +
					"Use this to verify that an image:tag exists before referencing it in a Dockerfile, chart or manifest.",
				"flags": []map[string]interface{}{
					{"name": "image", "type": "string", "required": true, "description": "Image reference, e.g. 'redis', 'library/nginx', 'ghcr.io/cli/cli', 'myreg.example.com/team/app'."},
					{"name": "registry", "type": "string", "required": false, "description": "Override the registry base URL (e.g. https://harbor.example.com). Usually inferred from image."},
					{"name": "limit", "type": "integer", "required": false, "description": "Max tags to return. Default 200, ceiling 1000."},
					{"name": "sort", "type": "string", "required": false, "description": "Order before truncating: newest (highest version first) | oldest | name | pushed (most recently pushed first, OCI). Omit for registry order. Needed to find the latest tags in registries that list oldest-first (e.g. GHCR)."},
					{"name": "last", "type": "integer", "required": false, "description": "Shorthand: the N most recent tags (implies sort=newest, limit=N)."},
					{"name": "username", "type": "string", "required": false, "description": "DISCOURAGED (lands in the transcript; redacted from output). Backward-compatible only — use CHATCLI_REGISTRY_<HOST>_USERNAME or docker config instead."},
					{"name": "password", "type": "string", "required": false, "description": "DISCOURAGED (lands in the transcript; redacted from output). Use CHATCLI_REGISTRY_<HOST>_PASSWORD instead."},
					{"name": "token", "type": "string", "required": false, "description": "DISCOURAGED (lands in the transcript; redacted from output). Use CHATCLI_REGISTRY_<HOST>_TOKEN or CHATCLI_REGISTRY_TOKEN instead."},
				},
				"examples": []string{
					`{"image":"redis"}`,
					`{"image":"ghcr.io/cli/cli","last":5}`,
					`{"image":"myreg.example.com/team/app"}  (private: user exports CHATCLI_REGISTRY_MYREG_EXAMPLE_COM_TOKEN first)`,
				},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and lists the tags.
func (p *BuiltinRegistryTagsPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream lists tags, emitting a progress line as it resolves the
// registry and negotiates auth.
func (p *BuiltinRegistryTagsPlugin) ExecuteWithStream(ctx context.Context, args []string, onOutput func(string)) (string, error) {
	cfg, err := parseRegistryTagsArgs(args)
	if err != nil {
		return "", fmt.Errorf("@registry-tags: %w", redactRegistrySecrets(err, cfg))
	}
	emit := func(line string) {
		if onOutput != nil {
			onOutput(line)
		}
	}

	registry, image := detectRegistryTarget(cfg.Image, cfg.Registry)
	if err := validateRegistryURL(registry); err != nil {
		return "", fmt.Errorf("@registry-tags: %w", redactRegistrySecrets(err, cfg))
	}

	resolveRegistryCredentials(registry, &cfg)
	emit(i18n.T("plugins.registrytags.describe", image))

	tags, truncated, err := fetchRegistryTags(ctx, registry, image, cfg)
	if err != nil {
		return "", fmt.Errorf("@registry-tags: %w (auth: %s)", redactRegistrySecrets(err, cfg), cfg.AuthSource)
	}
	if len(tags) == 0 {
		return i18n.T("plugins.registrytags.none", image) + "\nauth: " + cfg.AuthSource, nil
	}

	host := registryHost(registry)
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s) — %d tag(s)", image, host, len(tags))
	if cfg.Sort != "" {
		fmt.Fprintf(&b, ", sorted by %s", cfg.Sort)
	}
	if truncated {
		fmt.Fprintf(&b, ", %s", i18n.T("plugins.registrytags.truncated", cfg.Limit))
	}
	fmt.Fprintf(&b, "\nauth: %s\n", cfg.AuthSource)
	if cfg.AuthSource == registryAuthInline {
		b.WriteString(registryMsgInlineWarning + "\n")
	}
	for _, t := range tags {
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Credential sources reported on the "auth:" line.
const (
	registryAuthInline = "inline (discouraged: credentials passed in the call are visible in the transcript)"
	registryAuthNone   = "none (anonymous)"

	registryMsgInlineWarning = "note: next time ask the user to export CHATCLI_REGISTRY_<HOST>_TOKEN (or _USERNAME/_PASSWORD) instead of passing credentials in the call."

	// Environment variable names, generic and legacy. Per-host variants are
	// derived with registryHostEnvKey.
	registryTokenEnv       = "CHATCLI_REGISTRY_TOKEN"
	registryUserEnv        = "CHATCLI_REGISTRY_USERNAME"
	registryPassEnv        = "CHATCLI_REGISTRY_PASSWORD"
	registryLegacyTokenEnv = "REGISTRY_TOKEN"
	registryLegacyUserEnv  = "REGISTRY_USERNAME"
	registryLegacyPassEnv  = "REGISTRY_PASSWORD"
)

// resolveRegistryCredentials fills cfg's credentials from the first source
// that has them — inline, per-host env, generic env, legacy env, docker
// config — and records the winner in cfg.AuthSource (names only, never
// values).
func resolveRegistryCredentials(registry string, cfg *registryTagsArgs) {
	if cfg.hasInlineCreds() {
		cfg.AuthSource = registryAuthInline
		return
	}
	host := registryHost(registry)
	for _, key := range registryHostEnvKeys(host) {
		if registryCredsFromEnv(cfg, "CHATCLI_REGISTRY_"+key+"_TOKEN", "CHATCLI_REGISTRY_"+key+"_USERNAME", "CHATCLI_REGISTRY_"+key+"_PASSWORD") {
			return
		}
	}
	if registryCredsFromEnv(cfg, registryTokenEnv, registryUserEnv, registryPassEnv) {
		return
	}
	if registryCredsFromEnv(cfg, registryLegacyTokenEnv, registryLegacyUserEnv, registryLegacyPassEnv) {
		return
	}
	var note string
	cfg.Username, cfg.Password, cfg.Token, note = loadDockerCredentials(registry)
	switch {
	case cfg.hasInlineCreds():
		cfg.AuthSource = "docker-config (" + dockerConfigPath() + ")"
	case note != "":
		cfg.AuthSource = registryAuthNone + " — " + note
	default:
		cfg.AuthSource = registryAuthNone
	}
}

// registryCredsFromEnv reads a token var, else a username+password pair,
// and reports whether anything was found. The source line names the
// variable(s), never the values.
func registryCredsFromEnv(cfg *registryTagsArgs, tokenVar, userVar, passVar string) bool {
	if v := strings.TrimSpace(os.Getenv(tokenVar)); v != "" {
		cfg.Token = v
		cfg.AuthSource = "env (" + tokenVar + ")"
		return true
	}
	u, p := strings.TrimSpace(os.Getenv(userVar)), os.Getenv(passVar)
	if u != "" || p != "" {
		cfg.Username, cfg.Password = u, p
		cfg.AuthSource = "env (" + userVar + " + " + passVar + ")"
		return true
	}
	return false
}

// registryHostEnvKeys returns the env-key spellings for a registry host:
// upper-cased with every non-alphanumeric turned into "_" (ghcr.io ->
// GHCR_IO, localhost:5000 -> LOCALHOST_5000). Docker Hub also answers to
// DOCKER_IO, the name people actually use for it.
func registryHostEnvKeys(host string) []string {
	keys := []string{registryHostEnvKey(host)}
	if host == "hub.docker.com" {
		keys = append(keys, "DOCKER_IO")
	}
	return keys
}

// registryHostEnvKey is the single-host form of registryHostEnvKeys.
func registryHostEnvKey(host string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(host) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// redactRegistrySecrets replaces every inline credential value that shows
// up in err's text with a marker, so a transport or parse error can never
// echo a password back into the transcript.
func redactRegistrySecrets(err error, cfg registryTagsArgs) error {
	if err == nil || !cfg.hasInlineCreds() {
		return err
	}
	msg := err.Error()
	for _, secret := range []string{cfg.Token, cfg.Password} {
		if len(secret) >= 3 {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	return errors.New(msg)
}

// parseRegistryTagsArgs supports flat JSON, the {"cmd","args"} envelope and
// --flag argv form, reusing the shared json* extractors.
func parseRegistryTagsArgs(args []string) (registryTagsArgs, error) {
	out := registryTagsArgs{Limit: registryTagsDefaultLimit}
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
		raw = registryTagsArgvToMap(args)
	}

	out.Image = strings.TrimSpace(jsonString(raw, "image", "name", "repository", "ref"))
	out.Registry = strings.TrimSpace(jsonString(raw, "registry", "registryUrl", "url"))
	out.Username = jsonString(raw, "username", "user")
	out.Password = jsonString(raw, "password", "pass")
	out.Token = jsonString(raw, "token")
	if v := jsonInt(raw, "limit", "max"); v > 0 {
		out.Limit = v
	}
	out.Sort = strings.ToLower(strings.TrimSpace(jsonString(raw, "sort", "order")))
	out.Last = jsonInt(raw, "last", "recent")
	// --last N is sugar: the N most recent tags = sort by newest, limit N.
	if out.Last > 0 {
		if out.Sort == "" {
			out.Sort = "newest"
		}
		out.Limit = out.Last
	}

	if out.Image == "" {
		return out, fmt.Errorf(`"image" is required (e.g. {"image":"redis"})`)
	}
	switch out.Sort {
	case "", "newest", "oldest", "name", "pushed":
	default:
		return out, fmt.Errorf("invalid sort %q (valid: newest|oldest|name|pushed)", out.Sort)
	}
	if out.Limit > registryTagsHardCap {
		out.Limit = registryTagsHardCap
	}
	return out, nil
}

// registryTagsArgvToMap converts `--flag value`, `--flag=value` and a single
// bare positional (the image) into the raw map the JSON path produces.
func registryTagsArgvToMap(args []string) map[string]json.RawMessage {
	raw := map[string]json.RawMessage{}
	put := func(k, v string) {
		if b, err := json.Marshal(v); err == nil {
			raw[k] = b
		}
	}
	for i := 0; i < len(args); i++ {
		a := strings.TrimSpace(args[i])
		switch {
		case strings.HasPrefix(a, "--") && strings.Contains(a, "="):
			kv := strings.SplitN(strings.TrimPrefix(a, "--"), "=", 2)
			put(kv[0], trimQuotes(kv[1]))
		case strings.HasPrefix(a, "--"):
			key := strings.TrimPrefix(a, "--")
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				put(key, trimQuotes(args[i+1]))
				i++
			}
		case a != "":
			if _, ok := raw["image"]; !ok {
				put("image", a)
			}
		}
	}
	return raw
}

// detectRegistryTarget infers the registry base URL and the bare repository
// path from an image reference. A reference whose first path segment contains
// a dot or a port (`ghcr.io/...`, `host:5000/...`) names a registry; otherwise
// the image lives on Docker Hub. An explicit registry override wins.
func detectRegistryTarget(image, override string) (registry, repo string) {
	image = strings.TrimSpace(image)
	if override != "" {
		registry = ensureScheme(override)
		return registry, image
	}

	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 && looksLikeRegistryHost(parts[0]) {
		host := parts[0]
		repo = parts[1]
		switch host {
		case "docker.io", "registry.hub.docker.com", "index.docker.io":
			return "https://hub.docker.com", repo
		default:
			return ensureScheme(host), repo
		}
	}
	// Bare name → Docker Hub.
	return "https://hub.docker.com", image
}

// looksLikeRegistryHost reports whether a leading path segment is a registry
// host rather than a Docker Hub namespace. Hosts have a dot (registry.io),
// a port (localhost:5000) or are exactly "localhost".
func looksLikeRegistryHost(seg string) bool {
	return strings.Contains(seg, ".") || strings.Contains(seg, ":") || seg == "localhost"
}

// ensureScheme prepends https:// when the URL carries no scheme.
func ensureScheme(s string) string {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return strings.TrimRight(s, "/")
	}
	return "https://" + strings.TrimRight(s, "/")
}

// validateRegistryURL applies the shared SSRF policy (scheme + cloud-metadata)
// to the registry host. Private/loopback hosts stay allowed so internal
// corporate registries work; metadata endpoints are refused.
func validateRegistryURL(registry string) error {
	_, err := validateWebTarget(registry)
	return err
}

// registryHost returns the bare host of a registry URL for display.
func registryHost(registry string) string {
	if u, err := url.Parse(registry); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(registry, "https://"), "http://")
}

// isDockerHub reports whether the registry is Docker Hub's repository API,
// which uses a different (paginated JSON) shape than the OCI tags endpoint.
func isDockerHub(registry string) bool {
	return strings.Contains(registry, "hub.docker.com")
}

// fetchRegistryTags lists tags for image on registry, returning the (capped)
// tag slice and whether the listing was truncated at the limit.
func fetchRegistryTags(ctx context.Context, registry, image string, cfg registryTagsArgs) ([]string, bool, error) {
	var (
		tags      []string
		truncated bool
		err       error
	)
	hub := isDockerHub(registry)
	if hub {
		tags, truncated, err = fetchDockerHubTags(ctx, registry, image, cfg)
	} else {
		tags, truncated, err = fetchOCITags(ctx, registry, image, cfg)
	}
	if err != nil {
		return nil, false, err
	}

	if cfg.Sort != "" {
		sortTags(tags, cfg.Sort)
		if len(tags) > cfg.Limit {
			tags = tags[:cfg.Limit]
			truncated = true
		}
	}
	return tags, truncated, nil
}

// sortTags orders tags in place according to mode. "newest"/"oldest" use a
// semver-aware comparator (non-semver tags rank below versioned ones, then
// lexically); "name" is lexical ascending; "pushed" reverses the registry's
// push order so the most recently pushed tags come first.
func sortTags(tags []string, mode string) {
	switch mode {
	case "name":
		sort.Strings(tags)
	case "pushed":
		for i, j := 0, len(tags)-1; i < j; i, j = i+1, j-1 {
			tags[i], tags[j] = tags[j], tags[i]
		}
	case "oldest":
		sort.SliceStable(tags, func(i, j int) bool { return compareTagVersions(tags[i], tags[j]) < 0 })
	case "newest":
		sort.SliceStable(tags, func(i, j int) bool { return compareTagVersions(tags[i], tags[j]) > 0 })
	}
}

// semverTag is a parsed, comparable version tag.
type semverTag struct {
	major, minor, patch int
	pre                 string
}

// semverTagRe matches an optional v-prefix, major.minor, optional .patch and an
// optional -pre/+build suffix. Lenient by design — registry tags are messy.
var semverTagRe = regexp.MustCompile(`^[vV]?(\d+)\.(\d+)(?:\.(\d+))?(?:[-+](.+))?$`)

// parseSemverish parses a tag into a semverTag, reporting whether it looked like
// a version at all.
func parseSemverish(tag string) (semverTag, bool) {
	m := semverTagRe.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return semverTag{}, false
	}
	maj, _ := strconv.Atoi(m[1])
	minr, _ := strconv.Atoi(m[2])
	patch := 0
	if m[3] != "" {
		patch, _ = strconv.Atoi(m[3])
	}
	return semverTag{maj, minr, patch, m[4]}, true
}

// compareTagVersions returns -1/0/1 in ascending version order. Versioned tags
// outrank non-versioned ones; a release outranks its pre-release; non-versioned
// tags fall back to lexical comparison.
func compareTagVersions(a, b string) int {
	av, aok := parseSemverish(a)
	bv, bok := parseSemverish(b)
	switch {
	case aok && bok:
		return av.compare(bv)
	case aok:
		return 1
	case bok:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

// compare orders two semverTags ascending.
func (v semverTag) compare(o semverTag) int {
	if c := cmpInt(v.major, o.major); c != 0 {
		return c
	}
	if c := cmpInt(v.minor, o.minor); c != 0 {
		return c
	}
	if c := cmpInt(v.patch, o.patch); c != 0 {
		return c
	}
	// Equal numerics: a release (no pre-release) outranks a pre-release.
	switch {
	case v.pre == "" && o.pre == "":
		return 0
	case v.pre == "":
		return 1
	case o.pre == "":
		return -1
	default:
		return strings.Compare(v.pre, o.pre)
	}
}

// cmpInt is the three-way comparison for ints.
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// fetchDockerHubTags walks the Docker Hub repository tags API, following the
// `next` cursor until the limit is reached.
func fetchDockerHubTags(ctx context.Context, registry, image string, cfg registryTagsArgs) ([]string, bool, error) {
	repo := image
	if !strings.Contains(repo, "/") {
		repo = "library/" + repo // official images live under library/
	}
	next := fmt.Sprintf("%s/v2/repositories/%s/tags/?page_size=100", registry, repo)
	budget := cfg.fetchBudget()

	var tags []string
	for page := 0; next != "" && page < registryTagsMaxPages; page++ {
		body, err := registryGet(ctx, next, registryBasicAuth(cfg))
		if err != nil {
			return nil, false, err
		}
		var parsed struct {
			Next    string `json:"next"`
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, false, fmt.Errorf("parsing Docker Hub response: %w", err)
		}
		for _, r := range parsed.Results {
			tags = append(tags, r.Name)
			if len(tags) >= budget {
				return tags[:budget], true, nil
			}
		}
		next = parsed.Next
	}
	return tags, false, nil
}

// fetchOCITags queries the OCI /v2/<image>/tags/list endpoint, performing the
// Bearer-token negotiation on a 401 challenge and following Link pagination.
func fetchOCITags(ctx context.Context, registry, image string, cfg registryTagsArgs) ([]string, bool, error) {
	bearer := ""
	if cfg.Token != "" {
		bearer = "Bearer " + cfg.Token
	}

	next := fmt.Sprintf("%s/v2/%s/tags/list?n=100", registry, image)
	budget := cfg.fetchBudget()
	var tags []string
	for page := 0; next != "" && page < registryTagsMaxPages; page++ {
		auth := bearer
		if auth == "" {
			auth = registryBasicAuth(cfg)
		}
		status, header, body, err := registryFetch(ctx, next, auth)
		if err != nil {
			return nil, false, err
		}

		// Anonymous OCI registries (GHCR, Quay, GCR) gate even public reads
		// behind a token: a 401 carries a WWW-Authenticate Bearer challenge we
		// satisfy once, then retry the same page.
		if status == http.StatusUnauthorized && bearer == "" {
			tok, terr := negotiateBearerToken(ctx, header.Get("WWW-Authenticate"), cfg)
			if terr != nil {
				return nil, false, terr
			}
			bearer = "Bearer " + tok
			status, header, body, err = registryFetch(ctx, next, bearer)
			if err != nil {
				return nil, false, err
			}
		}
		if serr := registryStatusErr(status, body); serr != nil {
			return nil, false, serr
		}

		var parsed struct {
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, false, fmt.Errorf("parsing registry response: %w", err)
		}
		for _, t := range parsed.Tags {
			tags = append(tags, t)
			if len(tags) >= budget {
				return tags[:budget], true, nil
			}
		}
		next = nextLinkURL(registry, header.Get("Link"))
	}
	return tags, false, nil
}

// negotiateBearerToken performs the Docker registry token handshake: it parses
// the WWW-Authenticate Bearer challenge (realm/service/scope), requests a token
// from the realm (carrying Basic auth for private repositories) and returns it.
func negotiateBearerToken(ctx context.Context, challenge string, cfg registryTagsArgs) (string, error) {
	realm, params := parseBearerChallenge(challenge)
	if realm == "" {
		return "", fmt.Errorf("registry returned 401 without a Bearer realm (check credentials)")
	}
	q := url.Values{}
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	if scope := params["scope"]; scope != "" {
		q.Set("scope", scope)
	}
	tokenURL := realm
	if enc := q.Encode(); enc != "" {
		tokenURL += "?" + enc
	}

	body, err := registryGet(ctx, tokenURL, registryBasicAuth(cfg))
	if err != nil {
		return "", fmt.Errorf("token negotiation: %w", err)
	}
	var parsed struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}
	if parsed.Token != "" {
		return parsed.Token, nil
	}
	if parsed.AccessToken != "" {
		return parsed.AccessToken, nil
	}
	return "", fmt.Errorf("token negotiation returned an empty token")
}

// parseBearerChallenge extracts the realm and the remaining key="value" params
// from a `Bearer realm="…",service="…",scope="…"` WWW-Authenticate header.
func parseBearerChallenge(h string) (realm string, params map[string]string) {
	params = map[string]string{}
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return "", params
	}
	for _, part := range splitBearerParams(h[len("Bearer "):]) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if key == "realm" {
			realm = val
		} else {
			params[key] = val
		}
	}
	return realm, params
}

// splitBearerParams splits a challenge parameter list on commas that are not
// inside a quoted value (a scope value can itself contain commas).
func splitBearerParams(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// nextLinkURL resolves the `rel=next` target of an RFC 5988 Link header to an
// absolute URL against the registry base, or "" when there is no next page.
func nextLinkURL(registry, link string) string {
	link = strings.TrimSpace(link)
	if link == "" || !strings.Contains(link, `rel="next"`) {
		return ""
	}
	start := strings.IndexByte(link, '<')
	end := strings.IndexByte(link, '>')
	if start < 0 || end < 0 || end <= start+1 {
		return ""
	}
	ref := link[start+1 : end]
	base, err := url.Parse(registry)
	if err != nil {
		return ""
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return base.ResolveReference(rel).String()
}

// registryBasicAuth builds a Basic Authorization header value from cfg's
// username/password, or "" when no credentials are present.
func registryBasicAuth(cfg registryTagsArgs) string {
	if cfg.Username == "" && cfg.Password == "" {
		return ""
	}
	enc := base64.StdEncoding.EncodeToString([]byte(cfg.Username + ":" + cfg.Password))
	return "Basic " + enc
}

// registryGet returns the body for a 2xx response, mapping common failures
// (401/403/404) to actionable errors.
func registryGet(ctx context.Context, rawURL, authHeader string) ([]byte, error) {
	status, _, body, err := registryFetch(ctx, rawURL, authHeader)
	if err != nil {
		return nil, err
	}
	if serr := registryStatusErr(status, body); serr != nil {
		return nil, serr
	}
	return body, nil
}

// registryFetch issues a single GET through the shared proxy/TLS-aware client
// and returns the status, response headers and the fully-read body. It closes
// the response body itself, so no caller can leak it.
//
// gosec G704 flags the request as an SSRF taint flow because the registry host
// can come from operator/agent input; the egress is constrained by the shared
// ssrfDialControl (metadata/link-local always refused) and validateRegistryURL
// upstream, mirroring the @webfetch/@osv convention.
func registryFetch(ctx context.Context, rawURL, authHeader string) (int, http.Header, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil) //#nosec G704 -- registry URL validated by validateRegistryURL + ssrfDialControl (metadata/link-local refused)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "chatcli-registry-tags/2.0")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := registryTagsHTTPClient.Do(req) //#nosec G704 -- see validateRegistryURL + ssrfDialControl
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
}

// registryStatusErr maps the non-2xx statuses registries use to actionable
// errors, or nil for a 200.
func registryStatusErr(status int, body []byte) error {
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("authentication failed (%d) — set username/password or token for this registry", status)
	case http.StatusNotFound:
		return fmt.Errorf("image or repository not found (404)")
	default:
		snippet := body
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		return fmt.Errorf("registry returned HTTP %d: %s", status, strings.TrimSpace(string(snippet)))
	}
}

// dockerConfigPath is $DOCKER_CONFIG/config.json (the docker CLI's own
// override) or ~/.docker/config.json.
func dockerConfigPath() string {
	if dir := strings.TrimSpace(os.Getenv("DOCKER_CONFIG")); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".docker", "config.json")
}

// loadDockerCredentials reads the docker config and returns the
// username/password (decoded from the base64 `auth`) or an identity token
// for the registry that best matches host. credsStore/credHelpers (which
// shell out to external docker-credential-* binaries) are intentionally not
// invoked; when one would have served the host, note says so, so the user
// learns why the login docker knows about did not apply and which env var
// to export instead.
func loadDockerCredentials(registry string) (username, password, token, note string) {
	path := dockerConfigPath()
	if path == "" {
		return "", "", "", ""
	}
	data, err := os.ReadFile(path) //#nosec G304 -- fixed, well-known per-user path (or the docker CLI's own DOCKER_CONFIG override)
	if err != nil {
		return "", "", "", ""
	}
	var cfg struct {
		Auths map[string]struct {
			Auth          string `json:"auth"`
			IdentityToken string `json:"identitytoken"`
		} `json:"auths"`
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", "", ""
	}

	want := registryHost(registry)
	for key, entry := range cfg.Auths {
		if !registryKeyMatches(key, want) {
			continue
		}
		if entry.IdentityToken != "" {
			return "", "", entry.IdentityToken, ""
		}
		decoded, derr := base64.StdEncoding.DecodeString(entry.Auth)
		if derr != nil {
			continue
		}
		if u, p, ok := strings.Cut(string(decoded), ":"); ok {
			return u, p, "", ""
		}
	}
	for key, helper := range cfg.CredHelpers {
		if registryKeyMatches(key, want) && helper != "" {
			return "", "", "", "docker config uses credHelper " + helper + " for this host, which this tool does not invoke; export CHATCLI_REGISTRY_" + registryHostEnvKey(want) + "_TOKEN instead"
		}
	}
	if cfg.CredsStore != "" {
		return "", "", "", "docker config uses credsStore " + cfg.CredsStore + ", which this tool does not invoke; export CHATCLI_REGISTRY_" + registryHostEnvKey(want) + "_TOKEN instead"
	}
	return "", "", "", ""
}

// registryKeyMatches reports whether a ~/.docker/config.json auth key refers to
// the wanted host, tolerating scheme prefixes and trailing slashes on either
// side. Docker Hub stores its key as the legacy index URL.
func registryKeyMatches(key, want string) bool {
	norm := func(s string) string {
		s = strings.TrimPrefix(s, "https://")
		s = strings.TrimPrefix(s, "http://")
		return strings.TrimRight(s, "/")
	}
	k, w := norm(key), norm(want)
	if w == "hub.docker.com" && (strings.Contains(k, "docker.io") || strings.Contains(k, "index.docker.io")) {
		return true
	}
	return k == w || strings.HasPrefix(k, w+"/") || strings.HasPrefix(w, k+"/")
}
