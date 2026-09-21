/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// connEnds turns the process-wide bus on and returns a collector of the
// connection end events seen so far.
func connEnds(t *testing.T) func(n int) []pulse.Event {
	t.Helper()
	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })
	var seen []pulse.Event
	return func(n int) []pulse.Event {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for len(seen) < n {
			select {
			case ev := <-ch:
				if ev.Kind == pulse.KindConn && ev.Phase == pulse.PhaseEnd {
					seen = append(seen, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of %d connection end events: %+v", len(seen), n, seen)
			}
		}
		return seen
	}
}

// A self-update downloads the checksums and then the release binary. Both
// show as connections, the second with the real size of the download, which
// is counted while it is read and never buffered.
func TestSelfUpdateDownloadsShowAsConnections(t *testing.T) {
	asset, err := AssetName()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	collect := connEnds(t)
	const newBinary = "new binary v2 with some length to it"
	newReleaseServer(t, "v2.0.0", fmt.Sprintf("%s  %s\n", sha256Hex(newBinary), asset), map[string]string{asset: newBinary})
	target := filepath.Join(t.TempDir(), "chatcli")
	if err := os.WriteFile(target, []byte("old binary v1"), 0o755); err != nil { // #nosec G306 -- test binary
		t.Fatal(err)
	}
	if err := SelfReplace(context.Background(), target, "v2.0.0"); err != nil {
		t.Fatalf("SelfReplace: %v", err)
	}

	ends := collect(2)
	sawBinary := false
	for _, end := range ends {
		if end.Name != "127.0.0.1" || end.Attrs["status"] != "200" {
			t.Fatalf("end = %+v", end)
		}
		sawBinary = sawBinary || end.Attrs["resp_bytes"] == strconv.Itoa(len(newBinary))
	}
	if !sawBinary {
		t.Fatalf("no connection reported the real size of the binary (%d): %+v", len(newBinary), ends)
	}
}
