/*
 * ChatCLI - Local command TTS provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Shells out to a local text-to-speech CLI (macOS `say`, espeak-ng, piper, or
 * any wrapper). This is the keyless, serverless backend — no API key, no HTTP
 * server. The command writes audio to a temp file at {output}; {text} carries
 * the input. The template is split into argv and executed WITHOUT a shell, so
 * there is no shell-injection surface; the command is operator-supplied config.
 */
package tts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CommandSynthesizer runs a local TTS command that writes audio to {output}.
type CommandSynthesizer struct {
	argv  []string // template split into argv; contains {text} and {output}
	ext   string   // output file extension (no dot), e.g. "aiff", "wav"
	label string
}

// NewCommandSynthesizer builds the provider from a template such as
// `say {text} -o {output}` or `espeak-ng {text} -w {output}`. The template must
// reference both {text} and {output}. ext is the file extension the command
// writes (so the right MIME is reported).
func NewCommandSynthesizer(template, ext, label string) (*CommandSynthesizer, error) {
	argv := strings.Fields(strings.TrimSpace(template))
	if len(argv) == 0 {
		return nil, fmt.Errorf("tts: empty command template")
	}
	if !strings.Contains(template, "{text}") || !strings.Contains(template, "{output}") {
		return nil, fmt.Errorf("tts: command template must reference {text} and {output}")
	}
	if strings.TrimSpace(ext) == "" {
		ext = "wav"
	}
	if strings.TrimSpace(label) == "" {
		label = "command"
	}
	return &CommandSynthesizer{argv: argv, ext: strings.TrimPrefix(ext, "."), label: label}, nil
}

// Name reports the backend and the binary, e.g. "command:say".
func (c *CommandSynthesizer) Name() string { return c.label + ":" + c.argv[0] }

// Synthesize runs the command and returns the audio it wrote to {output}.
// The format hint is ignored — a local engine produces its native ext.
func (c *CommandSynthesizer) Synthesize(ctx context.Context, text, _, _ string) (Audio, error) {
	if strings.TrimSpace(text) == "" {
		return Audio{}, fmt.Errorf("tts: empty text")
	}

	out, err := os.CreateTemp("", "chatcli-tts-*."+c.ext)
	if err != nil {
		return Audio{}, fmt.Errorf("tts: temp file: %w", err)
	}
	outPath := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(outPath) }()

	argv := make([]string, len(c.argv))
	for i, a := range c.argv {
		a = strings.ReplaceAll(a, "{output}", outPath)
		a = strings.ReplaceAll(a, "{text}", text)
		argv[i] = a
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- operator-supplied template, no shell
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return Audio{}, fmt.Errorf("tts: command failed: %w: %s", err, msg)
		}
		return Audio{}, fmt.Errorf("tts: command failed: %w", err)
	}

	data, err := os.ReadFile(outPath) // #nosec G304 -- temp file we created
	if err != nil {
		return Audio{}, fmt.Errorf("tts: read output: %w", err)
	}
	if len(data) == 0 {
		return Audio{}, fmt.Errorf("tts: command produced no audio")
	}
	mime, ext := mimeFor(c.ext)
	return Audio{Data: data, Mime: mime, Ext: ext}, nil
}
