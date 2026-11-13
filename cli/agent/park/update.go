/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package park

import "sync"

// tokenLocks serializes the load-modify-save cycles on one snapshot inside
// this process. Save is atomic, but two cycles interleaved are not: the
// resume loaded the snapshot, a directive was appended and saved, and the
// resume's own save then put the old directive list back and dropped the
// new one — or the reverse, erasing LastResumeAt. Processes do not share
// these locks; a caller must not rewrite a snapshot another live process
// owns (the cli layer refuses that before getting here).
var tokenLocks sync.Map // token → *sync.Mutex

// lockToken takes the lock of token and returns its release.
func lockToken(token string) func() {
	mu, _ := tokenLocks.LoadOrStore(token, &sync.Mutex{})
	m, ok := mu.(*sync.Mutex)
	if !ok {
		return func() {}
	}
	m.Lock()
	return m.Unlock
}

// Update loads the snapshot of token, applies fn and saves it, as one step
// with respect to every other Update and AppendDirective of that token in
// this process. fn returning an error leaves the file untouched.
func Update(token string, fn func(*Snapshot) error) error {
	defer lockToken(token)()
	snap, err := Load(token)
	if err != nil {
		return err
	}
	if err := fn(snap); err != nil {
		return err
	}
	return snap.Save()
}
