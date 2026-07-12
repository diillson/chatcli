/*
 * ChatCLI - Windows console input sanitizer tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"bytes"
	"errors"
	"testing"

	prompt "github.com/c-bata/go-prompt"
)

func TestStripAltGrEscape(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{
			name: "altgr slash (ABNT2 AltGr+Q) drops the ESC",
			in:   []byte{0x1b, '/'},
			want: []byte{'/'},
		},
		{
			name: "altgr at-sign (German AltGr+Q) drops the ESC",
			in:   []byte{0x1b, '@'},
			want: []byte{'@'},
		},
		{
			name: "altgr euro sign (multi-byte rune) drops the ESC",
			in:   append([]byte{0x1b}, []byte("€")...),
			want: []byte("€"),
		},
		{
			name: "altgr question mark (ABNT2 AltGr+W) drops the ESC",
			in:   []byte{0x1b, '?'},
			want: []byte{'?'},
		},
		{
			name: "lone ESC (Escape key) passes through",
			in:   []byte{0x1b},
			want: []byte{0x1b},
		},
		{
			name: "CSI arrow sequence passes through",
			in:   []byte{0x1b, '[', 'D'},
			want: []byte{0x1b, '[', 'D'},
		},
		{
			name: "SS3 home sequence passes through",
			in:   []byte{0x1b, 'O', 'H'},
			want: []byte{0x1b, 'O', 'H'},
		},
		{
			name: "double-ESC arrow variant passes through",
			in:   []byte{0x1b, 0x1b, '[', 'C'},
			want: []byte{0x1b, 0x1b, '[', 'C'},
		},
		{
			name: "bound alt+f word navigation passes through",
			in:   []byte{0x1b, 'f'},
			want: []byte{0x1b, 'f'},
		},
		{
			name: "bound alt+b word navigation passes through",
			in:   []byte{0x1b, 'b'},
			want: []byte{0x1b, 'b'},
		},
		{
			name: "bound alt+backspace (DEL) passes through",
			in:   []byte{0x1b, 0x7f},
			want: []byte{0x1b, 0x7f},
		},
		{
			name: "bound alt+backspace (BS) passes through",
			in:   []byte{0x1b, 0x08},
			want: []byte{0x1b, 0x08},
		},
		{
			name: "ESC + control rune (alt+enter) passes through",
			in:   []byte{0x1b, '\r'},
			want: []byte{0x1b, '\r'},
		},
		{
			name: "ESC + more than one rune passes through",
			in:   []byte{0x1b, '/', '/'},
			want: []byte{0x1b, '/', '/'},
		},
		{
			name: "ESC + truncated multi-byte rune passes through",
			in:   []byte{0x1b, 0xe2, 0x82},
			want: []byte{0x1b, 0xe2, 0x82},
		},
		{
			name: "plain text passes through",
			in:   []byte("hello"),
			want: []byte("hello"),
		},
		{
			name: "single printable byte passes through",
			in:   []byte{'/'},
			want: []byte{'/'},
		},
		{
			name: "NUL wake byte passes through",
			in:   []byte{0x00},
			want: []byte{0x00},
		},
		{
			name: "empty batch passes through",
			in:   []byte{},
			want: []byte{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripAltGrEscape(tt.in)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("stripAltGrEscape(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// fakeConsoleParser feeds canned batches to the sanitizer wrapper.
type fakeConsoleParser struct {
	batches  [][]byte
	idx      int
	readErr  error
	setupped bool
	toredown bool
}

func (f *fakeConsoleParser) Setup() error                { f.setupped = true; return nil }
func (f *fakeConsoleParser) TearDown() error             { f.toredown = true; return nil }
func (f *fakeConsoleParser) GetWinSize() *prompt.WinSize { return &prompt.WinSize{Row: 24, Col: 80} }
func (f *fakeConsoleParser) Read() ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.idx >= len(f.batches) {
		return nil, nil
	}
	b := f.batches[f.idx]
	f.idx++
	return b, nil
}

func TestAltGrParserReadSanitizes(t *testing.T) {
	inner := &fakeConsoleParser{batches: [][]byte{
		{0x1b, '/'},      // AltGr+Q on ABNT2 → "/"
		{0x1b, '[', 'A'}, // Up arrow → untouched
		[]byte("plain"),  // plain text → untouched
	}}
	p := newAltGrParser(inner)

	got1, err := p.Read()
	if err != nil || !bytes.Equal(got1, []byte{'/'}) {
		t.Fatalf("Read #1 = (%v, %v), want (['/'], nil)", got1, err)
	}
	got2, _ := p.Read()
	if !bytes.Equal(got2, []byte{0x1b, '[', 'A'}) {
		t.Fatalf("Read #2 = %v, want untouched CSI", got2)
	}
	got3, _ := p.Read()
	if !bytes.Equal(got3, []byte("plain")) {
		t.Fatalf("Read #3 = %q, want %q", got3, "plain")
	}
}

func TestAltGrParserDelegates(t *testing.T) {
	inner := &fakeConsoleParser{}
	p := newAltGrParser(inner)

	if err := p.Setup(); err != nil || !inner.setupped {
		t.Fatalf("Setup: err=%v, delegated=%v", err, inner.setupped)
	}
	if ws := p.GetWinSize(); ws == nil || ws.Col != 80 || ws.Row != 24 {
		t.Fatalf("GetWinSize = %+v, want 80x24", ws)
	}
	if err := p.TearDown(); err != nil || !inner.toredown {
		t.Fatalf("TearDown: err=%v, delegated=%v", err, inner.toredown)
	}
}

func TestAltGrParserReadPropagatesError(t *testing.T) {
	wantErr := errors.New("EAGAIN")
	p := newAltGrParser(&fakeConsoleParser{readErr: wantErr})

	data, err := p.Read()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Read error = %v, want %v", err, wantErr)
	}
	if data != nil {
		t.Fatalf("Read data = %v, want nil on error", data)
	}
}
