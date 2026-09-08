/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package devincli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	i18n.Init()
	os.Exit(m.Run())
}

// fakeDevin writes a shell script that impersonates the devin binary. It
// records its argv and the prompt-file contents into the given dir, then
// prints the canned stdout. Skips on Windows (script-based fake).
func fakeDevin(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script-based fake devin binary is unix-only")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "devin")
	full := "#!/bin/sh\n" + script
	require.NoError(t, os.WriteFile(bin, []byte(full), 0o700))
	return bin
}

func TestSendPrompt_ExtractsSentinelReply(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	bin := fakeDevin(t, `
echo "$@" > `+record+`
# copy the prompt file (last value after --prompt-file) for inspection
prev=""
for a in "$@"; do
  if [ "$prev" = "--prompt-file" ]; then cp "$a" `+record+`.prompt; fi
  prev="$a"
done
printf 'devin harness banner\n<<<CHATCLI_REPLY_BEGIN>>>\nresposta final\n<<<CHATCLI_REPLY_END>>>\ntrailing chrome\n'
`)

	logger := zap.NewNop()
	c := NewClient(bin, "gpt-5.6-terra", logger, 1, 0)
	history := []models.Message{
		{Role: "system", Content: "Você é o ChatCLI."},
		{Role: "user", Content: "oi"},
		{Role: "assistant", Content: "olá!"},
	}
	got, err := c.SendPrompt(context.Background(), "qual é a boa?", history, 0)
	require.NoError(t, err)
	assert.Equal(t, "resposta final", got, "only the sentinel-framed reply must survive")

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	args := string(argv)
	assert.Contains(t, args, "-p", "must run in non-interactive print mode")
	assert.Contains(t, args, "--model gpt-5.6-terra")
	assert.Contains(t, args, "--permission-mode auto")
	assert.Contains(t, args, "--prompt-file")
	assert.NotContains(t, args, "--resume", "stateless per turn: session state must stay in ChatCLI")

	prompt, err := os.ReadFile(record + ".prompt")
	require.NoError(t, err)
	p := string(prompt)
	assert.Contains(t, p, "System: Você é o ChatCLI.")
	assert.Contains(t, p, "User: oi")
	assert.Contains(t, p, "Assistant: olá!")
	assert.Contains(t, p, "User: qual é a boa?")
	assert.Contains(t, p, replyBegin, "transport preamble must instruct the sentinel framing")
}

func TestSendPrompt_FallsBackToFullOutputWithoutSentinels(t *testing.T) {
	bin := fakeDevin(t, `printf '\033[1mplain\033[0m answer without markers\n'`)
	c := NewClient(bin, "swe-1.7", zap.NewNop(), 1, 0)
	got, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "plain answer without markers", got, "ANSI must be stripped and full output returned")
}

func TestSendPrompt_AuthErrorIsActionable(t *testing.T) {
	bin := fakeDevin(t, `echo "Not logged in." >&2; exit 1`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "devin auth login", "auth failures must tell the user exactly what to run")
}

func TestSendPrompt_ExecErrorCarriesStderrTail(t *testing.T) {
	bin := fakeDevin(t, `echo "boom: unknown flag" >&2; exit 2`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom: unknown flag")
}

func TestSendPrompt_EmptyOutputIsError(t *testing.T) {
	bin := fakeDevin(t, `exit 0`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.Error(t, err)
}

func TestSendPrompt_TimeoutKillsSubprocess(t *testing.T) {
	t.Setenv("DEVIN_CLI_TIMEOUT", "300ms")
	bin := fakeDevin(t, `sleep 5; echo late`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	start := time.Now()
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 3*time.Second, "the subprocess must die with the timeout, not linger")
}

func TestSendPrompt_EnvKnobsReachArgv(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	agentCfg := filepath.Join(t.TempDir(), "agent.json")
	require.NoError(t, os.WriteFile(agentCfg, []byte("{}"), 0o600))
	t.Setenv("DEVIN_CLI_PERMISSION_MODE", "accept-edits")
	t.Setenv("DEVIN_CLI_AGENT_CONFIG", agentCfg)
	t.Setenv("DEVIN_CLI_SANDBOX", "true")
	t.Setenv("DEVIN_CLI_EXTRA_ARGS", "--respect-workspace-trust false")

	bin := fakeDevin(t, `echo "$@" > `+record+`; echo ok`)
	c := NewClient(bin, "kimi-k2.7", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	args := string(argv)
	assert.Contains(t, args, "--permission-mode accept-edits")
	assert.Contains(t, args, "--agent-config "+agentCfg)
	assert.Contains(t, args, "--sandbox")
	assert.Contains(t, args, "--respect-workspace-trust false")
}

func TestResolveBinary_EnvOverrideAndMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only fake binary")
	}
	bin := fakeDevin(t, `echo ok`)
	t.Setenv("DEVIN_CLI_PATH", bin)
	got, err := ResolveBinary()
	require.NoError(t, err)
	assert.Equal(t, bin, got)

	t.Setenv("DEVIN_CLI_PATH", filepath.Join(t.TempDir(), "nope"))
	_, err = ResolveBinary()
	require.Error(t, err, "an explicit DEVIN_CLI_PATH that doesn't exist must fail loudly, not fall back")
}

// swapKnownInstallDirs substitutes the probe list for one test. Not parallel-
// safe, matching the t.Setenv-based tests around it.
func swapKnownInstallDirs(t *testing.T, dirs []string) {
	t.Helper()
	orig := knownInstallDirs
	knownInstallDirs = func() []string { return dirs }
	t.Cleanup(func() { knownInstallDirs = orig })
}

// TestResolveBinary_FallsBackToKnownInstallDirs pins the ACP/MCP scenario:
// the server inherits the minimal GUI-session PATH (no Homebrew/npm dirs), so
// the PATH lookup misses even though the CLI is installed — the well-known
// install dirs probe must still resolve it.
func TestResolveBinary_FallsBackToKnownInstallDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-only fake binary")
	}
	bin := fakeDevin(t, `echo ok`)
	t.Setenv("DEVIN_CLI_PATH", "")
	t.Setenv("PATH", t.TempDir())
	swapKnownInstallDirs(t, []string{"", filepath.Dir(bin)})

	got, err := ResolveBinary()
	require.NoError(t, err)
	assert.Equal(t, bin, got)
}

// TestResolveBinary_MissingEverywhere pins that a non-executable file in a
// known dir does not resolve and the original PATH lookup error surfaces.
func TestResolveBinary_MissingEverywhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	nonexec := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(nonexec, "devin"), []byte("data"), 0o600))
	t.Setenv("DEVIN_CLI_PATH", "")
	t.Setenv("PATH", t.TempDir())
	swapKnownInstallDirs(t, []string{nonexec, filepath.Join(t.TempDir(), "absent")})

	_, err := ResolveBinary()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PATH", "the PATH lookup error must surface, not a probe artifact")
}

func TestExtractReply_TakesLastSentinelPair(t *testing.T) {
	raw := "noise " + replyBegin + " first " + replyEnd + " middle " + replyBegin + "\nfinal answer\n" + replyEnd + " tail"
	assert.Equal(t, "final answer", extractReply(raw))
}

// TestExtractReply_NeverLeaksSentinels pins the anti-leak contract: when the
// model mangles the framing (end marker without a begin, begin fenced so it
// no longer matches, marker mid-text), no path may return a literal sentinel
// — a leaked marker enters the history and self-reinforces on every
// subsequent stateless turn.
func TestExtractReply_NeverLeaksSentinels(t *testing.T) {
	cases := map[string]string{
		"end without begin":     "the answer\n" + replyEnd,
		"begin fenced, end raw": "```\n" + replyBegin + "\n```\nthe answer\n" + replyEnd,
		"begin without end":     replyBegin + "\nthe answer",
		"doubled end marker":    replyBegin + "\nthe answer\n" + replyEnd + replyEnd,
		"marker mid-text":       replyBegin + "\nthe " + replyBegin + "answer\n" + replyEnd,
	}
	for name, raw := range cases {
		got := extractReply(raw)
		assert.NotContains(t, got, replyBegin, name)
		assert.NotContains(t, got, replyEnd, name)
		assert.Contains(t, got, "answer", name)
	}
}

// TestBuildConversation_ScrubsLeakedSentinelsFromHistory pins the
// decontamination contract: a sentinel that leaked into a stored assistant
// message (from a pre-fix turn or a mangled framing) must not be replayed in
// the flattened prompt, where the model would imitate it every turn after.
func TestBuildConversation_ScrubsLeakedSentinelsFromHistory(t *testing.T) {
	history := []models.Message{
		{Role: "assistant", Content: "previous reply\n" + replyEnd},
		{Role: "user", Content: "quoted " + replyBegin + " marker"},
	}
	flat := buildConversation(history, "next question")
	// Exactly the two occurrences from the transport preamble's framing
	// instruction — none from the replayed history.
	assert.Equal(t, 1, strings.Count(flat, replyBegin))
	assert.Equal(t, 1, strings.Count(flat, replyEnd))
	assert.Contains(t, flat, "Assistant: previous reply")
}

func TestBuildConversation_DoesNotDuplicateLastUserTurn(t *testing.T) {
	history := []models.Message{{Role: "user", Content: "same prompt"}}
	flat := buildConversation(history, "same prompt")
	assert.Equal(t, 1, strings.Count(flat, "User: same prompt"))
}

// TestSendPrompt_ToolProtocolSurvivesTransport pins the agent/coder parity
// contract: ChatCLI's textual tool-call markup must flow both ways intact —
// the tool catalog in system messages reaches the prompt file, the preamble
// explicitly defers to it, and markup emitted by the model comes back
// unmodified for the agent loop to parse.
func TestSendPrompt_ToolProtocolSurvivesTransport(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	toolCall := `<tool_call name="@coder" args='{"cmd":"read","args":{"file":"main.go"}}' />`
	bin := fakeDevin(t, `
prev=""
for a in "$@"; do
  if [ "$prev" = "--prompt-file" ]; then cp "$a" `+record+`.prompt; fi
  prev="$a"
done
cat <<'REPLY'
<<<CHATCLI_REPLY_BEGIN>>>
Vou ler o arquivo.
`+toolCall+`
<<<CHATCLI_REPLY_END>>>
REPLY
`)
	c := NewClient(bin, "gpt-5.6-sol", zap.NewNop(), 1, 0)
	history := []models.Message{
		{Role: "system", Content: "Ferramentas disponíveis:\n@coder — read/write/exec\nEmita <tool_call .../> para usar."},
		{Role: "user", Content: "leia o main.go"},
	}
	got, err := c.SendPrompt(context.Background(), "leia o main.go", history, 0)
	require.NoError(t, err)
	assert.Contains(t, got, toolCall, "tool-call markup must come back byte-identical for the agent loop")

	prompt, err := os.ReadFile(record + ".prompt")
	require.NoError(t, err)
	p := string(prompt)
	assert.Contains(t, p, "Ferramentas disponíveis", "tool catalog from system messages must reach the model")
	assert.Contains(t, p, "tool-call markup", "preamble must defer to ChatCLI's tool protocol")
	assert.NotContains(t, p, "do NOT use any tools", "the old blanket tool ban must be gone — it blinded the model to ChatCLI's catalog")
}

// TestTransportPreamble_NoIdentityCoercion pins the anti-refusal contract:
// the preamble must never assign the model an identity ("you are ChatCLI" /
// "you are the LLM backend") nor demand secrecy about the transport — Devin
// was observed refusing whole tasks rather than misrepresent who it is. The
// model keeps its own identity and simply cooperates through the protocol.
func TestTransportPreamble_NoIdentityCoercion(t *testing.T) {
	lower := strings.ToLower(transportPreamble)
	assert.NotContains(t, lower, "you are the llm backend", "must not assign an identity to the model")
	assert.NotContains(t, lower, "you are chatcli", "must not tell the model it IS ChatCLI")
	assert.NotContains(t, lower, "do not mention", "must not demand secrecy — it reads as deception and triggers refusals")
	assert.Contains(t, lower, "keep your own identity", "must explicitly release the model from any impersonation")
	assert.Contains(t, lower, "answer truthfully", "identity questions must be answerable honestly")
	assert.Contains(t, transportPreamble, replyBegin)
	assert.Contains(t, transportPreamble, replyEnd)
}

// TestSendPrompt_SerializesConcurrentInvocations pins the fix for the memory
// worker racing an agent turn: two in-process calls must never have their
// devin subprocesses alive at the same time (the CLI contends on per-user
// state). The fake binary errors if it ever observes an active peer.
func TestSendPrompt_SerializesConcurrentInvocations(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "overlap")
	bin := fakeDevin(t, `
if [ -e `+lock+` ]; then echo "concurrent invocation detected" >&2; exit 1; fi
touch `+lock+`
sleep 0.3
rm -f `+lock+`
echo serialized-ok
`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs, "serialized invocations must both succeed")
	}
}

// TestSendPrompt_QueuedCallerHonorsContext pins that a caller waiting for
// the serialization slot still respects its own deadline instead of hanging
// behind a long-running peer.
func TestSendPrompt_QueuedCallerHonorsContext(t *testing.T) {
	bin := fakeDevin(t, `sleep 2; echo done`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)

	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = c.SendPrompt(context.Background(), "long", nil, 0)
	}()
	<-started
	time.Sleep(100 * time.Millisecond) // let the first call take the slot

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.SendPrompt(ctx, "queued", nil, 0)
	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "queued caller must give up at its own deadline")
}

// TestSendPrompt_SanitizesInvalidUTF8 pins the fix for the memory worker's
// real-world failure: conversation segments carrying raw non-UTF-8 bytes
// (tool outputs, binary file reads) made the Rust CLI reject the prompt file
// with "stream did not contain valid UTF-8". The transport must coerce to
// valid UTF-8 exactly like encoding/json does for the HTTP providers.
func TestSendPrompt_SanitizesInvalidUTF8(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	bin := fakeDevin(t, `
prev=""
for a in "$@"; do
  if [ "$prev" = "--prompt-file" ]; then cp "$a" `+record+`.prompt; fi
  prev="$a"
done
echo ok
`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	history := []models.Message{
		{Role: "user", Content: "binário cru: \x80\xfe\xed segue \xc3"},
	}
	_, err := c.SendPrompt(context.Background(), "resuma", history, 0)
	require.NoError(t, err)

	prompt, err := os.ReadFile(record + ".prompt")
	require.NoError(t, err)
	assert.True(t, utf8.Valid(prompt), "prompt file must always be valid UTF-8 for the Rust CLI")
	assert.Contains(t, string(prompt), "binário cru", "surrounding valid text must survive sanitization")
}

// TestSendPrompt_WaivesWorkspaceTrustByDefault is the regression this flag
// exists for: from the 3000.6 CLI line onward, --print refuses to run in a
// directory it has not been told to trust, and every ChatCLI turn runs in a
// throwaway temp dir that can never be trusted. Without the waiver the whole
// provider stops answering.
func TestSendPrompt_WaivesWorkspaceTrustByDefault(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	// Impersonates a current CLI: refuses --print outside a trusted dir
	// unless the check is explicitly waived.
	bin := fakeDevin(t, `
echo "$@" > `+record+`
case " $* " in
  *" --respect-workspace-trust false "*) ;;
  *) echo "Error: workspace is not trusted; run devin in a trusted directory" >&2; exit 1;;
esac
printf '<<<CHATCLI_REPLY_BEGIN>>>\nok\n<<<CHATCLI_REPLY_END>>>\n'
`)
	c := NewClient(bin, "claude-sonnet-4.6", zap.NewNop(), 1, 0)
	got, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Contains(t, string(argv), "--respect-workspace-trust false")
}

// TestWorkspaceTrustArgs_HonorsTheOperator covers the two ways an operator
// overrides the waiver. Setting the env to true restores the CLI's own
// default; pinning the flag through DEVIN_CLI_EXTRA_ARGS must suppress ours
// entirely, because passing the same flag twice is a parse error.
func TestWorkspaceTrustArgs_HonorsTheOperator(t *testing.T) {
	t.Cleanup(func() { workspaceTrustUnsupported.Store(false) })

	t.Run("default waives the check", func(t *testing.T) {
		assert.Equal(t, []string{"--respect-workspace-trust", "false"}, workspaceTrustArgs())
	})

	t.Run("env true restores the CLI default", func(t *testing.T) {
		t.Setenv("DEVIN_CLI_RESPECT_WORKSPACE_TRUST", "true")
		assert.Equal(t, []string{"--respect-workspace-trust", "true"}, workspaceTrustArgs())
	})

	t.Run("env false is explicit but identical", func(t *testing.T) {
		t.Setenv("DEVIN_CLI_RESPECT_WORKSPACE_TRUST", "false")
		assert.Equal(t, []string{"--respect-workspace-trust", "false"}, workspaceTrustArgs())
	})

	t.Run("extra args pinning the flag wins outright", func(t *testing.T) {
		t.Setenv("DEVIN_CLI_EXTRA_ARGS", "--respect-workspace-trust true")
		assert.Nil(t, workspaceTrustArgs(), "ours must not be added a second time")
	})

	t.Run("a build that rejected it never sees it again", func(t *testing.T) {
		workspaceTrustUnsupported.Store(true)
		assert.Nil(t, workspaceTrustArgs())
	})
}

// TestSendPrompt_ArgvNeverRepeatsWorkspaceTrust guards the concrete parse
// error the dedupe prevents: clap rejects the flag given twice, so an
// operator who pinned it in DEVIN_CLI_EXTRA_ARGS would otherwise break every
// turn by following the docs.
func TestSendPrompt_ArgvNeverRepeatsWorkspaceTrust(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	t.Setenv("DEVIN_CLI_EXTRA_ARGS", "--respect-workspace-trust false")
	bin := fakeDevin(t, `echo "$@" > `+record+`; echo ok`)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	_, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)

	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(argv), "--respect-workspace-trust"),
		"the flag must appear exactly once")
}

// TestSendPrompt_OldCLIRejectingWorkspaceTrustIsRetriedWithoutIt keeps the
// provider working on a binary that predates the flag. Dropping it there is
// always safe: a build that cannot parse it also does not gate --print on
// workspace trust.
func TestSendPrompt_OldCLIRejectingWorkspaceTrustIsRetriedWithoutIt(t *testing.T) {
	t.Cleanup(func() { workspaceTrustUnsupported.Store(false) })
	record := filepath.Join(t.TempDir(), "argv")
	bin := fakeDevin(t, `
echo "$@" >> `+record+`
case " $* " in
  *" --respect-workspace-trust "*) echo "error: unexpected argument '--respect-workspace-trust' found" >&2; exit 2;;
esac
printf '<<<CHATCLI_REPLY_BEGIN>>>\nok\n<<<CHATCLI_REPLY_END>>>\n'
`)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	got, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.True(t, workspaceTrustUnsupported.Load(), "the rejection must latch for the process")

	lines := argvLines(t, record)
	require.Len(t, lines, 2, "one rejected attempt, one retry without the flag")
	assert.Contains(t, lines[0], "--respect-workspace-trust")
	assert.NotContains(t, lines[1], "--respect-workspace-trust")

	// Later turns skip it up front instead of paying for the rejection again.
	_, err = c.SendPrompt(context.Background(), "again", nil, 0)
	require.NoError(t, err)
	lines = argvLines(t, record)
	require.Len(t, lines, 3)
	assert.NotContains(t, lines[2], "--respect-workspace-trust")
}

// TestSendPrompt_BuildPredatingBothFlagsStillAnswers covers the oldest binary
// we still support. The CLI's parser names only ONE unexpected argument per
// run, so recovering needs a retry per flag — a single fallback would leave
// the turn failing on the second one.
func TestSendPrompt_BuildPredatingBothFlagsStillAnswers(t *testing.T) {
	t.Cleanup(func() {
		exportUnsupported.Store(false)
		workspaceTrustUnsupported.Store(false)
	})
	record := filepath.Join(t.TempDir(), "argv")
	bin := fakeDevin(t, `
echo "$@" >> `+record+`
case " $* " in
  *" --export "*) echo "error: unexpected argument '--export' found" >&2; exit 2;;
esac
case " $* " in
  *" --respect-workspace-trust "*) echo "error: unexpected argument '--respect-workspace-trust' found" >&2; exit 2;;
esac
printf '<<<CHATCLI_REPLY_BEGIN>>>\nok\n<<<CHATCLI_REPLY_END>>>\n'
`)
	c := NewClient(bin, "", zap.NewNop(), 1, 0)
	got, err := c.SendPrompt(context.Background(), "hi", nil, 0)
	require.NoError(t, err)
	assert.Equal(t, "ok", got)

	lines := argvLines(t, record)
	require.Len(t, lines, 3, "one attempt per rejected flag, then the clean run")
	assert.NotContains(t, lines[2], "--export")
	assert.NotContains(t, lines[2], "--respect-workspace-trust")
}

// TestFlagRejected_IsNarrow keeps the retry from firing on real failures. A
// trust check that RAN and said no names the flag in its hint, and retrying
// that would loop instead of surfacing the error.
func TestFlagRejected_IsNarrow(t *testing.T) {
	const flag = "--respect-workspace-trust"
	assert.True(t, flagRejected("error: unexpected argument '--respect-workspace-trust' found", flag))
	assert.True(t, flagRejected("Found argument '--respect-workspace-trust' which wasn't expected", flag))
	assert.False(t, flagRejected("Error: workspace is not trusted; pass --respect-workspace-trust false", flag),
		"a check that ran and refused is not a missing flag")
	assert.False(t, flagRejected("Error: Not logged in. Run `devin auth login`", flag))
	assert.False(t, flagRejected("error: unexpected argument '--export' found", flag),
		"a rejection naming another flag must not latch this one")
}

// argvLines reads the recorded invocations of the fake binary, one per line.
func argvLines(t *testing.T, record string) []string {
	t.Helper()
	argv, err := os.ReadFile(record)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(argv)), "\n")
}

// TestProviderSwitchThenTurn_KeepsTheWorkspaceTrustWaiver walks the exact
// sequence from the field: start elsewhere, switch to DEVIN — which lists the
// account's models first — then send a message. The listing used to poison a
// process-wide latch and strip the waiver from the turn that followed, so the
// first thing the user typed after switching died with "Refusing to run in an
// untrusted workspace" on a CLI that was perfectly capable of the flag.
func TestProviderSwitchThenTurn_KeepsTheWorkspaceTrustWaiver(t *testing.T) {
	t.Cleanup(func() { workspaceTrustUnsupported.Store(false) })
	workspaceTrustUnsupported.Store(false)
	record := filepath.Join(t.TempDir(), "argv")

	// The real 3000.6 CLI: `models list` rejects the flag outright, while
	// --print refuses to run without it.
	bin := fakeDevin(t, `
echo "$@" >> `+record+`
case "$1" in
  models)
    case " $* " in
      *" --respect-workspace-trust "*)
        echo "error: unexpected argument '--respect-workspace-trust' found" >&2; exit 2;;
    esac
    echo '{"models":[]}'; exit 0;;
esac
case " $* " in
  *" --respect-workspace-trust false "*) ;;
  *) echo "Error: Refusing to run in an untrusted workspace: /tmp/x" >&2; exit 1;;
esac
printf '<<<CHATCLI_REPLY_BEGIN>>>\nOK\n<<<CHATCLI_REPLY_END>>>\n'
`)
	c := NewClient(bin, "kimi-k3", zap.NewNop(), 1, 0)

	// The provider switch lists models…
	_, _ = c.ListModels(context.Background())
	// …and the very next message must still reach the model.
	got, err := c.SendPrompt(context.Background(), "oi", nil, 0)
	require.NoError(t, err, "the turn right after a provider switch must still carry the waiver")
	assert.Equal(t, "OK", got)

	lines := argvLines(t, record)
	require.GreaterOrEqual(t, len(lines), 2)
	assert.NotContains(t, lines[0], "--respect-workspace-trust", "the listing must not send it")
	assert.Contains(t, lines[len(lines)-1], "--respect-workspace-trust false", "the turn must send it")
}
