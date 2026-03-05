/*
 * ChatCLI - Skill Registry Command Handler
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
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
		fmt.Println(colorize(" Skill registry not initialized.", ColorYellow))
		if sh.initErr != nil {
			fmt.Printf("  Error: %s\n", sh.initErr.Error())
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
			fmt.Println(colorize(" Usage: /skill search <query>", ColorYellow))
			return
		}
		query := strings.Join(args[2:], " ")
		sh.Search(query)

	case "install":
		if len(args) < 3 {
			fmt.Println(colorize(" Usage: /skill install <name>", ColorYellow))
			return
		}
		sh.Install(args[2])

	case "uninstall", "remove":
		if len(args) < 3 {
			fmt.Println(colorize(" Usage: /skill uninstall <name>", ColorYellow))
			return
		}
		sh.Uninstall(args[2])

	case "list", "ls":
		sh.List()

	case "registries", "registry":
		sh.ShowRegistries()

	case "info":
		if len(args) < 3 {
			fmt.Println(colorize(" Usage: /skill info <name>", ColorYellow))
			return
		}
		sh.Info(args[2])

	case "help":
		sh.ShowHelp()

	default:
		fmt.Printf(" Unknown subcommand '%s'. Use /skill help for usage.\n", subcommand)
	}
}

// Search performs a fan-out search across all registries.
func (sh *SkillHandler) Search(query string) {
	fmt.Printf("\n  Searching registries for %s...\n",
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
		fmt.Printf("\n  %s\n\n", colorize("No skills found.", ColorYellow))
		return
	}

	fmt.Printf("\n  Results (%d found):\n\n", len(merged))

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

		// Moderation tag
		modTag := ""
		modStr := registry.FormatModerationTag(skill.Moderation)
		if modStr != "" {
			modTag = " " + modStr
		}

		// Installed marker
		installed := ""
		if sh.registryMgr.IsInstalled(skill.Name) {
			installed = " " + colorize("[installed]", ColorGreen)
		}

		// Build the line
		line := fmt.Sprintf("    %d. %s", i+1, colorize(paddedName, ColorCyan))
		if versionStr != "" {
			line += "  " + colorize(versionStr, ColorGray)
		}
		if skill.Author != "" {
			line += "  by " + skill.Author
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

		if skill.Description != "" {
			fmt.Printf("       %s\n", colorize(skill.Description, ColorGray))
		}
	}

	fmt.Printf("\n  Use %s to install a skill.\n\n",
		colorize("/skill install <name>", ColorCyan))
}

// Install downloads and installs a skill from a registry.
func (sh *SkillHandler) Install(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// First, get skill metadata to check moderation
	meta, err := sh.registryMgr.GetSkillMeta(ctx, name)
	if err != nil {
		// Try installing directly (search + download)
		fmt.Printf("\n  Installing %s...\n", colorize(name, ColorCyan))

		result, installErr := sh.registryMgr.Install(ctx, name)
		if installErr != nil {
			fmt.Printf("  %s %s\n\n", colorize("Error:", ColorRed), installErr.Error())
			return
		}

		sh.showInstallResult(result)
		return
	}

	// Check moderation
	warning := registry.CheckModeration(meta)
	if warning != "" {
		if registry.ShouldBlock(meta.Moderation) {
			fmt.Printf("\n  %s %s\n\n", colorize("BLOCKED:", ColorRed), warning)
			return
		}
		// Suspicious — ask for confirmation
		fmt.Printf("\n  %s %s\n", colorize("WARNING:", ColorYellow), warning)
		fmt.Print("  Continue with installation? (y/N): ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("  Installation cancelled.")
			return
		}
	}

	fmt.Printf("\n  Installing %s", colorize(meta.Name, ColorCyan))
	if meta.Version != "" {
		fmt.Printf(" v%s", meta.Version)
	}
	fmt.Printf(" from %s...\n", colorize(meta.RegistryName, ColorGray))

	result, err := sh.registryMgr.Install(ctx, name)
	if err != nil {
		fmt.Printf("  %s %s\n\n", colorize("Error:", ColorRed), err.Error())
		return
	}

	sh.showInstallResult(result)
}

func (sh *SkillHandler) showInstallResult(result *registry.InstallResult) {
	action := "Installed"
	if result.WasDuplicate {
		action = "Updated"
	}

	fmt.Printf("  %s %s", colorize(action, ColorGreen), colorize(result.Name, ColorCyan))
	if result.Version != "" {
		fmt.Printf(" v%s", result.Version)
	}
	fmt.Printf(" from %s\n", colorize(result.Source, ColorGray))
	fmt.Printf("  Path: %s\n", colorize(result.InstallPath, ColorGray))
	fmt.Printf("\n  Skill '%s' is now available.\n", result.Name)
	fmt.Printf("  Verify with: %s\n\n", colorize("/agent skills", ColorCyan))

	// Refresh persona loader to pick up new skill
	if sh.personaMgr != nil {
		_, _ = sh.personaMgr.RefreshSkills()
	}
}

// Uninstall removes an installed skill.
func (sh *SkillHandler) Uninstall(name string) {
	if !sh.registryMgr.IsInstalled(name) {
		fmt.Printf("\n  Skill '%s' is not installed.\n\n", name)
		return
	}

	fmt.Printf("\n  Removing skill '%s'...\n", colorize(name, ColorCyan))

	if err := sh.registryMgr.Uninstall(name); err != nil {
		fmt.Printf("  %s %s\n\n", colorize("Error:", ColorRed), err.Error())
		return
	}

	fmt.Printf("  %s Skill '%s' uninstalled.\n\n",
		colorize("Done.", ColorGreen), name)

	// Refresh persona loader
	if sh.personaMgr != nil {
		_, _ = sh.personaMgr.RefreshSkills()
	}
}

// List shows all installed skills.
func (sh *SkillHandler) List() {
	installed, err := sh.registryMgr.ListInstalled()
	if err != nil {
		fmt.Printf("  %s %s\n", colorize("Error:", ColorRed), err.Error())
		return
	}

	fmt.Println()
	if len(installed) == 0 {
		fmt.Println(colorize("  No skills installed.", ColorYellow))
		fmt.Printf("\n  Use %s to find and install skills.\n\n",
			colorize("/skill search <query>", ColorCyan))
		return
	}

	fmt.Printf("  %s (%d):\n\n",
		colorize("Installed Skills", ColorCyan), len(installed))

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
	fmt.Printf("\n  Registries (%d):\n", len(regs))
	for _, r := range regs {
		status := colorize("[enabled]", ColorGreen)
		if r.TempDisabled {
			status = colorize("[paused]", ColorYellow)
		} else if !r.Enabled {
			status = colorize("[disabled]", ColorGray)
		}
		fmt.Printf("    %-12s  %s  %s\n", r.Name, colorize(r.URL, ColorGray), status)
	}
	fmt.Println()
}

// Info shows metadata about a skill, checking local installed first, then registries.
func (sh *SkillHandler) Info(name string) {
	// 1. Check local installed
	local := sh.registryMgr.GetInstalledInfo(name)

	// 2. Try registry (short timeout)
	var remote *registry.SkillMeta
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meta, err := sh.registryMgr.GetSkillMeta(ctx, name)
	if err == nil && meta != nil && meta.Name != "" {
		remote = meta
	}

	// Nothing found anywhere
	if local == nil && remote == nil {
		fmt.Printf("\n  Skill '%s' not found.\n\n", name)
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
	fmt.Printf("  %s  %s\n", colorize("Name:", ColorCyan), displayName)

	// Description
	desc := ""
	if remote != nil && remote.Description != "" {
		desc = remote.Description
	} else if local != nil && local.Description != "" {
		desc = local.Description
	}
	if desc != "" {
		fmt.Printf("  %s  %s\n", colorize("Desc:", ColorCyan), desc)
	}

	// Version
	ver := ""
	if remote != nil && remote.Version != "" {
		ver = remote.Version
	} else if local != nil && local.Version != "" {
		ver = local.Version
	}
	if ver != "" {
		fmt.Printf("  %s  %s\n", colorize("Version:", ColorCyan), ver)
	}

	// Author
	if remote != nil && remote.Author != "" {
		fmt.Printf("  %s  %s\n", colorize("Author:", ColorCyan), remote.Author)
	}

	// Source / Registry
	source := ""
	if remote != nil && remote.RegistryName != "" {
		source = remote.RegistryName
	} else if local != nil && local.Source != "" {
		source = local.Source
	}
	if source != "" {
		fmt.Printf("  %s  %s\n", colorize("Source:", ColorCyan), source)
	}

	// Tags
	if remote != nil && len(remote.Tags) > 0 {
		fmt.Printf("  %s  %s\n", colorize("Tags:", ColorCyan),
			colorize(strings.Join(remote.Tags, ", "), ColorGray))
	}

	// Downloads
	if remote != nil && remote.Downloads > 0 {
		fmt.Printf("  %s  %d\n", colorize("Downloads:", ColorCyan), remote.Downloads)
	}

	// Moderation
	if remote != nil {
		modTag := registry.FormatModerationTag(remote.Moderation)
		if modTag != "" {
			fmt.Printf("  %s  %s\n", colorize("Moderation:", ColorCyan),
				colorize(modTag, ColorYellow))
		}
	}

	// Install status and path
	if local != nil {
		fmt.Printf("  %s  %s\n", colorize("Status:", ColorCyan),
			colorize("installed", ColorGreen))
		fmt.Printf("  %s  %s\n", colorize("Path:", ColorCyan),
			colorize(local.Path, ColorGray))
	} else {
		fmt.Printf("  %s  not installed\n", colorize("Status:", ColorCyan))
	}

	fmt.Println()
}

// ShowRegistries displays all configured registries.
func (sh *SkillHandler) ShowRegistries() {
	regs := sh.registryMgr.GetRegistries()

	fmt.Printf("\n  %s\n\n", colorize("Configured Registries:", ColorCyan))

	for i, r := range regs {
		status := colorize("[enabled]", ColorGreen)
		if r.TempDisabled {
			remaining := ""
			if r.DisabledUntil != nil {
				remaining = fmt.Sprintf(" ~%ds", int(time.Until(*r.DisabledUntil).Seconds()))
			}
			status = colorize(fmt.Sprintf("[paused: %d failures%s]", r.FailureCount, remaining), ColorYellow)
		} else if !r.Enabled {
			status = colorize("[disabled]", ColorGray)
		}
		fmt.Printf("    %d. %-12s  %s  %s\n", i+1, r.Name, colorize(r.URL, ColorGray), status)
	}

	fmt.Printf("\n  Config: %s\n", colorize(sh.registryMgr.GetConfigPath(), ColorGray))
	fmt.Println("  Edit the config file to add custom registries.")
	fmt.Println()
}

// ShowHelp displays usage information.
func (sh *SkillHandler) ShowHelp() {
	fmt.Println()
	fmt.Printf("  %s\n", colorize("Skill Registry Commands:", ColorCyan))
	fmt.Printf("  %s\n\n", colorize(strings.Repeat("─", 50), ColorGray))

	commands := []struct {
		cmd  string
		desc string
	}{
		{"/skill search <query>", "Search for skills across registries"},
		{"/skill install <name>", "Install a skill from a registry"},
		{"/skill uninstall <name>", "Remove an installed skill"},
		{"/skill list", "List installed skills"},
		{"/skill info <name>", "Show skill details (local + registry)"},
		{"/skill registries", "Show configured registries"},
		{"/skill help", "Show this help"},
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

	fmt.Printf("\n  Skills dir: %s\n",
		colorize(sh.registryMgr.GetInstallDir(), ColorGray))
	fmt.Printf("  Config:     %s\n\n",
		colorize(sh.registryMgr.GetConfigPath(), ColorGray))
}

// shortenRegistryError returns a concise error description for display.
func shortenRegistryError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "no such host") {
		return "unavailable (DNS lookup failed)"
	}
	if strings.Contains(msg, "connection refused") {
		return "unavailable (connection refused)"
	}
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timeout") {
		return "unavailable (timeout)"
	}
	if strings.Contains(msg, "certificate") {
		return "TLS certificate error"
	}
	if strings.Contains(msg, "not_found_error") || strings.Contains(msg, "Not Found") {
		return "API endpoint not found"
	}
	if len(msg) > 60 {
		return msg[:57] + "..."
	}
	return msg
}
