/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * wait.go — blocking on a background process: until its output matches a
 * pattern (a server's "listening on" line), until it exits, or until a
 * bound elapses. The agent used to poll logs turn after turn for the same
 * line; one wait does it in-process.
 */
package proc

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// waitPoll is how often the output is re-read while waiting.
const waitPoll = 200 * time.Millisecond

// WaitResult is the outcome of WaitFor.
type WaitResult struct {
	Matched  bool   // the pattern was found
	Line     string // the line that matched
	Exited   bool   // the process ended before (or without) a match
	TimedOut bool   // the bound elapsed first
	Info     Info
	Tail     string // the last lines of the output at the end of the wait
}

// WaitFor blocks until the process's combined output matches re, the
// process exits, timeout elapses or ctx ends. A match anywhere in the
// output counts, including lines written before the wait began: a server
// that announced its port a second ago is ready now.
func (s *Supervisor) WaitFor(ctx context.Context, id string, re *regexp.Regexp, timeout time.Duration) (WaitResult, error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok {
		defer s.mu.Unlock()
		return WaitResult{}, s.unknownIDErrLocked(id)
	}
	s.mu.Unlock()

	deadline := time.Now().Add(timeout)
	for {
		mp.outMu.Lock()
		out := string(mp.out)
		mp.outMu.Unlock()
		if re != nil {
			if line, found := firstMatchingLine(out, re); found {
				return s.waitResult(mp, out, WaitResult{Matched: true, Line: line}), nil
			}
		}
		select {
		case <-mp.done:
			// Output may have landed with the exit; look once more.
			mp.outMu.Lock()
			out = string(mp.out)
			mp.outMu.Unlock()
			if re != nil {
				if line, found := firstMatchingLine(out, re); found {
					return s.waitResult(mp, out, WaitResult{Matched: true, Line: line, Exited: true}), nil
				}
			}
			return s.waitResult(mp, out, WaitResult{Exited: true}), nil
		case <-ctx.Done():
			return s.waitResult(mp, out, WaitResult{TimedOut: true}), ctx.Err()
		case <-time.After(waitPoll):
		}
		if time.Now().After(deadline) {
			return s.waitResult(mp, out, WaitResult{TimedOut: true}), nil
		}
	}
}

// waitResult completes a WaitResult with the process info and output tail.
func (s *Supervisor) waitResult(mp *managedProcess, out string, r WaitResult) WaitResult {
	s.mu.Lock()
	r.Info = mp.info
	s.mu.Unlock()
	r.Tail = tailString(out, 20)
	return r
}

// firstMatchingLine returns the first line of out that re matches.
func firstMatchingLine(out string, re *regexp.Regexp) (string, bool) {
	for _, line := range strings.Split(out, "\n") {
		if re.MatchString(line) {
			return strings.TrimSpace(line), true
		}
	}
	return "", false
}

// LogsMatching returns the lines of the process's output that re matches
// (the last tailLines of them), with the count of matches and of lines.
func (s *Supervisor) LogsMatching(id string, re *regexp.Regexp, tailLines int) (lines []string, matched, total int, info Info, err error) {
	s.mu.Lock()
	mp, ok := s.procs[id]
	if !ok {
		defer s.mu.Unlock()
		return nil, 0, 0, Info{}, s.unknownIDErrLocked(id)
	}
	info = mp.info
	s.mu.Unlock()
	if tailLines <= 0 {
		tailLines = DefaultTailLines
	}
	if tailLines > MaxTailLines {
		tailLines = MaxTailLines
	}
	mp.outMu.Lock()
	out := strings.TrimRight(string(mp.out), "\n")
	mp.outMu.Unlock()
	if out == "" {
		return nil, 0, 0, info, nil
	}
	all := strings.Split(out, "\n")
	total = len(all)
	for _, l := range all {
		if re.MatchString(l) {
			lines = append(lines, l)
		}
	}
	matched = len(lines)
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	return lines, matched, total, info, nil
}
