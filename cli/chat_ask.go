/*
 * ChatCLI - Chat-mode controlled exception for ask_user.
 * Copyright (c) 2024 Edilson Freitas. License: Apache-2.0.
 *
 * Chat mode is tool-less by design. The ONE sanctioned exception: chat may use
 * EXACTLY ask_user (no exec/file/search tools). Native providers use a buffered
 * SendPromptWithTools turn; non-native ones (Claude OAuth) use a buffered XML
 * turn with the @ask format injected and the markup suppressed. In both cases
 * the turn is BUFFERED and the text RETURNED — handleChatTurnResult renders it;
 * printing here (after the alt-screen overlay) is what made the answer vanish.
 */
package cli

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/diillson/chatcli/cli/agent"
	"github.com/diillson/chatcli/cli/agent/ask"
	"github.com/diillson/chatcli/cli/agent/workers"
	"github.com/diillson/chatcli/cli/palette"
	"github.com/diillson/chatcli/i18n"
	client "github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"golang.org/x/term"
)

// chatAskEnvVar is the env knob backing the chat-mode ask_user exception. It is
// read live on every turn (so /config chat ask on|off flips it at runtime) and
// can also be set in .env. Default ON.
const chatAskEnvVar = "CHATCLI_CHAT_ASK"

// chatAskEnabled reports whether the chat-mode ask_user exception is on.
// Default is ON when the env var is unset.
func chatAskEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(chatAskEnvVar))) {
	case "false", "0", "off", "no":
		return false
	case "true", "1", "on", "yes":
		return true
	default:
		return true // default ON
	}
}

// maybeChatAskTurn runs the chat ask_user exception when it is enabled. It
// returns (response, handled, error): when handled is false the caller proceeds
// with the normal streaming/buffered path.
func (cli *ChatCLI) maybeChatAskTurn(
	ctx context.Context,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	effectiveMaxTokens int,
	resolution SkillClientResolution,
	stopSpinner func(),
) (string, bool, error) {
	if !chatAskEnabled() {
		return "", false, nil
	}
	// Native tool-use providers: buffered decision turn offering only ask_user.
	if tac, ok := client.AsToolAware(activeClient); ok && tac.SupportsNativeTools() {
		out, err := cli.executeChatAskNative(ctx, tac, activeClient, userInput, additionalContext,
			tempHistory, effectiveMaxTokens, resolution, stopSpinner)
		return out, true, err
	}
	// Providers without native tools (e.g. Claude in OAuth mode): XML transport.
	out, err := cli.executeChatAskXML(ctx, activeClient, userInput, additionalContext,
		tempHistory, effectiveMaxTokens, stopSpinner)
	return out, true, err
}

// finishSpinner stops the thinking animation and the prefix spinner and resets
// the prompt — the same teardown the normal chat paths run before returning, so
// handleChatTurnResult renders the envelope cleanly.
func (cli *ChatCLI) finishSpinner(stopSpinner func()) {
	cli.animation.StopThinkingAnimation()
	stopSpinner()
	cli.interactionState = StateNormal
	cli.forceRefreshPrompt()
	time.Sleep(50 * time.Millisecond)
}

// executeChatAskNative runs the buffered decision turn for native tool-use
// providers, offering ONLY ask_user.
func (cli *ChatCLI) executeChatAskNative(
	ctx context.Context,
	tac client.ToolAwareClient,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	effectiveMaxTokens int,
	resolution SkillClientResolution,
	stopSpinner func(),
) (string, error) {
	tools := []models.ToolDefinition{workers.AskUserToolDefinition()}
	prompt := userInput + additionalContext

	resp, err := tac.SendPromptWithTools(ctx, prompt, tempHistory, tools, effectiveMaxTokens)
	if err != nil && cli.refreshClientOnAuthError(err) {
		resp, err = tac.SendPromptWithTools(ctx, prompt, tempHistory, tools, effectiveMaxTokens)
	}
	if err != nil {
		cli.finishSpinner(stopSpinner)
		return "", err
	}
	if resp != nil && resp.Usage != nil && cli.costTracker != nil {
		cli.costTracker.RecordRealUsage(resolution.Provider, resolution.Model, resp.Usage)
	}

	var askArgs string
	if resp != nil {
		for _, tc := range resp.ToolCalls {
			if isAskToolName(tc.Name) {
				askArgs = tc.ArgumentsJSON()
				break
			}
		}
	}

	// No ask: the buffered content is the answer.
	if askArgs == "" {
		cli.finishSpinner(stopSpinner)
		if resp != nil {
			return resp.Content, nil
		}
		return "", nil
	}

	// Ask: stop the spinner, render the overlay, then buffered follow-up.
	cli.finishSpinner(stopSpinner)
	result := cli.runChatAsk(ctx, askArgs)
	return cli.chatAskFollowup(ctx, activeClient, userInput, additionalContext, tempHistory, result, effectiveMaxTokens)
}

// executeChatAskXML runs the buffered decision turn for providers WITHOUT native
// tools, using an injected XML @ask format. Buffered so the raw <tool_call>
// markup never streams to the screen.
func (cli *ChatCLI) executeChatAskXML(
	ctx context.Context,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	effectiveMaxTokens int,
	stopSpinner func(),
) (string, error) {
	prompt := userInput + additionalContext + chatAskXMLInstruction()

	resp, err := activeClient.SendPrompt(ctx, prompt, tempHistory, effectiveMaxTokens)
	if cli.refreshClientOnAuthError(err) {
		resp, err = activeClient.SendPrompt(ctx, prompt, tempHistory, effectiveMaxTokens)
	}
	if err != nil {
		cli.finishSpinner(stopSpinner)
		return "", err
	}

	calls, _ := agent.ParseToolCalls(resp)
	var askArgs string
	for _, tc := range calls {
		if isAskToolName(tc.Name) {
			askArgs = tc.Args
			break
		}
	}

	// No ask: the buffered text is the answer, minus any stray tool-call markup.
	if askArgs == "" {
		cli.finishSpinner(stopSpinner)
		clean := resp
		for _, tc := range calls {
			if tc.Raw != "" {
				clean = strings.ReplaceAll(clean, tc.Raw, "")
			}
		}
		return strings.TrimSpace(clean), nil
	}

	cli.finishSpinner(stopSpinner)
	result := cli.runChatAsk(ctx, askArgs)
	return cli.chatAskFollowup(ctx, activeClient, userInput, additionalContext, tempHistory, result, effectiveMaxTokens)
}

// runChatAsk renders the overlay for the parsed args and returns the formatted
// tool result. Falls back to the non-interactive result when there is no TTY.
func (cli *ChatCLI) runChatAsk(ctx context.Context, argsJSON string) string {
	qs, err := ask.ParseRequest(argsJSON)
	if err != nil {
		return ask.ErrorResult(err)
	}
	fd := int(os.Stdin.Fd())
	if cli.unattended || !term.IsTerminal(fd) {
		return ask.FallbackResult(qs)
	}
	// Chat has no centralized stdin reader (that is agent-mode only); go-prompt
	// is idle inside the executor callback here. Snapshot/restore cooked mode to
	// be safe against a dirty raw-mode exit.
	state, _ := term.GetState(fd)
	answers, canceled, runErr := palette.RunAsk(ctx, palette.NewAsk(qs))
	if state != nil {
		_ = term.Restore(fd, state)
	}
	if runErr != nil {
		return ask.ErrorResult(runErr)
	}
	if canceled {
		return ask.CanceledResult()
	}
	return ask.FormatResult(answers)
}

// chatAskFollowup produces the final answer with the user's selections injected
// as a fresh prompt. It is BUFFERED and returns the text — the caller's
// handleChatTurnResult renders it. It reconstructs a clean text conversation
// (original user turn) so the model has full context.
func (cli *ChatCLI) chatAskFollowup(
	ctx context.Context,
	activeClient client.LLMClient,
	userInput, additionalContext string,
	tempHistory []models.Message,
	toolResult string,
	effectiveMaxTokens int,
) (string, error) {
	follow := make([]models.Message, 0, len(tempHistory)+1)
	follow = append(follow, tempHistory...)
	follow = append(follow, models.Message{Role: "user", Content: userInput + additionalContext})
	prompt := "ask_user result:\n" + toolResult + "\n\n" + i18n.T("ask.chat.continue")

	// Restart the SAME prompt-prefix spinner the normal turn uses for the
	// follow-up wait. ShowThinkingAnimation is suppressed during
	// processLLMRequest, and the original prefix spinner was stopped for the
	// overlay — so we drive it directly: StateProcessing makes livePrefix render
	// the braille glyph, runPrefixSpinner ticks the redraw, both undone after.
	cli.interactionState = StateProcessing
	cli.isExecuting.Store(true)
	spinnerDone := make(chan struct{})
	if runtime.GOOS != "windows" {
		go cli.runPrefixSpinner(spinnerDone)
	}

	resp, err := activeClient.SendPrompt(ctx, prompt, follow, effectiveMaxTokens)
	if cli.refreshClientOnAuthError(err) {
		resp, err = activeClient.SendPrompt(ctx, prompt, follow, effectiveMaxTokens)
	}

	close(spinnerDone)
	atomic.StoreInt32(&cli.prefixSpinnerIdx, 0)
	cli.interactionState = StateNormal
	cli.forceRefreshPrompt()
	return resp, err
}

// chatAskXMLInstruction is appended to the decision-turn prompt for non-native
// providers. It overrides the "chat is tool-less" framing for THIS turn and
// pins the exact tool-call format the XML parser expects.
func chatAskXMLInstruction() string {
	return "\n\n[Chat exception — ask_user is ENABLED for this turn]\n" +
		"You normally have no tools in chat, but for THIS turn you MAY ask the user a multiple-choice question, " +
		"and it WILL be executed (this overrides the usual tool-less rule). " +
		"If — and only if — you need the user to choose before answering, reply with EXACTLY this single tag and nothing else:\n" +
		`<tool_call name="@ask" args='{"questions":[{"header":"<short label>","question":"<full question>","multiSelect":false,"options":[{"label":"<option>","description":"<why>"}]}]}' />` + "\n" +
		"Set multiSelect to true when the user may pick more than one. Up to 6 questions per call; the user can also type a free-text \"Other\". " +
		"Emit ONLY the tag, with no prose before or after it. You will then receive the user's selections to continue. " +
		"If you do NOT need to ask, just answer normally."
}

// isAskToolName matches the tool name in either its plugin form (@ask) or its
// native form (ask_user), case-insensitively.
func isAskToolName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "@ask" || n == "ask_user" || n == "ask"
}
