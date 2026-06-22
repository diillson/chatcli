/*
 * ChatCLI - Reflexion wiring (Phase 4 of seven-pattern rollout).
 *
 * Builds the LLM and memory-persist callbacks the ReflexionHook needs,
 * plus the durable lesson queue (lessonq.Runner) used in enterprise
 * mode. Lives in cli/ so the quality package never imports cli.ChatCLI.
 *
 * Subcommands of /reflect:
 *   /reflect                       → show queue + DLQ status
 *   /reflect <text>                → persist a user-supplied lesson
 *   /reflect list                  → list pending + DLQ entries
 *   /reflect failed                → list DLQ entries with errors
 *   /reflect retry <id>            → move DLQ entry back to active queue
 *   /reflect purge <id>            → permanently delete a DLQ entry
 *   /reflect drain                 → force processing of pending queue
 */
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/agent/quality"
	"github.com/diillson/chatcli/cli/agent/quality/lessonq"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
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

		added := mgr.Facts.AddFactWithSource(lesson.FactContent(), "lesson", tags, mgr.WorkspaceDir())
		// Mirror genuinely new lessons into a human-readable LESSONS.md so
		// failures mined from sessions accumulate as durable, browsable
		// documentation (the agent can @read/@knowledge it too). Using the
		// dedup bool keeps the doc free of near-duplicate repeats.
		if added {
			cli.appendLessonDoc(mgr.WorkspaceDir(), lesson)
		}
		// AddFactWithSource never returns an error — it deduplicates
		// silently. We translate "false" (already existed) into nil
		// so the hook's logger doesn't spam on near-duplicate runs.
		return nil
	}
}

// lessonsDocName is the human-readable lessons file written under the
// reflexion base dir.
const lessonsDocName = "LESSONS.md"

// appendLessonDoc appends one lesson as a Markdown entry to
// <workspaceDir>/.chatcli/reflexion/LESSONS.md. Failures are logged at debug
// and never propagate — documentation is best-effort, it must not break the
// reflexion pipeline. Controlled by CHATCLI_QUALITY_REFLEXION_DOC (default on).
func (cli *ChatCLI) appendLessonDoc(workspaceDir string, lesson quality.Lesson) {
	if strings.EqualFold(strings.TrimSpace(utils.GetEnvOrDefault("CHATCLI_QUALITY_REFLEXION_DOC", "on")), "off") {
		return
	}
	dir := filepath.Join(workspaceDir, ".chatcli", "reflexion")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		cli.logger.Debug("reflexion: could not create lessons dir", zap.Error(err))
		return
	}
	path := filepath.Join(dir, lessonsDocName)

	when := lesson.CreatedAt
	if when.IsZero() {
		when = time.Now()
	}
	var b strings.Builder
	// Seed a title once when the file does not yet exist.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		b.WriteString("# Lessons learned\n\nAuto-generated from agent reflexion. Newest entries appended at the end.\n")
	}
	fmt.Fprintf(&b, "\n## %s — trigger: %s\n", when.Format(time.RFC3339), nonEmpty(lesson.Trigger, "manual"))
	if lesson.Situation != "" {
		fmt.Fprintf(&b, "- **Situation:** %s\n", lesson.Situation)
	}
	if lesson.Mistake != "" {
		fmt.Fprintf(&b, "- **Mistake:** %s\n", lesson.Mistake)
	}
	if lesson.Correction != "" {
		fmt.Fprintf(&b, "- **Correction:** %s\n", lesson.Correction)
	}
	if len(lesson.Tags) > 0 {
		fmt.Fprintf(&b, "- **Tags:** %s\n", strings.Join(lesson.Tags, ", "))
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- path under our own ~/.chatcli reflexion dir
	if err != nil {
		cli.logger.Debug("reflexion: could not open lessons doc", zap.Error(err))
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		cli.logger.Debug("reflexion: could not append lesson doc", zap.Error(err))
	}
}

// nonEmpty returns s, or def when s is blank.
func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// ensureReflexionRunner lazily constructs (and starts) the durable
// lesson queue when queue mode is enabled in qualityConfig. Returns
// nil when queue is disabled, memory is unavailable, or construction
// fails — callers then fall back to the legacy detached-goroutine
// path (hook handles that gracefully).
//
// Idempotent — safe to call multiple times; the second call is a
// no-op returning the cached runner.
func (cli *ChatCLI) ensureReflexionRunner(ctx context.Context, cfg quality.ReflexionQueueConfig) *lessonq.Runner {
	if cli == nil || !cfg.Enabled {
		return nil
	}
	cli.reflexionRunnerMu.Lock()
	defer cli.reflexionRunnerMu.Unlock()
	if cli.reflexionRunner != nil {
		return cli.reflexionRunner
	}

	baseDir := cfg.BaseDir
	if baseDir == "" {
		// Default layout: <workspace>/.chatcli/reflexion
		if cli.memoryStore == nil {
			return nil
		}
		mgr := cli.memoryStore.Manager()
		if mgr == nil || mgr.WorkspaceDir() == "" {
			return nil
		}
		baseDir = filepath.Join(mgr.WorkspaceDir(), ".chatcli", "reflexion")
	}

	policy := lessonq.OverflowBlock
	if cfg.OverflowDropOldest {
		policy = lessonq.OverflowDropOldest
	}

	rcfg := lessonq.RunnerConfig{
		BaseDir:             baseDir,
		Workers:             cfg.Workers,
		QueueCapacity:       cfg.Capacity,
		OverflowPolicy:      policy,
		EnqueueBlockTimeout: cfg.EnqueueBlockTimeout,
		Retry: lessonq.RetryPolicy{
			InitialDelay:   cfg.InitialDelay,
			MaxDelay:       cfg.MaxDelay,
			Multiplier:     2.0,
			JitterFraction: cfg.JitterFraction,
			MaxAttempts:    cfg.MaxAttempts,
		},
		PerJobTimeout: cfg.PerJobTimeout,
		StaleAfter:    cfg.StaleAfter,
	}
	runner, err := lessonq.NewRunner(rcfg, cli.logger)
	if err != nil {
		cli.logger.Warn("reflexion: failed to build durable runner, falling back to legacy mode",
			zap.Error(err))
		return nil
	}

	llm := cli.makeLessonLLM()
	persist := cli.makeLessonPersister()
	proc := lessonq.NewProcessor(llm, persist, lessonq.GetMetrics(), cli.logger)

	// The durable runner must outlive the triggering request, so detach
	// cancellation while inheriting context values.
	runnerCtx := context.WithoutCancel(ctx)
	if err := runner.Start(runnerCtx, proc); err != nil {
		cli.logger.Warn("reflexion: failed to start durable runner, falling back to legacy mode",
			zap.Error(err))
		runner.DrainAndShutdown(time.Second)
		return nil
	}
	// Replay pending WAL records from a previous session asynchronously
	// so we don't block agent startup on a potentially large drain.
	go func() {
		n, err := runner.Replay(runnerCtx)
		if err != nil {
			cli.logger.Warn("reflexion: replay failed", zap.Error(err))
			return
		}
		if n > 0 {
			cli.logger.Info("reflexion: replayed pending lessons from previous session",
				zap.Int("count", n))
		}
	}()
	cli.reflexionRunner = runner
	return runner
}

// reflexionEnqueuer returns the enqueuer to inject into the pipeline
// deps. Returns nil when queue is disabled or construction failed.
func (cli *ChatCLI) reflexionEnqueuer(ctx context.Context, cfg quality.ReflexionQueueConfig) quality.LessonEnqueuer {
	runner := cli.ensureReflexionRunner(ctx, cfg)
	if runner == nil {
		return nil
	}
	return runnerEnqueuerAdapter{runner: runner}
}

// runnerEnqueuerAdapter wraps a *lessonq.Runner as a
// quality.LessonEnqueuer. Translates between the two (near-identical)
// Enqueue signatures — quality.LessonRequest is just re-used.
type runnerEnqueuerAdapter struct{ runner *lessonq.Runner }

func (a runnerEnqueuerAdapter) Enqueue(ctx context.Context, req quality.LessonRequest) error {
	return a.runner.Enqueue(ctx, req)
}

// handleReflectCommand implements /reflect and its subcommands.
//
//	/reflect                       — show status summary (queue depth, DLQ size)
//	/reflect <free text>           — persist a user-supplied lesson directly
//	/reflect list                  — list active + DLQ entries
//	/reflect failed                — list DLQ entries with last errors
//	/reflect retry <id>            — requeue a DLQ entry
//	/reflect purge <id>            — permanently remove a DLQ entry
//	/reflect drain                 — force replay of any WAL-pending jobs
func (cli *ChatCLI) handleReflectCommand(ctx context.Context, userInput string) {
	rest := strings.TrimSpace(strings.TrimPrefix(userInput, "/reflect"))
	parts := strings.Fields(rest)

	// Zero-arg: status summary.
	if len(parts) == 0 {
		cli.reflectShowStatus()
		return
	}

	// Reserved subcommand verbs. A single token that matches here is
	// treated as a command; otherwise the whole rest is treated as
	// free-text lesson (backward compatible with the original /reflect
	// <text> behavior).
	switch strings.ToLower(parts[0]) {
	case "list":
		cli.reflectList()
		return
	case "failed":
		cli.reflectListFailed()
		return
	case "retry":
		if len(parts) < 2 {
			fmt.Println(colorize("  "+i18n.T("reflect.retry_usage"), ColorYellow))
			return
		}
		cli.reflectRetry(ctx, parts[1])
		return
	case "purge":
		if len(parts) < 2 {
			fmt.Println(colorize("  "+i18n.T("reflect.purge_usage"), ColorYellow))
			return
		}
		cli.reflectPurge(parts[1])
		return
	case "drain":
		cli.reflectDrain(ctx)
		return
	}

	// Free-text path — preserved from the original implementation.
	if cli.memoryStore == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.no_memory"), ColorYellow))
		return
	}
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

// reflectShowStatus prints queue depth, DLQ size, and a hint if the
// queue is disabled (so users know why there's no data).
func (cli *ChatCLI) reflectShowStatus() {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()

	if rnr == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.status_legacy"), ColorGray))
		fmt.Println(colorize("  "+i18n.T("reflect.armed_blank"), ColorGray))
		return
	}
	fmt.Println(colorize("  "+i18n.T("reflect.status_queue", rnr.QueueDepth()), ColorCyan))
	fmt.Println(colorize("  "+i18n.T("reflect.status_dlq", rnr.DLQCount()), ColorCyan))
	fmt.Println(colorize("  "+i18n.T("reflect.status_hint"), ColorGray))
}

func (cli *ChatCLI) reflectList() {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.queue_disabled"), ColorYellow))
		return
	}
	pending := rnr.PendingSnapshot()
	fmt.Println(colorize("  "+i18n.T("reflect.list_pending_header", len(pending)), ColorCyan))
	for _, j := range pending {
		fmt.Printf("    %s  [trigger=%s  attempts=%d  age=%s]\n",
			j.ID, j.Request.Trigger, j.Attempts, time.Since(j.EnqueuedAt).Truncate(time.Second))
		fmt.Printf("      %s: %s\n", i18n.T("reflect.field_task"), truncate(j.Request.Task, 100))
	}
	failed, err := rnr.DLQList()
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf("  ⚠  %s: %v", i18n.T("reflect.dlq_list_failed"), err), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("reflect.list_dlq_header", len(failed)), ColorCyan))
	for _, j := range failed {
		fmt.Printf("    %s  [trigger=%s  attempts=%d]\n", j.ID, j.Request.Trigger, j.Attempts)
		if j.LastError != "" {
			fmt.Printf("      %s: %s\n", i18n.T("reflect.field_error"), truncate(j.LastError, 160))
		}
	}
}

func (cli *ChatCLI) reflectListFailed() {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.queue_disabled"), ColorYellow))
		return
	}
	failed, err := rnr.DLQList()
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf("  ⚠  %s: %v", i18n.T("reflect.dlq_list_failed"), err), ColorYellow))
		return
	}
	if len(failed) == 0 {
		fmt.Println(colorize("  "+i18n.T("reflect.dlq_empty"), ColorGreen))
		return
	}
	fmt.Println(colorize("  "+i18n.T("reflect.list_dlq_header", len(failed)), ColorCyan))
	for _, j := range failed {
		fmt.Printf("    %s  [trigger=%s  attempts=%d  enqueued=%s]\n",
			j.ID, j.Request.Trigger, j.Attempts,
			j.EnqueuedAt.Format("2006-01-02 15:04"))
		fmt.Printf("      %s: %s\n", i18n.T("reflect.field_task"), truncate(j.Request.Task, 100))
		if j.LastError != "" {
			fmt.Printf("      %s: %s\n", i18n.T("reflect.field_error"), truncate(j.LastError, 240))
		}
	}
}

func (cli *ChatCLI) reflectRetry(ctx context.Context, id string) {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.queue_disabled"), ColorYellow))
		return
	}
	if err := rnr.DLQReplay(ctx, lessonq.JobID(id)); err != nil {
		fmt.Println(colorize(fmt.Sprintf("  ⚠  %s: %v", i18n.T("reflect.retry_failed"), err), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("reflect.retry_ok", id), ColorGreen))
}

func (cli *ChatCLI) reflectPurge(id string) {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.queue_disabled"), ColorYellow))
		return
	}
	if err := rnr.DLQPurge(lessonq.JobID(id)); err != nil {
		fmt.Println(colorize(fmt.Sprintf("  ⚠  %s: %v", i18n.T("reflect.purge_failed"), err), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("reflect.purge_ok", id), ColorGreen))
}

func (cli *ChatCLI) reflectDrain(ctx context.Context) {
	cli.reflexionRunnerMu.Lock()
	rnr := cli.reflexionRunner
	cli.reflexionRunnerMu.Unlock()
	if rnr == nil {
		fmt.Println(colorize("  "+i18n.T("reflect.queue_disabled"), ColorYellow))
		return
	}
	n, err := rnr.Replay(ctx)
	if err != nil {
		fmt.Println(colorize(fmt.Sprintf("  ⚠  %s: %v", i18n.T("reflect.drain_failed"), err), ColorYellow))
		return
	}
	fmt.Println(colorize("  "+i18n.T("reflect.drain_ok", n), ColorGreen))
}

// truncate clips s to n runes and appends "…" if truncation occurred.
// Used for pretty-printing /reflect list entries.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Compile-time guard: reflexion_setup.go uses the memory package only
// for its types here, so we verify the import path is legitimate via
// a no-op variable.
var _ = (*memory.Fact)(nil)
