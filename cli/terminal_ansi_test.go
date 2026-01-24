//go:build !windows
// +build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: MIT
 */
package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestEnsureANSIReset(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "String without reset", input: "Hello World", expected: "Hello World\033[0m"},
		{name: "String with reset", input: "Hello World\033[0m", expected: "Hello World\033[0m"},
		{name: "String with short reset", input: "Hello World\033[m", expected: "Hello World\033[m"},
		{name: "Empty string", input: "", expected: "\033[0m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ensureANSIReset(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestRenderMarkdownWithReset(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			return
		}
	}(logger)

	cli := &ChatCLI{logger: logger}
	input := "## Teste\n\nTexto de teste"
	result := cli.renderMarkdown(input)

	if !strings.HasSuffix(result, "\033[0m") {
		t.Errorf("Expected renderMarkdown output to end with ANSI reset, got: %q", result)
	}
}

func TestAnimationManagerStopCleansTerminal(t *testing.T) {
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	am := NewAnimationManager()
	am.ShowThinkingAnimation("Teste")
	time.Sleep(200 * time.Millisecond)
	am.StopThinkingAnimation()

	w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)
	output := string(out)

	if !strings.Contains(output, "\033[K") {
		t.Error("Expected output to contain clear line sequence")
	}
	if !strings.Contains(output, "\033[0m") {
		t.Error("Expected output to contain ANSI reset")
	}
}

func TestAnimationManagerUpdateMessage(t *testing.T) {
	am := NewAnimationManager()
	am.ShowThinkingAnimation("Mensagem Inicial")
	time.Sleep(100 * time.Millisecond)

	am.UpdateMessage("Mensagem Atualizada")
	time.Sleep(100 * time.Millisecond)

	am.mu.Lock()
	currentMsg := am.currentMessage
	am.mu.Unlock()

	if currentMsg != "Mensagem Atualizada" {
		t.Errorf("Expected current message to be 'Mensagem Atualizada', got '%s'", currentMsg)
	}

	am.StopThinkingAnimation()
}

func TestTypewriterEffectSyncsOutput(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			return
		}
	}(logger)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli := &ChatCLI{logger: logger}
	text := "Teste de saída"
	cli.typewriterEffect(text, 1*time.Millisecond)

	w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)

	if !bytes.Contains(out, []byte(text)) {
		t.Errorf("Expected output to contain '%s', got: %q", text, string(out))
	}
}

func TestSignalUnixForceRefreshCleansTerminal(t *testing.T) {
	logger, _ := zap.NewDevelopment()
	defer func(logger *zap.Logger) {
		err := logger.Sync()
		if err != nil {
			return
		}
	}(logger)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cli := &ChatCLI{logger: logger}
	cli.forceRefreshPrompt()

	w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)
	output := string(out)

	if !strings.Contains(output, "\033[K") {
		t.Error("Expected output to contain clear line sequence")
	}
	if !strings.Contains(output, "\033[0m") {
		t.Error("Expected output to contain ANSI reset")
	}
}
