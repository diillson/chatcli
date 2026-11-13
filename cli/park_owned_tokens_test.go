/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

// The owner of a park keeps announcing it in the hub until its resume has
// completed: with the resume queued, and while it runs. It used to stop
// at the moment the job fired, which is exactly when another terminal
// looking for a live owner most needs to find one.
func TestOwnerKeepsAnnouncingAParkUntilItsResumeCompletes(t *testing.T) {
	cli := &ChatCLI{logger: zap.NewNop()}
	cli.registerActivePark("tok-waiting-00000001", "10:00:00")
	cli.registerActivePark("tok-queued-000000002", "10:00:00")
	cli.pendingResumeQueue = []string{"tok-queued-000000002"}
	// A resume in progress: unregistered when its run started, marked as
	// resuming until it ends.
	cli.markResuming("tok-resuming-0000003")
	owned := strings.Join(cli.ownedParkTokens(), ",")
	for _, token := range []string{"tok-waiting-00000001", "tok-queued-000000002", "tok-resuming-0000003"} {
		if !strings.Contains(owned, token) {
			t.Errorf("%s is still this process's park and must be announced; announced: %s", token, owned)
		}
	}
	cli.doneResuming("tok-resuming-0000003")
	if strings.Contains(strings.Join(cli.ownedParkTokens(), ","), "tok-resuming") {
		t.Error("a park whose resume ended is no longer announced")
	}
}
