/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - send_adapter.go
 *
 * Implements plugins.SendAdapter so the @send builtin tool can deliver
 * proactive outbound messages through the live gateway platform adapters —
 * the same Telegram/WhatsApp/Discord/Slack/webhook integrations the gateway
 * daemon uses for replies. Supplied to plugins.SetSendAdapter at startup.
 *
 * Target resolution:
 *   "platform"            → the platform's configured home channel
 *                           (CHATCLI_<PLATFORM>_HOME_CHANNEL)
 *   "platform:chat_id"    → an explicit chat id (the rest is passed verbatim,
 *                           so "telegram:-100123:42" keeps the thread suffix)
 *
 * Adapters are built on demand via gateway.BuildConfigured() — only platforms
 * with valid credentials are instantiated, so an unconfigured target yields a
 * clear "not configured" error instead of a silent drop.
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/diillson/chatcli/cli/gateway"
	"github.com/diillson/chatcli/i18n"
	"go.uber.org/zap"
)

// sendPluginAdapter is the concrete plugins.SendAdapter bound to the gateway
// platform registry.
type sendPluginAdapter struct {
	cli *ChatCLI
}

// homeChannelEnv returns the env var holding a platform's default target.
func homeChannelEnv(platform string) string {
	return "CHATCLI_" + strings.ToUpper(platform) + "_HOME_CHANNEL"
}

// splitTarget separates "platform:chat_id" into its parts. A bare "platform"
// returns an empty chatID, signaling the home-channel lookup.
func splitTarget(target string) (platform, chatID string) {
	t := strings.TrimSpace(target)
	if i := strings.IndexByte(t, ':'); i >= 0 {
		return strings.ToLower(strings.TrimSpace(t[:i])), strings.TrimSpace(t[i+1:])
	}
	return strings.ToLower(t), ""
}

// configuredAdapters builds every adapter that currently has valid credentials,
// keyed by platform name.
func (a *sendPluginAdapter) configuredAdapters() (map[string]gateway.Adapter, error) {
	built, err := gateway.BuildConfigured()
	if err != nil {
		return nil, err
	}
	out := make(map[string]gateway.Adapter, len(built))
	for _, ad := range built {
		out[strings.ToLower(ad.Name())] = ad
	}
	return out, nil
}

// Send delivers message to target through the matching gateway adapter.
func (a *sendPluginAdapter) Send(ctx context.Context, target, message string) (string, error) {
	platform, chatID := splitTarget(target)
	if platform == "" {
		return "", fmt.Errorf("%s", i18n.T("send.tool.no_platform"))
	}

	adapters, err := a.configuredAdapters()
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("send.tool.build_failed"), err)
	}

	ad, ok := adapters[platform]
	if !ok {
		return "", fmt.Errorf("%s", i18n.T("send.tool.not_configured", platform, strings.Join(sortedKeys(adapters), ", ")))
	}

	if chatID == "" {
		chatID = strings.TrimSpace(os.Getenv(homeChannelEnv(platform)))
		if chatID == "" {
			return "", fmt.Errorf("%s", i18n.T("send.tool.no_chatid", platform, homeChannelEnv(platform)))
		}
	}

	if err := ad.Send(ctx, gateway.OutboundMessage{ChatID: chatID, Text: message}); err != nil {
		a.log().Warn("@send delivery failed", zap.String("platform", platform), zap.String("chat_id", chatID), zap.Error(err))
		return "", fmt.Errorf("%s: %w", i18n.T("send.tool.failed", platform), err)
	}

	a.log().Info("@send delivered", zap.String("platform", platform), zap.String("chat_id", chatID), zap.Int("bytes", len(message)))
	return i18n.T("send.tool.sent.ok", platform, chatID), nil
}

// List reports the configured platforms and whether each has a home channel.
func (a *sendPluginAdapter) List(ctx context.Context) (string, error) {
	adapters, err := a.configuredAdapters()
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("send.tool.build_failed"), err)
	}
	if len(adapters) == 0 {
		return i18n.T("send.tool.list.empty"), nil
	}

	var b strings.Builder
	b.WriteString(i18n.T("send.tool.list.header"))
	b.WriteByte('\n')
	for _, name := range sortedKeys(adapters) {
		home := strings.TrimSpace(os.Getenv(homeChannelEnv(name)))
		if home == "" {
			b.WriteString(fmt.Sprintf("  • %s — %s\n", name, i18n.T("send.tool.list.no_home", homeChannelEnv(name))))
		} else {
			b.WriteString(fmt.Sprintf("  • %s — %s (%s)\n", name, i18n.T("send.tool.list.home"), home))
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (a *sendPluginAdapter) log() *zap.Logger {
	if a.cli != nil && a.cli.logger != nil {
		return a.cli.logger
	}
	return zap.NewNop()
}

// sortedKeys returns the map keys in stable order for deterministic output.
func sortedKeys(m map[string]gateway.Adapter) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
