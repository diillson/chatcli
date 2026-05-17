/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package llm

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPartialInputReader_EmitsCompleteFields verifies the happy path:
// a JSON object built up across multiple Feed calls eventually parses
// cleanly and surfaces every top-level string field exactly once.
func TestPartialInputReader_EmitsCompleteFields(t *testing.T) {
	r := NewPartialInputReader()
	var emitted []PartialInputField
	r.OnField = func(f PartialInputField) {
		emitted = append(emitted, f)
	}

	r.Feed(`{"query":"golang errg`)
	assert.Empty(t, emitted, "incomplete JSON must not emit yet")

	r.Feed(`roup"}`)
	require.Len(t, emitted, 1, "the closed string yields exactly one event")
	assert.Equal(t, "query", emitted[0].Name)
	assert.Equal(t, "golang errgroup", emitted[0].Value)
}

// TestPartialInputReader_DoesNotEmitTwice pins the dedup contract:
// once a field has been surfaced, subsequent re-parses (because more
// fragments arrived) must not re-emit it.
func TestPartialInputReader_DoesNotEmitTwice(t *testing.T) {
	r := NewPartialInputReader()
	emits := 0
	r.OnField = func(_ PartialInputField) { emits++ }

	r.Feed(`{"url":"https://example.com"}`)
	r.Feed("") // no-op
	r.Feed(`{"url":"https://example.com","extra":1}`)
	// The reader resets on each Feed by re-parsing the full buffer
	// — but the emitted map ensures one fire per field.
	assert.Equal(t, 1, emits, "the same field never re-emits")
}

// TestPartialInputReader_MultipleStringFields covers the common shape
// produced by @coder exec: cmd + cwd in one envelope.
func TestPartialInputReader_MultipleStringFields(t *testing.T) {
	r := NewPartialInputReader()
	var names []string
	r.OnField = func(f PartialInputField) { names = append(names, f.Name) }

	r.Feed(`{"cmd":"ls","cwd":"/tmp"}`)
	assert.ElementsMatch(t, []string{"cmd", "cwd"}, names)
}

// TestPartialInputReader_IgnoresNonStringFields documents that numbers,
// booleans, arrays, and nested objects are skipped — they don't fit
// the spinner UX and the per-provider streaming code can pull them
// after the full block lands.
func TestPartialInputReader_IgnoresNonStringFields(t *testing.T) {
	r := NewPartialInputReader()
	var seen []string
	r.OnField = func(f PartialInputField) { seen = append(seen, f.Name) }

	r.Feed(`{"query":"x","max_results":10,"flag":true,"items":[1,2],"nested":{"k":"v"}}`)
	assert.Equal(t, []string{"query"}, seen)
}

// TestPartialInputReader_FeedAfterClose is a no-op safety check:
// after Close, additional fragments are silently dropped rather than
// re-triggering emit. Prevents races where the SSE loop holds onto
// the reader past its lifetime.
func TestPartialInputReader_FeedAfterClose(t *testing.T) {
	r := NewPartialInputReader()
	emits := 0
	r.OnField = func(_ PartialInputField) { emits++ }

	r.Feed(`{"q":"a"}`)
	r.Close()
	r.Feed(`{"q":"b","other":"c"}`)
	assert.Equal(t, 1, emits, "post-Close Feeds are ignored")
}

// TestPartialInputReader_HonoursMaxBuffer guards against pathological
// streams (a misbehaving provider sending megabytes of partial JSON).
// We refuse to grow past the configured cap.
func TestPartialInputReader_HonoursMaxBuffer(t *testing.T) {
	// Cap chosen to fit the first object but reject the second.
	r := NewPartialInputReader().WithMaxBuffer(20)
	emits := 0
	r.OnField = func(_ PartialInputField) { emits++ }

	// 12 bytes — fits.
	r.Feed(`{"k":"abc"}`)
	// Push past the cap with a long second object.
	for i := 0; i < 100; i++ {
		r.Feed("xxxx")
	}
	// First object emits; subsequent oversized feeds are refused.
	assert.Equal(t, 1, emits)
	assert.LessOrEqual(t, len(r.Snapshot()), 20)
}

// TestPartialInputReader_FedConcurrentlyDoesNotRace exercises the
// mutex under a small fan-in scenario. We don't expect concurrent
// Feeds in production (a single SSE goroutine drives the reader), but
// the type is documented as goroutine-safe so we pin that contract.
func TestPartialInputReader_FedConcurrentlyDoesNotRace(t *testing.T) {
	r := NewPartialInputReader()
	r.OnField = func(_ PartialInputField) {}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Feed(`{"a":"b"}`)
		}()
	}
	wg.Wait()
}

// TestPartialInputReader_NoFieldCallbackIsNoOp confirms the reader
// tolerates being used as a pure buffer (caller pulls Snapshot/Close
// when it's ready and parses itself).
func TestPartialInputReader_NoFieldCallbackIsNoOp(t *testing.T) {
	r := NewPartialInputReader()
	r.Feed(`{"q":"abc"}`)
	final := r.Close()
	assert.Equal(t, `{"q":"abc"}`, final)
}

// TestPartialInputReader_NonObjectIsIgnored makes sure a streaming
// payload that doesn't start with { (an array or scalar) does not
// produce events. Tool inputs are always objects; anything else is
// almost certainly a bug upstream.
func TestPartialInputReader_NonObjectIsIgnored(t *testing.T) {
	r := NewPartialInputReader()
	emits := 0
	r.OnField = func(_ PartialInputField) { emits++ }
	r.Feed(`[1,2,3]`)
	r.Feed(`"just a string"`)
	r.Feed(`42`)
	assert.Equal(t, 0, emits)
}

// TestPartialInputReader_Snapshot lets diagnostics peek at the
// internal buffer without disturbing the streaming state.
func TestPartialInputReader_Snapshot(t *testing.T) {
	r := NewPartialInputReader()
	r.Feed(`{"k":`)
	assert.Equal(t, `{"k":`, r.Snapshot())
	r.Feed(`"v"}`)
	assert.Equal(t, `{"k":"v"}`, r.Snapshot())
}
