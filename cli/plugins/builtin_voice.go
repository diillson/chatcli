/*
 * ChatCLI - Built-in @voice plugin: per-conversation voice reply control.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Lets gateway users control audio replies in natural language: when someone
 * asks "answer me in audio" or "stop sending voice messages", the model calls
 * this tool and the preference sticks to that conversation (persisted across
 * daemon restarts). Outside a gateway run there is no conversation to bind
 * to, so the tool refuses with a clear message instead of guessing.
 */
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/gateway"
)

// BuiltinVoicePlugin toggles per-conversation voice replies.
type BuiltinVoicePlugin struct {
	prefs *gateway.VoicePrefs
}

// NewBuiltinVoicePlugin builds the plugin over the shared preference store.
func NewBuiltinVoicePlugin(prefs *gateway.VoicePrefs) *BuiltinVoicePlugin {
	if prefs == nil {
		prefs = gateway.SharedVoicePrefs()
	}
	return &BuiltinVoicePlugin{prefs: prefs}
}

// Name is the tool identifier.
func (*BuiltinVoicePlugin) Name() string { return "@voice" }

// Description surfaces the tool to the model.
func (*BuiltinVoicePlugin) Description() string {
	return "Control audio (voice) replies for THIS conversation. Call with cmd on when the user explicitly asks to receive answers in audio/voice; cmd off when the user asks to stop receiving audio; cmd auto to return to the default (voice answers voice messages); cmd status to report the current setting. Only works in messaging gateway conversations."
}

// Usage explains the canonical invocation.
func (*BuiltinVoicePlugin) Usage() string {
	return `<tool_call name="@voice" args='{"cmd":"on"}' />

Subcommands (cmd):
  on      speak every reply in this conversation (user asked for audio)
  off     never speak in this conversation (user asked to stop audio)
  auto    back to default: replies carry audio only when the user sent audio
  status  report the conversation's current voice setting`
}

// Version is semver.
func (*BuiltinVoicePlugin) Version() string { return "1.0.0" }

// Path is empty for builtins.
func (*BuiltinVoicePlugin) Path() string { return "" }

// Schema describes the accepted envelope for the agent tool layer.
func (*BuiltinVoicePlugin) Schema() string {
	schema := map[string]interface{}{
		"name": "@voice",
		"subcommands": []map[string]interface{}{
			{"cmd": "on", "description": "speak every reply in this conversation"},
			{"cmd": "off", "description": "stop speaking in this conversation"},
			{"cmd": "auto", "description": "default: voice answers voice"},
			{"cmd": "status", "description": "report the current setting"},
		},
	}
	b, _ := json.MarshalIndent(schema, "", "  ")
	return string(b)
}

// ExecuteWithStream delegates to Execute — this tool is instant, nothing to stream.
func (p *BuiltinVoicePlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	return p.Execute(ctx, args)
}

// Execute parses the envelope and applies the per-conversation preference.
func (p *BuiltinVoicePlugin) Execute(_ context.Context, args []string) (string, error) {
	cmd, err := parseVoiceCmd(args)
	if err != nil {
		return "", fmt.Errorf("@voice: %w", err)
	}
	session := p.prefs.ActiveSession()
	if session == "" {
		return "@voice: no active gateway conversation — this tool only works for messaging (Telegram/WhatsApp/Discord/Slack) chats.", nil
	}
	switch cmd {
	case "status":
		return "@voice: current setting for this conversation: " + describeVoicePref(p.prefs.Get(session)), nil
	case "on":
		if err := p.prefs.Set(session, gateway.VoicePrefAlways); err != nil {
			return "", fmt.Errorf("@voice: %w", err)
		}
		return "@voice: audio replies ENABLED for this conversation — every answer will include a voice note. Confirm this to the user in their language.", nil
	case "off":
		if err := p.prefs.Set(session, gateway.VoicePrefNever); err != nil {
			return "", fmt.Errorf("@voice: %w", err)
		}
		return "@voice: audio replies DISABLED for this conversation — answers will be text-only. Confirm this to the user in their language.", nil
	case "auto":
		if err := p.prefs.Set(session, ""); err != nil {
			return "", fmt.Errorf("@voice: %w", err)
		}
		return "@voice: back to the default for this conversation — voice messages get voice answers, text gets text. Confirm this to the user in their language.", nil
	}
	return "", fmt.Errorf("@voice: unknown cmd %q (valid: on|off|auto|status)", cmd)
}

// parseVoiceCmd accepts either a JSON envelope {"cmd":"on"} or a bare word.
func parseVoiceCmd(args []string) (string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if payload == "" {
		return "status", nil
	}
	if strings.HasPrefix(payload, "{") {
		var raw struct {
			Cmd string `json:"cmd"`
		}
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", fmt.Errorf("parse envelope: %w", err)
		}
		payload = raw.Cmd
	}
	cmd := strings.ToLower(strings.TrimSpace(payload))
	switch cmd {
	case "on", "enable", "start":
		return "on", nil
	case "off", "disable", "stop":
		return "off", nil
	case "auto", "default", "reset":
		return "auto", nil
	case "status", "":
		return "status", nil
	}
	return "", fmt.Errorf("unknown cmd %q (valid: on|off|auto|status)", cmd)
}

// describeVoicePref renders a stored preference for the status reply.
func describeVoicePref(pref string) string {
	switch pref {
	case gateway.VoicePrefAlways:
		return "always — every reply includes a voice note"
	case gateway.VoicePrefNever:
		return "never — text-only replies"
	}
	return "default — voice messages get voice answers, text gets text"
}
