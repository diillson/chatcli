/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/registry"
)

// skillToolSearchCap: a broad query matches hundreds; the model needs a few.
const skillToolSearchCap = 20

// skillPluginAdapter gives @skill the session-bound half of /skill (pin,
// unpin, registry search and info). It has no install path on purpose.
type skillPluginAdapter struct {
	cli *ChatCLI
}

// handler returns the skill handler; a broken config leaves registryMgr nil.
func (a *skillPluginAdapter) handler(needRegistry bool) (*SkillHandler, error) {
	if a.cli == nil || a.cli.skillHandler == nil {
		return nil, errors.New(i18n.T("kp.skill.tool.unavailable"))
	}
	if needRegistry && a.cli.skillHandler.registryMgr == nil {
		return nil, errors.New(i18n.T("skill.registry.not_initialized"))
	}
	return a.cli.skillHandler, nil
}

// Pin implements plugins.SkillSessionAdapter through the same pinSkill
// /skill pin uses, so the manual-only refusal holds for the model too.
func (a *skillPluginAdapter) Pin(name string) (string, error) {
	sh, err := a.handler(false)
	if err != nil {
		return "", err
	}
	skill, already, err := sh.pinSkill(name)
	switch {
	case errors.Is(err, errSkillPinNotFound):
		return "", errors.New(i18n.T("kp.skill.show.not_found", name))
	case errors.Is(err, errSkillPinManualOnly):
		return "", errors.New(i18n.T("kp.skill.tool.pin_manual_only", skill.Name))
	case err != nil:
		return "", err
	case already:
		return i18n.T("kp.skill.tool.pin_already", skill.Name), nil
	}
	return i18n.T("kp.skill.tool.pinned", skill.Name), nil
}

func (a *skillPluginAdapter) Unpin(name string) (string, error) {
	sh, err := a.handler(false)
	if err != nil {
		return "", err
	}
	if !sh.unpinSkill(name) {
		return i18n.T("kp.skill.tool.not_pinned", name), nil
	}
	return i18n.T("kp.skill.tool.unpinned", name), nil
}

// RegistrySearch returns what /skill search lists, as plain text.
func (a *skillPluginAdapter) RegistrySearch(ctx context.Context, query string) (string, error) {
	sh, err := a.handler(true)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	merged, results := sh.registryMgr.SearchAll(ctx, query)

	var b strings.Builder
	for _, r := range results {
		if r.Error != nil {
			fmt.Fprintf(&b, "! %s: %s\n", r.RegistryName, shortenRegistryError(r.Error))
		}
	}
	if len(merged) == 0 {
		b.WriteString(i18n.T("skill.search.no_results"))
		return b.String(), nil
	}
	shown := merged
	if len(shown) > skillToolSearchCap {
		shown = shown[:skillToolSearchCap]
	}
	b.WriteString(i18n.T("kp.skill.tool.search_header", len(shown), len(merged), query) + "\n")
	for i := range shown {
		b.WriteString(skillMetaLine(sh, &shown[i]) + "\n")
	}
	b.WriteString(i18n.T("kp.skill.tool.install_is_users"))
	return b.String(), nil
}

// skillMetaLine renders one registry skill and whether it is installed.
func skillMetaLine(sh *SkillHandler, m *registry.SkillMeta) string {
	line := "- " + m.Name
	if m.Version != "" {
		line += " v" + m.Version
	}
	line += " [" + m.RegistryName + "]"
	if m.Downloads > 0 {
		line += fmt.Sprintf(" (%s %s)", registry.FormatInstallCount(m.Downloads), i18n.T("skill.cmd.installs_suffix"))
	}
	if m.Author != "" {
		line += " " + i18n.T("skill.cmd.by_author") + " " + m.Author
	}
	if tag := registry.FormatModerationTag(m.Moderation); tag != "" {
		line += " " + tag
	}
	if sh.registryMgr.IsInstalledAny(m.Name) {
		line += " [" + i18n.T("skill.installed") + "]"
	}
	if m.Description != "" {
		line += "\n    " + m.Description
	}
	return line
}

// RegistryInfo returns what /skill info shows, minus the partner security
// audits: those are a second network round-trip per call.
func (a *skillPluginAdapter) RegistryInfo(ctx context.Context, name, from string) (string, error) {
	sh, err := a.handler(true)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	local := sh.findBestLocalInfo(name)
	remote := sh.resolveRemoteSkillMeta(ctx, name, from)
	if local == nil && remote == nil {
		return "", errors.New(i18n.T("skill.info.not_found", name))
	}
	var b strings.Builder
	if remote != nil {
		b.WriteString(skillMetaLine(sh, remote) + "\n")
	}
	installs := sh.registryMgr.GetAllInstalledInfo(name)
	for i := range installs {
		l := &installs[i]
		b.WriteString(i18n.T("kp.skill.tool.info_local", l.Name, localSource(l), l.Path) + "\n")
		if remote == nil && l.Description != "" {
			b.WriteString("    " + l.Description + "\n")
		}
	}
	if local == nil {
		b.WriteString(i18n.T("kp.skill.tool.install_is_users"))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
