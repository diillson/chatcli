/*
 * Tests the autocomplete for /jobs subcommands and flags. The user
 * report ("não esqueça dos completer comando, subcomando, e flags")
 * gated this in — the new /jobs clear|clean|prune subcommand and its
 * flag set must surface in the dropdown so the experience matches the
 * existing /jobs list completion.
 *
 * go-prompt's Document holds cursorPosition as an unexported field,
 * so we drive completer tests through Buffer.InsertText (the public
 * API) which advances the cursor as text is appended — exactly the
 * shape the live REPL produces.
 */
package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	prompt "github.com/c-bata/go-prompt"
)

// docFor returns a Document whose cursor is at the end of the given
// text — what go-prompt sees when the user has typed `text` and the
// completer fires on each keystroke.
func docFor(text string) prompt.Document {
	b := prompt.NewBuffer()
	b.InsertText(text, false, true)
	return *b.Document()
}

func suggestTexts(suggs []prompt.Suggest) []string {
	out := make([]string, 0, len(suggs))
	for _, s := range suggs {
		out = append(out, s.Text)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// TestJobsCompleter_SubcommandIncludesClear: typing "/jobs " should
// surface clear/clean/prune alongside the existing subcommands.
func TestJobsCompleter_SubcommandIncludesClear(t *testing.T) {
	cli := &ChatCLI{}
	got := suggestTexts(cli.getJobsSuggestions(docFor("/jobs ")))
	for _, sub := range []string{"clear", "clean", "prune"} {
		if !contains(got, sub) {
			t.Errorf("subcommand %q missing from /jobs completer; got %v", sub, got)
		}
	}
}

// TestJobsCompleter_ClearFlags: after typing "/jobs clear ", the
// dropdown must list every flag jobsClear accepts. The user can then
// tab to pick the right one without consulting docs.
func TestJobsCompleter_ClearFlags(t *testing.T) {
	cli := &ChatCLI{}
	for _, sub := range []string{"clear", "clean", "prune"} {
		got := suggestTexts(cli.getJobsSuggestions(docFor("/jobs " + sub + " ")))
		want := []string{
			"--failed", "--succeeded", "--cancelled", "--timed-out",
			"--status", "--older-than", "--name", "--mine", "--yes",
		}
		for _, flag := range want {
			if !contains(got, flag) {
				t.Errorf("/jobs %s completer missing flag %q; got %v", sub, flag, got)
			}
		}
	}
}

// TestJobsCompleter_ClearStatusValues: after "/jobs clear --status ",
// the dropdown must show only TERMINAL status values — pruning
// non-terminal jobs is not allowed by Prune anyway, so suggesting
// pending/running/etc. would just frustrate the user.
func TestJobsCompleter_ClearStatusValues(t *testing.T) {
	cli := &ChatCLI{}
	got := suggestTexts(cli.getJobsSuggestions(docFor("/jobs clear --status ")))

	for _, s := range []string{"completed", "failed", "cancelled", "timed_out", "skipped"} {
		if !contains(got, s) {
			t.Errorf("missing terminal status %q in --status completer; got %v", s, got)
		}
	}
	for _, s := range []string{"pending", "blocked", "waiting", "running", "paused"} {
		if contains(got, s) {
			t.Errorf("non-terminal status %q surfaced in clear --status completer (Prune ignores active jobs); got %v", s, got)
		}
	}
}

// TestJobsCompleter_ClearFlagPrefixFilter: typing "/jobs clear --f"
// should narrow the dropdown to flags beginning with --f (--failed).
// Smoke test that prefix filtering still works after our additions.
func TestJobsCompleter_ClearFlagPrefixFilter(t *testing.T) {
	cli := &ChatCLI{}
	got := suggestTexts(cli.getJobsSuggestions(docFor("/jobs clear --f")))
	if !contains(got, "--failed") {
		t.Errorf("prefix filter dropped --failed; got %v", got)
	}
	for _, unwanted := range []string{"--mine", "--yes", "--name"} {
		if contains(got, unwanted) {
			t.Errorf("prefix filter leaked %q after --f; got %v", unwanted, got)
		}
	}
}

// TestJobsCompleter_ListStatusUnchanged: regression guard — the
// existing /jobs list --status completer still surfaces every status
// (terminal + active), since List can legitimately filter on either.
// We don't want the clear additions to reuse the same vocabulary by
// accident.
func TestJobsCompleter_ListStatusUnchanged(t *testing.T) {
	cli := &ChatCLI{}
	got := suggestTexts(cli.getJobsSuggestions(docFor("/jobs list --status ")))
	for _, s := range []string{"pending", "running", "completed", "failed"} {
		if !contains(got, s) {
			t.Errorf("/jobs list --status completer missing %q; got %v", s, got)
		}
	}
}

// TestJobsCompleter_ClearAfterFlagWithValue: ensure that after a
// --status <value> is consumed, the next tab returns to the flag
// list (not value list) — i.e., the lastFlag tracking is wired right.
func TestJobsCompleter_ClearAfterFlagWithValue(t *testing.T) {
	cli := &ChatCLI{}
	got := suggestTexts(cli.getJobsSuggestions(docFor("/jobs clear --status failed ")))
	if !contains(got, "--mine") || !contains(got, "--yes") {
		t.Errorf("after consumed --status value, expected flags again; got %v", got)
	}
	if contains(got, "completed") || contains(got, "cancelled") {
		t.Errorf("status values leaked after --status was consumed; got %v", got)
	}
}

// TestI18nKeys_PresentInBundle reads the locale JSONs directly and
// asserts each clear-related key has a translation. We can't rely on
// i18n.T at test time (the package vars in scheduler_completer.go
// snapshot whatever T returns at init — typically the raw key, since
// i18n.Init runs from main(). That's a separate UX wart; this test
// confirms at minimum that the bundle has the entries so localization
// works once Init has run.
func TestI18nKeys_PresentInBundle(t *testing.T) {
	keys := []string{
		"sched.jobs.sub.clear",
		"sched.jobs.clear.flag.failed",
		"sched.jobs.clear.flag.succeeded",
		"sched.jobs.clear.flag.cancelled",
		"sched.jobs.clear.flag.timed_out",
		"sched.jobs.clear.flag.status",
		"sched.jobs.clear.flag.older_than",
		"sched.jobs.clear.flag.name",
		"sched.jobs.clear.flag.mine",
		"sched.jobs.clear.flag.yes",
	}
	for _, locale := range []string{"pt-BR", "en-US", "en"} {
		path := "../i18n/locales/" + locale + ".json"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var bundle map[string]string
		if err := json.Unmarshal(data, &bundle); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, k := range keys {
			v, ok := bundle[k]
			if !ok {
				t.Errorf("locale %q missing key %q", locale, k)
				continue
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("locale %q has empty value for key %q", locale, k)
			}
		}
	}
}
