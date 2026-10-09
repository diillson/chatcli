/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/pkg/evals"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// evalMockLLM plays three roles on one OpenAI-compatible endpoint: the chat
// candidate, the coder candidate (one write tool call, then a final answer)
// and the judge.
func evalMockLLM(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var coderTurns, judgeCalls atomic.Int32
	reply := func(w http.ResponseWriter, content string) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
		})
		_, _ = w.Write(b)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := string(body)
		switch {
		case strings.Contains(s, "impartial evaluator"):
			judgeCalls.Add(1)
			reply(w, `{"score": 0.9, "reasoning": "states the answer"}`)
		case strings.Contains(s, "EVAL_CODER_CASE"):
			if coderTurns.Add(1) == 1 {
				// base64("written by coder")
				// The coder contract: a <plan> block before any tool call
				// (a bare tool call is bounced as a FORMAT ERROR).
				reply(w, "<plan>\n1. write out.txt\n</plan>\n"+`<tool_call name="@coder" args='{"cmd":"write","args":{"file":"out.txt","content":"d3JpdHRlbiBieSBjb2Rlcg==","encoding":"base64"}}' />`)
				return
			}
			reply(w, "Done: out.txt was written.")
		case strings.Contains(s, "EVAL_CHAT_CASE"):
			reply(w, "The answer is 42.")
		default:
			reply(w, "ok")
		}
	}))
	return srv, &judgeCalls
}

func TestE2E_EvalRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("suite uses a POSIX command check")
	}
	auth.InvalidateCache()
	srv, judgeCalls := evalMockLLM(t)
	defer srv.Close()

	dir := t.TempDir()
	suite := `
name: e2e
judge: {provider: OPENAI, model: judge-model}
defaults:
  timeout: 2m
cases:
  - id: chat-answer
    prompt: "EVAL_CHAT_CASE what is six times seven?"
    checks:
      - contains: "42"
      - not_contains: "As an AI"
      - judge: {rubric: "Says the answer is 42.", threshold: 0.7}
  - id: coder-write
    mode: coder
    prompt: "EVAL_CODER_CASE create out.txt"
    checks:
      - file_contains: {path: out.txt, text: "written by coder"}
      - tool_called: "@coder write"
      - command: {run: "test -s out.txt"}
      - max_turns: 5
  - id: chat-wrong
    prompt: "EVAL_CHAT_CASE again"
    checks:
      - contains: "43"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "suite.yaml"), []byte(suite), 0o600))

	// A user hook on every coder tool call. Inside an eval candidate it must
	// never fire: it is a side effect on the user's machine, and a hook that
	// runs an eval would recurse. HOME is the package's isolated home.
	hookMarker := filepath.Join(t.TempDir(), "hook-fired")
	hooksDir := filepath.Join(os.Getenv("HOME"), ".chatcli")
	require.NoError(t, os.MkdirAll(hooksDir, 0o700))
	hooksFile := filepath.Join(hooksDir, "hooks.json")
	require.NoError(t, os.WriteFile(hooksFile, []byte(`{"hooks":[{"name":"marker","event":"PostToolUse","type":"command","command":"touch '`+hookMarker+`'"}]}`), 0o600))
	t.Cleanup(func() { _ = os.Remove(hooksFile) })
	report := filepath.Join(dir, "report.json")
	md := filepath.Join(dir, "report.md")

	env := append(os.Environ(),
		"OPENAI_API_KEY=test-key",
		"LLM_PROVIDER=OPENAI",
		"OPENAI_API_URL="+srv.URL+"/v1/chat/completions",
		"OPENAI_MODEL=candidate-model",
		"OPENAI_USE_RESPONSES=false",
		"CHATCLI_AUTH_DIR="+t.TempDir(),
		"CHATCLI_LANG=en",
	)
	run := func(args ...string) (int, string, string) {
		cmd := exec.Command(chatcliBinary, append([]string{"eval"}, args...)...)
		cmd.Env = env
		var out, errb strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("eval did not run: %v", err)
		}
		return code, out.String(), errb.String()
	}

	code, stdout, stderr := run("run", filepath.Join(dir, "suite.yaml"), "--concurrency", "1", "--out", report, "--markdown", md)
	// chat-wrong fails by design: pass rate 2/3 < the default 1.0 → exit 1.
	require.Equal(t, 1, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)

	rep, err := evals.ReadReport(report)
	require.NoError(t, err)
	byID := map[string]evals.CaseResult{}
	for _, c := range rep.Cases {
		byID[c.ID] = c
	}
	require.Equal(t, evals.StatusPass, byID["chat-answer"].Status, "%+v", byID["chat-answer"].Trials)
	require.Equal(t, evals.StatusPass, byID["coder-write"].Status, "%+v", byID["coder-write"].Trials)
	assert.Equal(t, evals.StatusFail, byID["chat-wrong"].Status)

	_, hookErr := os.Stat(hookMarker)
	assert.True(t, os.IsNotExist(hookErr), "a user hook fired inside an eval candidate")

	coder := byID["coder-write"].Trials[0]
	assert.Contains(t, coder.ToolCalls, "@coder")
	assert.Contains(t, coder.Final, "out.txt was written")
	assert.Greater(t, coder.InputTokens, int64(0), "usage reaches the record")
	assert.Equal(t, "OPENAI", coder.Provider)

	assert.Equal(t, int32(1), judgeCalls.Load(), "one judge check, one sample")
	require.NotNil(t, rep.Judge)
	assert.Equal(t, "judge-model", rep.Judge.Model)
	assert.False(t, rep.SelfJudged)

	mdBody, err := os.ReadFile(md)
	require.NoError(t, err)
	assert.Contains(t, string(mdBody), "`e2e/chat-wrong`")
	assert.Contains(t, stdout, "e2e/chat-wrong")

	// The same run gated against itself as a baseline, at a pass-rate floor
	// it meets: no regression, exit 0.
	code, stdout, stderr = run("run", filepath.Join(dir, "suite.yaml"), "--filter", "chat-*", "--baseline", report, "--min-pass-rate", "0.5", "--quiet")
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", stdout, stderr)

	// validate/list need no provider and spend nothing.
	code, stdout, _ = run("validate", filepath.Join(dir, "suite.yaml"))
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "3 case(s)")

	code, _, _ = run("compare", report, report)
	require.Equal(t, 0, code)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("cases:\n  - {id: a, prompt: x}\n"), 0o600))
	code, _, stderr = run("validate", filepath.Join(dir, "bad.yaml"))
	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "no checks")
}
