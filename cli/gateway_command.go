/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - gateway_command.go
 *
 * /gateway [start|stop|status] runs ChatCLI as a messaging daemon. To keep the
 * interactive REPL free, `start` re-execs the binary as a detached child
 * (`chatcli gateway`) — its own process, its own stdout — and tracks it via a
 * pidfile + log under ~/.chatcli/. The child runs RunGatewayForeground.
 *
 * In the daemon, each inbound message runs through the real agent loop fully
 * unattended (no stdin confirmations; full autonomy — the operator opted in).
 * Progress streams back as a short, filtered action feed and the run closes
 * with the model's clean prose answer. Access control is at the edge: Telegram
 * allow-list, Slack signing secret, webhook secret, plus the agent security
 * mode (CHATCLI_AGENT_SECURITY_MODE).
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/server/hub"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// SetUnattended toggles fully non-interactive agent execution (used by the
// gateway daemon). When set, the agent never prompts for confirmation and the
// "thinking" spinner is suppressed — its frames (`model... |/-\`) carry
// alphanumerics, so they slip past gatewayCleanLine and flood the action feed
// when stdout is a captured pipe rather than a TTY. Suppressing at the source
// kills the noise outright instead of trying to filter it downstream.
func (cli *ChatCLI) SetUnattended(v bool) {
	cli.unattended = v
	if cli.animation != nil {
		cli.animation.SetSuppressed(v)
	}
}

func (cli *ChatCLI) handleGatewayCommand(input string) {
	sub := strings.TrimSpace(strings.TrimPrefix(input, "/gateway"))
	switch sub {
	case "", "start":
		cli.gatewayStartDetached()
	case "stop":
		cli.gatewayStop()
	case "status", "platforms":
		cli.gatewayStatus()
	default:
		fmt.Println(colorize("  "+i18n.T("gateway.usage"), ColorGray))
	}
}

// gatewayStatus reports the running daemon (if any) and which platforms are
// registered/configured.
func (cli *ChatCLI) gatewayStatus() {
	names := gateway.RegisteredNames()
	adapters, _ := gateway.BuildConfigured()
	if pid, ok := gatewayRunningPID(); ok {
		fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("gateway.status_running", pid))
	} else {
		fmt.Printf("  %s %s\n", colorize("--", ColorGray), i18n.T("gateway.status_stopped"))
	}
	fmt.Printf("  %s %s\n", colorize(i18n.T("gateway.registered"), ColorYellow), strings.Join(names, ", "))
	fmt.Printf("  %s %d\n", colorize(i18n.T("gateway.configured"), ColorYellow), len(adapters))
}

// gatewayStartDetached re-execs `chatcli gateway` as a detached background
// process so the interactive REPL stays free, tracking it via a pidfile.
func (cli *ChatCLI) gatewayStartDetached() {
	if pid, ok := gatewayRunningPID(); ok {
		fmt.Printf("  %s %s\n", colorize("--", ColorYellow), i18n.T("gateway.already_running", pid))
		return
	}

	adapters, err := gateway.BuildConfigured()
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	if len(adapters) == 0 {
		fmt.Println(colorize("  "+i18n.T("gateway.no_platforms"), ColorYellow))
		return
	}
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	logPath := gatewayStatePath("gateway.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o750)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- daemon-scoped
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "gateway") // #nosec G204 -- exe is self, no user args
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = gatewayDetachAttr()
	if err := cmd.Start(); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	if err := os.WriteFile(gatewayStatePath("gateway.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		cli.logger.Warn("gateway: could not write pidfile")
	}

	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen),
		i18n.T("gateway.started_detached", cmd.Process.Pid, strings.Join(names, ", "), logPath))
}

// gatewayStop signals the detached daemon to terminate and clears the pidfile.
func (cli *ChatCLI) gatewayStop() {
	pid, ok := gatewayRunningPID()
	if !ok {
		fmt.Printf("  %s %s\n", colorize("--", ColorGray), i18n.T("gateway.status_stopped"))
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	if err := gatewayTerminate(proc); err != nil {
		fmt.Printf("  %s %v\n", colorize("ERR", ColorRed), err)
		return
	}
	_ = os.Remove(gatewayStatePath("gateway.pid"))
	fmt.Printf("  %s %s\n", colorize("OK", ColorGreen), i18n.T("gateway.stopped_pid", pid))
}

// RunGatewayForeground builds the configured adapters and runs the messaging
// runner in the foreground until ctx is canceled. It is the body of the
// detached `chatcli gateway` subcommand; the agent runs fully unattended. It
// opens its own hub database — durable and cross-channel, but live-tail push to
// a remote CLI only spans processes via DB-on-connect/resync. For real-time
// cross-process push, co-locate the gateway in the server via
// RunGatewayWithBroker (see CHATCLI_GATEWAY_IN_SERVER).
func (cli *ChatCLI) RunGatewayForeground(ctx context.Context) error {
	cli.SetUnattended(true)

	// /gateway start advertises ~/.chatcli/gateway.log, but the structured (zap)
	// logs go to app.log — so the advertised file sat empty (a false positive).
	// Tee the daemon's logger into gateway.log so the place we point the operator
	// at actually carries the gateway's activity. Done before adapters/runner/
	// agent are built so they all inherit the teed logger.
	if closeTee := cli.teeLoggerToGatewayLog(); closeTee != nil {
		defer closeTee()
	}

	// Cross-channel continuity: back conversations with the shared hub so a
	// thread started on Telegram continues on the notebook (and vice versa). If
	// the hub can't be opened, degrade gracefully to per-message handling rather
	// than failing the daemon. A typed-nil must not reach newHubSessions, so we
	// only assign broker on success.
	var broker hub.Store
	if m, err := hub.OpenDefault(ctx, cli.logger); err != nil {
		cli.logger.Warn("gateway: conversation hub unavailable; continuing without cross-channel continuity", zap.Error(err))
	} else {
		broker = m
		defer func() { _ = m.Close() }()
	}
	return cli.runGateway(ctx, broker)
}

// RunGatewayWithBroker runs the gateway sharing an existing hub broker (the gRPC
// server's). Because the fan-out Manager is in-memory, sharing one broker in a
// single process is what makes a Telegram message push live to a connected
// notebook in real time. The caller owns the broker's lifecycle.
func (cli *ChatCLI) RunGatewayWithBroker(ctx context.Context, broker hub.Broker) error {
	cli.SetUnattended(true)
	return cli.runGateway(ctx, broker)
}

// runGateway builds the configured adapters and runs the messaging runner until
// ctx is canceled, backing conversations with the given hub store (nil = no
// cross-channel continuity).
func (cli *ChatCLI) runGateway(ctx context.Context, broker hub.Store) error {
	adapters, err := gateway.BuildConfigured()
	if err != nil {
		return err
	}
	if len(adapters) == 0 {
		return fmt.Errorf("%s", i18n.T("gateway.no_platforms"))
	}
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
		// Builders create adapters with a no-op logger (they run at import
		// time). Inject the daemon's real logger now so adapter events and
		// every external API request land in the log.
		if la, ok := a.(gateway.LoggerAware); ok {
			la.SetLogger(cli.logger)
		}
	}
	// Startup line so gateway.log is never empty while the daemon is live —
	// immediate proof the advertised log is the one actually being written.
	cli.logger.Info("gateway started",
		zap.Strings("platforms", names), zap.Int("adapters", len(adapters)))

	sessions := newHubSessions(broker, cli.logger)
	sessions.loadBindings(ctx)

	runner := gateway.NewRunner(adapters, cli.gatewayAgentFunc(sessions), cli.logger, 0)
	return runner.Run(ctx)
}

// teeLoggerToGatewayLog adds a JSON sink at gatewayStatePath("gateway.log") to
// cli.logger so the daemon's structured logs land in the file /gateway start
// advertises (in addition to app.log). Returns a closer for the file, or nil
// when it can't be opened (logging then stays app.log-only — no false promise
// is broken because the tee simply didn't attach).
func (cli *ChatCLI) teeLoggerToGatewayLog() func() {
	path := gatewayStatePath("gateway.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- daemon-scoped
	if err != nil {
		cli.logger.Warn("gateway: could not open gateway.log for tee", zap.Error(err))
		return nil
	}
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	enc := zapcore.NewJSONEncoder(encCfg)
	extra := zapcore.NewCore(enc, zapcore.AddSync(f), zapcore.InfoLevel)
	cli.logger = cli.logger.WithOptions(zap.WrapCore(func(c zapcore.Core) zapcore.Core {
		return zapcore.NewTee(c, extra)
	}))
	return func() { _ = f.Close() }
}

// gatewayAgentFunc returns an AgentFunc that runs each inbound message through
// the real (unattended) coder ReAct loop, streaming a short filtered action
// feed via the ctx emitter and returning the model's clean prose answer. The
// coder engine — not the legacy ```execute one-shot — gives the daemon real
// tool capability: it can read/create/edit files, run commands and iterate,
// while the gateway persona keeps the reply concise and chat-friendly. Runs
// serialize because the loop mutates shared ChatCLI state (history,
// lastAgentReply) and redirects os.Stdout for capture.
func (cli *ChatCLI) gatewayAgentFunc(sessions *hubSessions) gateway.AgentFunc {
	var mu sync.Mutex
	return func(ctx context.Context, session, text string) (string, error) {
		if cli.Client == nil {
			return "", fmt.Errorf("no active model")
		}

		mu.Lock()
		defer mu.Unlock()

		// Recover the originating message (Platform/UserID drive cross-channel
		// identity). The Runner always installs it; the fallback derives a best-
		// effort identity from the session key so the agent still works.
		msg, ok := gateway.InboundFromContext(ctx)
		if !ok {
			platform, chatID, _ := strings.Cut(session, ":")
			msg = gateway.InboundMessage{Platform: platform, ChatID: chatID, UserID: chatID, Text: text}
		}

		// Resolve the sender's shared conversation and record the incoming turn
		// before running, so the message survives even if the run fails. preamble
		// carries the prior dialog (across every channel) as context.
		conv := sessions.begin(ctx, msg)
		task := msg.Text
		if pre := conv.preamble; pre != "" {
			task = pre + "\n\nCurrent request: " + msg.Text
		}

		emit := gateway.Progress(ctx)
		var lastSent string
		stream := func(line string) {
			s := gatewayCleanLine(line)
			if s == "" || s == lastSent { // drop noise and consecutive duplicates
				return
			}
			lastSent = s
			emit(s)
		}
		if _, err := cli.RunGatewayCoderStreaming(ctx, task, stream); err != nil {
			return "", err
		}

		// The clean prose answer was captured (and not printed) during the run.
		reply := strings.TrimSpace(cli.lastAgentReply)
		if reply == "" {
			reply = "✅ " + i18n.T("gateway.task_done")
		}
		conv.finish(ctx, reply)
		return reply, nil
	}
}

// gatewayCleanLine trims a streamed line, strips box-drawing/decorative runes,
// and drops anything left without letters or digits, so the chat sees concise
// action lines instead of UI chrome.
func gatewayCleanLine(line string) string {
	// Strip box-drawing, block and arrow decoration that surrounds agent UI.
	cleaned := strings.Map(func(r rune) rune {
		if r == '\r' {
			return -1
		}
		if r >= 0x2500 && r <= 0x259F { // box drawing + block elements
			return -1
		}
		return r
	}, line)

	s := strings.TrimSpace(cleaned)
	if s == "" {
		return ""
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return s
		}
	}
	return ""
}

// ── daemon state helpers ───────────────────────────────────────

// gatewayStatePath returns ~/.chatcli/<name>, falling back to the temp dir.
func gatewayStatePath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), name)
	}
	return filepath.Join(home, ".chatcli", name)
}

// gatewayRunningPID returns the daemon PID if the pidfile points at a live
// process, clearing a stale pidfile otherwise.
func gatewayRunningPID() (int, bool) {
	data, err := os.ReadFile(gatewayStatePath("gateway.pid")) // #nosec G304 -- daemon-scoped
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !gatewayProcessAlive(pid) {
		_ = os.Remove(gatewayStatePath("gateway.pid"))
		return 0, false
	}
	return pid, true
}

// gatewayContextTurns bounds how many prior dialog turns are fed back as
// context per run, keeping the prompt bounded on a long-lived daemon.
const gatewayContextTurns = 12

// hubSessions backs gateway conversations with the shared conversation hub, so
// a thread is the same across Telegram/Slack/WhatsApp and the notebook CLI. A
// sender (platform,userID) is mapped to a principal: an explicit binding merges
// channels into one named identity (shared with the connected CLI), while an
// unbound sender falls back to an isolated per-channel principal — the daemon
// still works for everyone, and no message leaks across identities.
type hubSessions struct {
	store  hub.Store // nil when the hub could not be opened (degrade to no continuity)
	logger *zap.Logger
}

func newHubSessions(store hub.Store, logger *zap.Logger) *hubSessions {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &hubSessions{store: store, logger: logger}
}

// loadBindings seeds the store with explicit channel→principal bindings from
// CHATCLI_HUB_BINDINGS, formatted as "telegram:123=alice;slack:U1=alice"
// (entries separated by ';' or ','). Malformed entries are skipped with a warn.
func (s *hubSessions) loadBindings(ctx context.Context) {
	if s.store == nil {
		return
	}
	raw := strings.TrimSpace(os.Getenv("CHATCLI_HUB_BINDINGS"))
	if raw == "" {
		return
	}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		left, principal, ok := strings.Cut(entry, "=")
		platform, userID, ok2 := strings.Cut(strings.TrimSpace(left), ":")
		principal = strings.TrimSpace(principal)
		if !ok || !ok2 || platform == "" || userID == "" || principal == "" {
			s.logger.Warn("gateway: skipping malformed hub binding", zap.String("entry", entry))
			continue
		}
		if err := s.store.Bind(ctx, platform, userID, principal); err != nil {
			s.logger.Warn("gateway: failed to apply hub binding", zap.String("entry", entry), zap.Error(err))
		}
	}
}

// principalFor maps a sender to its principal: the bound identity when one
// exists, else an isolated per-channel principal.
func (s *hubSessions) principalFor(ctx context.Context, platform, userID string) string {
	if s.store != nil {
		if p, err := s.store.ResolvePrincipal(ctx, platform, userID); err == nil {
			return p
		}
	}
	return platform + ":" + userID
}

// gatewayTurn is the per-message handle returned by begin: it carries the
// preamble to feed the run and knows where to record the assistant reply.
type gatewayTurn struct {
	sessions  *hubSessions
	convID    string
	principal string
	channel   string
	preamble  string
}

// begin resolves the sender's shared conversation, records the incoming user
// turn, and builds the preamble from prior dialog. It never fails the run:
// hub errors degrade to an empty preamble.
func (s *hubSessions) begin(ctx context.Context, msg gateway.InboundMessage) *gatewayTurn {
	turn := &gatewayTurn{sessions: s, channel: msg.Platform}
	if s.store == nil {
		return turn
	}
	turn.principal = s.principalFor(ctx, msg.Platform, msg.UserID)
	convID, err := s.store.Resolve(ctx, turn.principal)
	if err != nil {
		s.logger.Warn("gateway: hub resolve failed; no continuity this turn", zap.Error(err))
		return turn
	}
	turn.convID = convID

	// Context first, from the dialog so far (before this message lands).
	recent, err := s.store.Read(ctx, convID, 0, 0)
	if err != nil {
		s.logger.Warn("gateway: hub read failed", zap.Error(err))
	}
	turn.preamble = renderGatewayPreamble(recent)

	if _, err := s.store.Append(ctx, models.ConversationEvent{
		ConvID:    convID,
		Principal: turn.principal,
		Channel:   msg.Platform,
		Role:      models.ConvRoleUser,
		Content:   msg.Text,
	}); err != nil {
		s.logger.Warn("gateway: hub append (user) failed", zap.Error(err))
	}
	return turn
}

// finish records the assistant reply on the shared conversation.
func (t *gatewayTurn) finish(ctx context.Context, reply string) {
	if t.sessions == nil || t.sessions.store == nil || t.convID == "" || reply == "" {
		return
	}
	if _, err := t.sessions.store.Append(ctx, models.ConversationEvent{
		ConvID:    t.convID,
		Principal: t.principal,
		Channel:   t.channel,
		Role:      models.ConvRoleAssistant,
		Content:   reply,
	}); err != nil {
		t.sessions.logger.Warn("gateway: hub append (assistant) failed", zap.Error(err))
	}
}

// renderGatewayPreamble turns the most recent dialog turns into a compact
// context block, or "" when the conversation is new. tool_summary/checkpoint
// events are surfaced as system context (see ConversationEvent.ToMessage).
func renderGatewayPreamble(events []models.ConversationEvent) string {
	if len(events) == 0 {
		return ""
	}
	if len(events) > gatewayContextTurns {
		events = events[len(events)-gatewayContextTurns:]
	}
	var b strings.Builder
	b.WriteString("Earlier in this conversation (across all channels):")
	for _, ev := range events {
		text := strings.TrimSpace(ev.Content)
		if text == "" {
			continue
		}
		switch ev.Role {
		case models.ConvRoleUser:
			b.WriteString("\n- user: ")
		case models.ConvRoleAssistant:
			b.WriteString("\n- assistant: ")
		default:
			b.WriteString("\n- note: ")
		}
		b.WriteString(text)
	}
	return b.String()
}
