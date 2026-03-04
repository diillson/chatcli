/*
 * ChatCLI - Skill Registry Command Handler
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 *
 * Handles /skill commands: search, install, uninstall, list, registries, help.
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
	fmt.Printf("\n  Searching across registries for %s...\n\n",
		colorize(fmt.Sprintf("%q", query), ColorCyan))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	merged, results := sh.registryMgr.SearchAll(ctx, query)

	// Show errors from individual registries
	for _, r := range results {
		if r.Error != nil {
			fmt.Printf("  %s %s: %s\n",
				colorize("!", ColorYellow),
				r.RegistryName,
				colorize(r.Error.Error(), ColorGray))
		}
	}

	if len(merged) == 0 {
		fmt.Println(colorize("  No skills found.", ColorYellow))
		fmt.Println()
		return
	}

	fmt.Printf("  Results (%d found):\n\n", len(merged))

	for i, skill := range merged {
		// Name and version
		nameStr := colorize(skill.Name, ColorCyan)
		versionStr := ""
		if skill.Version != "" {
			versionStr = colorize(fmt.Sprintf("(v%s)", skill.Version), ColorGray)
		}

		// Author
		authorStr := ""
		if skill.Author != "" {
			authorStr = fmt.Sprintf("by %s", skill.Author)
		}

		// Registry tag
		regTag := colorize(fmt.Sprintf("[%s]", skill.RegistryName), ColorGray)

		// Moderation tag
		modTag := ""
		modStr := registry.FormatModerationTag(skill.Moderation)
		if modStr == "BLOCKED" {
			modTag = colorize(" BLOCKED", ColorRed)
		} else if modStr == "SUSPICIOUS" {
			modTag = colorize(" SUSPICIOUS", ColorYellow)
		} else if modStr == "QUARANTINED" {
			modTag = colorize(" QUARANTINED", ColorRed)
		}

		// Installed marker
		installed := ""
		if sh.registryMgr.IsInstalled(skill.Name) {
			installed = colorize(" [installed]", ColorGreen)
		}

		fmt.Printf("    %d. %s %s %s  %s%s%s\n",
			i+1, nameStr, versionStr, authorStr, regTag, modTag, installed)

		if skill.Description != "" {
			fmt.Printf("       %s\n", skill.Description)
		}
		if len(skill.Tags) > 0 {
			fmt.Printf("       Tags: %s\n", colorize(strings.Join(skill.Tags, ", "), ColorGray))
		}
		fmt.Println()
	}

	fmt.Printf("  Use %s to install a skill.\n\n",
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

	fmt.Printf("\n  Installing %s v%s from %s...\n",
		colorize(meta.Name, ColorCyan),
		meta.Version,
		colorize(meta.RegistryName, ColorGray))

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

	fmt.Printf("  %s %s v%s from %s\n",
		colorize(action, ColorGreen),
		colorize(result.Name, ColorCyan),
		result.Version,
		colorize(result.Source, ColorGray))
	fmt.Printf("  Path: %s\n", colorize(result.InstallPath, ColorGray))
	fmt.Println()
	fmt.Printf("  Skill '%s' is now available.\n", result.Name)
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

	for _, s := range installed {
		nameStr := colorize(s.Name, ColorCyan)
		versionStr := ""
		if s.Version != "" {
			versionStr = colorize(fmt.Sprintf("v%s", s.Version), ColorGray)
		}
		sourceStr := colorize(fmt.Sprintf("[%s]", s.Source), ColorGray)
		pathStr := colorize(s.Path, ColorGray)

		fmt.Printf("    %-20s  %-8s  %-12s  %s\n", nameStr, versionStr, sourceStr, pathStr)
	}

	// Show registries summary
	regs := sh.registryMgr.GetRegistries()
	enabledCount := 0
	for _, r := range regs {
		if r.Enabled {
			enabledCount++
		}
	}
	fmt.Printf("\n  Registries (%d):\n", enabledCount)
	for _, r := range regs {
		status := colorize("[enabled]", ColorGreen)
		if !r.Enabled {
			status = colorize("[disabled]", ColorGray)
		}
		fmt.Printf("    %-12s  %s  %s\n", r.Name, r.URL, status)
	}
	fmt.Println()
}

// Info shows metadata about a skill from registries.
func (sh *SkillHandler) Info(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	meta, err := sh.registryMgr.GetSkillMeta(ctx, name)
	if err != nil {
		fmt.Printf("\n  Skill '%s' not found in any registry.\n\n", name)
		return
	}

	fmt.Println()
	fmt.Printf("  %s %s\n", colorize("Name:", ColorCyan), meta.Name)
	if meta.Description != "" {
		fmt.Printf("  %s %s\n", colorize("Description:", ColorCyan), meta.Description)
	}
	if meta.Version != "" {
		fmt.Printf("  %s %s\n", colorize("Version:", ColorCyan), meta.Version)
	}
	if meta.Author != "" {
		fmt.Printf("  %s %s\n", colorize("Author:", ColorCyan), meta.Author)
	}
	fmt.Printf("  %s %s\n", colorize("Registry:", ColorCyan), meta.RegistryName)
	if len(meta.Tags) > 0 {
		fmt.Printf("  %s %s\n", colorize("Tags:", ColorCyan), strings.Join(meta.Tags, ", "))
	}
	if meta.Downloads > 0 {
		fmt.Printf("  %s %d\n", colorize("Downloads:", ColorCyan), meta.Downloads)
	}

	// Moderation
	modTag := registry.FormatModerationTag(meta.Moderation)
	if modTag != "" {
		fmt.Printf("  %s %s\n", colorize("Moderation:", ColorCyan),
			colorize(modTag, ColorYellow))
	}

	// Installed status
	if sh.registryMgr.IsInstalled(meta.Name) {
		fmt.Printf("  %s %s\n", colorize("Status:", ColorCyan),
			colorize("installed", ColorGreen))
	}
	fmt.Println()
}

// ShowRegistries displays all configured registries.
func (sh *SkillHandler) ShowRegistries() {
	regs := sh.registryMgr.GetRegistries()

	fmt.Printf("\n  %s\n\n", colorize("Configured Registries:", ColorCyan))

	for i, r := range regs {
		status := colorize("[enabled]", ColorGreen)
		if !r.Enabled {
			status = colorize("[disabled]", ColorGray)
		}
		fmt.Printf("    %d. %-12s  %s  %s\n", i+1, r.Name, r.URL, status)
	}

	fmt.Printf("\n  Config: %s\n", colorize(sh.registryMgr.GetConfigPath(), ColorGray))
	fmt.Println("  Edit the config file to add custom registries.")
	fmt.Println()
}

// ShowHelp displays usage information.
func (sh *SkillHandler) ShowHelp() {
	fmt.Println()
	fmt.Println(colorize("  Skill Registry Commands:", ColorCyan))
	fmt.Println(strings.Repeat("  "+string(rune(0x2500)), 25))
	fmt.Println()
	fmt.Printf("    %s           Search for skills across registries\n",
		colorize("/skill search <query>", ColorCyan))
	fmt.Printf("    %s          Install a skill from a registry\n",
		colorize("/skill install <name>", ColorCyan))
	fmt.Printf("    %s        Remove an installed skill\n",
		colorize("/skill uninstall <name>", ColorCyan))
	fmt.Printf("    %s                    List installed skills\n",
		colorize("/skill list", ColorCyan))
	fmt.Printf("    %s             Show skill metadata from registry\n",
		colorize("/skill info <name>", ColorCyan))
	fmt.Printf("    %s              Show configured registries\n",
		colorize("/skill registries", ColorCyan))
	fmt.Printf("    %s                    Show this help\n",
		colorize("/skill help", ColorCyan))
	fmt.Println()
	fmt.Printf("  Skills are installed to: %s\n",
		colorize(sh.registryMgr.GetInstallDir(), ColorGray))
	fmt.Printf("  Config: %s\n\n",
		colorize(sh.registryMgr.GetConfigPath(), ColorGray))
}
