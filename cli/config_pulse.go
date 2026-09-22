/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config dash — the live telemetry section.
 */
package cli

import (
	"fmt"
	"strconv"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseDashEnv makes a process record live telemetry from boot instead of
// waiting for a dashboard to ask for it.
const pulseDashEnv = "CHATCLI_DASH"

// showConfigPulse renders the live telemetry state of this process.
func (cli *ChatCLI) showConfigPulse() {
	sectionHeader("📡", "cfg.section.dash.title", ColorCyan)
	p := uiPrefix(ColorCyan)
	kv(p, pulseDashEnv, envOr(pulseDashEnv))
	kv(p, dashToolEnv, envBool(dashToolEnv))

	state := i18n.T("cfg.dash.state_idle")
	if cli.pulse.Recording() {
		state = i18n.T("cfg.dash.state_recording")
	}
	kv(p, i18n.T("cfg.dash.state"), state)
	if root := cli.pulse.Root(); root != "" {
		kv(p, i18n.T("cfg.dash.spool"), root)
	}
	st := pulse.Default().Stats()
	kv(p, i18n.T("cfg.dash.events"), strconv.FormatUint(st.Published, 10))
	kv(p, i18n.T("cfg.dash.dropped"), strconv.FormatUint(st.Dropped+st.SlowDropped, 10))

	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.dash.about"), ColorGray))
	sectionEnd(ColorCyan)
}
