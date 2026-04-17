/*
 * ChatCLI - Skill Registry Command Handler
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Handles /skill commands: search, install, uninstall, list, info, registries, help.
 */
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/persona"
	"github.com/diillson/chatcli/pkg/registry"
	"go.uber.org/zap"
)

// SkillHandler handles /skill commands.
type SkillHandler struct {
	registryMgr *registry.RegistryManager
	personaMgr  *persona.Manager
	logger      *zap.Logger
	initErr     error // stores initialization error for lazy display
}

// NewSkillHandler creates a new skill handler with registry manager.
func NewSkillHandler(logger *zap.Logger, personaMgr *persona.Manager) *SkillHandler {
	cfg, err := registry.LoadConfig()
	if err != nil {
		logger.Warn("Failed to load registry config, using defaults", zap.Error(err))
		cfg = registry.DefaultConfig()
	}

	mgr, err := registry.NewRegistryManager(cfg, logger)
	if err != nil {
		logger.Warn("Failed to initialize registry manager", zap.Error(err))
		return &SkillHandler{
			personaMgr: personaMgr,
			logger:     logger,
			initErr:    err,
		}
	}

	return &SkillHandler{
		registryMgr: mgr,
		personaMgr:  personaMgr,
		logger:      logger,
	}
}

// HandleCommand routes /skill subcommands.
func (sh *SkillHandler) HandleCommand(userInput string) {
	if sh.registryMgr == nil {
		fmt.Println(colorize(" "+i18n.T("skill.registry.not_initialized"), ColorYellow))
		if sh.initErr != nil {
			fmt.Printf("  %s: %s\n", i18n.T("skill.error"), sh.initErr.Error())
		}
		return
	}

	args := strings.Fields(userInput)
	if len(args) < 2 {
		sh.ShowHelp()
		return
	}

	subcommand := strings.ToLower(args[1])

	switch subcommand {
	case "search":
		if len(args) < 3 {
			fmt.Println(colorize(" "+i18n.T("skill.usage.search"), ColorYellow))
			return
		}
		query := strings.Join(args[2:], " ")
		sh.Search(query)

	case "install":
		if len(args) < 3 {
			fmt.Println(colorize(" "+i18n.T("skill.usage.install"), ColorYellow))
			return
		}
		// Parse optional --from <registry> flag
		skillName, fromRegistry := parseInstallArgs(args[2:])
		if skillName == "" {
			fmt.Println(colorize(" "+i18n.T("skill.usage.install"), ColorYellow))
			return
		}
		sh.Install(skillName, fromRegistry)

	case "uninstall", "remove":
		if len(args) < 3 {
			fmt.Println(colorize(" "+i18n.T("skill.usage.uninstall"), ColorYellow))
			return
		}
		sh.Uninstall(args[2])

	case "list", "ls":
		sh.List()

	case "registries", "registry":
		if len(args) >= 4 {
			action := strings.ToLower(args[2])
			regName := args[3]
			switch action {
			case "enable":
				sh.SetRegistryEnabled(regName, true)
				return
			case "disable":
				sh.SetRegistryEnabled(regName, false)
				return
			}
		}
		sh.ShowRegistries()

	case "info":
		if len(args) < 3 {
			fmt.Println(colorize(" "+i18n.T("skill.usage.info"), ColorYellow))
			return
		}
		infoName, infoFrom := parseInstallArgs(args[2:])
		if infoName == "" {
			fmt.Println(colorize(" "+i18n.T("skill.usage.info"), ColorYellow))
			return
		}
		sh.Info(infoName, infoFrom)

	case "prefer":
		sh.Prefer(args[2:])

	case "help":
		sh.ShowHelp()

	default:
		fmt.Printf(" %s\n", i18n.T("skill.unknown_subcommand", subcommand))
	}
}

// Search performs a fan-out search across all registries.
func (sh *SkillHandler) Search(query string) {
	fmt.Printf("\n  %s %s...\n",
		i18n.T("skill.search.searching"),
		colorize(fmt.Sprintf("%q", query), ColorCyan))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	merged, results := sh.registryMgr.SearchAll(ctx, query)

	// Show concise errors from individual registries (only first-time errors, auto-disable handles the rest)
	hasErrors := false
	for _, r := range results {
		if r.Error != nil {
			if !hasErrors {
				fmt.Println()
				hasErrors = true
			}
			fmt.Printf("  %s %s: %s\n",
				colorize("!", ColorYellow),
				r.RegistryName,
				colorize(shortenRegistryError(r.Error), ColorGray))
		}
	}

	if len(merged) == 0 {
		fmt.Printf("\n  %s\n\n", colorize(i18n.T("skill.search.no_results"), ColorYellow))
		return
	}

	fmt.Printf("\n  %s:\n\n", i18n.T("skill.search.results", len(merged)))

	// Compute max name length for alignment
	maxNameLen := 0
	for _, skill := range merged {
		if len(skill.Name) > maxNameLen {
			maxNameLen = len(skill.Name)
		}
	}

	for i, skill := range merged {
		// Padded name (pad raw string, then colorize)
		paddedName := fmt.Sprintf("%-*s", maxNameLen, skill.Name)

		// Version
		versionStr := ""
		if skill.Version != "" {
			versionStr = fmt.Sprintf("v%s", skill.Version)
		}

		// Registry tag
		regTag := fmt.Sprintf("[%s]", skill.RegistryName)

		// Install count (formatted for skills.sh)
		installStr := ""
		if skill.Downloads > 0 {
			installStr = registry.FormatInstallCount(skill.Downloads)
		}

		// Moderation tag
		modTag := ""
		modStr := registry.FormatModerationTag(skill.Moderation)
		if modStr != "" {
			modTag = " " + modStr
		}

		// Installed marker — check by source to avoid false positives
		installed := ""
		if sh.registryMgr.IsInstalledFromSource(skill.Name, skill.RegistryName) {
			installed = " " + colorize("["+i18n.T("skill.installed")+"]", ColorGreen)
		} else if sh.registryMgr.IsInstalledAny(skill.Name) {
			installed = " " + colorize("["+i18n.T("skill.installed_other")+"]", ColorYellow)
		}

		// Build the line
		line := fmt.Sprintf("    %d. %s", i+1, colorize(paddedName, ColorCyan))
		if versionStr != "" {
			line += "  " + colorize(versionStr, ColorGray)
		}
		if installStr != "" {
			line += "  " + colorize(fmt.Sprintf("(%s %s)", installStr, i18n.T("skill.cmd.installs_suffix")), ColorGray)
		}
		if skill.Author != "" {
			line += "  " + i18n.T("skill.cmd.by_author") + " " + skill.Author
		}
		line += "  " + colorize(regTag, ColorGray)
		if modTag != "" {
			switch modStr {
			case "BLOCKED", "QUARANTINED":
				line += colorize(modTag, ColorRed)
			default:
				line += colorize(modTag, ColorYellow)
			}
		}
		line += installed
		fmt.Println(line)

		// Show source repo for skills.sh skills
		descLine := ""
		if skill.Description != "" {
			descLine = skill.Description
		}
		if registry.IsSkillsShSource(skill.RegistryName) && skill.Slug != "" && skill.Slug != skill.Name {
			if descLine != "" {
				descLine += "  "
			}
			descLine += fmt.Sprintf("(%s)", skill.Slug)
		}
		if descLine != "" {
			fmt.Printf("       %s\n", colorize(descLine, ColorGray))
		}
	}

	fmt.Printf("\n  %s\n\n",
		i18n.T("skill.search.install_hint", colorize("/skill install <name>", ColorCyan)))
}

// Install downloads and installs a skill from a registry.
// If fromRegistry is non-empty, only that registry is used.
// If multiple registries have the skill, the user is prompted to choose.
//
// Supported invocations:
//
//	/skill install frontend-design                    → auto-detect or disambiguate
//	/skill install frontend-design --from skills.sh   → explicit registry
//	/skill install anthropics/skills/frontend-design   → skills.sh slug (unambiguous)
func (sh *SkillHandler) Install(name string, fromRegistry string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Gather metadata from all registries
	allMeta := sh.registryMgr.GetAllSkillMeta(ctx, name)

	// Filter by --from registry if specified
	if fromRegistry != "" {
		var filtered []*registry.SkillMeta
		for _, m := range allMeta {
			if m.RegistryName == fromRegistry {
				filtered = append(filtered, m)
			}
		}
		if len(filtered) == 0 {
			fmt.Printf("\n  %s %q %s %q\n",
				colorize(i18n.T("skill.install.not_found_in")+":", ColorYellow),
				name, i18n.T("skill.install.in_registry"), fromRegistry)
			if len(allMeta) > 0 {
				fmt.Printf("  %s:", i18n.T("skill.install.available_in"))
				for _, m := range allMeta {
					fmt.Printf(" %s", colorize(m.RegistryName, ColorCyan))
				}
				fmt.Println()
			}
			fmt.Println()
			return
		}
		allMeta = filtered
	}

	// If no metadata found at all, try direct install as fallback
	if len(allMeta) == 0 {
		fmt.Printf("\n  %s %s...\n", i18n.T("skill.install.installing"), colorize(name, ColorCyan))
		result, installErr := sh.registryMgr.Install(ctx, name)
		if installErr != nil {
			fmt.Printf("  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), installErr.Error())
			return
		}
		sh.showInstallResult(result)
		return
	}

	// If multiple registries have it and no --from was specified, disambiguate
	if len(allMeta) > 1 && fromRegistry == "" {
		// Check if registries are actually different
		registries := make(map[string]bool)
		for _, m := range allMeta {
			registries[m.RegistryName] = true
		}
		if len(registries) > 1 {
			fmt.Printf("\n  %s %q %s:\n\n",
				i18n.T("skill.install.found_in_multiple"),
				name,
				i18n.T("skill.install.registries_label"))
			for i, m := range allMeta {
				installStr := ""
				if m.Downloads > 0 {
					installStr = fmt.Sprintf("  (%s %s)", registry.FormatInstallCount(m.Downloads), i18n.T("skill.cmd.installs_suffix"))
				}
				fmt.Printf("    %d. %s%s\n", i+1,
					colorize(fmt.Sprintf("[%s]", m.RegistryName), ColorCyan),
					colorize(installStr, ColorGray))
				if m.Description != "" {
					desc := m.Description
					if len(desc) > 100 {
						desc = desc[:97] + "..."
					}
					fmt.Printf("       %s\n", colorize(desc, ColorGray))
				}
			}
			fmt.Printf("\n  %s\n",
				i18n.T("skill.install.use_from",
					colorize(fmt.Sprintf("/skill install %s --from <registry>", name), ColorCyan)))
			fmt.Println()
			return
		}
	}

	// Single match (or all from same registry) — proceed with install
	meta := allMeta[0]

	// Check moderation
	warning := registry.CheckModeration(meta)
	if warning != "" {
		if registry.ShouldBlock(meta.Moderation) {
			fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.install.blocked")+":", ColorRed), warning)
			return
		}
		fmt.Printf("\n  %s %s\n", colorize(i18n.T("skill.install.warning")+":", ColorYellow), warning)
		fmt.Print("  " + i18n.T("skill.install.confirm") + " (y/N): ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("  " + i18n.T("skill.install.cancelled"))
			return
		}
	}

	fmt.Printf("\n  %s %s", i18n.T("skill.install.installing"), colorize(meta.Name, ColorCyan))
	if meta.Version != "" {
		fmt.Printf(" v%s", meta.Version)
	}
	fmt.Printf(" %s %s...\n", i18n.T("skill.install.from"), colorize(meta.RegistryName, ColorGray))

	result, err := sh.registryMgr.InstallFrom(ctx, name, meta.RegistryName)
	if err != nil {
		fmt.Printf("  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}

	sh.showInstallResult(result)
}

// parseInstallArgs extracts the skill name and optional --from flag from install args.
// "/skill install frontend-design --from skills.sh" → ("frontend-design", "skills.sh")
// "/skill install frontend-design"                  → ("frontend-design", "")
func parseInstallArgs(args []string) (skillName string, fromRegistry string) {
	if len(args) == 0 {
		return "", ""
	}
	skillName = args[0]
	for i := 1; i < len(args); i++ {
		if (args[i] == "--from" || args[i] == "-f") && i+1 < len(args) {
			fromRegistry = args[i+1]
			i++ // skip the value
		}
	}
	return
}

func (sh *SkillHandler) showInstallResult(result *registry.InstallResult) {
	action := i18n.T("skill.install.result_installed")
	if result.WasDuplicate {
		action = i18n.T("skill.install.result_updated")
	}

	fmt.Printf("  %s %s", colorize(action, ColorGreen), colorize(result.Name, ColorCyan))
	if result.Version != "" {
		fmt.Printf(" v%s", result.Version)
	}
	fmt.Printf(" %s %s\n", i18n.T("skill.install.from"), colorize(result.Source, ColorGray))
	fmt.Printf("  %s: %s\n", i18n.T("skill.install.path"), colorize(result.InstallPath, ColorGray))
	fmt.Printf("\n  %s\n", i18n.T("skill.install.available", result.Name))
	fmt.Printf("  %s: %s\n\n", i18n.T("skill.install.verify"), colorize("/agent skills", ColorCyan))

	// Refresh persona loader to pick up new skill
	if sh.personaMgr != nil {
		if _, err := sh.personaMgr.RefreshSkills(); err != nil {
			fmt.Printf("  %s %s: %v\n", colorize(i18n.T("skill.warning")+":", ColorYellow), i18n.T("skill.refresh_failed"), err)
		}
	}
}

// Uninstall removes an installed skill.
// Supports both exact names ("anthropics-skills--frontend-design") and
// base names ("frontend-design"). When multiple installs match a base name,
// lists them and asks the user to specify which one to remove.
func (sh *SkillHandler) Uninstall(name string) {
	// Try exact name first
	if sh.registryMgr.IsInstalled(name) {
		sh.doUninstall(name)
		return
	}

	// Try base name lookup
	matches := sh.registryMgr.GetAllInstalledInfo(name)
	if len(matches) == 0 {
		fmt.Printf("\n  %s\n\n", i18n.T("skill.uninstall.not_installed", name))
		return
	}

	if len(matches) == 1 {
		// Single match — uninstall it
		sh.doUninstall(matches[0].Name)
		return
	}

	// Multiple matches — ask user to specify
	fmt.Printf("\n  %s %q %s:\n\n",
		i18n.T("skill.uninstall.multiple"),
		name,
		i18n.T("skill.uninstall.specify"))
	for i, m := range matches {
		fmt.Printf("    %d. %s  %s\n", i+1,
			colorize(m.Name, ColorCyan),
			colorize(fmt.Sprintf("[%s]", m.Source), ColorGray))
	}
	fmt.Printf("\n  %s\n\n",
		i18n.T("skill.uninstall.use_full_name"))
}

func (sh *SkillHandler) doUninstall(name string) {
	fmt.Printf("\n  %s %s...\n", i18n.T("skill.uninstall.removing"), colorize(name, ColorCyan))

	if err := sh.registryMgr.Uninstall(name); err != nil {
		fmt.Printf("  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}

	fmt.Printf("  %s %s\n\n",
		colorize(i18n.T("skill.uninstall.done"), ColorGreen), i18n.T("skill.uninstall.success", name))

	// Refresh persona loader
	if sh.personaMgr != nil {
		if _, err := sh.personaMgr.RefreshSkills(); err != nil {
			fmt.Printf("  %s %s: %v\n", colorize(i18n.T("skill.warning")+":", ColorYellow), i18n.T("skill.refresh_failed"), err)
		}
	}
}

// List shows all installed skills.
func (sh *SkillHandler) List() {
	installed, err := sh.registryMgr.ListInstalled()
	if err != nil {
		fmt.Printf("  %s %s\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}

	fmt.Println()
	if len(installed) == 0 {
		fmt.Println(colorize("  "+i18n.T("skill.list.empty"), ColorYellow))
		fmt.Printf("\n  %s\n\n",
			i18n.T("skill.list.search_hint", colorize("/skill search <query>", ColorCyan)))
		return
	}

	fmt.Printf("  %s (%d):\n\n",
		colorize(i18n.T("skill.list.header"), ColorCyan), len(installed))

	// Compute column widths
	maxNameLen := 0
	maxVerLen := 0
	maxSourceLen := 0
	for _, s := range installed {
		if len(s.Name) > maxNameLen {
			maxNameLen = len(s.Name)
		}
		ver := ""
		if s.Version != "" {
			ver = "v" + s.Version
		}
		if len(ver) > maxVerLen {
			maxVerLen = len(ver)
		}
		src := s.Source
		if src == "" {
			src = "local"
		}
		if len(src) > maxSourceLen {
			maxSourceLen = len(src)
		}
	}

	for _, s := range installed {
		// Pad raw strings first, then colorize
		paddedName := fmt.Sprintf("%-*s", maxNameLen, s.Name)

		ver := ""
		if s.Version != "" {
			ver = "v" + s.Version
		}
		paddedVer := fmt.Sprintf("%-*s", maxVerLen, ver)

		src := s.Source
		if src == "" {
			src = "local"
		}
		srcTag := fmt.Sprintf("[%s]", src)

		fmt.Printf("    %s  %s  %s\n",
			colorize(paddedName, ColorCyan),
			colorize(paddedVer, ColorGray),
			colorize(srcTag, ColorGray))
	}

	// Show registries summary
	regs := sh.registryMgr.GetRegistries()
	fmt.Printf("\n  %s (%d):\n", i18n.T("skill.registries.header"), len(regs))
	for _, r := range regs {
		status := colorize("["+i18n.T("skill.cmd.status_enabled")+"]", ColorGreen)
		if r.TempDisabled {
			status = colorize("["+i18n.T("skill.cmd.status_paused")+"]", ColorYellow)
		} else if !r.Enabled {
			status = colorize("["+i18n.T("skill.cmd.status_disabled")+"]", ColorGray)
		}
		fmt.Printf("    %-12s  %s  %s\n", r.Name, colorize(r.URL, ColorGray), status)
	}
	fmt.Println()
}

// Info shows metadata about a skill, checking local installed first, then registries.
// If fromRegistry is non-empty, only that registry is queried for remote metadata.
func (sh *SkillHandler) Info(name string, fromRegistry string) {
	// 1. Check local installed (any matching base name)
	localMatches := sh.registryMgr.GetAllInstalledInfo(name)
	var local *registry.InstalledSkillInfo
	if len(localMatches) > 0 {
		local = &localMatches[0]
	}

	// 2. Try registries for remote metadata.
	var remote *registry.SkillMeta
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if fromRegistry != "" {
		// Specific registry requested
		meta, err := sh.registryMgr.GetSkillMetaFrom(ctx, name, fromRegistry)
		if err == nil && meta != nil && meta.Name != "" {
			remote = meta
		}
	} else {
		// Try ALL registries, pick the richest result
		allRemote := sh.registryMgr.GetAllSkillMeta(ctx, name)
		if len(allRemote) > 0 {
			remote = allRemote[0]
			for _, m := range allRemote {
				if registry.IsSkillsShSource(m.RegistryName) {
					remote = m
					break
				}
				if m.Downloads > remote.Downloads {
					remote = m
				}
				if remote.Description == "" && m.Description != "" {
					remote = m
				}
			}
		}
	}

	// Nothing found anywhere
	if local == nil && remote == nil {
		fmt.Printf("\n  %s\n\n", i18n.T("skill.info.not_found", name))
		return
	}

	fmt.Println()

	// Name
	displayName := name
	if remote != nil && remote.Name != "" {
		displayName = remote.Name
	} else if local != nil && local.Name != "" {
		displayName = local.Name
	}
	fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.name")+":", ColorCyan), displayName)

	// Description
	desc := ""
	if remote != nil && remote.Description != "" {
		desc = remote.Description
	} else if local != nil && local.Description != "" {
		desc = local.Description
	}
	if desc != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.desc")+":", ColorCyan), desc)
	}

	// Version
	ver := ""
	if remote != nil && remote.Version != "" {
		ver = remote.Version
	} else if local != nil && local.Version != "" {
		ver = local.Version
	}
	if ver != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.version")+":", ColorCyan), ver)
	}

	// Author
	if remote != nil && remote.Author != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.author")+":", ColorCyan), remote.Author)
	}

	// Source / Registry
	source := ""
	if remote != nil && remote.RegistryName != "" {
		source = remote.RegistryName
	} else if local != nil && local.Source != "" {
		source = local.Source
	}
	if source != "" {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.source")+":", ColorCyan), source)
	}

	// Tags
	if remote != nil && len(remote.Tags) > 0 {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.tags")+":", ColorCyan),
			colorize(strings.Join(remote.Tags, ", "), ColorGray))
	}

	// Downloads / Installs
	if remote != nil && remote.Downloads > 0 {
		installLabel := i18n.T("skill.info.downloads")
		if registry.IsSkillsShSource(remote.RegistryName) {
			installLabel = i18n.T("skill.info.installs")
		}
		fmt.Printf("  %s  %s\n", colorize(installLabel+":", ColorCyan),
			registry.FormatInstallCount(remote.Downloads))
	}

	// Source repo (for skills.sh, show the GitHub source)
	if remote != nil && registry.IsSkillsShSource(remote.RegistryName) && remote.Slug != "" {
		// Extract owner/repo from slug
		parts := strings.SplitN(remote.Slug, "/", 3)
		if len(parts) >= 2 {
			repo := strings.Join(parts[:2], "/")
			fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.repo")+":", ColorCyan),
				colorize(fmt.Sprintf("https://github.com/%s", repo), ColorGray))
		}
		// Show skills.sh page
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.page")+":", ColorCyan),
			colorize(fmt.Sprintf("https://skills.sh/%s", remote.Slug), ColorGray))
	}

	// Security Audits (skills.sh only — fetch from partner audit API)
	if remote != nil && registry.IsSkillsShSource(remote.RegistryName) && remote.Slug != "" {
		sh.showSecurityAudits(ctx, remote)
	}

	// Moderation
	if remote != nil {
		modTag := registry.FormatModerationTag(remote.Moderation)
		if modTag != "" {
			fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.moderation")+":", ColorCyan),
				colorize(modTag, ColorYellow))
		}
	}

	// Install status — show ALL installed versions (local + registry) to surface conflicts
	allInstalled := sh.registryMgr.GetAllInstalledInfo(name)
	if len(allInstalled) == 0 {
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.status")+":", ColorCyan), i18n.T("skill.not_installed"))
	} else if len(allInstalled) == 1 {
		inst := allInstalled[0]
		fmt.Printf("  %s  %s  %s\n", colorize(i18n.T("skill.info.status")+":", ColorCyan),
			colorize(i18n.T("skill.installed"), ColorGreen),
			colorize(fmt.Sprintf("[%s]", inst.Source), ColorGray))
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.path")+":", ColorCyan),
			colorize(inst.Path, ColorGray))
	} else {
		// Multiple installs with the same base name from different sources
		fmt.Printf("  %s  %s\n", colorize(i18n.T("skill.info.status")+":", ColorCyan),
			colorize(fmt.Sprintf("%s (%d %s)", i18n.T("skill.installed"), len(allInstalled), i18n.T("skill.info.sources")), ColorYellow))
		for _, inst := range allInstalled {
			srcTag := fmt.Sprintf("[%s]", inst.Source)
			fmt.Printf("    %s  %s  %s\n",
				colorize(inst.Name, ColorCyan),
				colorize(srcTag, ColorGray),
				colorize(inst.Path, ColorGray))
		}
	}

	fmt.Println()
}

// Prefer manages source preferences for skills with name conflicts.
// Usage:
//
//	/skill prefer                              → list all preferences
//	/skill prefer frontend-design              → show current preference
//	/skill prefer frontend-design skills.sh    → prefer the skills.sh version
//	/skill prefer frontend-design local        → prefer the local version
//	/skill prefer frontend-design --reset      → remove preference (use default order)
func (sh *SkillHandler) Prefer(args []string) {
	prefs := registry.LoadPreferences()

	// No args: list all preferences
	if len(args) == 0 {
		all := prefs.ListPreferences()
		if len(all) == 0 {
			fmt.Printf("\n  %s\n", i18n.T("skill.prefer.none"))
			fmt.Printf("  %s\n\n", i18n.T("skill.prefer.hint",
				colorize("/skill prefer <name> <source>", ColorCyan)))
			return
		}

		fmt.Printf("\n  %s:\n\n", colorize(i18n.T("skill.prefer.header"), ColorCyan))
		for baseName, source := range all {
			fmt.Printf("    %s  →  %s\n",
				colorize(baseName, ColorCyan),
				colorize(source, ColorGreen))
		}
		fmt.Println()
		return
	}

	baseName := args[0]

	// Single arg: show current preference and available sources
	if len(args) == 1 {
		current := prefs.GetPreference(baseName)
		matches := sh.registryMgr.GetAllInstalledInfo(baseName)

		if len(matches) == 0 {
			fmt.Printf("\n  %s\n\n", i18n.T("skill.prefer.not_installed", baseName))
			return
		}

		fmt.Println()
		if current != "" {
			fmt.Printf("  %s %s → %s\n",
				colorize(i18n.T("skill.prefer.current")+":", ColorCyan),
				colorize(baseName, ColorCyan),
				colorize(current, ColorGreen))
		} else {
			fmt.Printf("  %s %s (%s)\n",
				colorize(i18n.T("skill.prefer.current")+":", ColorCyan),
				colorize(baseName, ColorCyan),
				i18n.T("skill.prefer.default_order"))
		}

		fmt.Printf("\n  %s:\n", i18n.T("skill.prefer.available"))
		for _, m := range matches {
			marker := ""
			if m.Source == current {
				marker = " " + colorize("← "+i18n.T("skill.prefer.active"), ColorGreen)
			}
			fmt.Printf("    %s  %s%s\n",
				colorize(m.Name, ColorCyan),
				colorize(fmt.Sprintf("[%s]", m.Source), ColorGray),
				marker)
		}
		fmt.Println()
		return
	}

	// Two args: set or reset preference
	source := args[1]

	if source == "--reset" || source == "reset" || source == "--clear" {
		if err := prefs.RemovePreference(baseName); err != nil {
			fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
			return
		}
		fmt.Printf("\n  %s %s\n\n",
			colorize(i18n.T("skill.prefer.reset"), ColorGreen),
			colorize(baseName, ColorCyan))
		return
	}

	// Validate that the source exists for this skill
	matches := sh.registryMgr.GetAllInstalledInfo(baseName)
	found := false
	for _, m := range matches {
		if m.Source == source {
			found = true
			break
		}
	}
	if !found && len(matches) > 0 {
		fmt.Printf("\n  %s %q %s %q.\n",
			colorize(i18n.T("skill.prefer.source_not_found")+":", ColorYellow),
			source, i18n.T("skill.prefer.for_skill"), baseName)
		fmt.Printf("  %s:", i18n.T("skill.prefer.available"))
		for _, m := range matches {
			fmt.Printf(" %s", colorize(m.Source, ColorCyan))
		}
		fmt.Print("\n\n")
		return
	}

	if err := prefs.SetPreference(baseName, source); err != nil {
		fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}

	fmt.Printf("\n  %s %s → %s\n",
		colorize(i18n.T("skill.prefer.set"), ColorGreen),
		colorize(baseName, ColorCyan),
		colorize(source, ColorGreen))
	fmt.Printf("  %s\n\n", i18n.T("skill.prefer.effect"))
}

// showSecurityAudits fetches and displays security risk assessments from
// the skills.sh partner audit API (Gen Agent Trust Hub, Socket, Snyk).
// This is best-effort with a short timeout — failures are silently ignored.
func (sh *SkillHandler) showSecurityAudits(ctx context.Context, meta *registry.SkillMeta) {
	// Extract source (owner/repo) and skill slug from the full ID
	parts := strings.SplitN(meta.Slug, "/", 3)
	if len(parts) < 3 {
		return
	}
	source := strings.Join(parts[:2], "/")
	skillSlug := parts[2]

	auditData, err := registry.FetchAuditData(ctx, source, []string{skillSlug})
	if err != nil {
		sh.logger.Debug("failed to fetch audit data", zap.Error(err))
		return
	}

	// Look up the audit for our skill
	data, ok := auditData[skillSlug]
	if !ok {
		// Try with the full name as fallback
		data, ok = auditData[meta.Name]
		if !ok {
			return
		}
	}

	fmt.Printf("  %s\n", colorize(i18n.T("skill.info.security")+":", ColorCyan))

	// Gen Agent Trust Hub (ATH)
	athLabel := "--"
	athColor := ColorGray
	if data.ATH != nil {
		athLabel = registry.FormatRiskLevel(data.ATH.Risk)
		athColor = riskColor(data.ATH.Risk)
	}
	fmt.Printf("    %-22s %s\n",
		colorize("Gen Agent Trust Hub:", ColorGray),
		colorize(athLabel, athColor))

	// Socket
	socketLabel := "--"
	socketColor := ColorGray
	if data.Socket != nil {
		socketLabel = registry.FormatSocketAlerts(data.Socket)
		if data.Socket.Alerts > 0 {
			socketColor = ColorRed
		} else {
			socketColor = ColorGreen
		}
	}
	fmt.Printf("    %-22s %s\n",
		colorize("Socket:", ColorGray),
		colorize(socketLabel, socketColor))

	// Snyk
	snykLabel := "--"
	snykColor := ColorGray
	if data.Snyk != nil {
		snykLabel = registry.FormatRiskLevel(data.Snyk.Risk)
		snykColor = riskColor(data.Snyk.Risk)
	}
	fmt.Printf("    %-22s %s\n",
		colorize("Snyk:", ColorGray),
		colorize(snykLabel, snykColor))
}

// riskColor returns the appropriate color for a risk level.
func riskColor(risk string) string {
	switch strings.ToLower(risk) {
	case "safe":
		return ColorGreen
	case "low":
		return ColorGreen
	case "medium":
		return ColorYellow
	case "high":
		return ColorRed
	case "critical":
		return ColorRed
	default:
		return ColorGray
	}
}

// ShowRegistries displays all configured registries.
func (sh *SkillHandler) ShowRegistries() {
	regs := sh.registryMgr.GetRegistries()

	fmt.Printf("\n  %s\n\n", colorize(i18n.T("skill.registries.configured")+":", ColorCyan))

	for i, r := range regs {
		status := colorize("["+i18n.T("skill.cmd.status_enabled")+"]", ColorGreen)
		if r.TempDisabled {
			remaining := ""
			if r.DisabledUntil != nil {
				remaining = fmt.Sprintf(" ~%ds", int(time.Until(*r.DisabledUntil).Seconds()))
			}
			status = colorize(fmt.Sprintf("[%s]", i18n.T("skill.cmd.status_paused_failures", r.FailureCount, remaining)), ColorYellow)
		} else if !r.Enabled {
			status = colorize("["+i18n.T("skill.cmd.status_disabled")+"]", ColorGray)
		}
		fmt.Printf("    %d. %-12s  %s  %s\n", i+1, r.Name, colorize(r.URL, ColorGray), status)
	}

	fmt.Printf("\n  %s: %s\n", i18n.T("skill.registries.config"), colorize(sh.registryMgr.GetConfigPath(), ColorGray))
	fmt.Printf("  %s\n", i18n.T("skill.registries.toggle_hint",
		colorize("/skill registry enable|disable <name>", ColorCyan)))
	fmt.Println()
}

// SetRegistryEnabled enables or disables a registry by name, persists the change,
// and hot-reloads the registry manager so the change takes effect immediately.
func (sh *SkillHandler) SetRegistryEnabled(name string, enabled bool) {
	cfg, err := registry.LoadConfig()
	if err != nil {
		fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}

	found := false
	for i := range cfg.Registries {
		if strings.EqualFold(cfg.Registries[i].Name, name) {
			cfg.Registries[i].IsActive = enabled
			found = true
			break
		}
	}

	if !found {
		fmt.Printf("\n  %s %q\n", colorize(i18n.T("skill.registry.not_found")+":", ColorYellow), name)
		fmt.Printf("  %s:", i18n.T("skill.registry.available"))
		for _, r := range cfg.Registries {
			fmt.Printf(" %s", colorize(r.Name, ColorCyan))
		}
		fmt.Print("\n\n")
		return
	}

	if err := registry.SaveConfig(cfg); err != nil {
		fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}

	// Hot-reload: recreate the manager with the updated config
	newMgr, err := registry.NewRegistryManager(cfg, sh.logger)
	if err != nil {
		sh.logger.Warn("Failed to reload registry manager", zap.Error(err))
		fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}
	sh.registryMgr = newMgr

	action := i18n.T("skill.registry.enabled")
	actionColor := ColorGreen
	if !enabled {
		action = i18n.T("skill.registry.disabled")
		actionColor = ColorYellow
	}

	fmt.Printf("\n  %s %s\n", colorize(action, actionColor), colorize(name, ColorCyan))
	fmt.Println()
}

// ShowHelp displays usage information.
func (sh *SkillHandler) ShowHelp() {
	fmt.Println()
	fmt.Printf("  %s\n", colorize(i18n.T("skill.help.header")+":", ColorCyan))
	fmt.Printf("  %s\n\n", colorize(strings.Repeat("─", 50), ColorGray))

	commands := []struct {
		cmd  string
		desc string
	}{
		{"/skill search <query>", i18n.T("skill.help.search")},
		{"/skill install <name> [--from <reg>]", i18n.T("skill.help.install")},
		{"/skill uninstall <name>", i18n.T("skill.help.uninstall")},
		{"/skill list", i18n.T("skill.help.list")},
		{"/skill info <name> [--from <reg>]", i18n.T("skill.help.info")},
		{"/skill registries", i18n.T("skill.help.registries")},
		{"/skill registry enable|disable <name>", i18n.T("skill.help.registry_toggle")},
		{"/skill prefer [name] [source]", i18n.T("skill.help.prefer")},
		{"/skill help", i18n.T("skill.help.show_help")},
	}

	// Find max command length for alignment
	maxLen := 0
	for _, c := range commands {
		if len(c.cmd) > maxLen {
			maxLen = len(c.cmd)
		}
	}

	for _, c := range commands {
		padded := fmt.Sprintf("%-*s", maxLen, c.cmd)
		fmt.Printf("    %s   %s\n", colorize(padded, ColorCyan), c.desc)
	}

	fmt.Printf("\n  %s: %s\n",
		i18n.T("skill.help.skills_dir"), colorize(sh.registryMgr.GetInstallDir(), ColorGray))
	fmt.Printf("  %s:     %s\n\n",
		i18n.T("skill.registries.config"), colorize(sh.registryMgr.GetConfigPath(), ColorGray))
}

// shortenRegistryError returns a concise error description for display.
func shortenRegistryError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "no such host") {
		return i18n.T("skill.cmd.err_dns")
	}
	if strings.Contains(msg, "connection refused") {
		return i18n.T("skill.cmd.err_conn_refused")
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return i18n.T("skill.cmd.err_timeout")
	}
	if strings.Contains(msg, "certificate") {
		return i18n.T("skill.cmd.err_tls")
	}
	if strings.Contains(msg, "not_found_error") || strings.Contains(msg, "Not Found") {
		return i18n.T("skill.cmd.err_not_found")
	}
	if len(msg) > 60 {
		return msg[:57] + "..."
	}
	return msg
}
