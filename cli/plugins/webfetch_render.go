/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Headless-browser escalation for @webfetch.
 *
 * Static fetches of client-rendered pages (SPAs, JS-built tables) return
 * an empty shell. When webfetch_jsdetect.go flags that, this file drives a
 * real Chromium via CDP (go-rod), waits for the DOM to settle and hands the
 * rendered HTML back to the regular extractText pipeline.
 *
 * Browser acquisition policy (keyless / self-hosted by design):
 *   1. A system Chrome / Edge / Chromium found on PATH is always preferred.
 *   2. With CHATCLI_WEBFETCH_RENDER_AUTOPROVISION=true and no system
 *      browser, rod downloads a pinned Chromium snapshot once (~150 MB,
 *      under the user cache dir) — same self-provisioning pattern as the
 *      embedded TTS assets. Without the opt-in, the plugin degrades to the
 *      static text plus an honest limitation note.
 *
 * Production posture:
 *   - One shared browser per process, launched lazily and reused across
 *     renders (a cold launch costs 1-2s; agent loops fetch in bursts).
 *     An idle timer tears it down after renderIdleShutdown so a finished
 *     session does not keep a ~200 MB Chromium resident.
 *   - Launch failures trip a circuit breaker: after renderFailureThreshold
 *     consecutive failures the escalation stays off for renderCooldown,
 *     so a broken local Chrome costs two attempts, not seconds per fetch.
 *   - Every render runs in its own incognito browser context: cookies and
 *     storage never leak between unrelated target sites.
 *
 * SSRF: the navigation target has already passed validateWebTarget, and
 * every sub-request the page makes is re-checked through a CDP hijack
 * before it leaves the browser — the in-browser equivalent of the
 * ssrfDialControl layer used for plain HTTP fetches. Verdicts for resolved
 * hostnames are cached briefly since SPAs fire hundreds of sub-requests.
 */
package plugins

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const (
	renderModeAuto   = "auto"
	renderModeAlways = "always"
	renderModeNever  = "never"

	defaultRenderTimeout = 25 * time.Second
	// domStableWindow / domStableDiff: the DOM is considered settled when it
	// changes less than 10% over a full second — long enough for late XHR
	// table fills, short enough not to stall on pages with carousels.
	domStableWindow = time.Second
	domStableDiff   = 0.1

	// renderIdleShutdown tears the shared browser down after a quiet period
	// so an interactive session doesn't keep Chromium resident forever.
	renderIdleShutdown = 2 * time.Minute
	// Circuit breaker: after renderFailureThreshold consecutive launch
	// failures the escalation is suspended for renderCooldown.
	renderFailureThreshold = 2
	renderCooldown         = 5 * time.Minute

	// maxRenderedHTMLBytes caps the serialized DOM handed to extractText,
	// mirroring the 10 MB cap of the static fetch path.
	maxRenderedHTMLBytes = 10 * 1024 * 1024

	// ssrfVerdictTTL / ssrfVerdictMaxEntries bound the per-host SSRF verdict
	// cache used for sub-requests in hardened (block-private) mode.
	ssrfVerdictTTL        = time.Minute
	ssrfVerdictMaxEntries = 4096
)

// webRenderMode resolves CHATCLI_WEBFETCH_RENDER (auto|always|never).
// Unknown values collapse to auto so a typo degrades to the default
// behaviour instead of silently disabling the feature.
func webRenderMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_WEBFETCH_RENDER"))) {
	case renderModeAlways, "on", "force":
		return renderModeAlways
	case renderModeNever, "off":
		return renderModeNever
	default:
		return renderModeAuto
	}
}

// webRenderTimeout resolves CHATCLI_WEBFETCH_RENDER_TIMEOUT (seconds).
func webRenderTimeout() time.Duration {
	if v := os.Getenv("CHATCLI_WEBFETCH_RENDER_TIMEOUT"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultRenderTimeout
}

// effectiveRenderMode merges the per-call flag (highest precedence) with
// the env-level mode.
func effectiveRenderMode(callFlag string) string {
	switch callFlag {
	case renderModeAlways, renderModeNever:
		return callFlag
	}
	return webRenderMode()
}

// resolveRenderBrowser locates the browser binary to drive, in order of
// precedence: explicit CHATCLI_WEBFETCH_RENDER_BROWSER path, rod's own
// system lookup (Chrome/Chromium/Edge), Chromium-based fallbacks rod does
// not know about (Brave), and finally the opt-in auto-provision. Empty bin
// with ok=true means "let rod download its pinned snapshot".
func resolveRenderBrowser() (bin string, ok bool, err error) {
	if explicit := strings.TrimSpace(os.Getenv("CHATCLI_WEBFETCH_RENDER_BROWSER")); explicit != "" {
		if _, statErr := os.Stat(explicit); statErr != nil {
			return "", false, fmt.Errorf("CHATCLI_WEBFETCH_RENDER_BROWSER points to %q but it is not accessible: %w", explicit, statErr)
		}
		return explicit, true, nil
	}
	if path, found := launcher.LookPath(); found {
		return path, true, nil
	}
	if path, found := lookupFallbackBrowser(); found {
		return path, true, nil
	}
	if boolEnvTrue("CHATCLI_WEBFETCH_RENDER_AUTOPROVISION") {
		return "", true, nil
	}
	return "", false, fmt.Errorf(
		"no Chrome/Chromium/Edge found for JS rendering: install one, point CHATCLI_WEBFETCH_RENDER_BROWSER at a Chromium-based binary, or set CHATCLI_WEBFETCH_RENDER_AUTOPROVISION=true to let chatcli download a pinned Chromium (~150 MB, one time)")
}

// fallbackBrowserCandidates lists Chromium-based browsers rod's LookPath
// misses. Bare names are resolved via PATH; absolute paths via stat.
var fallbackBrowserCandidates = []string{
	"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	"brave-browser",
	"brave",
	`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
}

func lookupFallbackBrowser() (string, bool) {
	for _, candidate := range fallbackBrowserCandidates {
		if strings.ContainsAny(candidate, `/\`) {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true
			}
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path, true
		}
	}
	return "", false
}

// ── Shared browser lifecycle ────────────────────────────────────────

// renderSession owns the process-wide headless browser. All fields are
// guarded by mu; the browser itself is safe for concurrent pages once
// handed out.
type renderSession struct {
	mu       sync.Mutex
	launcher *launcher.Launcher
	browser  *rod.Browser
	idle     *time.Timer

	consecutiveFailures int
	cooldownUntil       time.Time
}

var renderShared = &renderSession{}

// acquire returns a connected browser, reusing the live instance when it
// is still healthy and relaunching otherwise. Honors the failure cooldown.
func (s *renderSession) acquire() (*rod.Browser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if remaining := time.Until(s.cooldownUntil); remaining > 0 {
		return nil, fmt.Errorf("headless rendering suspended after repeated browser failures; retrying in %s", remaining.Round(time.Second))
	}
	if s.browser != nil {
		if _, err := (proto.BrowserGetVersion{}).Call(s.browser); err == nil {
			s.touchLocked()
			return s.browser, nil
		}
		// The browser died underneath us (OOM-killed, crashed): relaunch.
		s.closeLocked()
	}

	// A missing binary is a deterministic, cheap condition with an
	// actionable message — it must NOT trip the breaker.
	bin, ok, err := resolveRenderBrowser()
	if !ok {
		return nil, err
	}
	if err := s.launchLocked(bin); err != nil {
		s.registerFailureLocked()
		return nil, err
	}
	s.consecutiveFailures = 0
	s.touchLocked()
	return s.browser, nil
}

// launchLocked starts Chromium and connects CDP. Callers hold mu.
func (s *renderSession) launchLocked(bin string) error {
	// Leakless is disabled on purpose: it ships an embedded helper binary
	// that corporate AV products routinely quarantine. The idle shutdown
	// timer is our recovery path for orphaned browsers instead.
	l := launcher.New().Headless(true).Leakless(false)
	if bin != "" {
		l = l.Bin(bin)
	}
	controlURL, err := l.Launch()
	if err != nil {
		l.Cleanup()
		return fmt.Errorf("launching headless browser: %w", err)
	}
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		return fmt.Errorf("connecting to headless browser: %w", err)
	}
	s.launcher, s.browser = l, b
	return nil
}

// registerFailureLocked advances the circuit breaker. Callers hold mu.
func (s *renderSession) registerFailureLocked() {
	s.consecutiveFailures++
	if s.consecutiveFailures >= renderFailureThreshold {
		s.cooldownUntil = time.Now().Add(renderCooldown)
		s.consecutiveFailures = 0
	}
}

// touchLocked (re)arms the idle shutdown timer. Callers hold mu.
func (s *renderSession) touchLocked() {
	if s.idle != nil {
		s.idle.Stop()
	}
	s.idle = time.AfterFunc(renderIdleShutdown, s.shutdownIdle)
}

func (s *renderSession) shutdownIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

// closeLocked releases the browser, its launcher and the idle timer.
// Callers hold mu.
func (s *renderSession) closeLocked() {
	if s.browser != nil {
		_ = s.browser.Close()
		s.browser = nil
	}
	if s.launcher != nil {
		s.launcher.Kill()
		s.launcher.Cleanup()
		s.launcher = nil
	}
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

// ── Rendering ───────────────────────────────────────────────────────

// renderPageHTML navigates a headless browser to target and returns the
// rendered DOM serialized as HTML. target must already have passed
// validateWebTarget. The whole operation is bounded by webRenderTimeout.
func renderPageHTML(ctx context.Context, target string) (string, error) {
	browser, err := renderShared.acquire()
	if err != nil {
		return "", err
	}

	rctx, cancel := context.WithTimeout(ctx, webRenderTimeout())
	defer cancel()

	// Per-render incognito context: cookies, storage and cache are never
	// shared between unrelated target sites within the same session.
	inc, err := browser.Incognito()
	if err != nil {
		return "", fmt.Errorf("creating isolated browser context: %w", err)
	}
	page, err := inc.Page(proto.TargetCreateTarget{})
	if err != nil {
		return "", fmt.Errorf("opening page: %w", err)
	}
	page = page.Context(rctx)
	defer func() { _ = page.Close() }()

	if err := preparePage(page); err != nil {
		return "", err
	}
	return navigateAndSerialize(page, target)
}

// preparePage applies the UA override and installs the SSRF hijack before
// any navigation happens.
func preparePage(page *rod.Page) error {
	if err := page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: browserUA()}); err != nil {
		return fmt.Errorf("setting user agent: %w", err)
	}
	return hijackPageRequests(page)
}

// navigateAndSerialize drives the actual navigation and returns the
// settled DOM, capped at maxRenderedHTMLBytes.
func navigateAndSerialize(page *rod.Page, target string) (string, error) {
	if err := page.Navigate(target); err != nil {
		return "", fmt.Errorf("navigating: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("waiting for page load: %w", err)
	}
	// Late XHR fills: best-effort settle; on slow pages the deadline wins
	// and we serialize whatever has rendered so far.
	_ = page.WaitDOMStable(domStableWindow, domStableDiff)

	html, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("serializing rendered DOM: %w", err)
	}
	if len(html) > maxRenderedHTMLBytes {
		html = html[:maxRenderedHTMLBytes]
	}
	return html, nil
}

// hijackPageRequests installs the in-browser SSRF layer: every network
// request the rendered page issues is validated before it leaves Chromium,
// mirroring the dial-time guard of the plain HTTP path.
func hijackPageRequests(page *rod.Page) error {
	router := page.HijackRequests()
	err := router.Add("*", "", func(h *rod.Hijack) {
		if renderRequestAllowed(h.Request.URL().String()) {
			h.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		h.Response.Fail(proto.NetworkErrorReasonBlockedByClient)
	})
	if err != nil {
		return fmt.Errorf("installing request guard: %w", err)
	}
	go router.Run()
	return nil
}

// ── SSRF policy for in-page sub-requests ────────────────────────────

// renderRequestAllowed enforces the SSRF policy on a sub-request issued by
// the rendered page. Non-HTTP schemes (data:, blob:, about:) never leave
// the renderer and are allowed. For HTTP(S), the same upfront validation
// as the static path applies; in hardened deployments (block-private set)
// hostnames are additionally resolved and range-checked, since Chromium
// dials by itself and ssrfDialControl cannot intercept it.
func renderRequestAllowed(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return true
	}
	if _, err := validateWebTarget(rawURL); err != nil {
		return false
	}
	if !webBlockPrivate() {
		return true
	}
	return resolvedHostAllowed(parsed.Hostname())
}

// hostVerdicts memoizes resolve-and-check results per hostname: an SPA
// can fire hundreds of sub-requests against the same handful of hosts and
// a DNS round-trip per request would dominate render time.
var hostVerdicts = struct {
	mu      sync.Mutex
	entries map[string]hostVerdict
}{entries: map[string]hostVerdict{}}

type hostVerdict struct {
	allowed bool
	expires time.Time
}

// resolvedHostAllowed resolves host and applies checkWebIP to every
// address. Literal IPs were already handled by validateWebTarget; a
// resolution failure fails closed (the request is blocked).
func resolvedHostAllowed(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}

	hostVerdicts.mu.Lock()
	if v, ok := hostVerdicts.entries[host]; ok && time.Now().Before(v.expires) {
		hostVerdicts.mu.Unlock()
		return v.allowed
	}
	hostVerdicts.mu.Unlock()

	allowed := resolveAndCheckHost(host)

	hostVerdicts.mu.Lock()
	if len(hostVerdicts.entries) >= ssrfVerdictMaxEntries {
		hostVerdicts.entries = map[string]hostVerdict{}
	}
	hostVerdicts.entries[host] = hostVerdict{allowed: allowed, expires: time.Now().Add(ssrfVerdictTTL)}
	hostVerdicts.mu.Unlock()
	return allowed
}

func resolveAndCheckHost(host string) bool {
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if err := checkWebIP(ip, true, host); err != nil {
			return false
		}
	}
	return true
}

// ── Pipeline entry point ────────────────────────────────────────────

// extractWithRenderEscalation is the @webfetch text pipeline with the JS
// escalation chain: static extraction first; when the result looks like an
// SPA shell (or the caller forced render), a headless render replaces it;
// when no browser is available, framework state embedded in the static
// HTML is surfaced as a fallback, with an honest limitation note otherwise.
func (p *BuiltinWebFetchPlugin) extractWithRenderEscalation(ctx context.Context, parsed fetchArgs, safeURL, rawHTML string, onOutput func(string)) string {
	text := extractText(rawHTML)
	mode := effectiveRenderMode(parsed.Render)
	if mode == renderModeNever {
		return text
	}
	if mode == renderModeAuto && !looksJSRendered(rawHTML, text) {
		return text
	}

	if onOutput != nil {
		onOutput("Page appears JS-rendered; escalating to headless browser...")
	}
	rendered, err := renderPageHTML(ctx, safeURL)
	if err == nil {
		if rt := extractText(rendered); len(strings.TrimSpace(rt)) > len(strings.TrimSpace(text)) {
			return rt
		}
		return text
	}

	if onOutput != nil {
		onOutput("Headless render unavailable: " + err.Error())
	}
	if state, found := extractEmbeddedState(rawHTML); found {
		return text + "\n\n[recovered __NEXT_DATA__ page state (JSON) — the page renders client-side and this is its embedded data]\n" + state
	}
	return text + "\n\n[note: this page renders its content client-side with JavaScript; the static fetch above may be incomplete. " + err.Error() + "]"
}
