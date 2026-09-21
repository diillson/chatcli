/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * /dash — the live telemetry dashboard. Opening it takes the recording lease,
 * which makes every chatcli process on the machine start reporting; closing
 * it (or just closing the browser tab) lets the lease run out and they all
 * go quiet again. The dashboard is a read-only observer: nothing it does can
 * affect a running turn, which is what makes it safe as a mid-run side
 * command.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/cli/pulsedash"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/ui/theme"
	"golang.org/x/term"
)

// dashBrowserLauncher opens the dashboard URL. A package var so tests swap
// it and never launch a real browser.
var dashBrowserLauncher = openBrowserURL

// dashState is the one dashboard server a session may hold.
type dashState struct {
	mu  sync.Mutex
	srv *pulsedash.Server
}

// handleDashCommand routes /dash [open|url|status|off].
func (cli *ChatCLI) handleDashCommand(ctx context.Context, userInput string) {
	args := strings.Fields(strings.TrimSpace(userInput))
	sub := "open"
	if len(args) >= 2 {
		sub = strings.ToLower(args[1])
	}
	switch sub {
	case "open", "on", "start", "ui":
		cli.dashOpen(true)
	case "url", "link":
		cli.dashOpen(false)
	case "status", "st":
		cli.dashStatus()
	case "off", "stop", "close":
		cli.dashStop(ctx)
	default:
		fmt.Println(colorize("  "+i18n.T("dash.usage.title"), ColorCyan))
		fmt.Println(colorize("  "+i18n.T("dash.usage.body"), ColorGray))
	}
}

// dashOpen starts the dashboard (once per session) and prints its address.
// The browser only opens from an interactive terminal: never from a daemon,
// a pipe or a surface whose stdout is a protocol stream.
func (cli *ChatCLI) dashOpen(launch bool) {
	if cli.pulse == nil {
		fmt.Println(colorize("  "+i18n.T("dash.unavailable"), ColorYellow))
		return
	}
	cli.dash.mu.Lock()
	srv := cli.dash.srv
	if srv == nil {
		started, err := pulsedash.Start(dashOptions(cli.pulse.Root(), "chatcli-"+strconv.Itoa(os.Getpid())))
		if err != nil {
			cli.dash.mu.Unlock()
			fmt.Println(colorize("  "+i18n.T("dash.start_failed", err), ColorYellow))
			return
		}
		srv, cli.dash.srv = started, started
	}
	cli.dash.mu.Unlock()

	// The lease was just taken: record now, not at the next poll.
	cli.pulse.Wake()
	fmt.Println(colorize("  "+i18n.T("dash.serving", srv.URL()), ColorCyan))
	fmt.Println(colorize("  "+i18n.T("dash.serving_hint"), ColorGray))
	if launch && term.IsTerminal(int(os.Stdout.Fd())) {
		_ = dashBrowserLauncher(srv.URL())
	}
}

// dashStop shuts the dashboard down and releases the lease.
func (cli *ChatCLI) dashStop(ctx context.Context) {
	// Stopping must complete even when the turn that typed it was cancelled.
	if !cli.shutdownDash(context.WithoutCancel(ctx)) {
		fmt.Println(colorize("  "+i18n.T("dash.not_running"), ColorGray))
		return
	}
	cli.pulse.Wake()
	fmt.Println(colorize("  "+i18n.T("dash.stopped"), ColorCyan))
}

// shutdownDash stops the server if one is up, reporting whether it was.
func (cli *ChatCLI) shutdownDash(ctx context.Context) bool {
	if cli == nil {
		return false
	}
	cli.dash.mu.Lock()
	srv := cli.dash.srv
	cli.dash.srv = nil
	cli.dash.mu.Unlock()
	if srv == nil {
		return false
	}
	_ = srv.Shutdown(ctx)
	return true
}

// dashStatus prints what is being recorded and by whom.
func (cli *ChatCLI) dashStatus() {
	cli.dash.mu.Lock()
	srv := cli.dash.srv
	cli.dash.mu.Unlock()

	if srv != nil {
		fmt.Println(colorize("  "+i18n.T("dash.serving", srv.URL()), ColorCyan))
	} else {
		fmt.Println(colorize("  "+i18n.T("dash.not_running"), ColorGray))
	}
	state := i18n.T("cfg.dash.state_idle")
	if cli.pulse.Recording() {
		state = i18n.T("cfg.dash.state_recording")
	}
	st := pulse.Default().Stats()
	fmt.Println("  " + i18n.T("dash.status.bus", state, st.Published, st.Dropped+st.SlowDropped))

	now := time.Now()
	for _, m := range pulse.ListInstances(cli.pulse.Root()) {
		if !m.Alive(now) {
			continue
		}
		mark := " "
		if m.Instance == pulse.Default().Instance() {
			mark = "*"
		}
		fmt.Println("  " + i18n.T("dash.status.instance", mark, m.Surface, m.PID, m.Instance))
	}
}

// RunDashForeground serves the dashboard until ctx is cancelled. It backs the
// `chatcli dash` subcommand: a way to watch every chatcli process from a
// terminal of its own, including the ones that have no prompt to type /dash
// into (ACP inside an IDE, the MCP server, the gateway daemon).
func RunDashForeground(ctx context.Context) error {
	root, err := pulse.DefaultRoot()
	if err != nil {
		return err
	}
	srv, err := pulsedash.Start(dashOptions(root, "chatcli-dash-"+strconv.Itoa(os.Getpid())))
	if err != nil {
		return err
	}
	fmt.Println(i18n.T("dash.cli.serving", srv.URL()))
	fmt.Println(i18n.T("dash.cli.hint"))
	if term.IsTerminal(int(os.Stdout.Fd())) {
		_ = dashBrowserLauncher(srv.URL())
	}
	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return srv.Shutdown(stopCtx)
}

// dashOptions builds the page boot data from the active theme and locale.
func dashOptions(root, holder string) pulsedash.Options {
	return pulsedash.Options{
		Root:    root,
		Holder:  holder,
		Self:    pulse.Default().Instance(),
		Lang:    i18n.ActiveTag().String(),
		Theme:   dashThemeVars(theme.Active()),
		Strings: dashStrings(),
	}
}

// dashThemeVars maps the terminal theme onto the page chrome. The palette's
// Background is its surface color (what code blocks sit on), so it becomes
// the panel and the page ground is derived from it. Node-kind colors are NOT
// themed: a fixed categorical set keeps eleven kinds distinguishable, which
// the role colors of an arbitrary theme do not guarantee.
func dashThemeVars(t theme.Theme) map[string]string {
	p := t.Palette
	ground := shadeHex(p.Background.Hex, -0.30)
	if t.Variant == theme.VariantLight {
		ground = shadeHex(p.Background.Hex, 0.45)
	}
	return map[string]string{
		"--bg":     ground,
		"--panel":  p.Background.Hex,
		"--border": p.Border.Hex,
		"--text":   p.Text.Hex,
		"--strong": p.TextStrong.Hex,
		"--muted":  p.Muted.Hex,
		"--accent": p.Primary.Hex,
		"--ok":     p.Success.Hex,
		"--err":    p.Danger.Hex,
		"--warn":   p.Warning.Hex,
	}
}

// shadeHex moves a #rrggbb color toward black (amount < 0) or white
// (amount > 0). Anything that is not #rrggbb is returned untouched.
func shadeHex(hex string, amount float64) string {
	if len(hex) != 7 || hex[0] != '#' {
		return hex
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return hex
	}
	target := 0.0
	if amount > 0 {
		target = 255
	} else {
		amount = -amount
	}
	mix := func(c uint64) uint64 { return uint64(float64(c) + (target-float64(c))*amount + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", mix(v>>16&0xff), mix(v>>8&0xff), mix(v&0xff))
}

// dashStringKeys are the page strings; each maps to the catalog key
// "dash.ui.<key>".
var dashStringKeys = []string{
	"title", "feed", "nodes", "active", "rate", "fit", "reset", "reset_hint", "pause", "resume", "empty", "ended", "turn", "tools", "errors",
	"forbidden", "offline", "p.kind", "p.status", "p.calls", "p.errors", "p.avg", "p.took", "p.recent",
	"kind.session", "kind.agent", "kind.turn", "kind.llm", "kind.tool", "kind.skill", "kind.mcp", "kind.pattern",
	"kind.background", "kind.conn", "kind.rpc",
}

func dashStrings() map[string]string {
	out := make(map[string]string, len(dashStringKeys))
	for _, k := range dashStringKeys {
		out[k] = i18n.T("dash.ui." + k)
	}
	return out
}
