/*
 * ChatCLI - /schedule & /jobs completer i18n regression tests
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/cli/scheduler"
)

// TestSchedulerCompleterDescriptionsResolve guards the init-order bug: the
// suggestion builders must call i18n.T at call time (after Init), never bake
// the raw "sched.*" key at package-load time. TestMain forces CHATCLI_LANG=en
// and i18n.Init(), so every description must be resolved text.
func TestSchedulerCompleterDescriptionsResolve(t *testing.T) {
	groups := map[string][]prompt.Suggest{
		"scheduleStatusValues":      scheduleStatusValues(),
		"scheduleOwnerValues":       scheduleOwnerValues(),
		"scheduleOnTimeoutValues":   scheduleOnTimeoutValues(),
		"scheduleWhenHints":         scheduleWhenHints(),
		"scheduleUntilHints":        scheduleUntilHints(),
		"scheduleDoHints":           scheduleDoHints(),
		"jobsSubcommandSuggestions": jobsSubcommandSuggestions(),
		"jobsListFlagSuggestions":   jobsListFlagSuggestions(),
		"jobsClearFlagSuggestions":  jobsClearFlagSuggestions(),
		"jobsClearStatusValues":     jobsClearStatusValues(),
		"jobsDaemonSubcommands":     jobsDaemonSubcommands(),
	}
	for name, suggs := range groups {
		if len(suggs) == 0 {
			t.Errorf("%s: empty", name)
		}
		for _, s := range suggs {
			if strings.HasPrefix(s.Description, "sched.") {
				t.Errorf("%s: %q has an unresolved i18n key as description: %q", name, s.Text, s.Description)
			}
		}
	}
}

// TestSchedulerCompleterCancelledFromEnum pins that the StatusCancelled
// status/flag are derived from the canonical scheduler enum (the persisted
// JobStatus value), so completion always matches what the parser accepts.
func TestSchedulerCompleterCancelledFromEnum(t *testing.T) {
	want := string(scheduler.StatusCancelled)

	var inStatus, inClear bool
	for _, s := range scheduleStatusValues() {
		if s.Text == want {
			inStatus = true
		}
	}
	for _, s := range jobsClearFlagSuggestions() {
		if s.Text == "--"+want {
			inClear = true
		}
	}
	if !inStatus {
		t.Errorf("status completer missing %q", want)
	}
	if !inClear {
		t.Errorf("clear-flag completer missing %q", "--"+want)
	}
}
