/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

const pagingKey = "aaaabbbbccccdddd"

// longOriginal is a numbered log whose lines are easy to locate.
func longOriginal(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "line %05d: request served in %d ms\n", i, i%97)
	}
	return b.String()
}

func recallWith(t *testing.T, content string, args ...string) string {
	t.Helper()
	defer withAdapter(&fakeCompressionAdapter{store: map[string]string{pagingKey: content}})()
	out, err := NewBuiltinRecallPlugin().Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("recall %v: %v", args, err)
	}
	return out
}

func TestRecallPagesALongOriginal(t *testing.T) {
	t.Setenv("CHATCLI_TOOL_RESULT_MAX_CHARS", "")
	original := longOriginal(2000) // ~84k characters
	page := recallPageSize()

	first := recallWith(t, original, `{"key":"`+pagingKey+`"}`)
	body, footer, ok := strings.Cut(first, "\n\n[@recall: characters 0-")
	if !ok {
		t.Fatalf("a long original must come back paged, got %d chars", len(first))
	}
	if len(body) > page || !strings.HasSuffix(body, "\n") || !strings.HasPrefix(original, body) {
		t.Fatalf("the first page must be a prefix of the original, cut at a line break, within %d chars (got %d)", page, len(body))
	}
	if len(first) > recallDefaultResultMax {
		t.Fatalf("a page with its footer must fit one tool result: %d > %d", len(first), recallDefaultResultMax)
	}
	next := `"offset":` + strconv.Itoa(len(body))
	if !strings.Contains(footer, next) || !strings.Contains(footer, `"match":"text"`) {
		t.Fatalf("the footer must give the next call: %q", footer)
	}

	// Walk every page, JSON and argv forms alternating, and rebuild it.
	rebuilt := body
	offset := len(body)
	for i := 0; offset < len(original); i++ {
		var out string
		if i%2 == 0 {
			out = recallWith(t, original, fmt.Sprintf(`{"key":"%s","offset":%d}`, pagingKey, offset))
		} else {
			out = recallWith(t, original, "--key", pagingKey, "--offset", strconv.Itoa(offset))
		}
		chunk, rest, _ := strings.Cut(out, "\n\n[@recall: characters ")
		rebuilt += chunk
		offset += len(chunk)
		if strings.Contains(rest, "end of the original") {
			break
		}
		if i > 100 {
			t.Fatal("paging never reached the end")
		}
	}
	if rebuilt != original {
		t.Fatalf("the pages must rebuild the original exactly (%d vs %d chars)", len(rebuilt), len(original))
	}

	past := recallWith(t, original, fmt.Sprintf(`{"key":"%s","offset":%d}`, pagingKey, len(original)+5))
	if !strings.Contains(past, "past the end") {
		t.Fatalf("an offset past the end says so: %q", past)
	}
}

func TestRecallKeepsAShortOriginalVerbatim(t *testing.T) {
	if out := recallWith(t, "short original", `{"key":"`+pagingKey+`"}`); out != "short original" {
		t.Fatalf("an original that fits one result comes back byte-identical: %q", out)
	}
	paged := recallWith(t, "short original", `{"key":"`+pagingKey+`","offset":6}`)
	if !strings.HasPrefix(paged, "original") || !strings.Contains(paged, "end of the original") {
		t.Fatalf("an explicit offset pages even a short original: %q", paged)
	}
}

func TestRecallHonorsASmallerPage(t *testing.T) {
	out := recallWith(t, longOriginal(500), `{"key":"`+pagingKey+`","max_chars":1000}`)
	body, _, _ := strings.Cut(out, "\n\n[@recall:")
	if len(body) > 1000 || len(body) < 800 {
		t.Fatalf("max_chars sets the page size: got %d", len(body))
	}
	huge := recallWith(t, longOriginal(2000), `{"key":"`+pagingKey+`","max_chars":999999}`)
	if b, _, _ := strings.Cut(huge, "\n\n[@recall:"); len(b) > recallPageSize() {
		t.Fatalf("max_chars never exceeds what fits one result: %d", len(b))
	}
}

func TestRecallMatchReturnsNumberedLines(t *testing.T) {
	original := "alpha ok\nBeta ERROR one\ngamma ok\ndelta error two\n"
	out := recallWith(t, original, `{"key":"`+pagingKey+`","match":"error"}`)
	want := "[@recall: 2 of 5 lines contain \"error\"]\nL2: Beta ERROR one\nL4: delta error two"
	if out != want {
		t.Fatalf("match =\n%q\nwant\n%q", out, want)
	}
	if out := recallWith(t, original, "--key", pagingKey, "--grep", "gamma"); !strings.Contains(out, "L3: gamma ok") {
		t.Fatalf("grep is an alias of match: %q", out)
	}
	if out := recallWith(t, original, `{"key":"`+pagingKey+`","match":"zeta"}`); !strings.Contains(out, "no line of the original (5 lines)") {
		t.Fatalf("no match says so: %q", out)
	}
	many := recallWith(t, longOriginal(5000), `{"key":"`+pagingKey+`","match":"request"}`)
	if !strings.Contains(many, "more matching lines not shown") || len(many) > recallDefaultResultMax {
		t.Fatalf("a match larger than a page is capped and says how much is left (%d chars)", len(many))
	}
	long := strings.Repeat("x", 1000) + " needle"
	if out := recallWith(t, long, `{"key":"`+pagingKey+`","match":"x"}`); !strings.HasSuffix(out, "…") {
		t.Fatalf("a very long matching line is clipped: %q", out[len(out)-20:])
	}
}

func TestRecallSharesAPageAcrossKeys(t *testing.T) {
	big := longOriginal(2000)
	defer withAdapter(&fakeCompressionAdapter{store: map[string]string{
		"aaaaaaaaaaaaaaaa": big, "bbbbbbbbbbbbbbbb": "small one",
	}})()
	p := NewBuiltinRecallPlugin()
	out, err := p.Execute(context.Background(), []string{`{"key":"<<ccr:aaaaaaaaaaaaaaaa>> <<ccr:bbbbbbbbbbbbbbbb>>"}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > recallDefaultResultMax || !strings.Contains(out, "section cut at") || !strings.Contains(out, "small one") {
		t.Fatalf("sections share one page (%d chars):\n%s", len(out), out[len(out)-300:])
	}
	matched, err := p.Execute(context.Background(), []string{`{"key":"<<ccr:aaaaaaaaaaaaaaaa>> <<ccr:bbbbbbbbbbbbbbbb>>","match":"line 00042"}`})
	if err != nil || !strings.Contains(matched, "L42: line 00042") || !strings.Contains(matched, "no line of the original (1 lines)") {
		t.Fatalf("match applies to every key: %q (%v)", matched, err)
	}
}

func TestRecallPageSizeFollowsTheResultCap(t *testing.T) {
	cases := map[string]int{"": recallDefaultResultMax - recallPageHeadroom, "50000": 48000, "3000": recallPageMin, "junk": recallDefaultResultMax - recallPageHeadroom}
	for env, want := range cases {
		t.Setenv("CHATCLI_TOOL_RESULT_MAX_CHARS", env)
		if got := recallPageSize(); got != want {
			t.Errorf("CHATCLI_TOOL_RESULT_MAX_CHARS=%q → page %d, want %d", env, got, want)
		}
	}
}

func TestRecallPagesNeverSplitARune(t *testing.T) {
	original := strings.Repeat("ação é ótima ", 3000) // no line breaks, multi-byte runes
	out := recallWith(t, original, `{"key":"`+pagingKey+`","max_chars":1001}`)
	body, _, _ := strings.Cut(out, "\n\n[@recall:")
	if !utf8.ValidString(body) {
		t.Fatal("a page must end on a rune boundary")
	}
	next := recallWith(t, original, fmt.Sprintf(`{"key":"%s","offset":%d,"max_chars":1001}`, pagingKey, len(body)+1))
	if nb, _, _ := strings.Cut(next, "\n\n[@recall:"); !utf8.ValidString(nb) {
		t.Fatal("an offset inside a rune starts at the rune")
	}
}

func TestParseRecallOptions(t *testing.T) {
	cases := []struct {
		args []string
		want recallOptions
	}{
		{[]string{`{"key":"k"}`}, recallOptions{}},
		{[]string{`{"key":"k","offset":"40","limit":10}`}, recallOptions{offset: 40, maxChars: 10, paged: true}},
		{[]string{`{"cmd":"recall","args":{"key":"k","offset":5,"grep":"x"}}`}, recallOptions{offset: 5, match: "x", paged: true}},
		{[]string{`{"key":"k","offset":-3,"max_chars":-1}`}, recallOptions{paged: true}},
		{[]string{"--key", "k", "--offset", "7", "--max_chars", "9", "--match", " y "}, recallOptions{offset: 7, maxChars: 9, match: "y", paged: true}},
		{[]string{"ccr:aaaabbbbccccdddd"}, recallOptions{}},
		{[]string{`{not json`}, recallOptions{}},
	}
	for _, c := range cases {
		if got := parseRecallOptions(c.args); got != c.want {
			t.Errorf("parseRecallOptions(%v) = %+v, want %+v", c.args, got, c.want)
		}
	}
}

func TestRecallFindsTheKeyInTheAgentArgvForm(t *testing.T) {
	for _, args := range [][]string{
		{"recall", "--key", pagingKey},
		{"--key", "<<ccr:" + pagingKey + ">>", "--offset", "0"},
		{"recall", pagingKey},
	} {
		if out := recallWith(t, "the original", args...); !strings.HasPrefix(out, "the original") {
			t.Errorf("%v → %q", args, out)
		}
	}
}
