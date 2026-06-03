/*
 * ChatCLI - tests for the local command transcription provider.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package transcription

import (
	"context"
	"strings"
	"testing"
)

func TestNewCommandTranscriber_Validation(t *testing.T) {
	if _, err := NewCommandTranscriber("", ""); err == nil {
		t.Error("empty template must error")
	}
	if _, err := NewCommandTranscriber("whisper-cli -f audio.ogg", ""); err == nil {
		t.Error("template without {input} must error")
	}
	p, err := NewCommandTranscriber("whisper-cli -nt -f {input}", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "command:whisper-cli" {
		t.Errorf("Name = %q", p.Name())
	}
}

func TestCommandTranscriber_Transcribe(t *testing.T) {
	// `cat {input}` echoes the temp file back: with text bytes as the "audio",
	// stdout is the transcript — a portable stand-in for a real STT binary.
	p, err := NewCommandTranscriber("cat {input}", "command")
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Transcribe(context.Background(), []byte("  hello from a voice note  "), "audio/ogg", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello from a voice note" {
		t.Errorf("transcript = %q", out)
	}
}

func TestCommandTranscriber_EmptyAudio(t *testing.T) {
	p, _ := NewCommandTranscriber("cat {input}", "")
	if _, err := p.Transcribe(context.Background(), nil, "", "", ""); err == nil {
		t.Error("empty audio must error before running the command")
	}
}

func TestCommandTranscriber_CommandFails(t *testing.T) {
	// A command that exits non-zero surfaces an error rather than a transcript.
	p, _ := NewCommandTranscriber("false {input}", "")
	if _, err := p.Transcribe(context.Background(), []byte("x"), "", "", ""); err == nil {
		t.Error("a failing command must return an error")
	}
}

func TestCommandTranscriber_OutputDir(t *testing.T) {
	// `cp {input} {output_dir}/transcript.txt` stands in for a file-output STT
	// tool (openai-whisper): the transcript is read from the .txt it writes.
	p, err := NewCommandTranscriber("cp {input} {output_dir}/transcript.txt", "local")
	if err != nil {
		t.Fatal(err)
	}
	if !p.usesOutputDir() {
		t.Error("template with {output_dir} must use file-output mode")
	}
	out, err := p.Transcribe(context.Background(), []byte("hello from a file"), "audio/ogg", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello from a file" {
		t.Errorf("transcript = %q", out)
	}
}

func TestCommandTranscriber_OutputDirNoFile(t *testing.T) {
	// `true` writes nothing into {output_dir} → must error.
	p, _ := NewCommandTranscriber("true {input} {output_dir}", "local")
	if _, err := p.Transcribe(context.Background(), []byte("x"), "", "", ""); err == nil {
		t.Error("no output file must error")
	}
}

func TestCommandTranscriber_NoOutput(t *testing.T) {
	// `true` succeeds but prints nothing — must be treated as no transcript.
	p, _ := NewCommandTranscriber("true {input}", "")
	if _, err := p.Transcribe(context.Background(), []byte("x"), "", "", ""); err == nil || !strings.Contains(err.Error(), "no transcript") {
		t.Errorf("empty stdout must error with 'no transcript'; got %v", err)
	}
}
