/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config selfevolve renders the self-evolution engine state: the autonomy
 * mode plus live observability of what the engine owns (skills it authored) vs
 * the total skill set. Read-only — set the mode via CHATCLI_SELFEVOLVE_MODE.
 * Activation analytics for individual skills live in the @skill stats tool.
 */
package cli

import (
	"fmt"
	"os"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/i18n"
)

// showConfigSelfEvolve renders the /config selfevolve section.
func (cli *ChatCLI) showConfigSelfEvolve() {
	sectionHeader("🧬", "cfg.section.selfevolve.title", ColorBlue)
	p := uiPrefix(ColorBlue)

	subheader(p, "cfg.sub.selfevolve.mode")
	desc := i18n.T("cfg.kv.selfevolve.mode_" + resolveSelfEvolveMode().String())
	if os.Getenv(config.SelfEvolveModeEnv) == "" {
		desc = defaultMarker + desc
	}
	kv(p, config.SelfEvolveModeEnv, desc)

	subheader(p, "cfg.sub.selfevolve.skills")
	total := 0
	if cli.personaHandler != nil {
		if skills, err := cli.personaHandler.GetManager().ListSkills(); err == nil {
			total = len(skills)
		}
	}
	authored := len(loadSelfEvolveManifest().Skills)
	kv(p, i18n.T("cfg.kv.selfevolve.skills_total"), fmt.Sprintf("%d", total))
	kv(p, i18n.T("cfg.kv.selfevolve.skills_authored"), fmt.Sprintf("%d", authored))
	fmt.Println(colorize("  "+i18n.T("cfg.kv.selfevolve.stats_hint"), ColorGray))

	sectionEnd(ColorBlue)
}
