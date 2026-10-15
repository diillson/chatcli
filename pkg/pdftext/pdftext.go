/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package pdftext extracts the text of a PDF, page by page, in pure Go.
//
// It serves every place chatcli reads a PDF without a model that reads PDFs
// natively: @file and @read for text-only models, /context and knowledge
// corpora, @docs-flatten. Text comes out in content-stream order, which is
// reading order for the PDFs that office suites, browsers and LaTeX write;
// coordinate sorting was tried and breaks on generators that flip the page
// transform.
//
// A scanned PDF has no text layer: Extract then reports it (Scanned) so
// callers can say so, or hand the file to a model that reads page images.
// Malformed files never crash the caller: the parser's panics are
// recovered and returned as errors.
package pdftext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

// Limits bound one extraction.
type Limits struct {
	MaxPages int           // 0 = DefaultMaxPages
	MaxChars int           // 0 = DefaultMaxChars; text beyond it is cut at a page boundary
	Timeout  time.Duration // 0 = DefaultTimeout
}

// Defaults.
const (
	DefaultMaxPages = 500
	DefaultMaxChars = 2_000_000
	DefaultTimeout  = 60 * time.Second
	// scannedCharsPerPage: below this many text characters per page on
	// average, the PDF is treated as scanned (page images, no text layer).
	scannedCharsPerPage = 20
)

// Page is one page's text.
type Page struct {
	Number int
	Text   string
}

// Result is an extraction.
type Result struct {
	Pages      []Page
	TotalPages int  // pages in the file
	Truncated  bool // MaxPages or MaxChars stopped the extraction
	Scanned    bool // little or no text layer: probably page images
}

// Text renders the pages with "--- page N ---" separators, so a model can
// cite pages.
func (r Result) Text() string {
	var b strings.Builder
	for i, p := range r.Pages {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "--- page %d ---\n%s\n", p.Number, strings.TrimSpace(p.Text))
	}
	if r.Truncated {
		fmt.Fprintf(&b, "\n[… text truncated: %d of %d pages extracted]\n", len(r.Pages), r.TotalPages)
	}
	return b.String()
}

// Chars is the number of text characters extracted.
func (r Result) Chars() int {
	n := 0
	for _, p := range r.Pages {
		n += len(strings.TrimSpace(p.Text))
	}
	return n
}

// ErrEncrypted is returned for password-protected PDFs.
var ErrEncrypted = errors.New("the PDF is encrypted (password-protected)")

// Extract reads the text of a PDF held in data.
func Extract(ctx context.Context, data []byte, lim Limits) (res Result, err error) {
	if lim.MaxPages <= 0 {
		lim.MaxPages = DefaultMaxPages
	}
	if lim.MaxChars <= 0 {
		lim.MaxChars = DefaultMaxChars
	}
	if lim.Timeout <= 0 {
		lim.Timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, lim.Timeout)
	defer cancel()

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		var o outcome
		defer func() {
			if r := recover(); r != nil {
				o = outcome{err: fmt.Errorf("malformed PDF: %v", r)}
			}
			done <- o
		}()
		o.res, o.err = extract(ctx, data, lim)
	}()
	select {
	case o := <-done:
		return o.res, o.err
	case <-ctx.Done():
		return Result{}, fmt.Errorf("PDF text extraction: %w", ctx.Err())
	}
}

func extract(ctx context.Context, data []byte, lim Limits) (Result, error) {
	if !bytes.Contains(head(data), []byte("%PDF-")) {
		return Result{}, errors.New("not a PDF file")
	}
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "encrypt") {
			return Result{}, ErrEncrypted
		}
		return Result{}, fmt.Errorf("malformed PDF: %w", err)
	}
	res := Result{TotalPages: r.NumPage()}
	chars := 0
	for i := 1; i <= res.TotalPages; i++ {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if len(res.Pages) >= lim.MaxPages || chars >= lim.MaxChars {
			res.Truncated = true
			break
		}
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		text, err := p.GetPlainText(nil)
		if err != nil {
			text = ""
		}
		text = cleanText(text)
		chars += len(text)
		res.Pages = append(res.Pages, Page{Number: i, Text: text})
	}
	if res.TotalPages > 0 && res.Chars()/max(len(res.Pages), 1) < scannedCharsPerPage {
		res.Scanned = true
	}
	return res, nil
}

// PageCount returns the number of pages, or 0 when the file cannot be read.
func PageCount(data []byte) (n int) {
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0
	}
	return r.NumPage()
}

func head(data []byte) []byte {
	if len(data) > 1024 {
		return data[:1024]
	}
	return data
}

// cleanText drops control characters the text layer sometimes carries and
// collapses runs of blank lines.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || r == 0xfffd:
			return -1
		}
		return r
	}, s)
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
