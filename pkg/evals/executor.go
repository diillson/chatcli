/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package evals

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
)

// Request is one candidate run.
type Request struct {
	Mode    string
	Prompt  string
	Workdir string
	Env     map[string]string
	Timeout time.Duration
}

// Execution is what one candidate run produced.
type Execution struct {
	Final    string
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	Record   *Record
}

// Executor runs the candidate. BinaryExecutor drives the real chatcli
// binary; tests inject fakes.
type Executor interface {
	Execute(ctx context.Context, req Request) (*Execution, error)
}

// hermeticEnv switches off every subsystem that reads or writes the user's
// personal state, so a case's outcome depends on the suite alone: long-term
// memory and its bootstrap, memory and session recall, session autosave,
// coder checkpoints, REPL history and the update check.
var hermeticEnv = map[string]string{
	"CHATCLI_MEMORY_ENABLED":        "false",
	"CHATCLI_BOOTSTRAP_ENABLED":     "false",
	"CHATCLI_MEMORY_AUTORECALL":     "false",
	"CHATCLI_SESSION_AUTORECALL":    "false",
	"CHATCLI_SESSION_AUTOSAVE":      "false",
	"CHATCLI_CODER_CHECKPOINTS":     "off",
	"CHATCLI_DISABLE_HISTORY":       "true",
	"CHATCLI_DISABLE_VERSION_CHECK": "true",
}

// alwaysEnv applies even with WithMemory: none of it changes what the model
// knows, all of it would leave eval debris in the user's state.
var alwaysEnv = map[string]string{
	"CHATCLI_SESSION_AUTOSAVE":      "false",
	"CHATCLI_CODER_CHECKPOINTS":     "off",
	"CHATCLI_DISABLE_HISTORY":       "true",
	"CHATCLI_DISABLE_VERSION_CHECK": "true",
}

// BinaryExecutor runs `chatcli -p` as a subprocess inside the sandbox.
type BinaryExecutor struct {
	Bin        string // chatcli binary; the harness passes its own executable
	Provider   string // candidate provider override ("" = the binary's default)
	Model      string // candidate model override
	WithMemory bool   // evaluate with the user's real memory and recall
	Dotenv     string // environment file the parent resolved (the sandbox has none)
}

// BaseEnv returns the environment overrides every candidate run gets.
func (b *BinaryExecutor) BaseEnv() map[string]string {
	src := hermeticEnv
	if b.WithMemory {
		src = alwaysEnv
	}
	env := make(map[string]string, len(src)+1)
	for k, v := range src {
		env[k] = v
	}
	if b.Dotenv != "" {
		env["CHATCLI_DOTENV"] = b.Dotenv
	}
	return env
}

// Execute runs one case prompt and collects the answer and its Record.
func (b *BinaryExecutor) Execute(ctx context.Context, req Request) (*Execution, error) {
	recDir, err := os.MkdirTemp("", "chatcli-eval-rec-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(recDir) }()
	recPath := filepath.Join(recDir, "record.json")

	prompt := req.Prompt
	if req.Mode == ModeCoder {
		prompt = "/coder " + prompt
	}
	// The binary's own --timeout bounds the turn; the context kill is the
	// backstop for a process that ignores it.
	args := []string{"-p", prompt, "--raw", "--no-anim", "--timeout", req.Timeout.String()}
	if b.Provider != "" {
		args = append(args, "--provider", b.Provider)
	}
	if b.Model != "" {
		args = append(args, "--model", b.Model)
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Timeout+30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, b.Bin, args...) //#nosec G204 -- the harness runs its own binary with suite-authored prompts
	cmd.Dir = req.Workdir
	env := b.BaseEnv()
	for k, v := range req.Env {
		env[k] = v
	}
	env[RecordEnv] = recPath
	cmd.Env = mergedEnviron(env)
	// No stdin: the one-shot must not read a prompt from it, and a coder
	// confirmation prompt reads EOF and is denied rather than hanging.
	cmd.Stdin = nil
	var stdout, stderr limitedBuffer
	stdout.max, stderr.max = 4<<20, 1<<20
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	start := time.Now()
	runErr := cmd.Run()
	ex := &Execution{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}
	if rec, err := ReadRecord(recPath); err == nil {
		ex.Record = rec
		ex.Final = rec.Final
	} else {
		ex.Final = strings.TrimSpace(ex.Stdout)
	}

	if runCtx.Err() == context.DeadlineExceeded {
		return ex, errors.New(i18n.T("evals.run.timeout", req.Timeout.String()))
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			ex.ExitCode = ee.ExitCode()
			msg := tail(ex.Stderr, 800)
			if ex.Record != nil && ex.Record.Error != "" {
				msg = ex.Record.Error
			}
			return ex, errors.New(i18n.T("evals.run.exit", ex.ExitCode, msg))
		}
		return ex, runErr
	}
	if ex.Record != nil && ex.Record.Error != "" {
		return ex, errors.New(ex.Record.Error)
	}
	return ex, nil
}
