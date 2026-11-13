/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
)

// handleShowOrRestore runs /skill show <name> and /skill restore <name>
// [--yes].
func (sh *SkillHandler) handleShowOrRestore(sub string, rest []string) {
	usage := i18n.T("kp.skill.usage.show")
	if sub == "restore" {
		usage = i18n.T("kp.skill.usage.restore")
	}
	name, yes := "", false
	for _, arg := range rest {
		switch {
		case arg == "--yes" && sub == "restore":
			yes = true
		case strings.HasPrefix(arg, "-") || name != "":
			fmt.Println(colorize(" "+i18n.T("kp.skill.unexpected_arg", arg)+" "+usage, ColorYellow))
			return
		default:
			name = arg
		}
	}
	switch {
	case name == "":
		fmt.Println(colorize(" "+usage, ColorYellow))
	case sub == "show":
		sh.Show(name)
	default:
		sh.Restore(name, yes)
	}
}

// noteIfLoadedElsewhere says so when the skill the session loads under name
// is not the authored copy /skill restore works on: the loader prefers a
// project-local skill, so show printed one file while restore rewrote another.
func (sh *SkillHandler) noteIfLoadedElsewhere(name, authored string) {
	if sh.personaMgr == nil {
		return
	}
	skill, err := sh.personaMgr.GetSkillByName(name)
	if err != nil || skill == nil || skill.Path == "" {
		return
	}
	loaded, errLoaded := os.Stat(skill.Path)
	target, errTarget := os.Stat(authored)
	if errLoaded == nil && errTarget == nil && os.SameFile(loaded, target) {
		return
	}
	fmt.Printf("  %s\n", colorize(i18n.T("kp.skill.other_file", name, skill.Path, authored), ColorYellow))
}

// handleRegistryCommand backs /skill registries and /skill registry
// enable|disable <name>. Only the bare form lists: an action missing its
// registry name, or an unknown action, used to fall through to the listing
// and read as if it had been applied.
func (sh *SkillHandler) handleRegistryCommand(rest []string) {
	if len(rest) == 0 {
		sh.ShowRegistries()
		return
	}
	action := strings.ToLower(rest[0])
	if (action != "enable" && action != "disable") || len(rest) < 2 {
		fmt.Println(colorize(" "+i18n.T("kp.skill.registry.usage"), ColorYellow))
		return
	}
	sh.SetRegistryEnabled(rest[1], action == "enable")
}

// Show prints a skill as the model receives it: where it lives and its
// body. The human counterpart of @skill show.
func (sh *SkillHandler) Show(name string) {
	if sh.personaMgr == nil {
		fmt.Printf("\n  %s\n\n", colorize(i18n.T("skill.pin.no_manager"), ColorRed))
		return
	}
	skill, err := sh.personaMgr.GetSkillByName(name)
	if err != nil || skill == nil {
		fmt.Printf("\n  %s\n\n", colorize(i18n.T("kp.skill.show.not_found", name), ColorYellow))
		return
	}
	fmt.Printf("\n  %s\n", colorize(skill.Name, ColorCyan+ColorBold))
	if skill.Description != "" {
		fmt.Printf("  %s\n", skill.Description)
	}
	fmt.Printf("  %s\n", colorize(skill.Path, ColorGray))
	// Only worth saying when an authored copy exists for restore to act on.
	if authored, _, err := plugins.SkillRestoreFiles(name); err == nil && plugins.SkillExists(name) {
		sh.noteIfLoadedElsewhere(name, authored)
	}
	fmt.Println(colorize("  ─────────────────────────────────────────", ColorGray))
	for _, line := range strings.Split(strings.TrimSpace(skill.Content), "\n") {
		fmt.Printf("  %s\n", line)
	}
	fmt.Println()
}

// Restore brings back the version of a skill saved before the
// self-evolution engine rewrote it. It overwrites SKILL.md and consumes the
// backup, so it is two-step like /jobs clear: without confirmed it only
// says what would change. It never prompts — /skill also runs headless
// over ACP, where a read from stdin would hang the session.
func (sh *SkillHandler) Restore(name string, confirmed bool) {
	skillMD, backup, err := plugins.SkillRestoreFiles(name)
	if err != nil {
		fmt.Printf("\n  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}
	if !plugins.HasBackup(name) {
		fmt.Printf("\n  %s\n\n", colorize(i18n.T("skill.tool.restore_none", name), ColorYellow))
		return
	}
	fmt.Println()
	sh.noteIfLoadedElsewhere(name, skillMD)
	if !confirmed {
		fmt.Printf("  %s\n\n", i18n.T("kp.skill.restore.preview", skillMD, backup, name))
		return
	}
	if _, err := plugins.RestoreSkill(name); err != nil {
		fmt.Printf("  %s %s\n\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		return
	}
	// The loader caches parsed skills; without a refresh the next turn
	// would still inject the version that was just undone.
	if sh.personaMgr != nil {
		if _, err := sh.personaMgr.RefreshSkills(); err != nil {
			fmt.Printf("  %s %s\n", colorize(i18n.T("skill.error")+":", ColorRed), err.Error())
		}
	}
	fmt.Printf("  %s\n\n", colorize(i18n.T("skill.tool.restored", name), ColorGreen))
}
