/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package web

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"testing"

	"golang.org/x/net/html"

	v1 "github.com/diillson/chatcli/operator/api/v1alpha1"
)

// The dashboard is one self-contained file the REST server serves as is, so
// a restyle is only safe when the contract the inline script relies on
// survives it: every element the script looks up by id is in the document,
// the tab bar still names the ten sections, and the stylesheet declares the
// design tokens the templates reference. These tests are that contract.

func dashboardSource(t *testing.T) string {
	t.Helper()
	raw, err := StaticFiles.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read embedded dashboard: %v", err)
	}
	return string(raw)
}

// parseDashboard returns the ids declared in the markup, the ids declared in
// the inline script, the script text, the style text and the tab bar values.
type dashboardShape struct {
	markupIDs map[string]bool
	scriptIDs map[string]bool
	script    string
	style     string
	catalog   string
	tabs      []string
}

func parseDashboard(t *testing.T, src string) dashboardShape {
	t.Helper()
	shape := dashboardShape{markupIDs: map[string]bool{}, scriptIDs: map[string]bool{}}
	tz := html.NewTokenizer(strings.NewReader(src))
	var scripts, styles []string
	for {
		tt := tz.Next()
		switch tt {
		case html.ErrorToken:
			if err := tz.Err(); err != nil && !errors.Is(err, io.EOF) {
				t.Fatalf("tokenize dashboard: %v", err)
			}
			shape.script = strings.Join(scripts, "\n")
			shape.style = strings.Join(styles, "\n")
			// Elements the script builds itself: id="x" inside a template,
			// or el.id = 'x' on a node it created.
			for _, re := range []*regexp.Regexp{
				regexp.MustCompile(`\bid="([A-Za-z0-9_-]+)"`),
				regexp.MustCompile(`\.id = '([A-Za-z0-9_-]+)'`),
			} {
				for _, m := range re.FindAllStringSubmatch(shape.script, -1) {
					shape.scriptIDs[m[1]] = true
				}
			}
			return shape
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := tz.Token()
			var id, tab, typ string
			for _, a := range tok.Attr {
				switch a.Key {
				case "id":
					id = a.Val
				case "data-tab":
					tab = a.Val
				case "type":
					typ = a.Val
				}
			}
			if id != "" {
				if shape.markupIDs[id] {
					t.Errorf("duplicate id %q in markup", id)
				}
				shape.markupIDs[id] = true
			}
			if tok.Data == "button" && tab != "" {
				shape.tabs = append(shape.tabs, tab)
			}
			if tok.Data == "script" || tok.Data == "style" {
				if tz.Next() == html.TextToken {
					text := string(tz.Text())
					switch {
					case tok.Data == "style":
						styles = append(styles, text)
					case typ == "application/json" && id == "i18n-catalog":
						shape.catalog = text
					default:
						scripts = append(scripts, text)
					}
				}
			}
		}
	}
}

func TestDashboardDeclaresDesignTokens(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	root := shape.style[strings.Index(shape.style, ":root"):]
	root = root[:strings.Index(root, "}")]
	for _, tok := range []string{"--bg", "--panel", "--accent", "--ok", "--err"} {
		if !strings.Contains(root, tok+":") {
			t.Errorf(":root does not declare %s", tok)
		}
	}
	// Legacy names the inline templates use must keep resolving.
	for _, tok := range []string{"--text-muted", "--sev-critical", "--sev-high", "--sev-medium", "--sev-low", "--card-bg", "--success", "--warning"} {
		if !strings.Contains(root, tok+":") {
			t.Errorf(":root does not declare legacy token %s referenced by the script", tok)
		}
	}
	for _, want := range []string{"prefers-color-scheme:light", "prefers-reduced-motion:reduce", ":focus-visible"} {
		if !strings.Contains(shape.style, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
}

func TestDashboardScriptFindsEveryID(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	refs := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`getElementById\('([A-Za-z0-9_-]+)'\)`),
		regexp.MustCompile(`\$\('#([A-Za-z0-9_-]+)'\)`),
	} {
		for _, m := range re.FindAllStringSubmatch(shape.script, -1) {
			refs[m[1]] = true
		}
	}
	if len(refs) < 20 {
		t.Fatalf("expected the script to reference dozens of ids, found %d", len(refs))
	}
	var missing []string
	for id := range refs {
		if !shape.markupIDs[id] && !shape.scriptIDs[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("script references ids that no markup declares: %v", missing)
	}
	// The elements the auth flow and the polling loop drive stay in place.
	for _, id := range []string{"tabBar", "healthBadge", "refreshDot", "refreshSelect", "refreshTimer", "logoutBtn", "timeDropdown", "timeRangeLabel", "toastContainer", "overviewStats", "approvalBadge"} {
		if !shape.markupIDs[id] {
			t.Errorf("markup lacks id %q", id)
		}
	}
	for _, id := range []string{"apiKeyOverlay", "apiKeyInput", "apiKeySubmit", "apiKeyError"} {
		if !shape.scriptIDs[id] && !strings.Contains(shape.script, "'"+id+"'") {
			t.Errorf("login flow lost id %q", id)
		}
	}
}

func TestDashboardTabBarKeepsItsSections(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	want := []string{"overview", "incidents", "slos", "approvals", "aiinsights", "remediations", "runbooks", "postmortems", "clusters", "audit"}
	if strings.Join(shape.tabs, ",") != strings.Join(want, ",") {
		t.Fatalf("tab bar = %v, want %v", shape.tabs, want)
	}
	for _, tab := range want {
		if !shape.markupIDs["page-"+tab] {
			t.Errorf("tab %q has no page-%s panel", tab, tab)
		}
	}
}

func TestDashboardKeepsItsWiring(t *testing.T) {
	src := dashboardSource(t)
	shape := parseDashboard(t, src)
	for _, want := range []string{
		"const API = '/api/v1'",
		"localStorage.getItem('chatcli_api_key')",
		"'X-API-Key'",
		"fetch('/healthz')",
		"setInterval(",
		"localStorage.getItem('panelOrder_' + containerId)",
	} {
		if !strings.Contains(shape.script, want) {
			t.Errorf("script lost %q", want)
		}
	}
	for _, forbidden := range []string{"<link ", "https://", "http://", "@import", "@font-face"} {
		if strings.Contains(shape.style, forbidden) || strings.Contains(src[:strings.Index(src, "<body")], forbidden) {
			t.Errorf("dashboard head or stylesheet reaches outside the file: %q", forbidden)
		}
	}
}

// ---- Theme switcher and i18n -------------------------------------------
//
// The header switchers persist to localStorage (chatcli_dash_theme,
// chatcli_dash_lang), the theme is pinned through data-theme on <html>, and
// every string the page renders resolves through the JSON catalog. These
// tests keep that contract: the catalog parses, both languages carry the
// same keys, and every key the markup or the script asks for exists.

func dashboardCatalog(t *testing.T, shape dashboardShape) map[string]map[string]string {
	t.Helper()
	if strings.TrimSpace(shape.catalog) == "" {
		t.Fatal("dashboard has no <script type=\"application/json\" id=\"i18n-catalog\"> block")
	}
	var catalog map[string]map[string]string
	if err := json.Unmarshal([]byte(shape.catalog), &catalog); err != nil {
		t.Fatalf("i18n catalog is not valid JSON: %v", err)
	}
	return catalog
}

var placeholderRe = regexp.MustCompile(`\{(\w+)\}`)

func TestDashboardCatalogLanguagesMatch(t *testing.T) {
	catalog := dashboardCatalog(t, parseDashboard(t, dashboardSource(t)))
	for _, want := range []string{"en", "pt-BR"} {
		if _, ok := catalog[want]; !ok {
			t.Fatalf("catalog lacks language %q", want)
		}
	}
	en, pt := catalog["en"], catalog["pt-BR"]
	if len(en) < 200 {
		t.Fatalf("expected a few hundred keys, en has %d", len(en))
	}
	for key, val := range en {
		if strings.TrimSpace(val) == "" {
			t.Errorf("en[%q] is empty", key)
		}
		ptVal, ok := pt[key]
		if !ok {
			t.Errorf("pt-BR lacks key %q", key)
			continue
		}
		if strings.TrimSpace(ptVal) == "" {
			t.Errorf("pt-BR[%q] is empty", key)
		}
		// The same {var} placeholders in both, so t(key, vars) fills each.
		enVars := placeholderRe.FindAllString(val, -1)
		ptVars := placeholderRe.FindAllString(ptVal, -1)
		sort.Strings(enVars)
		sort.Strings(ptVars)
		if strings.Join(enVars, ",") != strings.Join(ptVars, ",") {
			t.Errorf("placeholders differ for %q: en %v, pt-BR %v", key, enVars, ptVars)
		}
	}
	for key := range pt {
		if _, ok := en[key]; !ok {
			t.Errorf("pt-BR has key %q that en lacks", key)
		}
	}
}

func TestDashboardEveryI18nKeyExists(t *testing.T) {
	src := dashboardSource(t)
	shape := parseDashboard(t, src)
	en := dashboardCatalog(t, shape)["en"]
	refs := map[string]string{}
	// Static markup and the templates the script builds: data-i18n="key",
	// data-i18n-title="key", data-i18n-placeholder, data-i18n-aria, data-i18n-label.
	for _, m := range regexp.MustCompile(`data-i18n(?:-[a-z]+)?="([^"]+)"`).FindAllStringSubmatch(src, -1) {
		refs[m[1]] = "markup"
	}
	// Script calls: t('key') and t("key").
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`\bt\('([^']+)'`),
		regexp.MustCompile(`\bt\("([^"]+)"`),
	} {
		for _, m := range re.FindAllStringSubmatch(shape.script, -1) {
			refs[m[1]] = "script"
		}
	}
	if len(refs) < 150 {
		t.Fatalf("expected the page to reference well over a hundred keys, found %d", len(refs))
	}
	var missing []string
	for key, where := range refs {
		if _, ok := en[key]; !ok {
			missing = append(missing, key+" ("+where+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("keys referenced but absent from the catalog: %v", missing)
	}
	// The switchers and the recent-incidents filters are wired by id and
	// labeled through the catalog.
	for _, id := range []string{"themeSelect", "langSelect", "recentFilters", "recentSeverityFilter", "recentStateFilter", "recentKindFilter", "recentChaosFilter", "recentNamespaceFilter", "recentFiltersReset"} {
		if !shape.markupIDs[id] {
			t.Errorf("markup lacks id %q", id)
		}
	}
	for _, key := range []string{"theme.label", "theme.system", "theme.dark", "theme.light", "lang.label", "filter.reset", "filter.allSeverities", "filter.allStates", "filter.allKinds", "filter.hideChaos", "filter.allSources", "filter.onlyChaos", "filter.namespace", "recent.empty", "recent.emptyFilter"} {
		if _, ok := en[key]; !ok {
			t.Errorf("catalog lacks key %q", key)
		}
	}
	for _, want := range []string{"function t(key, vars)", "function applyTranslations(", "localStorage.setItem('chatcli_dash_lang'", "RECENT_FILTERS_KEY = 'chatcli_dash_recentFilters'", "localStorage.getItem(RECENT_FILTERS_KEY)", "localStorage.setItem(RECENT_FILTERS_KEY", "new Intl.DateTimeFormat(lang", "new Intl.NumberFormat(lang"} {
		if !strings.Contains(shape.script, want) {
			t.Errorf("script lost %q", want)
		}
	}
}

// Enum values arrive raw from the API and are displayed through the
// catalog, so every CRD state and severity the operator can emit needs a
// label in both languages.
func TestDashboardCatalogCoversCRDEnums(t *testing.T) {
	catalog := dashboardCatalog(t, parseDashboard(t, dashboardSource(t)))
	var keys []string
	for _, s := range []v1.IssueSeverity{v1.IssueSeverityCritical, v1.IssueSeverityHigh, v1.IssueSeverityMedium, v1.IssueSeverityLow} {
		keys = append(keys, "severity."+string(s))
	}
	for _, s := range []v1.IssueState{v1.IssueStateDetected, v1.IssueStateAnalyzing, v1.IssueStateRemediating, v1.IssueStateContained, v1.IssueStateResolved, v1.IssueStateEscalated, v1.IssueStateFailed} {
		keys = append(keys, "state."+string(s))
	}
	for _, s := range []v1.RemediationState{v1.RemediationStatePending, v1.RemediationStateWaitingApproval, v1.RemediationStateExecuting, v1.RemediationStateVerifying, v1.RemediationStateCompleted, v1.RemediationStateFailed, v1.RemediationStateRolledBack} {
		keys = append(keys, "state."+string(s))
	}
	for _, s := range []v1.ApprovalRequestState{v1.ApprovalStatePending, v1.ApprovalStateApproved, v1.ApprovalStateRejected, v1.ApprovalStateExpired} {
		keys = append(keys, "state."+string(s))
	}
	for _, s := range []v1.PostMortemState{v1.PostMortemStateOpen, v1.PostMortemStateInReview, v1.PostMortemStateClosed} {
		keys = append(keys, "state."+string(s))
	}
	for _, lang := range []string{"en", "pt-BR"} {
		for _, key := range keys {
			if catalog[lang][key] == "" {
				t.Errorf("%s lacks enum label %q", lang, key)
			}
		}
	}
}

func TestDashboardThemeSwitcher(t *testing.T) {
	src := dashboardSource(t)
	shape := parseDashboard(t, src)
	head := src[:strings.Index(src, "<body")]
	// The bootstrap runs in <head>, before the stylesheet, and reads the
	// persisted choice so the first paint is already in the chosen theme.
	if !strings.Contains(head, "localStorage.getItem") || !strings.Contains(head, "chatcli_dash_theme") {
		t.Error("head lacks the theme bootstrap reading chatcli_dash_theme")
	}
	if strings.Index(head, "chatcli_dash_theme") > strings.Index(head, "<style>") {
		t.Error("theme bootstrap must run before the stylesheet")
	}
	for _, want := range []string{"chatcli_dash_theme", "chatcli_dash_lang", "params.get('theme')", "params.get('lang')", "navigator.language"} {
		if !strings.Contains(head, want) {
			t.Errorf("head bootstrap lacks %q", want)
		}
	}
	for _, want := range []string{`[data-theme="light"]`, `[data-theme="dark"]`, `:root:not([data-theme="dark"])`, "prefers-color-scheme:light"} {
		if !strings.Contains(shape.style, want) {
			t.Errorf("stylesheet lacks %q", want)
		}
	}
	for _, want := range []string{"localStorage.setItem('chatcli_dash_theme'", "function applyTheme(", "function changeTheme(", "function changeLanguage("} {
		if !strings.Contains(shape.script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
}

// TestDashboardScriptNeverShadowsTranslate guards the i18n helper: a local
// named t inside any function hides the global t() and every translated
// string rendered after it throws "t is not a function". It happened once
// (timeAgo) and took every table with a relative time down.
func TestDashboardScriptNeverShadowsTranslate(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	re := regexp.MustCompile(`(?m)\b(?:const|let|var)\s+t\s*=|function\s*\([^)]*\bt\b[^)]*\)|\(\s*t\s*\)\s*=>|catch\s*\(\s*t\s*\)`)
	if m := re.FindAllString(shape.script, -1); len(m) > 0 {
		t.Fatalf("the inline script shadows the translation helper t(): %v", m)
	}
}

// The overview "Recent Incidents" panel sorts by any column and the
// remediation activity panel flips its time order. Both live next to other
// tables in the same tab, so the sort memory has to be keyed per panel.
func TestDashboardOverviewPanelsSort(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	for _, want := range []string{
		"function makeSortable(tableEl, defaultSortCol, stateKey)",
		"const key = stateKey || currentTab;",
		"makeSortable(el.querySelector('table'), RECENT_AGE_COL, RECENT_SORT_KEY)",
		`data-sort-value="${SEVERITY_RANK[i.severity] || 0}"`,
		`data-sort-value="${escapeHtml(i.state || '')}"`,
		`data-sort-value="${escapeHtml(i.namespace || '')}"`,
		"function toggleRemediationSort()",
		"function renderRemediationTimeline()",
		"t('timeline.sortNewest')",
		"t('timeline.sortOldest')",
		`<ol class="step-list">`,
	} {
		if !strings.Contains(shape.script, want) {
			t.Errorf("dashboard script lost the sorting wiring %q", want)
		}
	}
}

// The runbook step list draws its own cards: the global reset zeroes the list
// padding, so a browser marker would render outside the box over the border.
func TestDashboardRunbookStepsHaveNoBrowserMarker(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	if !strings.Contains(shape.style, ".expand-content ol.step-list { list-style:none;") {
		t.Fatalf("runbook step list must hide the browser marker and lay out its own cards")
	}
}

// A form the operator is filling in must survive the auto-refresh and any
// other reload: drafts are remembered per field, restored after a
// re-render, and the countdown holds while editing. Rows are keyed by
// object name so an expanded post-mortem stays open when the list reorders.
func TestDashboardKeepsDraftsAcrossRefresh(t *testing.T) {
	shape := parseDashboard(t, dashboardSource(t))
	for _, want := range []string{
		"function initDraftGuard()",
		"function restoreDrafts()",
		"function isEditing()",
		"new MutationObserver(() => restoreDrafts())",
		"if (isEditing()) {",
		"t('refresh.editing')",
		"rememberDraft('fb-accuracy-' + pm, val)",
		"forgetDrafts('approval-reason-' + name)",
		"function rowKey(item, idx)",
		"initDraftGuard();",
	} {
		if !strings.Contains(shape.script, want) {
			t.Errorf("dashboard script lost the draft guard wiring %q", want)
		}
	}
	if strings.Contains(shape.script, "const id = 'pm-' + idx;") {
		t.Errorf("post-mortem rows are still keyed by list position; an expanded row would jump on reorder")
	}
	if strings.Contains(shape.script, "approval-reason-${idx}") {
		t.Errorf("approval reason field is still keyed by list position")
	}
	// refreshCurrentTab must not click the tab: switchTab() clears the
	// expanded rows and would collapse an open post-mortem on every refresh.
	re := regexp.MustCompile(`function refreshCurrentTab\(\) \{\s*loadCurrentTab\(\);\s*\}`)
	if !re.MatchString(shape.script) {
		t.Errorf("refreshCurrentTab must reload in place through loadCurrentTab()")
	}
}
