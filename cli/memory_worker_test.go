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

	// Should be treated as daily (no markers)
	if daily != "NOTHING_NEW" {
		t.Errorf("unexpected daily: %q", daily)
	}
	if longTerm != "" {
		t.Errorf("expected empty longterm, got: %q", longTerm)
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
	if longTerm != "NOTHING_NEW" {
		t.Errorf("unexpected longterm: %q", longTerm)
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
