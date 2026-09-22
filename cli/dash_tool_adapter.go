/*
 * ChatCLI - @dash tool adapter
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Implements plugins.DashAdapter over the live session: the same server
 * and recorder /dash drives, plus a reader of the spool that reduces the
 * recorded events into the graph the page draws (pulse.Summarize) and
 * renders it as text for the model. Nothing here prints: the tool runs on
 * every surface, including the ones whose stdout is a protocol stream.
 * Supplied to plugins.SetDashAdapter at startup.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/utils"
	"golang.org/x/term"
)

const (
	// dashToolEnv is the kill switch of the @dash tool (default on).
	dashToolEnv = "CHATCLI_AGENT_DASH_TOOL"
	// dashEventsDefault and dashEventsMax bound what "events" hands the model.
	dashEventsDefault = 30
	dashEventsMax     = 200
	// dashReadBatch is the page size used to walk a spool.
	dashReadBatch = 5000
	// dashReadCap bounds one summary at roughly a full spool.
	dashReadCap = 200000
	// dashMarkMaxRunes caps a timeline label.
	dashMarkMaxRunes = 80
	// dashSummaryPerKind caps the lines of one kind in a summary.
	dashSummaryPerKind = 40
)

// isDashToolEnabled gates the @dash tool registration. Default on; set
// CHATCLI_AGENT_DASH_TOOL=false to keep the AI from managing the dashboard.
func isDashToolEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(utils.GetEnvOrDefault(dashToolEnv, "true")))
	return v != "false" && v != "0" && v != "off"
}

// dashToolAdapter is the concrete plugins.DashAdapter.
type dashToolAdapter struct{ cli *ChatCLI }

func (a *dashToolAdapter) ready() error {
	if a.cli == nil || a.cli.pulse == nil {
		return errors.New(i18n.T("dash.tool.unavailable"))
	}
	return nil
}

// Status implements plugins.DashAdapter.
func (a *dashToolAdapter) Status(_ context.Context) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cli := a.cli
	state := i18n.T("cfg.dash.state_idle")
	if cli.pulse.Recording() {
		state = i18n.T("cfg.dash.state_recording")
	}
	cli.dash.mu.Lock()
	srv := cli.dash.srv
	cli.dash.mu.Unlock()
	url := i18n.T("dash.tool.status.no_server")
	if srv != nil {
		url = srv.URL()
	}
	st := pulse.Default().Stats()
	var b strings.Builder
	b.WriteString(i18n.T("dash.tool.status.header", state, url))
	b.WriteString("\n")
	b.WriteString(i18n.T("dash.status.bus", state, st.Published, st.Dropped+st.SlowDropped))
	b.WriteString("\n")
	live := a.liveInstances(true)
	if len(live) == 0 {
		b.WriteString(i18n.T("dash.tool.status.none"))
		return b.String(), nil
	}
	b.WriteString(i18n.T("dash.tool.status.instances"))
	self := pulse.Default().Instance()
	for _, m := range live {
		mark := " "
		if m.Instance == self {
			mark = "*"
		}
		b.WriteString("\n" + i18n.T("dash.status.instance", mark, m.Surface, strconv.Itoa(m.PID), m.Instance))
	}
	return b.String(), nil
}

// Open implements plugins.DashAdapter. The browser only opens from an
// interactive terminal that is not running unattended.
func (a *dashToolAdapter) Open(_ context.Context, browser bool) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	srv, err := a.cli.dashEnsureServer()
	if err != nil {
		return "", err
	}
	msg := i18n.T("dash.tool.opened", srv.URL()) + "\n" + i18n.T("dash.tool.open_hint")
	if browser && !a.cli.unattended && term.IsTerminal(int(os.Stdout.Fd())) {
		if dashBrowserLauncher(srv.URL()) == nil {
			msg += "\n" + i18n.T("dash.tool.browser_opened")
		}
	}
	return msg, nil
}

// Off implements plugins.DashAdapter.
func (a *dashToolAdapter) Off(ctx context.Context) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	if !a.cli.shutdownDash(context.WithoutCancel(ctx)) {
		return i18n.T("dash.not_running"), nil
	}
	a.cli.pulse.Wake()
	return i18n.T("dash.stopped"), nil
}

// Summary implements plugins.DashAdapter.
func (a *dashToolAdapter) Summary(_ context.Context, all bool) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	metas := a.liveInstances(all)
	if len(metas) == 0 {
		return i18n.T("dash.tool.not_recording"), nil
	}
	root := a.cli.pulse.Root()
	var b strings.Builder
	for i, m := range metas {
		if i > 0 {
			b.WriteString("\n\n")
		}
		events := readSpool(root, m.Instance)
		sum := pulse.Summarize(events)
		b.WriteString(i18n.T("dash.tool.summary.header", m.Surface, strconv.Itoa(m.PID), sum.Events, dashSpan(sum)))
		if sum.Events == 0 {
			b.WriteString("\n" + i18n.T("dash.tool.summary.empty"))
			continue
		}
		renderDashSummary(&b, sum)
	}
	return b.String(), nil
}

// Events implements plugins.DashAdapter.
func (a *dashToolAdapter) Events(_ context.Context, q plugins.DashEventsQuery) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	metas := a.liveInstances(q.All)
	if len(metas) == 0 {
		return i18n.T("dash.tool.not_recording"), nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = dashEventsDefault
	}
	if limit > dashEventsMax {
		limit = dashEventsMax
	}
	root := a.cli.pulse.Root()
	var matched []pulse.Event
	total := 0
	for _, m := range metas {
		for _, ev := range readSpool(root, m.Instance) {
			total++
			if q.Kind != "" && string(ev.Kind) != q.Kind {
				continue
			}
			if q.Status != "" && ev.Status != q.Status {
				continue
			}
			matched = append(matched, ev)
		}
	}
	if len(matched) == 0 {
		return i18n.T("dash.tool.events.none"), nil
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].TS.Before(matched[j].TS) })
	if len(matched) > limit {
		matched = matched[len(matched)-limit:]
	}
	filter := ""
	if q.Kind != "" || q.Status != "" {
		filter = " (" + strings.TrimSpace(strings.Join([]string{q.Kind, q.Status}, " ")) + ")"
	}
	var b strings.Builder
	b.WriteString(i18n.T("dash.tool.events.header", len(matched), total, filter))
	for _, ev := range matched {
		b.WriteString("\n" + renderDashEvent(ev, len(metas) > 1))
	}
	return b.String(), nil
}

// Mark implements plugins.DashAdapter. The note is a label: one line, short,
// authored by the model — never file or user content.
func (a *dashToolAdapter) Mark(_ context.Context, note string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	note = dashCleanNote(note)
	if note == "" {
		return "", errors.New(i18n.T("dash.tool.mark.empty"))
	}
	if !pulse.Enabled() {
		return "", errors.New(i18n.T("dash.tool.not_recording"))
	}
	ev := pulse.Event{Kind: pulse.KindSession, Phase: pulse.PhasePoint, ID: pulseSessionNodeID, Status: pulse.StatusOK}
	pulse.Emit(ev.With("note", note))
	return i18n.T("dash.tool.mark.done", note), nil
}

// liveInstances lists the processes a call reads: this one while it
// records, or every live one on the machine.
func (a *dashToolAdapter) liveInstances(all bool) []pulse.Meta {
	root := a.cli.pulse.Root()
	if root == "" {
		return nil
	}
	now := time.Now()
	self := pulse.Default().Instance()
	var out []pulse.Meta
	for _, m := range pulse.ListInstances(root) {
		if !m.Alive(now) {
			continue
		}
		if all || m.Instance == self {
			out = append(out, m)
		}
	}
	if !all && !a.cli.pulse.Recording() {
		return nil
	}
	return out
}

// readSpool walks one instance's spool from the start.
func readSpool(root, instance string) []pulse.Event {
	var out []pulse.Event
	var cursor uint64
	for len(out) < dashReadCap {
		batch, next, err := pulse.ReadSince(root, instance, cursor, dashReadBatch)
		if err != nil {
			break
		}
		out = append(out, batch...)
		if len(batch) < dashReadBatch || next == cursor {
			break
		}
		cursor = next
	}
	return out
}

// dashSpan is how long the recording covers, for the summary header.
func dashSpan(sum pulse.Summary) string {
	if sum.First.IsZero() || sum.Last.IsZero() {
		return "0s"
	}
	return sum.Last.Sub(sum.First).Round(time.Second).String()
}

// renderDashSummary writes one line per node, grouped by kind in the
// dashboard's order, then the recent errors.
func renderDashSummary(b *strings.Builder, sum pulse.Summary) {
	kinds := []pulse.Kind{pulse.KindSession, pulse.KindAgent, pulse.KindTurn, pulse.KindLLM, pulse.KindTool, pulse.KindSkill,
		pulse.KindMCP, pulse.KindPattern, pulse.KindBackground, pulse.KindConn, pulse.KindRPC}
	for _, k := range kinds {
		nodes := sum.ByKind(k)
		for i, n := range nodes {
			if i >= dashSummaryPerKind {
				fmt.Fprintf(b, "\n%s: … +%d", k, len(nodes)-i)
				break
			}
			b.WriteString("\n" + string(k) + ": " + renderDashNode(n))
		}
	}
	if len(sum.Errors) > 0 {
		b.WriteString("\n" + i18n.T("dash.tool.summary.errors", len(sum.Errors)))
		for _, ev := range sum.Errors {
			b.WriteString("\n  " + renderDashEvent(ev, false))
		}
	}
}

// renderDashNode is the text of one card: name, then the same figures the
// page puts on its subline.
func renderDashNode(n *pulse.Node) string {
	a := n.Attrs
	parts := []string{n.Name}
	switch n.Kind {
	case pulse.KindSession:
		if a["model"] != "" {
			parts = append(parts, strings.TrimPrefix(a["provider"]+":"+a["model"], ":"))
		}
		if a["route"] != "" && a["route"] != pulseRouteSession {
			parts = append(parts, "route "+a["route"]+dashVia(a["via"]))
		}
		parts = appendAttr(parts, a, "cost", "")
		parts = appendAttr(parts, a, "requests", " requests")
		parts = appendAttr(parts, a, "tokens", " tokens")
		if a["ctx"] != "" {
			parts = append(parts, "ctx "+a["ctx"])
		}
	case pulse.KindAgent:
		parts = append(parts, n.ID)
		if n.Status != "" {
			parts = append(parts, n.Status)
		}
		if a["turn"] != "" {
			parts = append(parts, "turn "+a["turn"]+"/"+a["max_turns"])
		}
		parts = appendAttr(parts, a, "tool_calls", " tools")
		if n.Ended && n.TotalMS > 0 {
			parts = append(parts, dashMS(n.TotalMS))
		} else if a["action"] != "" {
			parts = append(parts, a["action"])
		}
	default:
		if a["state"] != "" {
			parts = append(parts, a["state"])
		}
		parts = append(parts, fmt.Sprintf("%d×", n.Calls))
		if n.Active > 0 {
			parts = append(parts, fmt.Sprintf("%d active", n.Active))
		}
		if n.Timed > 0 {
			parts = append(parts, "~"+dashMS(n.AvgMS()))
		}
		if n.Errors > 0 {
			parts = append(parts, fmt.Sprintf("%d err", n.Errors))
		}
		parts = appendAttr(parts, a, "cost", "")
		parts = appendAttr(parts, a, "tokens", " tokens")
	}
	return strings.Join(parts, " · ")
}

func dashVia(via string) string {
	if via == "" {
		return ""
	}
	return " via " + via
}

func appendAttr(parts []string, attrs map[string]string, key, suffix string) []string {
	if v := attrs[key]; v != "" {
		return append(parts, v+suffix)
	}
	return parts
}

// renderDashEvent is one raw event on a line: time, kind, name, phase,
// status, duration and its attributes. Everything on it is metadata.
func renderDashEvent(ev pulse.Event, withInstance bool) string {
	name := ev.Name
	if name == "" {
		name = ev.ID
	}
	parts := []string{ev.TS.Format("15:04:05"), string(ev.Kind), name, string(ev.Phase)}
	if ev.Status != "" && ev.Phase != pulse.PhaseStart {
		parts = append(parts, ev.Status)
	}
	if ev.DurMS > 0 {
		parts = append(parts, dashMS(ev.DurMS))
	}
	if withInstance {
		parts = append([]string{ev.Instance}, parts...)
	}
	line := strings.Join(parts, " ")
	if len(ev.Attrs) > 0 {
		keys := make([]string, 0, len(ev.Attrs))
		for k := range ev.Attrs {
			if k != "snapshot" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		kv := make([]string, 0, len(keys))
		for _, k := range keys {
			kv = append(kv, k+"="+ev.Attrs[k])
		}
		if len(kv) > 0 {
			line += " {" + strings.Join(kv, " ") + "}"
		}
	}
	return dashClip(line, 220)
}

// dashClip caps a line at n runes.
func dashClip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func dashMS(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

// dashCleanNote folds a label onto one line and caps its length.
func dashCleanNote(note string) string {
	note = strings.Join(strings.Fields(note), " ")
	note = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, note)
	if r := []rune(note); len(r) > dashMarkMaxRunes {
		note = strings.TrimSpace(string(r[:dashMarkMaxRunes]))
	}
	return strings.TrimSpace(note)
}
