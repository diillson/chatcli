package cli

import (
	"testing"
)

func TestParseMemoryResponse_BothSections(t *testing.T) {
	response := `## DAILY
- Read main.go (50 lines)
- Fixed bug in handler

## LONGTERM
- Project uses Go 1.25 with gRPC`

	daily, longTerm := parseMemoryResponse(response)

	if daily == "" {
		t.Error("expected daily content, got empty")
	}
	if longTerm == "" {
		t.Error("expected longterm content, got empty")
	}
	if daily != "- Read main.go (50 lines)\n- Fixed bug in handler" {
		t.Errorf("unexpected daily: %q", daily)
	}
	if longTerm != "- Project uses Go 1.25 with gRPC" {
		t.Errorf("unexpected longterm: %q", longTerm)
	}
}

func TestParseMemoryResponse_OnlyDaily(t *testing.T) {
	response := `## DAILY
- Worked on auth module`

	daily, longTerm := parseMemoryResponse(response)

	if daily == "" {
		t.Error("expected daily content")
	}
	if longTerm != "" {
		t.Errorf("expected empty longterm, got: %q", longTerm)
	}
}

func TestParseMemoryResponse_NothingNew(t *testing.T) {
	response := "NOTHING_NEW"

	daily, longTerm := parseMemoryResponse(response)

	// NOTHING_NEW without markers should be filtered from daily too
	if daily != "" {
		t.Errorf("expected empty daily (NOTHING_NEW filtered), got: %q", daily)
	}
	if longTerm != "" {
		t.Errorf("expected empty longterm, got: %q", longTerm)
	}
}

func TestParseMemoryResponse_NothingNewVariations(t *testing.T) {
	variations := []string{
		"NOTHING_NEW",
		"nothing_new",
		"Nothing_New",
		"  NOTHING_NEW  ",
		"NOTHING_NEW.",
		"Nothing new",
		"N/A",
		"None",
	}
	for _, v := range variations {
		daily, longTerm := parseMemoryResponse(v)
		if daily != "" {
			t.Errorf("parseMemoryResponse(%q): expected empty daily, got: %q", v, daily)
		}
		if longTerm != "" {
			t.Errorf("parseMemoryResponse(%q): expected empty longterm, got: %q", v, longTerm)
		}
	}
}

func TestIsNothingNew(t *testing.T) {
	positives := []string{"NOTHING_NEW", "nothing_new", "Nothing New", "NOTHING-NEW", "N/A", "None", "NA", "NOTHING_NEW.", " none "}
	for _, s := range positives {
		if !isNothingNew(s) {
			t.Errorf("isNothingNew(%q) should be true", s)
		}
	}

	negatives := []string{"- Read main.go", "Some actual content", "NOTHING_NEW\n- but also this", ""}
	for _, s := range negatives {
		if isNothingNew(s) {
			t.Errorf("isNothingNew(%q) should be false", s)
		}
	}
}

func TestParseMemoryResponse_LongtermNothingNew(t *testing.T) {
	response := `## DAILY
- Debug session for API

## LONGTERM
NOTHING_NEW`

	daily, longTerm := parseMemoryResponse(response)

	if daily == "" {
		t.Error("expected daily content")
	}
	// NOTHING_NEW in longterm should be filtered out
	if longTerm != "" {
		t.Errorf("expected empty longterm (NOTHING_NEW filtered), got: %q", longTerm)
	}
}

func TestParseMemoryResponse_CaseInsensitive(t *testing.T) {
	response := `## Daily
- Read config files

## Longterm
- Uses PostgreSQL 16`

	daily, longTerm := parseMemoryResponse(response)

	if daily == "" {
		t.Error("expected daily content with mixed-case header")
	}
	if longTerm == "" {
		t.Error("expected longterm content with mixed-case header")
	}
}

func TestParseMemoryResponse_NoSpaceAfterHash(t *testing.T) {
	response := `##DAILY
- Fixed the bug

##LONGTERM
- Pattern: always use context.WithTimeout`

	daily, longTerm := parseMemoryResponse(response)

	if daily == "" {
		t.Error("expected daily content with ##DAILY (no space)")
	}
	if longTerm == "" {
		t.Error("expected longterm content with ##LONGTERM (no space)")
	}
}

func TestParseFlexibleDate(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"2026-03-04", false},
		{"20260304", false},
		{"04/03/2026", false},
		{"yesterday", false},
		{"today", false},
		{"invalid", true},
	}

	for _, tt := range tests {
		_, err := parseFlexibleDate(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseFlexibleDate(%q): wantErr=%v, got err=%v", tt.input, tt.wantErr, err)
		}
	}
}
