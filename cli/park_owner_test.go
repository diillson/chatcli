//go:build !windows

/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
	"go.uber.org/zap"
)

// The real probe, not a stub: pid 1 belongs to another user for anyone but
// root, and a signal to it is refused — refused is alive. This process is
// alive; a child that exited and was reaped is not.
func TestParkOwnerProbeReadsARefusedSignalAsAlive(t *testing.T) {
	if !parkOwnerAlive(1) {
		t.Error("pid 1 is running: a permission error must not read as dead")
	}
	if !parkOwnerAlive(os.Getpid()) {
		t.Error("this process is running")
	}
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Skipf("no child to reap: %v", err)
	}
	if parkOwnerAlive(child.Process.Pid) || parkOwnerAlive(0) || parkOwnerAlive(-5) {
		t.Error("a reaped child, and a pid that names nothing, are not running")
	}
}

// A park owned by a live process of another user — the probe answers
// "refused" — is kept as waiting, through the real probe. And a snapshot
// recorded in another pid namespace (a container sharing the hostname and
// this directory) is never judged by a pid that means something else here.
func TestSweepKeepsAParkOwnedUnderAnotherUserOrNamespace(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root is refused nothing")
	}
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	cli := newCLIWithScheduler(t)
	cli.logger = zap.NewNop()
	save := func(pid int, ns string) string {
		snap := &park.Snapshot{Token: park.NewToken(), CreatedAt: time.Now().Add(-48 * time.Hour),
			OwnerPID: pid, OwnerHost: parkHostname(), OwnerPIDNS: ns,
			Park: park.Request{Mode: park.ModeDelay, Delay: time.Hour}}
		if err := snap.Save(); err != nil {
			t.Fatal(err)
		}
		return snap.Token
	}
	otherUser := save(1, parkPIDNamespace())
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Skipf("no child to reap: %v", err)
	}
	otherNS := save(child.Process.Pid, "pid:[some-other-namespace]")

	rep := cli.sweepDeadParks(0)
	if len(rep.removed) != 0 || sortedJoin(rep.live...) != otherUser || sortedJoin(rep.unknown...) != otherNS {
		t.Fatalf("live under another user = waiting; another pid namespace = unknown; got %+v", rep)
	}
}
