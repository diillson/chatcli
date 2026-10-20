/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * Paging for @recall. An archived original can be far larger than one tool
 * result may be: the agent loop caps every result (CHATCLI_TOOL_RESULT_MAX_CHARS,
 * 20k characters by default) and keeps only a short head and tail of anything
 * bigger. A recall over that cap used to come back as that preview, so the
 * detail it was asked for was lost again. A recall now returns at most one
 * page, cut at a line break, with a footer naming the exact call for the next
 * page; "match" returns only the lines that contain a text, so the model can
 * find the part it needs without reading everything.
 */
package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/pkg/textfold"
)

// Model-facing layout of a paged recall.
const (
	recallPageFooterFmt   = "\n\n[@recall: characters %d-%d of %d. Next page: {\"key\":\"%s\",\"offset\":%d}. To find a part instead: {\"key\":\"%s\",\"match\":\"text\"}]"
	recallLastPageFmt     = "\n\n[@recall: characters %d-%d of %d, end of the original.]"
	recallPastEndFmt      = "[@recall: offset %d is past the end of the original (%d characters).]"
	recallMatchHeaderFmt  = "[@recall: %d of %d lines contain %q]\n"
	recallMatchLineFmt    = "L%d: %s\n"
	recallMatchMoreFmt    = "[@recall: %d more matching lines not shown; narrow the match, or page with {\"key\":\"%s\",\"offset\":N}]"
	recallMatchNoneFmt    = "[@recall: no line of the original (%d lines) contains %q]"
	recallSectionTrimFmt  = "\n[@recall: section cut at %d of %d characters; recall this key alone, with offset, to read on]"
	recallMatchLineMaxLen = 400
)

// recallPageMin keeps a page useful even under a tiny configured cap.
const recallPageMin = 2000

// recallPageHeadroom leaves room under the agent's per-result cap for the
// footer and the tool-output framing around the result.
const recallPageHeadroom = 2000

// recallDefaultResultMax mirrors the agent loop's default per-result cap.
const recallDefaultResultMax = 20000

// recallPageSize is the largest page a recall returns: the agent's
// per-result cap (CHATCLI_TOOL_RESULT_MAX_CHARS) minus headroom, so a page is
// never cut down to a preview on the way back.
func recallPageSize() int {
	limit := recallDefaultResultMax
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("CHATCLI_TOOL_RESULT_MAX_CHARS"))); err == nil && v > 0 {
		limit = v
	}
	if page := limit - recallPageHeadroom; page > recallPageMin {
		return page
	}
	return recallPageMin
}

// recallOptions is how much of an original a call asks for.
type recallOptions struct {
	offset   int    // first character to return
	maxChars int    // page size; 0 means the default
	match    string // return only the lines containing this (case-insensitive)
	paged    bool   // the call asked for a page explicitly
}

// parseRecallOptions reads offset, max_chars (or limit) and match (or grep)
// from a JSON envelope or the argv form the agent loop produces. Unknown or
// malformed values fall back to the defaults: a lenient parser never turns a
// recoverable recall into an error.
func parseRecallOptions(args []string) recallOptions {
	var o recallOptions
	payload := strings.TrimSpace(strings.Join(args, " "))
	if strings.HasPrefix(payload, "{") {
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(payload), &raw) == nil {
			if inner, ok := raw["args"]; ok {
				var nested map[string]json.RawMessage
				if json.Unmarshal(inner, &nested) == nil {
					for k, v := range nested {
						raw[k] = v
					}
				}
			}
			o.offset = lenientInt(raw["offset"])
			o.maxChars = lenientInt(firstRaw(raw, "max_chars", "limit"))
			o.match = rawString(firstRaw(raw, "match", "grep"))
			_, hasOffset := raw["offset"]
			o.paged = hasOffset || o.maxChars > 0
		}
	} else {
		flags, _, _ := splitFlatArgs(args)
		o.offset, _ = strconv.Atoi(flags["offset"])
		if v := firstNonEmptyStr(flags["max_chars"], flags["limit"]); v != "" {
			o.maxChars, _ = strconv.Atoi(v)
		}
		o.match = firstNonEmptyStr(flags["match"], flags["grep"])
		_, hasOffset := flags["offset"]
		o.paged = hasOffset || o.maxChars > 0
	}
	if o.offset < 0 {
		o.offset = 0
	}
	if o.maxChars < 0 {
		o.maxChars = 0
	}
	o.match = strings.TrimSpace(o.match)
	return o
}

func firstRaw(raw map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			return v
		}
	}
	return nil
}

func rawString(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return ""
}

// recallPage renders what one key returns under o. An original that fits
// one page, asked for without paging, comes back verbatim — the byte-exact
// restore that recall's callers rely on.
func recallPage(key, content string, o recallOptions) string {
	if o.match != "" {
		return recallMatch(key, content, o.match)
	}
	page := recallPageSize()
	if o.maxChars > 0 && o.maxChars < page {
		page = o.maxChars
	}
	total := len(content)
	if !o.paged && total <= page {
		return content
	}
	if o.offset >= total {
		return fmt.Sprintf(recallPastEndFmt, o.offset, total)
	}
	start := runeStart(content, o.offset)
	end := start + page
	if end >= total {
		return content[start:] + fmt.Sprintf(recallLastPageFmt, start, total, total)
	}
	end = lineCut(content, start, end)
	return content[start:end] + fmt.Sprintf(recallPageFooterFmt, start, end, total, key, end, key)
}

// recallMatch returns the lines of content containing match, numbered, up
// to one page.
func recallMatch(key, content, match string) string {
	lines := strings.Split(content, "\n")
	needle := strings.ToLower(match)
	page := recallPageSize()
	var b strings.Builder
	shown, total := 0, 0
	for i, line := range lines {
		if !strings.Contains(strings.ToLower(line), needle) {
			continue
		}
		total++
		if b.Len() >= page {
			continue
		}
		if len(line) > recallMatchLineMaxLen {
			line = textfold.Cut(line, runeStart(line, recallMatchLineMaxLen)) + "…"
		}
		fmt.Fprintf(&b, recallMatchLineFmt, i+1, line)
		shown++
	}
	if total == 0 {
		return fmt.Sprintf(recallMatchNoneFmt, len(lines), match)
	}
	out := fmt.Sprintf(recallMatchHeaderFmt, total, len(lines), match) + b.String()
	if total > shown {
		out += fmt.Sprintf(recallMatchMoreFmt, total-shown, key)
	}
	return strings.TrimRight(out, "\n")
}

// recallSection fits one key of a multi-key recall into its share of a page.
func recallSection(content string, share int) string {
	if len(content) <= share {
		return content
	}
	end := lineCut(content, 0, runeStart(content, share))
	return textfold.Cut(content, end) + fmt.Sprintf(recallSectionTrimFmt, end, len(content))
}

// runeStart moves i back to the start of the rune it falls in.
func runeStart(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// lineCut ends a page at the last line break in its final fifth, so a page
// does not stop mid-line; without one it cuts at end (on a rune boundary).
func lineCut(s string, start, end int) int {
	end = runeStart(s, end)
	floor := end - (end-start)/5
	if i := strings.LastIndexByte(s[floor:end], '\n'); i >= 0 {
		return floor + i + 1
	}
	return end
}
