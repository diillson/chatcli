/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * PDF input.
 *
 * A PDF attached to a turn (@file, @view, a channel, the web UI) becomes a
 * models.DocumentContent carrying its bytes and its extracted text. How it
 * reaches the model is decided per provider when the message is sent
 * (llm/internal/visionwire.SplitDocuments): models that read PDFs natively
 * get the document — scanned pages included — and every other provider
 * gets the extracted text. No provider is left without the document.
 *
 * The one gap text extraction cannot fill is a scanned PDF (page images,
 * no text layer) sent to a model without native PDF input. For that case
 * gateDocumentsForRoute asks a configured model that does read PDFs to
 * transcribe it — the same hybrid the vision pipeline uses for images.
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/atrest"
	"github.com/diillson/chatcli/pkg/pdftext"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// maxPDFAttachmentBytes caps one attached PDF (the largest native limit any
// provider documents is 50 MB).
const maxPDFAttachmentBytes = 50 << 20

// pdfTranscribeMaxTokens bounds one transcription of a scanned PDF.
const pdfTranscribeMaxTokens = 16000

// loadPDFAttachment reports whether path is a single PDF and, if so, reads
// it into a DocumentContent with its text extracted. Directories, other
// files and unreadable paths return ok=false; an oversized or encrypted
// PDF returns ok=true with err set, so the caller can say why instead of
// pasting raw bytes.
func (cli *ChatCLI) loadPDFAttachment(ctx context.Context, path string) (models.DocumentContent, bool, error) {
	expanded, err := utils.ExpandPath(path)
	if err != nil {
		expanded = path
	}
	info, err := os.Stat(expanded)
	if err != nil || info.IsDir() {
		return models.DocumentContent{}, false, nil
	}
	isPDFExt := strings.EqualFold(filepath.Ext(expanded), ".pdf")
	if info.Size() > maxPDFAttachmentBytes {
		if isPDFExt {
			return models.DocumentContent{}, true, fmt.Errorf("%s is %d MB; the limit for a PDF is %d MB", filepath.Base(expanded), info.Size()>>20, maxPDFAttachmentBytes>>20)
		}
		return models.DocumentContent{}, false, nil
	}
	data, err := os.ReadFile(expanded) //#nosec G304 -- user-specified @file path to attach as a document
	if err != nil {
		return models.DocumentContent{}, false, nil
	}
	if atrest.IsEncrypted(data) {
		if data, err = atrest.OpenAt(expanded, data); err != nil {
			return models.DocumentContent{}, false, nil
		}
	}
	if !models.IsPDF(data) {
		return models.DocumentContent{}, false, nil
	}
	doc := cli.readPDF(ctx, data, filepath.Base(expanded))
	cli.logger.Debug("PDF anexado",
		zap.String("path", expanded), zap.Int("bytes", len(data)), zap.Int("pages", doc.Pages), zap.Int("text_chars", len(doc.Text)))
	return doc, true, nil
}

// readPDF builds the DocumentContent for PDF bytes: page count and text.
// An unreadable text layer leaves Text empty (a scanned or damaged file);
// an encrypted PDF is still attached, since a native model may open it.
func (cli *ChatCLI) readPDF(ctx context.Context, data []byte, name string) models.DocumentContent {
	doc := models.DocumentContent{MediaType: models.PDFMediaType, Data: data, FileName: name}
	ctx, span := beginMediaTool(ctx, pulsePDFExtract)
	span.With("bytes_in", strconv.Itoa(len(data)))
	res, err := pdftext.Extract(ctx, data, pdftext.Limits{Timeout: 60 * time.Second})
	if err != nil {
		cli.logger.Warn("PDF: extração de texto falhou", zap.String("file", name), zap.Error(err))
		doc.Pages = pdftext.PageCount(data)
		span.With("pages", strconv.Itoa(doc.Pages)).EndErr(err)
		return doc
	}
	doc.Pages = res.TotalPages
	if !res.Scanned {
		doc.Text = res.Text()
	}
	span.With("pages", strconv.Itoa(doc.Pages)).
		With("scanned", pulseBool(res.Scanned)).
		With("chars_out", strconv.Itoa(len(doc.Text))).
		End(pulse.StatusOK)
	return doc
}

// gateDocumentsForRoute prepares a turn's PDFs for the model that will
// receive them. With CHATCLI_PDF_INPUT=off they are dropped. A scanned PDF
// bound for a model that cannot read it natively is transcribed by a model
// that can, when one is configured; otherwise the user is told. The
// returned documents keep their bytes: the provider adapter decides between
// a native block and the text.
func (cli *ChatCLI) gateDocumentsForRoute(ctx context.Context, provider, model string, docs []models.DocumentContent) []models.DocumentContent {
	if len(docs) == 0 {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CHATCLI_PDF_INPUT")), "off") {
		cli.logger.Info("PDF: input disabled by CHATCLI_PDF_INPUT=off", zap.Int("documents", len(docs)))
		pulsePDFRouteDecision(ctx, "off", docs)
		return nil
	}
	if catalog.NativePDFInput(provider, model) {
		pulsePDFRouteDecision(ctx, "native", docs)
		return docs
	}
	pulsePDFRouteDecision(ctx, "text", docs)
	for i := range docs {
		if strings.TrimSpace(docs[i].Text) != "" {
			continue
		}
		text, by, err := cli.transcribePDF(ctx, docs[i])
		if err != nil {
			fmt.Println(colorize("  "+i18n.T("pdf.warn.scanned_unreadable", docs[i].Name(), model), ColorYellow))
			cli.logger.Warn("PDF: documento sem texto e nenhum modelo com PDF nativo para transcrevê-lo",
				zap.String("file", docs[i].Name()), zap.Error(err))
			continue
		}
		fmt.Println(colorize("  "+i18n.T("pdf.info.transcribed", docs[i].Name(), by), ColorGray))
		docs[i].Text = text
	}
	return docs
}

// errNoNativePDFModel is returned when no configured provider reads PDFs.
var errNoNativePDFModel = errors.New("no configured model reads PDFs natively")

// maxPDFTranscribeAttempts bounds how many providers a transcription tries.
const maxPDFTranscribeAttempts = 3

// transcribePDF asks a model with native PDF input to transcribe a document
// (OCR of scanned pages included). Candidates come in preference order
// (the vision override, the current provider, then any configured
// provider); a candidate whose client or call fails — a provider listed as
// available whose credentials do not resolve, a quota error — hands over to
// the next instead of ending the attempt.
func (cli *ChatCLI) transcribePDF(ctx context.Context, doc models.DocumentContent) (string, string, error) {
	ctx, span := beginMediaPattern(ctx, pulsePDFTranscribe)
	span.With("pages", strconv.Itoa(doc.Pages)).With("bytes_in", strconv.Itoa(len(doc.Data)))
	candidates := cli.pdfTranscribeCandidates()
	if len(candidates) == 0 {
		span.Outcome(pulse.StatusError, "no native PDF model")
		return "", "", errNoNativePDFModel
	}
	text, by, attempts, err := cli.transcribePDFWith(ctx, doc, candidates)
	span.With("attempts", strconv.Itoa(attempts))
	if err != nil {
		span.Outcome(pulse.StatusOf(err), "transcription failed")
		return "", "", err
	}
	provider, model, _ := strings.Cut(by, "/")
	span.With("provider", provider).With("model", model).
		With("chars_out", strconv.Itoa(len(text))).
		Outcome(pulse.StatusOK, "transcribed")
	return text, by, nil
}

// transcribePDFWith tries the candidates in order and reports how many it
// tried.
func (cli *ChatCLI) transcribePDFWith(ctx context.Context, doc models.DocumentContent, candidates [][2]string) (string, string, int, error) {
	prompt := "Transcribe this document to plain text, page by page, keeping its structure (headings, lists, tables as rows). " +
		"Mark each page as \"--- page N ---\". Transcribe all visible text verbatim, including text inside figures; describe figures and diagrams briefly in brackets (their boxes and arrows, with labels). No preamble."
	msg := models.Message{Role: "user", Content: prompt, Documents: []models.DocumentContent{doc}}
	var errs []error
	attempts := 0
	for i, c := range candidates {
		if i >= maxPDFTranscribeAttempts {
			break
		}
		attempts++
		client, err := cli.manager.GetClient(c[0], c[1])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", c[0], c[1], err))
			continue
		}
		text, err := client.SendPrompt(ctx, prompt, []models.Message{msg}, pdfTranscribeMaxTokens)
		if err != nil || strings.TrimSpace(text) == "" {
			if err == nil {
				err = errors.New("empty transcription")
			}
			errs = append(errs, fmt.Errorf("%s/%s: %w", c[0], c[1], err))
			continue
		}
		return strings.TrimSpace(text), strings.ToLower(c[0]) + "/" + c[1], attempts, nil
	}
	return "", "", attempts, errors.Join(errs...)
}

// pdfTranscribeCandidates lists provider/model pairs that read PDFs
// natively, in preference order, one model per provider.
func (cli *ChatCLI) pdfTranscribeCandidates() [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	add := func(p, m string) {
		key := strings.ToUpper(p)
		if p == "" || m == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, [2]string{p, m})
	}
	if p, m := strings.TrimSpace(os.Getenv("CHATCLI_VISION_PROVIDER")), strings.TrimSpace(os.Getenv("CHATCLI_VISION_MODEL")); p != "" && m != "" &&
		catalog.SupportsNativePDF(p, m) {
		add(p, m)
	}
	available := cli.manager.GetAvailableProviders()
	ordered := make([]string, 0, len(available)+1)
	if cli.Provider != "" {
		ordered = append(ordered, cli.Provider)
	}
	for _, p := range available {
		if !strings.EqualFold(p, cli.Provider) {
			ordered = append(ordered, p)
		}
	}
	for _, p := range ordered {
		if !providerAvailable(available, p) {
			continue
		}
		if strings.EqualFold(p, cli.Provider) && catalog.SupportsNativePDF(p, cli.Model) {
			add(p, cli.Model)
			continue
		}
		for _, meta := range catalog.ListByProvider(p) {
			if catalog.SupportsNativePDF(p, meta.ID) && metaHasVision(meta) {
				add(p, meta.ID)
				break
			}
		}
	}
	return out
}

// attachedPDFNote tells the model which PDFs ride with the turn.
func attachedPDFNote(docs []models.DocumentContent) string {
	if len(docs) == 0 {
		return ""
	}
	names := make([]string, 0, len(docs))
	for _, d := range docs {
		n := d.Name()
		if d.Pages > 0 {
			n += fmt.Sprintf(" (%d pages)", d.Pages)
		}
		names = append(names, n)
	}
	return "\n[Attached PDF: " + strings.Join(names, ", ") + "]"
}

// completeDocuments fills page count and extracted text for documents that
// arrived as bytes only (web UI, RPC clients).
func (cli *ChatCLI) completeDocuments(ctx context.Context, docs []models.DocumentContent) []models.DocumentContent {
	out := make([]models.DocumentContent, 0, len(docs))
	for _, d := range docs {
		if !models.IsPDF(d.Data) {
			continue
		}
		if d.Pages == 0 && d.Text == "" {
			d = cli.readPDF(ctx, d.Data, d.Name())
		}
		out = append(out, d)
	}
	return out
}
