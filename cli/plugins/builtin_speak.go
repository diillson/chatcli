/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * BuiltinSpeakPlugin — text-to-speech as an @speak ReAct tool.
 *
 * It synthesizes text into an audio file using the configured TTS backend
 * (local macOS `say`/espeak, a self-hosted OpenAI-compatible endpoint, or
 * OpenAI), local/keyless-first. The same llm/tts package also powers the
 * gateway's optional voice replies. Self-contained — it reads the backend from
 * the environment via tts.NewFromEnv, so no adapter wiring is required.
 */
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/diillson/chatcli/llm/tts"
)

// BuiltinSpeakPlugin is the @speak tool.
type BuiltinSpeakPlugin struct{}

// NewBuiltinSpeakPlugin returns a ready-to-register plugin.
func NewBuiltinSpeakPlugin() *BuiltinSpeakPlugin { return &BuiltinSpeakPlugin{} }

// Name returns "@speak".
func (*BuiltinSpeakPlugin) Name() string { return "@speak" }

// Description surfaces the tool.
func (*BuiltinSpeakPlugin) Description() string {
	return "Convert text to speech and save it as an audio file using the configured TTS backend (local say/espeak, self-hosted, or OpenAI). Use when asked to 'say this out loud', 'read this aloud', 'make an audio of', 'text to speech'."
}

// Usage explains the canonical invocation.
func (*BuiltinSpeakPlugin) Usage() string {
	return `<tool_call name="@speak" args='{"cmd":"say","args":{"text":"Hello there"}}' />

Subcommands (cmd + args):
  say {text, voice?, format?, out?}
       text    the text to speak (required)
       voice   optional voice name (backend-dependent)
       format  optional: mp3|wav|opus|ogg|aac|flac (default mp3)
       out     optional output file path (default: a temp file)
  status  show the effective TTS backend`
}

// Version is semver.
func (*BuiltinSpeakPlugin) Version() string { return "1.0.0" }

// Path is empty for builtin plugins.
func (*BuiltinSpeakPlugin) Path() string { return "" }

// Schema describes the subcommands.
func (*BuiltinSpeakPlugin) Schema() string {
	schema := map[string]interface{}{
		"argsFormat": "JSON envelope {cmd, args} preferred",
		"subcommands": []map[string]interface{}{
			{
				"name":        "say",
				"description": "Synthesize text to an audio file.",
				"flags": []map[string]interface{}{
					{"name": "text", "type": "string", "required": true, "description": "Text to speak."},
					{"name": "voice", "type": "string", "required": false, "description": "Voice name (backend-dependent)."},
					{"name": "format", "type": "string", "required": false, "description": "mp3|wav|opus|ogg|aac|flac (default mp3)."},
					{"name": "out", "type": "string", "required": false, "description": "Output file path (default temp)."},
				},
				"examples": []string{`{"cmd":"say","args":{"text":"Build finished","format":"mp3"}}`},
			},
			{
				"name":        "status",
				"description": "Show the effective TTS backend.",
				"examples":    []string{`{"cmd":"status"}`},
			},
		},
	}
	data, _ := json.Marshal(schema)
	return string(data)
}

// Execute parses args and dispatches.
func (p *BuiltinSpeakPlugin) Execute(ctx context.Context, args []string) (string, error) {
	return p.ExecuteWithStream(ctx, args, nil)
}

// ExecuteWithStream ignores the stream callback.
func (p *BuiltinSpeakPlugin) ExecuteWithStream(ctx context.Context, args []string, _ func(string)) (string, error) {
	if len(args) == 0 {
		return "", errors.New(`@speak: empty args. Example: <tool_call name="@speak" args='{"cmd":"say","args":{"text":"hello"}}' />`)
	}
	cmd, inner, err := parseSpeakInvocation(args)
	if err != nil {
		return "", fmt.Errorf("@speak: %w", err)
	}

	provider := tts.NewFromEnv(nil)

	switch cmd {
	case "status":
		if tts.IsNull(provider) {
			return "@speak: no TTS backend configured. Set CHATCLI_TTS_CMD (local), CHATCLI_TTS_URL (self-hosted), or install `say`/`espeak-ng`.", nil
		}
		return "@speak backend: " + provider.Name(), nil
	case "say":
		var in struct {
			Text   string `json:"text"`
			Voice  string `json:"voice"`
			Format string `json:"format"`
			Out    string `json:"out"`
		}
		_ = json.Unmarshal([]byte(inner), &in)
		if strings.TrimSpace(in.Text) == "" {
			return "", errors.New(`@speak say: "text" is required`)
		}
		if tts.IsNull(provider) {
			return "", tts.ErrDisabled
		}
		audio, err := provider.Synthesize(ctx, in.Text, in.Voice, in.Format)
		if err != nil {
			return "", fmt.Errorf("@speak: %w", err)
		}
		path, err := writeAudio(in.Out, audio)
		if err != nil {
			return "", fmt.Errorf("@speak: %w", err)
		}
		return fmt.Sprintf("Spoke %d characters → %s (%s, %d bytes) via %s",
			len([]rune(in.Text)), path, audio.Mime, len(audio.Data), provider.Name()), nil
	default:
		return "", fmt.Errorf("@speak: unknown cmd %q (valid: say|status)", cmd)
	}
}

// writeAudio writes the clip to out (or a temp file when out is empty) and
// returns the absolute path.
func writeAudio(out string, audio tts.Audio) (string, error) {
	if strings.TrimSpace(out) != "" {
		if err := os.WriteFile(out, audio.Data, 0o600); err != nil {
			return "", err
		}
		abs, _ := filepath.Abs(out)
		return abs, nil
	}
	f, err := os.CreateTemp("", "chatcli-speak-*."+audio.Ext)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(audio.Data); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func parseSpeakInvocation(args []string) (string, string, error) {
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return "", "", fmt.Errorf("parse envelope: %w", err)
		}
		var cmdStr string
		if rc, ok := raw["cmd"]; ok {
			_ = json.Unmarshal(rc, &cmdStr)
		}
		canon := canonicalSpeakCmd(cmdStr)
		if canon == "" {
			return "", "", fmt.Errorf("missing or unknown cmd %q (valid: say|status)", cmdStr)
		}
		var inner string
		if rargs, ok := raw["args"]; ok && len(rargs) > 0 {
			inner = string(rargs)
		} else {
			delete(raw, "cmd")
			b, _ := json.Marshal(raw)
			inner = string(b)
		}
		return canon, inner, nil
	}
	canon := canonicalSpeakCmd(args[0])
	if canon == "" {
		return "", "", fmt.Errorf("expected JSON envelope or subcommand; got %q", args[0])
	}
	if canon == "say" {
		rest := strings.TrimSpace(strings.TrimPrefix(payload, args[0]))
		b, _ := json.Marshal(map[string]string{"text": rest})
		return canon, string(b), nil
	}
	return canon, "{}", nil
}

func canonicalSpeakCmd(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "say", "speak", "tts":
		return "say"
	case "status", "backend":
		return "status"
	}
	return ""
}
