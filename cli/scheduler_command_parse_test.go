/*
 * ChatCLI - scheduler_command parsing tests.
 *
 * Covers the regressions we fixed in April 2026:
 *
 *   1. tokenizeSchedulerInput must respect single- and double-quoted
 *      spans so /schedule --do "/run X Y" survives intact. The previous
 *      implementation used strings.Fields which truncated the value to
 *      "/run and silently leaked X Y as bare positionals.
 *
 *   2. parseScheduleArgs must surface unknown bare positionals instead
 *      of swallowing them. Silent drops mask quoting mistakes and
 *      produce confusing downstream errors like "action: cannot parse".
 */
package cli

import (
	"reflect"
	"strings"
	"testing"
)

func TestTokenizeSchedulerInput_RespectsDoubleQuotes(t *testing.T) {
	got, err := tokenizeSchedulerInput(` health-check --every 30s --do "/run healthcheck prod" --tag env=prod`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"health-check", "--every", "30s", "--do", "/run healthcheck prod", "--tag", "env=prod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestTokenizeSchedulerInput_RespectsSingleQuotes(t *testing.T) {
	got, err := tokenizeSchedulerInput(` deploy --when +0s --do 'shell: terraform apply -auto-approve'`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"deploy", "--when", "+0s", "--do", "shell: terraform apply -auto-approve"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestTokenizeSchedulerInput_BackslashEscapeInDoubleQuotes(t *testing.T) {
	got, err := tokenizeSchedulerInput(` foo --do "say \"hi\" twice"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"foo", "--do", `say "hi" twice`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens mismatch:\n got=%q\nwant=%q", got, want)
	}
}

func TestTokenizeSchedulerInput_UnterminatedQuoteIsErr(t *testing.T) {
	_, err := tokenizeSchedulerInput(` foo --do "/run X`)
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("expected unterminated-quote error, got %v", err)
	}
}

func TestTokenizeSchedulerInput_EmptyAndOnlySpaces(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n "} {
		got, err := tokenizeSchedulerInput(in)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", in, err)
		}
		if len(got) != 0 {
			t.Fatalf("expected zero tokens for %q, got %q", in, got)
		}
	}
}

// Doc example from features/scheduler.mdx — must round-trip cleanly
// through both the tokenizer and parseScheduleArgs.
func TestParseScheduleArgs_DocExample_HealthCheck(t *testing.T) {
	tokens, err := tokenizeSchedulerInput(` health-check --every 30s --do "/run healthcheck prod" --tag env=prod`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	in, err := parseScheduleArgs(tokens)
	if err != nil {
		t.Fatalf("parseScheduleArgs: %v", err)
	}
	if in.Name != "health-check" {
		t.Errorf("Name = %q, want health-check", in.Name)
	}
	if in.When != "every 30s" {
		t.Errorf("When = %q, want %q", in.When, "every 30s")
	}
	if in.Do != "/run healthcheck prod" {
		t.Errorf("Do = %q, want %q", in.Do, "/run healthcheck prod")
	}
	if in.Tags["env"] != "prod" {
		t.Errorf("Tags[env] = %q, want prod", in.Tags["env"])
	}
}

func TestParseScheduleArgs_DocExample_BackupCron(t *testing.T) {
	tokens, err := tokenizeSchedulerInput(` backup --cron "0 2 * * *" --do "/run backup --full"`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	in, err := parseScheduleArgs(tokens)
	if err != nil {
		t.Fatalf("parseScheduleArgs: %v", err)
	}
	if in.When != "cron:0 2 * * *" {
		t.Errorf("When = %q, want %q", in.When, "cron:0 2 * * *")
	}
	if in.Do != "/run backup --full" {
		t.Errorf("Do = %q, want %q", in.Do, "/run backup --full")
	}
}

func TestParseScheduleArgs_BarePositionalAfterNameErrors(t *testing.T) {
	// User forgot to quote the multi-word --do value. The old parser
	// silently dropped "healthcheck" and "prod" and left Do = "/run",
	// which then failed at a deeper layer. We want the error here.
	tokens, err := tokenizeSchedulerInput(` health-check --every 30s --do /run healthcheck prod`)
	if err != nil {
		t.Fatalf("tokenize: %v", err)
	}
	if _, err := parseScheduleArgs(tokens); err == nil {
		t.Fatal("expected error for unquoted multi-word --do value")
	} else if !strings.Contains(err.Error(), "positional") {
		t.Errorf("error message should mention positional, got: %v", err)
	}
}
