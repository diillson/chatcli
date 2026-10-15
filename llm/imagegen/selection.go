/*
 * ChatCLI - Image backend discovery and selection.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * What @image models, @image use and /config image share: which backends
 * have credentials, which models each one offers (catalog merged with the
 * provider's live listing), and the environment a switch of backend or model
 * comes down to. The factory reads that environment on every call, so a
 * switch takes effect on the next @image call.
 */
package imagegen

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/llm/bedrock"
	"go.uber.org/zap"
)

// Backend describes one image backend.
type Backend struct {
	Name string // the CHATCLI_IMAGE_PROVIDER value: openai, responses, google, xai, zai, minimax, bedrock, sdwebui, url
	// Catalog is the catalog provider name its models carry ("openai" for
	// both OpenAI backends).
	Catalog string
	// Requires names what the backend needs configured (environment
	// variable names, never values).
	Requires string
	// Configured reports whether that credential is present.
	Configured bool
}

// Backends lists every image backend with its credential state.
func Backends() []Backend {
	openai := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
	return []Backend{
		{Name: "openai", Catalog: "openai", Requires: "OPENAI_API_KEY", Configured: openai},
		{Name: "responses", Catalog: "openai", Requires: "OPENAI_API_KEY", Configured: openai},
		{Name: "google", Catalog: "google", Requires: "GOOGLEAI_API_KEY or GEMINI_API_KEY", Configured: googleImageKey() != ""},
		{Name: "xai", Catalog: "xai", Requires: "XAI_API_KEY", Configured: strings.TrimSpace(os.Getenv("XAI_API_KEY")) != ""},
		{Name: "zai", Catalog: "zai", Requires: "ZAI_API_KEY", Configured: strings.TrimSpace(os.Getenv("ZAI_API_KEY")) != ""},
		{Name: "minimax", Catalog: "minimax", Requires: "MINIMAX_API_KEY", Configured: strings.TrimSpace(os.Getenv("MINIMAX_API_KEY")) != ""},
		{Name: "bedrock", Catalog: "bedrock", Requires: "AWS credentials (AWS_PROFILE/BEDROCK_PROFILE, access keys or AWS_BEARER_TOKEN_BEDROCK)", Configured: bedrockConfigured()},
		{Name: "sdwebui", Catalog: "sdwebui", Requires: "CHATCLI_IMAGE_URL", Configured: strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_URL")) != ""},
		{Name: "url", Catalog: "url", Requires: "CHATCLI_IMAGE_URL", Configured: strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_URL")) != ""},
	}
}

// bedrockConfigured is a cheap, offline guess: some AWS credential source
// is named. The listing or the call itself reports a credential that fails.
func bedrockConfigured() bool {
	if p, _ := bedrock.ResolveProfile(); p != "" {
		return true
	}
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_BEARER_TOKEN_BEDROCK", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"} {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

// LookupBackend returns the backend named name (aliases accepted).
func LookupBackend(name string) (Backend, bool) {
	n := canonicalBackend(name)
	for _, b := range Backends() {
		if b.Name == n {
			return b, true
		}
	}
	return Backend{}, false
}

func canonicalBackend(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai", "images", "openai-images":
		return "openai"
	case "responses", "openai-responses":
		return "responses"
	case "google", "gemini", "imagen":
		return "google"
	case "xai", "grok":
		return "xai"
	case "zai", "zhipu", "glm", "cogview", "glm-image":
		return "zai"
	case "minimax", "hailuo":
		return "minimax"
	case "bedrock", "aws":
		return "bedrock"
	case "sdwebui", "automatic1111", "sd":
		return "sdwebui"
	case "url", "selfhosted":
		return "url"
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// BackendForModel returns the backend that serves model ("" if unknown).
func BackendForModel(model string) string {
	switch p := providerFromModel(model); p {
	case "openai-responses":
		return "responses"
	default:
		return p
	}
}

// ListedModel is a model as @image models shows it.
type ListedModel struct {
	ModelInfo
	// Live: the provider's listing reports it for this account (Bedrock:
	// in at least one region).
	Live bool
	// Supported: @image can drive it. False for a live model of a family
	// chatcli has no request shape for.
	Supported bool
}

// BackendListing is one backend's models.
type BackendListing struct {
	Backend
	Active bool
	// LiveChecked: the live listing ran, so models without Live are not
	// offered to this account.
	LiveChecked bool
	LiveErr     error
	Models      []ListedModel
}

// ListModels returns the models per backend: the catalog, merged with each
// provider's live listing when live is true and the backend is configured.
// only limits the result to one backend ("" = all). active is the backend
// in use (its listing is flagged).
func ListModels(ctx context.Context, only, active string, live bool, logger *zap.Logger) []BackendListing {
	if logger == nil {
		logger = zap.NewNop()
	}
	only = canonicalBackend(only)
	var out []BackendListing
	for _, b := range Backends() {
		if only != "" && b.Name != only {
			continue
		}
		bl := BackendListing{Backend: b, Active: b.Name == canonicalBackend(active)}
		api := ""
		switch b.Name {
		case "openai":
			api = "images"
		case "responses":
			api = "responses"
		}
		for _, m := range KnownModels() {
			if m.Provider == b.Catalog && (api == "" || m.API == api) {
				bl.Models = append(bl.Models, ListedModel{ModelInfo: m, Supported: true})
			}
		}
		if live && b.Configured && LiveListable(b.Catalog) && b.Name != "responses" {
			lm, err := ListLiveModels(ctx, b.Catalog, logger)
			bl.LiveErr = err
			if err == nil && lm != nil {
				bl.LiveChecked = true
				bl.Models = mergeLive(bl.Models, lm, b.Catalog)
			}
		}
		out = append(out, bl)
	}
	return out
}

// mergeLive marks the catalog models the listing reports and appends the
// ones the catalog does not know yet. Ids the catalog knows are retired stay
// out even when a provider still lists them.
func mergeLive(models []ListedModel, live []LiveModel, provider string) []ListedModel {
	idx := make(map[string]int, len(models))
	for i, m := range models {
		idx[strings.ToLower(m.Name)] = i
	}
	for _, l := range live {
		id := strings.ToLower(l.ID)
		if i, ok := idx[id]; ok {
			models[i].Live = true
			if len(l.Regions) > 0 {
				models[i].Regions = strings.Join(l.Regions, ",")
			}
			continue
		}
		if _, _, known := LookupModel(l.ID); known {
			// Retired (a provider may keep listing a shut-down id) or a
			// catalog model listed under another backend.
			continue
		}
		nm := ListedModel{ModelInfo: ModelInfo{Name: l.ID, Provider: provider, Status: "active", Regions: strings.Join(l.Regions, ","), Profile: l.ProfileOnly}, Live: true}
		if provider == "bedrock" {
			// Bedrock image models differ per family; one without a known
			// request shape cannot be driven.
			nm.Note = "Offered by Bedrock; @image has no request shape for it yet."
		} else {
			nm.Generate, nm.Edit, nm.Supported = true, provider != "zai" && provider != "minimax", true
			nm.Note = "Reported by the provider; not in the catalog yet."
		}
		models = append(models, nm)
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Default != models[j].Default {
			return models[i].Default
		}
		return false
	})
	return models
}

// Selection is the environment a switch comes down to.
type Selection struct {
	Backend string
	Model   string
	ForEdit bool
	// Env maps variables to their new values; "" means unset.
	Env      map[string]string
	Warnings []string
}

// Select resolves a switch of the image backend and/or model. With only a
// model, the backend is inferred from it; with only a backend, its catalog
// default is used. forEdit pins the backend that edits when the active one
// cannot (CHATCLI_IMAGE_EDIT_PROVIDER/MODEL) instead of the generation
// backend. It fails when the backend is unknown or has no credential.
func Select(backend, model string, forEdit bool) (Selection, error) {
	backend, model = strings.TrimSpace(backend), strings.TrimSpace(model)
	if backend == "" && model == "" {
		return Selection{}, fmt.Errorf("give a provider, a model, or both")
	}
	if backend == "" {
		backend = BackendForModel(model)
		if backend == "" {
			return Selection{}, fmt.Errorf("cannot tell which provider serves %q; pass provider too", model)
		}
	}
	b, ok := LookupBackend(backend)
	if !ok {
		return Selection{}, fmt.Errorf("unknown image provider %q (valid: openai, responses, google, xai, zai, minimax, bedrock, sdwebui, url)", backend)
	}
	if !b.Configured {
		return Selection{}, fmt.Errorf("provider %s is not configured: set %s", b.Name, b.Requires)
	}
	sel := Selection{Backend: b.Name, Model: model, ForEdit: forEdit, Env: map[string]string{}}
	if model == "" {
		api := ""
		switch b.Name {
		case "openai":
			api = "images"
		case "responses":
			api = "responses"
		}
		sel.Model = DefaultModelFor(b.Catalog, api)
	}
	if sel.Model != "" {
		if inferred := BackendForModel(sel.Model); inferred != "" && inferred != b.Name &&
			b.Name != "url" && b.Name != "sdwebui" {
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("%s looks like a %s model, not %s", sel.Model, inferred, b.Name))
		}
		if m, retired, known := LookupModel(sel.Model); known {
			switch {
			case retired:
				return Selection{}, fmt.Errorf("%s was shut down on %s; @image models lists the current ones", sel.Model, m.Retires)
			case forEdit && !m.Edit:
				return Selection{}, fmt.Errorf("%s does not edit images", sel.Model)
			case !forEdit && !m.Generate:
				sel.Warnings = append(sel.Warnings, fmt.Sprintf("%s only transforms an input image: @image gen will refuse it, @image edit works", sel.Model))
			}
			if m.Retires != "" {
				sel.Warnings = append(sel.Warnings, fmt.Sprintf("%s is deprecated and shuts down on %s", sel.Model, m.Retires))
			}
		} else {
			sel.Warnings = append(sel.Warnings, fmt.Sprintf("%s is not in the catalog; it is passed to %s as is", sel.Model, b.Name))
		}
	}
	if forEdit {
		sel.Env["CHATCLI_IMAGE_EDIT_PROVIDER"] = b.Name
		sel.Env["CHATCLI_IMAGE_EDIT_MODEL"] = sel.Model
		return sel, nil
	}
	sel.Env["CHATCLI_IMAGE_PROVIDER"] = b.Name
	sel.Env["CHATCLI_IMAGE_MODEL"] = sel.Model
	// CHATCLI_IMAGE_API=responses would turn "openai" into the Responses
	// backend; the backend name already says which one.
	sel.Env["CHATCLI_IMAGE_API"] = ""
	return sel, nil
}

// Apply sets (or unsets) the selection's variables in the process
// environment, where the factory reads them on the next call.
func (s Selection) Apply() {
	for k, v := range s.Env {
		if v == "" {
			_ = os.Unsetenv(k)
			continue
		}
		_ = os.Setenv(k, v)
	}
}

// ActiveBackend returns the backend name of a built provider, as Backends
// names it ("" for the disabled provider).
func ActiveBackend(p Provider) string {
	if IsNull(p) {
		return ""
	}
	switch p.Name() {
	case "openai-responses":
		return "responses"
	case "selfhosted", "openai-compatible":
		return "url"
	}
	return p.Name()
}
