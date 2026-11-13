/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package coder

import (
	"bufio"
	"context"
	"os"
	"strings"
	"time"
)

// maxInvalidSecurityAnswers is how many unreadable answers a security
// prompt takes before it gives up and denies. An answer the prompt does
// not understand is asked again rather than taken as a denial: a slip of
// the keys used to cost the user the action (the model was told "denied"
// and moved on).
const maxInvalidSecurityAnswers = 3

// parseSecurityAnswer maps what the user typed to a decision. ok is false
// when the text is none of the answers the prompt offers. A command with
// no rule pattern (isExec) is asked yes/no only.
func parseSecurityAnswer(input string, isExec bool) (decision SecurityDecision, ok bool) {
	switch strings.TrimSpace(strings.ToLower(input)) {
	case "", "y", "yes", "s", "sim":
		return DecisionRunOnce, true
	case "n", "no", "nao", "não":
		return DecisionDenyOnce, true
	}
	if isExec {
		return DecisionDenyOnce, false
	}
	switch strings.TrimSpace(strings.ToLower(input)) {
	case "a", "always":
		return DecisionAllowAlways, true
	case "d", "deny":
		return DecisionDenyForever, true
	}
	return DecisionDenyOnce, false
}

// securityLine is one read of the user's answer.
type securityLine struct {
	text     string
	canceled bool // the context ended first
	closed   bool // the input ended (EOF, channel closed): nobody can answer
}

// readSecurityLine reads one answer from the centralized stdin channel
// when there is one, else from stdin itself.
func readSecurityLine(ctx context.Context, inputCh <-chan string) securityLine {
	if inputCh != nil {
		select {
		case <-ctx.Done():
			return securityLine{canceled: true}
		case line, open := <-inputCh:
			return securityLine{text: line, closed: !open}
		}
	}
	type scanned struct {
		text string
		ok   bool
	}
	resultChan := make(chan scanned, 1)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		ok := scanner.Scan()
		resultChan <- scanned{text: scanner.Text(), ok: ok}
	}()
	select {
	case <-ctx.Done():
		_ = os.Stdin.SetReadDeadline(time.Now())
		return securityLine{canceled: true}
	case r := <-resultChan:
		return securityLine{text: r.text, closed: !r.ok}
	}
}

// promptDraftHooks set the user's half-typed line aside while a security
// prompt is open; see SetSecurityPromptDraftParker.
var promptDraftParker func() (restore func())

// SetSecurityPromptDraftParker installs the hook that sets the user's
// half-typed line aside for as long as a security prompt is open, and
// returns it afterwards. The agent loop reads stdin through one reader
// that keeps the line being typed; without the hook, whatever the user
// had typed before the prompt appeared was glued to the front of the
// answer ("refactor the y" instead of "y") and the prompt denied the
// action. Pass nil to detach.
func SetSecurityPromptDraftParker(fn func() (restore func())) {
	promptSalvageMu.Lock()
	promptDraftParker = fn
	promptSalvageMu.Unlock()
}

// parkPromptDraft runs the installed hook, if any, and returns its restore
// function (never nil).
func parkPromptDraft() func() {
	promptSalvageMu.RLock()
	parker := promptDraftParker
	promptSalvageMu.RUnlock()
	if parker == nil {
		return func() {}
	}
	if restore := parker(); restore != nil {
		return restore
	}
	return func() {}
}
