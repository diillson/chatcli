/*
 * ChatCLI - Reflexion wiring (Phase 4 of seven-pattern rollout).
 *
 * Builds the LLM and memory-persist callbacks the ReflexionHook needs.
 * Lives in cli/ so the quality package never imports cli.ChatCLI.
 */
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
)

// makeLessonLLM returns a LessonLLM closure that delegates to the
// active LLM client. nil when no client is wired (caller must
// nil-check; quality.BuildPipeline does).
func (cli *ChatCLI) makeLessonLLM() quality.LessonLLM {
	if cli == nil || cli.Client == nil {
		return nil
	}
	return func(ctx context.Context, history []models.Message) (string, error) {
		// We pull the user message out of history and pass it as the
		// prompt parameter so providers that distinguish system from
		// user content keep both pieces straight.
		var userPrompt string
		var systemAndPrior []models.Message
		for _, m := range history {
			if m.Role == "user" {
				userPrompt = m.Content
				continue
			}
			systemAndPrior = append(systemAndPrior, m)
		}
		return cli.Client.SendPrompt(ctx, userPrompt, systemAndPrior, 600)
	}
}

// makeLessonPersister returns a PersistLessonFunc that writes lessons
// into the long-term memory.Fact index. nil when memory is unavailable
// — the hook then degrades to a no-op.
func (cli *ChatCLI) makeLessonPersister() quality.PersistLessonFunc {
	if cli == nil || cli.memoryStore == nil {
		return nil
	}
	mgr := cli.memoryStore.Manager()
	if mgr == nil {
		return nil
	}
	return func(_ context.Context, lesson quality.Lesson) error {
		// Tags include the trigger so /memory and /config can
		// filter lessons by why they were generated.
		tags := append([]string{}, lesson.Tags...)
		tags = append(tags, "reflexion", "trigger:"+lesson.Trigger)

		mgr.Facts.AddFactWithSource(lesson.FactContent(), "lesson", tags, mgr.WorkspaceDir())
		// AddFactWithSource never returns an error — it deduplicates
		// silently. We translate "false" (already existed) into nil
		// so the hook's logger doesn't spam on near-duplicate runs.
		return nil
	}
}

// handleReflectCommand implements /reflect [task...]. Without args
// this prints the current reflexion state. With args, the command
// drops a synthetic "manual reflexion" task into history so the
// next user turn picks it up; the hook then sees the manual trigger
// flag set on the result and generates a lesson.
//
// For now /reflect arms a one-shot flag (cli.pendingReflexion). The
// Refiner/Verifier path consumes it transparently. Future evolution
// can pipe a free-text "lesson seed" through the same channel.
func (cli *ChatCLI) handleReflectCommand(userInput string) {
	rest := strings.TrimSpace(strings.TrimPrefix(userInput, "/reflect"))
	if rest == "" {
		fmt.Println(colorize("  "+i18n.T("reflect.armed_blank"), ColorGray))
		return
	}
	if cli.memoryStore == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.no_memory"), ColorYellow))
		return
	}
	// Synthesize a Lesson directly from the user's free-text and
	// persist it. This is the cheapest path: the user is telling us
	// the lesson; no LLM call needed.
	mgr := cli.memoryStore.Manager()
	tags := []string{"reflexion", "trigger:manual", "user-supplied"}
	lesson := quality.Lesson{
		Situation:  rest,
		Mistake:    i18n.T("reflect.mistake_user_supplied"),
		Correction: rest,
		Tags:       tags,
		Trigger:    "manual",
	}
	mgr.Facts.AddFactWithSource(lesson.FactContent(), "lesson", tags, mgr.WorkspaceDir())
	fmt.Println(colorize("  "+i18n.T("reflect.persisted"), ColorGreen))
}

// Compile-time guard: reflexion_setup.go uses the memory package only
// for its types here, so we verify the import path is legitimate via
// a no-op variable.
var _ = (*memory.Fact)(nil)
