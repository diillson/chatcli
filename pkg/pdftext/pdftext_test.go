/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pdftext

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/report.pdf is a synthetic two-page report (headless Chrome):
// a title, a paragraph, a table and a second page.
func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/report.pdf")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestExtract_PagesInReadingOrder(t *testing.T) {
	res, err := Extract(context.Background(), readFixture(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalPages != 2 || len(res.Pages) != 2 || res.Scanned || res.Truncated {
		t.Fatalf("result = %+v", res)
	}
	text := res.Text()
	order := []string{"--- page 1 ---", "Q3", "Ação corretiva", "sa-east-1", "Checkout", "99,95%", "--- page 2 ---", "R$ 12.400,00"}
	pos := -1
	for _, want := range order {
		i := strings.Index(text, want)
		if i < 0 || i < pos {
			t.Fatalf("%q missing or out of order in:\n%s", want, text)
		}
		pos = i
	}
	if PageCount(readFixture(t)) != 2 {
		t.Fatal("PageCount")
	}
}

func TestExtract_Limits(t *testing.T) {
	res, err := Extract(context.Background(), readFixture(t), Limits{MaxPages: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pages) != 1 || !res.Truncated || !strings.Contains(res.Text(), "1 of 2 pages") {
		t.Fatalf("page cap: %+v", res)
	}
}

func TestExtract_BadInputNeverPanics(t *testing.T) {
	cases := map[string][]byte{
		"not a pdf": []byte("hello"),
		"empty":     nil,
		"truncated": readFixture(t)[:400],
		"garbage":   append([]byte("%PDF-1.7\n"), make([]byte, 5000)...),
		"bad xref":  []byte("%PDF-1.4\n1 0 obj <<>> endobj\nxref\n0 1\nzz\ntrailer <<>>\nstartxref\n9\n%%EOF"),
	}
	for name, data := range cases {
		if _, err := Extract(context.Background(), data, Limits{Timeout: 5 * time.Second}); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if PageCount([]byte("nope")) != 0 {
		t.Error("PageCount of junk must be 0")
	}
}

func TestCleanText(t *testing.T) {
	got := cleanText("a\x00b\u0007c  \n\n\n\nd�")
	if got != "abc\n\nd" {
		t.Fatalf("cleanText = %q", got)
	}
}
