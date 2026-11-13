/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"testing"
	"time"

	"go.uber.org/zap"
)

// The sweep removes a park only on positive evidence that it is finished.
// Every way of not knowing — no hub bridge in this process, a mirror that
// is silent, slow or stale, a scheduler that never heard of the job, no
// scheduler at all in a daemon-connected process — keeps the snapshot.
func TestSweepKeepsWhatItCannotProveFinished(t *testing.T) {
	const old = 48 * time.Hour
	silent := &remoteParkProvider{owner: func(string) (remoteParkOwner, bool) { return remoteParkOwner{}, false }}
	for name, c := range map[string]struct {
		provider  *remoteParkProvider
		scheduler bool
		jobID     string
	}{
		"no hub bridge, job in another process":       {nil, true, "job-in-another-process"},
		"mirror silent or stale, job elsewhere":       {silent, true, "job-in-another-process"},
		"mirror silent, snapshot without a job":       {silent, true, ""},
		"daemon-connected process, no scheduler here": {nil, false, "job-in-the-daemon"},
		"daemon-connected, snapshot without a job":    {silent, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
			cli := &ChatCLI{logger: zap.NewNop()}
			if c.scheduler {
				cli = newCLIWithScheduler(t)
				cli.logger = zap.NewNop()
			}
			setRemoteParkProvider(c.provider)
			t.Cleanup(func() { setRemoteParkProvider(nil) })
			token := saveParkAged(t, old, c.jobID)
			if rep := cli.sweepDeadParks(0); len(rep.removed) != 0 || !parkOnDisk(token) {
				t.Fatalf("a park nobody proved finished was deleted: %+v", rep)
			}
		})
	}
}
