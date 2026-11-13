/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"github.com/diillson/chatcli/cli/plugins"
	"go.uber.org/zap"
)

// Two parks fire while the terminal is busy; the drain takes both off the
// queue and resumes the first. A prune issued from inside that resumed run
// (the model's @park prune, or /parked prune typed mid-run) must leave
// both snapshots alone: the first is being resumed, the second is what the
// drain loads next. Both jobs are terminal by then — they fired.
func TestPruneDuringADrainKeepsTheParksBeingResumed(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	first := saveParkAged(t, time.Hour, enqueueParkJob(t, cli, true))
	second := saveParkAged(t, time.Hour, enqueueParkJob(t, cli, true))
	for _, token := range []string{first, second} {
		cli.registerActivePark(token, "10:00:00")
	}
	cli.pendingResumeQueue = []string{first, second}
	plugins.SetParkCanceller(newLiveParkAdapter(cli))
	t.Cleanup(func() { plugins.SetParkCanceller(nil) })

	var resumed []string
	var pruneOut string
	cli.resumeRun = func(ctx context.Context, snap *park.Snapshot, _, _ string) error {
		resumed = append(resumed, snap.Token)
		if snap.Token != first {
			return nil
		}
		// Both ways a prune reaches the sweep mid-run.
		out, err := plugins.NewBuiltinParkPlugin().Execute(ctx, []string{"prune"})
		if err != nil {
			return err
		}
		pruneOut = out
		cli.parkPrune()
		if !parkOnDisk(first) || !parkOnDisk(second) {
			return errors.New("the prune deleted a park the drain still needs")
		}
		return nil
	}
	_ = captureStdout(t, func() { cli.drainPendingResumes(context.Background()) })

	if strings.Join(resumed, ",") != first+","+second {
		t.Fatalf("both parks must resume, in order; resumed %v", resumed)
	}
	if !strings.HasPrefix(pruneOut, "pruned 0 ") || !strings.Contains(pruneOut, parkShort(first)) || !strings.Contains(pruneOut, parkShort(second)) {
		t.Fatalf("the prune must name both as kept: %q", pruneOut)
	}
	// Once the drain is over nothing is held back: a finished park whose
	// job is terminal is dead weight again.
	if rep := cli.sweepDeadParks(0); len(rep.removed) != 2 || len(rep.live) != 0 {
		t.Fatalf("after the drain both snapshots are prunable: %+v", rep)
	}
}
