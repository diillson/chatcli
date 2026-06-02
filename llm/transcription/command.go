/*
 * ChatCLI - Local command transcription provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Shells out to a local speech-to-text CLI (whisper.cpp's whisper-cli,
 * openai-whisper, or any wrapper). This is the keyless, serverless backend —
 * the "just works locally" option: no API key, no HTTP server.
 *
 * The audio is written to a temp file; the configured command template is run
 * with {input} (and optional {lang}) substituted, and the transcript is read
 * from the command's stdout. The template is split into argv and executed
 * WITHOUT a shell, so there is no shell-injection surface — and the command is
 * operator-supplied configuration, the same trust level as any env var.
 */
package transcription

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CommandTranscriber runs a local STT command and reads the transcript from
// stdout.
type CommandTranscriber struct {
	argv  []string // template split into argv; contains {input} and optional {lang}
	label string
}

// NewCommandTranscriber builds the provider from a command template such as
// `whisper-cli -nt -f {input}`. The template must reference {input}. It may also
// reference {output_dir}: when present, the transcript is read from the .txt the
// command writes into that directory (the openai-whisper style) instead of from
// stdout (the whisper.cpp `-nt` style).
func NewCommandTranscriber(template, label string) (*CommandTranscriber, error) {
	argv := strings.Fields(strings.TrimSpace(template))
	if len(argv) == 0 {
		return nil, fmt.Errorf("transcription: empty command template")
	}
	if !strings.Contains(template, "{input}") {
		return nil, fmt.Errorf("transcription: command template must reference {input}")
	}
	if strings.TrimSpace(label) == "" {
		label = "command"
	}
	return &CommandTranscriber{argv: argv, label: label}, nil
}

// usesOutputDir reports whether the template reads its transcript from a file
// in {output_dir} rather than from stdout.
func (c *CommandTranscriber) usesOutputDir() bool {
	for _, a := range c.argv {
		if strings.Contains(a, "{output_dir}") {
			return true
		}
	}
	return false
}

// Name reports the backend and the binary, e.g. "command:whisper-cli".
func (c *CommandTranscriber) Name() string { return c.label + ":" + c.argv[0] }

// Transcribe writes the audio to a temp file, runs the command, and returns its
// stdout as the transcript.
func (c *CommandTranscriber) Transcribe(ctx context.Context, audio []byte, mimeType, filename, language string) (string, error) {
	if len(audio) == 0 {
		return "", fmt.Errorf("transcription: empty audio")
	}

	tmp, err := os.CreateTemp("", "chatcli-stt-*"+extensionForMime(mimeType))
	if err != nil {
		return "", fmt.Errorf("transcription: temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(audio); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("transcription: writing temp audio: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("transcription: closing temp audio: %w", err)
	}

	outDir := ""
	if c.usesOutputDir() {
		outDir, err = os.MkdirTemp("", "chatcli-stt-out-*")
		if err != nil {
			return "", fmt.Errorf("transcription: temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(outDir) }()
	}

	argv := make([]string, len(c.argv))
	for i, a := range c.argv {
		a = strings.ReplaceAll(a, "{input}", tmp.Name())
		a = strings.ReplaceAll(a, "{output_dir}", outDir)
		a = strings.ReplaceAll(a, "{lang}", language)
		argv[i] = a
	}

	var stdout, stderr bytes.Buffer
	// #nosec G204 -- argv comes from operator-supplied configuration, executed
	// without a shell (no interpolation into a shell string).
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("transcription: command %q failed: %w: %s", c.argv[0], err, snippet(stderr.Bytes()))
	}

	if outDir != "" {
		return readTranscriptFile(outDir, c.argv[0])
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("transcription: command %q produced no transcript on stdout", c.argv[0])
	}
	return out, nil
}

// readTranscriptFile reads the transcript a file-output STT tool wrote into dir.
// It prefers a .txt file (what openai-whisper emits), falling back to the first
// regular file present.
func readTranscriptFile(dir, tool string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("transcription: reading output dir: %w", err)
	}
	var fallback string
	pick := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(strings.ToLower(name), ".txt") {
			pick = name
			break
		}
		if fallback == "" {
			fallback = name
		}
	}
	if pick == "" {
		pick = fallback
	}
	if pick == "" {
		return "", fmt.Errorf("transcription: command %q wrote no output file", tool)
	}
	data, err := os.ReadFile(filepath.Join(dir, pick)) // #nosec G304 -- file is inside our own temp dir
	if err != nil {
		return "", fmt.Errorf("transcription: reading transcript: %w", err)
	}
	out := strings.TrimSpace(string(data))
	if out == "" {
		return "", fmt.Errorf("transcription: command %q produced an empty transcript", tool)
	}
	return out, nil
}
