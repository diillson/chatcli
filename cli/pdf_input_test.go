/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// pdfFixture is a synthetic two-page report shared with pkg/pdftext.
const pdfFixture = "../pkg/pdftext/testdata/report.pdf"

func copyPDFFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(pdfFixture)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadPDFAttachment(t *testing.T) {
	c := minimalCLI(t)
	p := copyPDFFixture(t, "relatorio.pdf")
	doc, ok, err := c.loadPDFAttachment(context.Background(), p)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if doc.Pages != 2 || !strings.Contains(doc.Text, "Ação corretiva") || doc.FileName != "relatorio.pdf" || !doc.IsValid() {
		t.Fatalf("doc = %+v", doc)
	}
	txt := filepath.Join(t.TempDir(), "notes.txt")
	_ = os.WriteFile(txt, []byte("plain"), 0o600)
	if _, ok, _ := c.loadPDFAttachment(context.Background(), txt); ok {
		t.Fatal("a text file is not a PDF attachment")
	}
	if _, ok, _ := c.loadPDFAttachment(context.Background(), filepath.Join(t.TempDir(), "missing.pdf")); ok {
		t.Fatal("a missing file is not attached")
	}
}

// @file report.pdf attaches the document instead of pasting its bytes.
func TestProcessFileCommand_AttachesPDF(t *testing.T) {
	c := minimalCLI(t)
	p := copyPDFFixture(t, "q3.pdf")
	input, ctxText, images, docs := c.processFileCommand(context.Background(), "@file "+p+" resuma o relatório")
	if len(images) != 0 || len(docs) != 1 || docs[0].FileName != "q3.pdf" {
		t.Fatalf("images=%d docs=%+v", len(images), docs)
	}
	if strings.Contains(ctxText, "%PDF") || strings.Contains(ctxText, "endobj") {
		t.Fatalf("raw PDF bytes leaked into the context: %.200s", ctxText)
	}
	if !strings.Contains(input, "resuma") {
		t.Fatalf("input = %q", input)
	}
}

func TestGateDocuments_OffDropsAndTextKept(t *testing.T) {
	c := minimalCLI(t)
	doc := models.DocumentContent{MediaType: models.PDFMediaType, Data: []byte("%PDF-1.7"), FileName: "a.pdf", Text: "--- page 1 ---\nhello"}
	t.Setenv("CHATCLI_PDF_INPUT", "off")
	if got := c.gateDocumentsForRoute(context.Background(), "OPENAI", "gpt-5.5", []models.DocumentContent{doc}); got != nil {
		t.Fatal("CHATCLI_PDF_INPUT=off drops documents")
	}
	t.Setenv("CHATCLI_PDF_INPUT", "")
	// A text-only route with a text layer needs no transcription.
	got := c.gateDocumentsForRoute(context.Background(), "XAI", "grok-4.20", []models.DocumentContent{doc})
	if len(got) != 1 || got[0].Text != doc.Text {
		t.Fatalf("got %+v", got)
	}
	if note := attachedPDFNote(got); !strings.Contains(note, "a.pdf") {
		t.Fatalf("note = %q", note)
	}
}

func TestViewPDF_StagesOnAgentLoopAndReadsWithout(t *testing.T) {
	c := minimalCLI(t)
	c.agentMode = NewAgentMode(c, c.logger)
	adapter := &viewToolAdapter{cli: c}
	p := copyPDFFixture(t, "manual.pdf")
	out, err := adapter.ViewImage(context.Background(), p)
	if err != nil || !strings.Contains(out, "staged") || !strings.Contains(out, "2 pages") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if docs := c.agentMode.drainViewedDocuments(); len(docs) != 1 || docs[0].Pages != 2 {
		t.Fatalf("staged docs = %+v", docs)
	}
	c.agentMode = nil
	out, err = adapter.ViewImage(context.Background(), p)
	if err != nil || !strings.Contains(out, "Ação corretiva") {
		t.Fatalf("without a loop @view returns the text: out=%q err=%v", out, err)
	}
	bogus := filepath.Join(t.TempDir(), "fake.pdf")
	_ = os.WriteFile(bogus, []byte("not a pdf"), 0o600)
	if _, err := adapter.ViewImage(context.Background(), bogus); err == nil {
		t.Fatal("a .pdf that is not a PDF must be refused")
	}
}

func TestSaveTurnDocuments(t *testing.T) {
	c := minimalCLI(t)
	c.stateRoot = t.TempDir()
	data, _ := os.ReadFile(pdfFixture)
	note := c.saveTurnDocuments([]models.DocumentContent{{MediaType: models.PDFMediaType, Data: data, FileName: "../../evil name.pdf"}})
	if !strings.Contains(note, documentNoteHeader) || !strings.Contains(note, ".pdf") || strings.Contains(note, "..") {
		t.Fatalf("note = %q", note)
	}
	if withoutAttachmentNote("hi"+note) != "hi" {
		t.Fatal("the note is stripped from the session title")
	}
}
