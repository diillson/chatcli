/*
 * ChatCLI - Pipeline state machine.
 *
 * The Pipeline has three observable states visible to callers:
 *
 *   Active    — Run() may be invoked; AddPre/AddPost register hooks.
 *               Registrations build a new immutable snapshot and swap
 *               it atomically so in-flight Run() calls never see a
 *               partial registration.
 *
 *   Draining  — Run() continues to execute in-flight calls but new
 *               Run() invocations return ErrPipelineDraining. Set by
 *               DrainAndClose to allow a graceful shutdown window.
 *
 *   Closed    — All Run() invocations return ErrPipelineClosed. In-
 *               flight calls are NOT canceled — the caller's ctx is
 *               the cancellation mechanism — but new work is rejected.
 *
 * The earlier "Setup" (can add hooks) vs "Running" (frozen) split that
 * a less-ambitious design might use is unnecessary here: because
 * registrations go through an atomic snapshot swap, there is no need
 * to freeze. Any thread can register at any time; in-flight Run()
 * calls still use the snapshot they grabbed on entry. This is the
 * cleaner COW / "read-mostly, write-rarely" enterprise pattern.
 */
package quality

import (
	"errors"
	"sync/atomic"
)

// ErrPipelineDraining is returned by Run when the Pipeline has been
// placed in Draining state by DrainAndClose. Callers should treat
// this as a graceful "shutting down" signal and fall through to a
// direct agent.Execute path if they have a legitimate request.
var ErrPipelineDraining = errors.New("quality pipeline: draining")

// ErrPipelineClosed is returned by Run after Close. The Pipeline is
// terminal — a new Pipeline must be constructed to accept work again.
var ErrPipelineClosed = errors.New("quality pipeline: closed")

// PipelineState is the externally observable state.
type PipelineState int32

const (
	StateActive PipelineState = iota
	StateDraining
	StateClosed
)

// String makes states log-friendly.
func (s PipelineState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateDraining:
		return "draining"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// stateMachine wraps an atomic state integer with CAS transitions.
// All transitions are logged and metric-emitted by the Pipeline;
// this type only enforces validity.
type stateMachine struct {
	val atomic.Int32
}

// Load returns the current state.
func (s *stateMachine) Load() PipelineState {
	return PipelineState(s.val.Load())
}

// transition moves from expected → target iff the current state is
// expected. Returns true on success. Used so concurrent callers can't
// both successfully drive a Close or a Drain.
func (s *stateMachine) transition(expected, target PipelineState) bool {
	return s.val.CompareAndSwap(int32(expected), int32(target))
}
