/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package catalog

import (
	"os"
	"strings"
)

// Capabilities that override the provider rules of SupportsNativePDF for one
// model: "pdf" forces native PDF input, "no_pdf" forbids it.
const (
	CapabilityPDF   = "pdf"
	CapabilityNoPDF = "no_pdf"
)

// SupportsNativePDF reports whether a model reads a PDF sent as a document
// block (text and page images, so scanned PDFs work too). For the others,
// chatcli sends the PDF's extracted text instead.
//
// Rules, from each provider's documentation (Oct 2026):
//   - Anthropic: every active Claude model (document block).
//   - OpenAI: vision models, gpt-4o and later (file content part /
//     input_file).
//   - Google: every Gemini model (inline application/pdf data).
//   - OpenRouter: any model (file content part; OpenRouter parses PDFs for
//     models that do not read them, OCR included).
//   - Bedrock: Claude models (InvokeModel, Anthropic document block) and
//     Amazon Nova Lite/Pro/Premier (Converse DocumentBlock); Nova Micro is
//     text-only.
//   - Z.AI: the GLM-5.3-Flash family only.
//   - Everyone else (xAI, Moonshot, MiniMax, Ollama, Copilot, Devin,
//     StackSpot, OpenAI Assistants, provider plugins): no native PDF input.
func SupportsNativePDF(provider, model string) bool {
	if HasCapability(provider, model, CapabilityNoPDF) {
		return false
	}
	if HasCapability(provider, model, CapabilityPDF) {
		return true
	}
	m := strings.ToLower(strings.TrimSpace(model))
	switch strings.ToUpper(strings.TrimSpace(provider)) {
	case ProviderClaudeAI:
		return m != ""
	case ProviderOpenAI:
		return HasCapability(provider, model, "vision") || strings.HasPrefix(m, "gpt-4o") ||
			strings.HasPrefix(m, "gpt-4.1") || strings.HasPrefix(m, "gpt-5") || strings.HasPrefix(m, "gpt-6")
	case ProviderGoogleAI:
		return strings.HasPrefix(m, "gemini")
	case ProviderOpenRouter:
		return m != ""
	case ProviderBedrock:
		if strings.Contains(m, "anthropic.") || strings.Contains(m, "claude") {
			return true
		}
		return strings.Contains(m, "amazon.nova") && !strings.Contains(m, "nova-micro")
	case ProviderZAI:
		return strings.HasPrefix(m, "glm-5.3-flash")
	}
	return false
}

// NativePDFInput is SupportsNativePDF with the user's override:
// CHATCLI_PDF_INPUT=text always sends extracted text, =native always sends
// document blocks, auto (default) follows SupportsNativePDF.
func NativePDFInput(provider, model string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CHATCLI_PDF_INPUT"))) {
	case "text", "extract":
		return false
	case "native", "on", "force":
		return true
	}
	return SupportsNativePDF(provider, model)
}
