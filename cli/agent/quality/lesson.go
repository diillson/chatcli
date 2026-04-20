/*
 * ChatCLI - Reflexion lesson generator (Phase 4).
 *
 * A Lesson is a structured "what should we remember from this attempt"
 * record. Reflexion runs after a quality trigger (error, hallucination,
 * low refine score) and persists the lesson into memory.Fact under the
 * `lesson` category so future RAG+HyDE retrievals surface it when
 * similar tasks come up — closing the loop without retraining.
 */
package quality

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/diillson/chatcli/models"
)

// Lesson is a single Reflexion record. Persisted as a single
// memory.Fact via PersistLessonFunc — the receiving side decides how
// to slice it into category/tags.
type Lesson struct {
	Situation  string    // brief description of the recurring situation
	Mistake    string    // what went wrong this time
	Correction string    // what to do differently next time
	Tags       []string  // semantic tags for retrieval
	Trigger    string    // "error" | "hallucination" | "low_quality" | "manual"
	CreatedAt  time.Time // wall-clock for ordering / staleness checks
}

// PersistLessonFunc is the callback the hook uses to write a lesson
// into long-term memory. Returning an error logs and continues —
// reflexion never blocks the user-facing turn.
type PersistLessonFunc func(ctx context.Context, lesson Lesson) error

// LessonLLM is the small LLM callback the lesson generator needs.
// Returns the raw model response which GenerateLesson then parses.
type LessonLLM func(ctx context.Context, history []models.Message) (string, error)

// LessonRequest bundles everything the generator needs to ask the LLM
// for a structured lesson.
type LessonRequest struct {
	Task    string // original user task
	Attempt string // what the agent actually did (response / tool log)
	Outcome string // why we're reflecting: error message, discrepancy report, etc.
	Trigger string // "error" | "hallucination" | "low_quality" | "manual"
}

// GenerateLesson asks the LLM to distill a Lesson from the request
// using the protocol below. Returns nil + nil when the LLM declares
// "no useful lesson" so the caller skips persistence cleanly.
//
// The protocol is XML-ish (matches the Refiner/Verifier style) for
// parser symmetry across quality patterns.
func GenerateLesson(ctx context.Context, llm LessonLLM, req LessonRequest) (*Lesson, error) {
	if llm == nil {
		return nil, fmt.Errorf("lesson generator: nil LLM callback")
	}
	system := lessonSystemPrompt
	user := fmt.Sprintf(`TASK:
%s

ATTEMPT:
%s

OUTCOME (why we are reflecting):
%s

TRIGGER: %s`, req.Task, truncateLessonText(req.Attempt, 1500), truncateLessonText(req.Outcome, 800), req.Trigger)

	raw, err := llm(ctx, []models.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
	if err != nil {
		return nil, err
	}
	return parseLesson(raw, req.Trigger)
}

// FactContent renders the lesson into the standard four-line fact body
// stored in memory.Fact.Content. The format is human-readable and
// also keyword-friendly for the existing scorer.
func (l Lesson) FactContent() string {
	var b strings.Builder
	fmt.Fprintf(&b, "LESSON: %s\n", oneLine(l.Situation))
	fmt.Fprintf(&b, "MISTAKE: %s\n", oneLine(l.Mistake))
	fmt.Fprintf(&b, "CORRECTION: %s\n", oneLine(l.Correction))
	fmt.Fprintf(&b, "TRIGGER: %s", l.Trigger)
	return b.String()
}

// ─── prompt + parser ──────────────────────────────────────────────────────

const lessonSystemPrompt = `You distill engineering lessons from a single agent attempt.
Your output is a small XML-ish block the chatcli memory system stores.

Rules:
- A "lesson" must be GENERAL enough to apply next time a similar task
  comes up — not one-off and not a play-by-play.
- If there is genuinely nothing to learn (e.g. the task was trivial and
  the failure was a transient network blip), reply with exactly:
  <skip>nothing actionable</skip>
- Otherwise emit ALL of the following blocks. Keep each to ONE line.
- "tags" is a comma-separated list of 2-5 short keywords (lowercase,
  hyphenated if needed) that future similar tasks will likely contain.

OUTPUT (verbatim shape):

<situation>brief description of when this lesson applies</situation>
<mistake>what went wrong this time</mistake>
<correction>what to do differently next time</correction>
<tags>tag1, tag2, tag3</tags>`

var lessonBlockRE = regexp.MustCompile(`(?s)<(situation|mistake|correction|tags|skip)>(.*?)</`)

// parseLesson extracts the named blocks. Returns nil + nil when the
// model emitted <skip>…</skip>.
func parseLesson(raw, trigger string) (*Lesson, error) {
	matches := lessonBlockRE.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("lesson parser: no recognized blocks in response")
	}
	fields := make(map[string]string, 5)
	for _, m := range matches {
		fields[m[1]] = strings.TrimSpace(m[2])
	}
	if _, ok := fields["skip"]; ok {
		return nil, nil
	}
	if fields["situation"] == "" || fields["correction"] == "" {
		return nil, fmt.Errorf("lesson parser: missing required blocks (situation, correction)")
	}
	return &Lesson{
		Situation:  fields["situation"],
		Mistake:    fields["mistake"],
		Correction: fields["correction"],
		Tags:       splitTagList(fields["tags"]),
		Trigger:    trigger,
		CreatedAt:  time.Now(),
	}, nil
}

func splitTagList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.ToLower(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(unknown)"
	}
	return strings.Join(strings.Fields(s), " ")
}

func truncateLessonText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
