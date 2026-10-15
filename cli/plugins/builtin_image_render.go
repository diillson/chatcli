/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/imagegen"
)

// ImageStatus describes the active image backend: provider, model, whether
// it edits, the pinned edit backend, and which other backends are ready.
// @image status and /config image share it.
func ImageStatus(provider imagegen.Provider) string {
	var b strings.Builder
	if imagegen.IsNull(provider) {
		b.WriteString(i18n.T("image.status.none"))
	} else {
		model := imagegen.ModelOf(provider)
		if model == "" {
			model = "-"
		}
		capability := i18n.T("image.status.gen_only")
		if _, edits := imagegen.AsEditor(provider); edits {
			capability = i18n.T("image.status.gen_edit")
		}
		b.WriteString(i18n.T("image.status.active", imagegen.ActiveBackend(provider), model, capability))
		if r, ok := provider.(interface{ Region() string }); ok && r.Region() != "" {
			b.WriteString("\n" + i18n.T("image.status.region", r.Region()))
		}
	}
	if pin := strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_EDIT_PROVIDER")); pin != "" {
		model := strings.TrimSpace(os.Getenv("CHATCLI_IMAGE_EDIT_MODEL"))
		if model == "" {
			model = "-"
		}
		b.WriteString("\n" + i18n.T("image.status.edit_pin", pin, model))
	}
	var ready, missing []string
	for _, be := range imagegen.Backends() {
		if be.Configured {
			ready = append(ready, be.Name)
		} else {
			missing = append(missing, be.Name)
		}
	}
	b.WriteString("\n" + i18n.T("image.status.ready", strings.Join(ready, ", ")))
	if len(missing) > 0 {
		b.WriteString("\n" + i18n.T("image.status.missing", strings.Join(missing, ", ")))
	}
	b.WriteString("\n" + i18n.T("image.status.hint"))
	return b.String()
}

// RenderImageModels renders the per-backend model listing.
func RenderImageModels(listings []imagegen.BackendListing) string {
	var b strings.Builder
	b.WriteString(i18n.T("image.models.legend"))
	for _, l := range listings {
		b.WriteString("\n\n")
		state := i18n.T("image.models.ready")
		if !l.Configured {
			state = i18n.T("image.models.needs", l.Credential)
		}
		marker := ""
		if l.Active {
			marker = " " + i18n.T("image.models.active")
		}
		fmt.Fprintf(&b, "%s%s — %s", l.Name, marker, state)
		if l.LiveErr != nil {
			b.WriteString("\n  " + i18n.T("image.models.live_failed", l.LiveErr.Error()))
		}
		if len(l.Models) == 0 {
			b.WriteString("\n  " + i18n.T("image.models.any_model"))
			continue
		}
		for _, m := range l.Models {
			b.WriteString("\n  " + modelLine(m, l.LiveChecked))
		}
	}
	return b.String()
}

// modelLine is one model: availability mark, id, capabilities, lifecycle,
// region and note.
func modelLine(m imagegen.ListedModel, liveChecked bool) string {
	mark := "·" // catalog only, not verified on the account
	switch {
	case m.Live:
		mark = "✓"
	case liveChecked:
		mark = "✗" // the listing ran and does not offer it
	}
	var caps []string
	if m.Generate {
		caps = append(caps, "gen")
	}
	if m.Edit {
		caps = append(caps, "edit")
	}
	capStr := strings.Join(caps, "+")
	if !m.Supported {
		capStr = i18n.T("image.models.unsupported")
	}
	parts := []string{fmt.Sprintf("%s %-46s %-8s", mark, m.Name, capStr)}
	if m.Default {
		parts = append(parts, i18n.T("image.models.default"))
	}
	if m.Retires != "" {
		parts = append(parts, i18n.T("image.models.retires", m.Retires))
	} else if m.Status == "preview" {
		parts = append(parts, "preview")
	}
	if m.Regions != "" {
		r := m.Regions
		if m.Profile {
			r = "us. profile: " + r
		}
		parts = append(parts, "["+r+"]")
	}
	if m.Note != "" {
		parts = append(parts, m.Note)
	}
	return strings.Join(parts, "  ")
}

// RenderImageSelection confirms a switch.
func RenderImageSelection(sel imagegen.Selection, provider imagegen.Provider) string {
	var b strings.Builder
	model := sel.Model
	if model == "" {
		model = "-"
	}
	if sel.ForEdit {
		b.WriteString(i18n.T("image.use.edit_ok", sel.Backend, model))
	} else {
		b.WriteString(i18n.T("image.use.ok", sel.Backend, model))
	}
	for _, w := range sel.Warnings {
		b.WriteString("\n" + i18n.T("image.use.warning", w))
	}
	b.WriteString("\n" + ImageStatus(provider))
	b.WriteString("\n" + i18n.T("image.use.persist"))
	return b.String()
}
