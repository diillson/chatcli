/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package park

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

func newTestSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	snap := &Snapshot{Token: NewToken(), Park: Request{Mode: ModeDelay}}
	if err := snap.Save(); err != nil {
		t.Fatal(err)
	}
	return snap
}

// Two saves of one token used the same "<file>.tmp": one could rename the
// other's half-written bytes into place. Each save now writes a temp file
// of its own — shown here by the old fixed name being unusable.
func TestSaveDoesNotShareATempFile(t *testing.T) {
	snap := newTestSnapshot(t)
	path, err := PathFor(snap.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := snap.Save(); err != nil {
		t.Fatalf("a save must not depend on the shared temp name: %v", err)
	}
	entries, err := os.ReadDir(func() string { d, _ := Dir(); return d }())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("a save must leave no temp file behind, directory has %d entries", len(entries))
	}
}

// Directive writes on one token are serialized: every one of a burst
// lands, and an update racing them (the resume stamping the snapshot)
// neither loses them nor is lost.
func TestDirectiveWritesOnOneTokenAreSerialized(t *testing.T) {
	snap := newTestSnapshot(t)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n+1)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- AppendDirective(snap.Token, fmt.Sprintf("note %d", i))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs <- Update(snap.Token, func(s *Snapshot) error {
			s.ResumeState = ResumeRunning
			return nil
		})
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(snap.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.PendingUserDirectives) != n || got.ResumeState != ResumeRunning {
		t.Fatalf("%d of %d directives survived, resume state %q", len(got.PendingUserDirectives), n, got.ResumeState)
	}
	if err := Update(snap.Token, func(*Snapshot) error { return os.ErrInvalid }); err == nil {
		t.Fatal("a failing update must report its error")
	}
	if err := Update("no-such-token-0000", func(*Snapshot) error { return nil }); err == nil {
		t.Fatal("updating a snapshot that does not exist must fail")
	}
}
