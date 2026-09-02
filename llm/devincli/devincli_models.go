/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package devincli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/llm/pricing"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// devinListModelsTimeout bounds `devin models list` when the caller supplied
// no deadline. Listing is best-effort UX — the static catalog is the
// fallback — so a slow or wedged CLI must never freeze the REPL.
const devinListModelsTimeout = 30 * time.Second

// devinModelIDPattern is the shape of a model id the Devin CLI accepts on
// --model and in the config "model" key (docs.devin.ai/cli/models: family
// slugs such as claude-opus-5, short aliases such as opus, variant ids such
// as swe-1-6-fast). `devin models list` also reports legacy internal
// enum-style uids (MODEL_PRIVATE_11, MODEL_GPT_5_2_LOW) for a few older
// families; nothing documents those as --model inputs and this client
// lowercases the model id at construction, so they are skipped — the
// family slug still exposes the model.
var devinModelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// devinModelsDoc mirrors `devin models list --format json`: models are
// grouped by family, each family carrying the slug --model accepts, its
// short aliases and the per-variant ids (reasoning level, -fast, -1m,
// -thinking, -priority suffixes) with the server-side token limits.
type devinModelsDoc struct {
	Families []devinFamily `json:"families"`
	// Models is an alternate flat shape (accepted for forward tolerance).
	Models []devinVariant `json:"models"`
}

type devinFamily struct {
	Slug     string         `json:"slug"`
	UID      string         `json:"family_uid"`
	Label    string         `json:"family_label"`
	Aliases  []string       `json:"aliases"`
	Variants []devinVariant `json:"variants"`
}

type devinVariant struct {
	UID         string `json:"model_uid"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Pointers on purpose: the "adaptive" router reports null limits.
	MaxContext *int `json:"max_context_tokens"`
	MaxOutput  *int `json:"max_output_tokens"`
	// CostSummary is the per-account rate as the CLI prints it, e.g.
	// "$5 / MTok In · $25 / MTok Out"; absent on the adaptive router.
	CostSummary string `json:"cost_summary"`
}

// costSummaryIn / costSummaryOut pull the two USD-per-million figures out
// of the CLI's cost_summary string. Tolerant of spacing, "MTok" / "1M"
// spellings and the separator glyph; anything else leaves the rate unset.
var (
	costSummaryIn  = regexp.MustCompile(`(?i)\$\s*([0-9]+(?:\.[0-9]+)?)\s*/\s*(?:MTok|1M)\b[^$]*?\bIn\b`)
	costSummaryOut = regexp.MustCompile(`(?i)\$\s*([0-9]+(?:\.[0-9]+)?)\s*/\s*(?:MTok|1M)\b[^$]*?\bOut\b`)
)

// parseCostSummary returns the input/output USD-per-MTok rate encoded in
// a cost_summary string, ok=false when neither figure parses.
func parseCostSummary(summary string) (pricing.Rate, bool) {
	var r pricing.Rate
	if m := costSummaryIn.FindStringSubmatch(summary); m != nil {
		r.InputPerMTok, _ = strconv.ParseFloat(m[1], 64)
	}
	if m := costSummaryOut.FindStringSubmatch(summary); m != nil {
		r.OutputPerMTok, _ = strconv.ParseFloat(m[1], 64)
	}
	return r, r.InputPerMTok > 0 || r.OutputPerMTok > 0
}

// ListModels runs `devin models list --format json` and returns every model
// the authenticated account can actually use — the enterprise Team Settings
// restriction is applied server-side, so this is the source of truth the
// static catalog can only approximate. Implements client.ModelLister so
// `/switch --model`, the completer, the @model tool and the RPC surfaces
// suggest invokable ids. Each discovered id is registered in the catalog
// with the CLI-reported context window and output cap, so an id the static
// catalog never saw still gets real budgets instead of the generic 50K
// fallback.
func (c *Client) ListModels(ctx context.Context) ([]client.ModelInfo, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, devinListModelsTimeout)
		defer cancel()
	}

	// Same per-process serialization as a turn: the CLI keeps per-user
	// state that concurrent instances contend on.
	release, err := acquireRunSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// Fresh empty workdir, like a turn: the CLI must never pick up a
	// project-local session store or config from wherever ChatCLI runs.
	workDir, err := os.MkdirTemp("", "chatcli-devin-models-*")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.devincli.list_failed"), err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	args := []string{"models", "list", "--format", "json"}
	// #nosec G204 G702 -- binPath resolves from operator configuration
	// (PATH lookup or DEVIN_CLI_PATH) and argv is a fixed subcommand.
	cmd := exec.CommandContext(ctx, c.binPath, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	c.logger.Debug("devincli: list models", zap.String("bin", c.binPath), zap.Strings("args", args))

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.devincli.list_timeout", devinListModelsTimeout.String()), ctx.Err())
	}
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		detail = utils.SanitizeSensitiveText(stripANSI(detail))
		if isAuthError(detail) {
			return nil, fmt.Errorf("%s", i18n.T("llm.devincli.auth_required"))
		}
		return nil, fmt.Errorf("%s: %w: %s", i18n.T("llm.devincli.list_failed"), runErr, tail(detail, 500))
	}

	families, err := parseDevinModels(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.devincli.list_decode"), err)
	}

	result, snapshot := projectDevinModels(families, c.logger)
	if err := saveSnapshot(snapshot); err != nil {
		// The listing itself succeeded; a snapshot miss only means the next
		// process starts from the static catalog until it lists again.
		c.logger.Warn("devincli: could not persist the model listing snapshot", zap.Error(err))
	}
	c.logger.Info(i18n.T("llm.info.fetched_models", "Devin CLI"),
		zap.Int("families", len(families)), zap.Int("count", len(result)))
	return result, nil
}

// parseDevinModels decodes the listing tolerantly: ANSI and any banner the
// CLI prints before the document (update notices) are skipped, and both
// the grouped shape ({"families": […]}) and a flat one ({"models": […]} or
// a bare array of variants) are accepted, the flat forms being synthesized
// into single-variant families.
func parseDevinModels(raw []byte) ([]devinFamily, error) {
	text := stripANSI(string(raw))
	start := strings.IndexAny(text, "{[")
	if start < 0 {
		return nil, fmt.Errorf("no JSON document in output: %q", tail(strings.TrimSpace(text), 200))
	}
	payload := []byte(text[start:])

	if bytes.HasPrefix(bytes.TrimSpace(payload), []byte("[")) {
		var variants []devinVariant
		if err := json.Unmarshal(payload, &variants); err != nil {
			return nil, err
		}
		return synthesizeFamilies(variants), nil
	}

	var doc devinModelsDoc
	if err := json.Unmarshal(payload, &doc); err != nil {
		return nil, err
	}
	if len(doc.Families) == 0 && len(doc.Models) > 0 {
		return synthesizeFamilies(doc.Models), nil
	}
	return doc.Families, nil
}

// synthesizeFamilies wraps flat variants as one family each so the
// projection has a single code path.
func synthesizeFamilies(variants []devinVariant) []devinFamily {
	out := make([]devinFamily, 0, len(variants))
	for _, v := range variants {
		out = append(out, devinFamily{Slug: v.UID, Label: v.Label, Variants: []devinVariant{v}})
	}
	return out
}

// projectDevinModels flattens the families into the listing order the UI
// shows — family slug first (what --model examples use), then its
// variants — deduplicated by id, and registers every entry in the catalog
// and, when the CLI printed a cost_summary, in the pricing registry. The
// second return value is the same set as snapshot entries for persistence.
func projectDevinModels(families []devinFamily, logger *zap.Logger) ([]client.ModelInfo, []snapshotModel) {
	seen := make(map[string]bool)
	var result []client.ModelInfo
	var snapshot []snapshotModel
	add := func(id, display string) bool {
		key := strings.ToLower(id)
		if id == "" || seen[key] {
			return false
		}
		if !devinModelIDPattern.MatchString(id) {
			logger.Debug("devincli: skipping model uid outside the --model id shape",
				zap.String("model_uid", id), zap.String("label", display))
			return false
		}
		seen[key] = true
		if display == "" {
			display = id
		}
		result = append(result, client.ModelInfo{ID: id, DisplayName: display, Source: client.ModelSourceAPI})
		return true
	}
	record := func(id, display string, aliases []string, ctxWin, outCap int, summary string) {
		registerDevinModel(id, display, aliases, ctxWin, outCap)
		entry := snapshotModel{ID: id, DisplayName: strings.TrimSpace(display), Aliases: aliases, ContextWindow: ctxWin, MaxOutputTokens: outCap}
		if rate, ok := parseCostSummary(summary); ok {
			pricing.Register(catalog.ProviderDevin, id, rate)
			entry.InputUSDPerMTok = rate.InputPerMTok
			entry.OutputUSDPerMTk = rate.OutputPerMTok
		}
		snapshot = append(snapshot, entry)
	}

	for _, f := range families {
		slug := strings.TrimSpace(f.Slug)
		if slug == "" && len(f.Variants) == 1 {
			slug = strings.TrimSpace(f.Variants[0].UID)
		}
		// The family's own specs and rate are the default variant's — the
		// CLI lists it first (medium reasoning, no suffix).
		ctxWin, outCap, summary := 0, 0, ""
		if len(f.Variants) > 0 {
			ctxWin, outCap = variantLimits(f.Variants[0])
			summary = f.Variants[0].CostSummary
		}
		if add(slug, strings.TrimSpace(f.Label)) {
			aliases := append([]string{}, f.Aliases...)
			if uid := strings.TrimSpace(f.UID); uid != "" && !strings.EqualFold(uid, slug) && devinModelIDPattern.MatchString(uid) {
				aliases = append(aliases, uid)
			}
			record(slug, f.Label, aliases, ctxWin, outCap, summary)
		}
		for _, v := range f.Variants {
			uid := strings.TrimSpace(v.UID)
			if add(uid, strings.TrimSpace(v.Label)) {
				vctx, vout := variantLimits(v)
				record(uid, v.Label, nil, vctx, vout, v.CostSummary)
			}
		}
	}
	return result, snapshot
}

func variantLimits(v devinVariant) (int, int) {
	ctxWin, outCap := 0, 0
	if v.MaxContext != nil && *v.MaxContext > 0 {
		ctxWin = *v.MaxContext
	}
	if v.MaxOutput != nil && *v.MaxOutput > 0 {
		outCap = *v.MaxOutput
	}
	return ctxWin, outCap
}

// registerDevinModel upserts a discovered id in the catalog. The CLI-reported
// limits win over the static entry (they are what the backend enforces);
// a null limit keeps whatever the static catalog knew, and the static
// aliases are preserved so hand-curated shortcuts keep resolving. Aliases
// go through the exact-alias pass of catalog.Resolve, so a short name the
// CLI accepts ("opus", "codex") maps to the right family's specs instead
// of the generic fallback.
func registerDevinModel(id, displayName string, aliases []string, ctxWin, outCap int) {
	meta := catalog.ModelMeta{
		ID:           id,
		DisplayName:  strings.TrimSpace(displayName),
		Provider:     catalog.ProviderDevin,
		Capabilities: []string{"tools"},
		PreferredAPI: catalog.APIChatCompletions,
	}
	if meta.DisplayName == "" {
		meta.DisplayName = id
	}
	existing, hasExisting := exactDevinMeta(id)
	if hasExisting {
		meta.ContextWindow = existing.ContextWindow
		meta.MaxOutputTokens = existing.MaxOutputTokens
		if len(existing.Capabilities) > 0 {
			meta.Capabilities = existing.Capabilities
		}
		if existing.PreferredAPI != "" {
			meta.PreferredAPI = existing.PreferredAPI
		}
	}
	if ctxWin > 0 {
		meta.ContextWindow = ctxWin
	}
	if outCap > 0 {
		meta.MaxOutputTokens = outCap
	}
	meta.Aliases = mergeAliases(id, existing.Aliases, aliases)
	catalog.Register(meta)
}

// exactDevinMeta finds the DEVIN entry whose ID equals id (case-insensitive).
// catalog.Resolve is deliberately not used: its prefix/contains passes
// would return the family for a variant id, and the variant must get its
// own entry with its own limits.
func exactDevinMeta(id string) (catalog.ModelMeta, bool) {
	for _, meta := range catalog.ListByProvider(catalog.ProviderDevin) {
		if strings.EqualFold(meta.ID, id) {
			return meta, true
		}
	}
	return catalog.ModelMeta{}, false
}

// mergeAliases returns id followed by the union of the alias lists, order
// preserved, case-insensitively deduplicated, empty strings dropped.
func mergeAliases(id string, lists ...[]string) []string {
	seen := map[string]bool{strings.ToLower(id): true}
	out := []string{id}
	for _, list := range lists {
		for _, a := range list {
			a = strings.TrimSpace(a)
			key := strings.ToLower(a)
			if a == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}
