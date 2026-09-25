/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /config web — the local web UI section.
 */
package cli

import (
	"fmt"
	"strconv"

	"github.com/diillson/chatcli/i18n"
)

func (cli *ChatCLI) showConfigWeb() {
	sectionHeader("🌐", "cfg.section.web.title", ColorCyan)
	p := uiPrefix(ColorCyan)
	cli.web.mu.Lock()
	proc, url, bound := cli.web.proc, cli.web.url, cli.web.bound
	cli.web.mu.Unlock()
	state := i18n.T("cfg.web.state_idle")
	if proc != nil {
		state = i18n.T("cfg.web.state_running")
	}
	kv(p, i18n.T("cfg.web.state"), state)
	if proc != nil {
		kv(p, i18n.T("cfg.web.url"), url)
		kv(p, i18n.T("cfg.web.pid"), strconv.Itoa(proc.PID()))
		kv(p, i18n.T("cfg.web.session"), bound)
	}
	kv(p, i18n.T("cfg.web.log"), webLogPath())
	fmt.Println(p)
	fmt.Println(p + colorize(i18n.T("cfg.web.about"), ColorGray))
	sectionEnd(ColorCyan)
}

// routeConfigWeb: the section is read-only, every argument shows the
// panorama. The process is driven by /web, not by /config.
func (cli *ChatCLI) routeConfigWeb(_ []string) { cli.showConfigWeb() }
