package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskCompleted  TaskStatus = "completed"
	TaskFailed     TaskStatus = "failed"
	// TaskBlocked is a task that cannot proceed until something outside
	// the agent's control happens (a user answer, an external system).
	// Task.Error carries the reason. It is not a failure: it never counts
	// toward replanning, and the plan's focus moves to the next workable
	// task.
	TaskBlocked TaskStatus = "blocked"
	// TaskCancelled is a task dropped on purpose (no longer needed, out of
	// scope). Task.Error carries the reason. Terminal: it leaves the
	// progress denominator, so a plan whose remaining tasks are cancelled
	// reads as done.
	TaskCancelled TaskStatus = "cancelled"
)

// Valid reports whether s is one of the tracker's statuses (the empty
// status is not valid; callers default it to pending themselves).
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskPending, TaskInProgress, TaskCompleted, TaskFailed, TaskBlocked, TaskCancelled:
		return true
	}
	return false
}

// Workable reports whether a task in status s still needs the agent's
// attention: pending, in progress or failed (a failed task is retried or
// replanned). Completed, cancelled and blocked tasks are skipped when the
// plan's focus advances.
func (s TaskStatus) Workable() bool {
	switch s {
	case TaskPending, TaskInProgress, TaskFailed, "":
		return true
	}
	return false
}

// TaskStatusList is the model-facing enumeration of statuses, in the order
// a plan usually moves through them.
const TaskStatusList = "pending|in_progress|completed|failed|blocked|cancelled"

type Task struct {
	ID          int
	Description string
	Status      TaskStatus
	StartedAt   time.Time
	CompletedAt time.Time
	Error       string
	Attempts    int
}

type TaskPlan struct {
	Tasks         []*Task
	CurrentTask   int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	NeedsReplan   bool
	FailureCount  int
	PlanSignature string
}

type TaskTracker struct {
	plan   *TaskPlan
	logger *zap.Logger
	mu     sync.Mutex
}

func NewTaskTracker(logger *zap.Logger) *TaskTracker {
	return &TaskTracker{
		logger: logger,
	}
}

// stripCheckbox removes leading checkbox markers like [x], [ ], [✓], [✔] from a string.
var checkboxRe = regexp.MustCompile(`^\s*\[[\sx✓✔!>]*\]\s*`)

func stripCheckbox(s string) string {
	return strings.TrimSpace(checkboxRe.ReplaceAllString(s, ""))
}

func (t *TaskTracker) ParseReasoning(reasoningText string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	re := regexp.MustCompile(`(?i)^(\d+)\.?\s*(.+)$`)
	lines := strings.Split(reasoningText, "\n")
	var tasks []*Task
	id := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) >= 3 {
			id++
			desc := stripCheckbox(matches[2])
			status := TaskPending

			low := strings.ToLower(line)
			if strings.Contains(low, "[x]") || strings.Contains(low, "[✓]") || strings.Contains(low, "[x#]") {
				status = TaskCompleted
			}

			tasks = append(tasks, &Task{
				ID:          id,
				Description: desc,
				Status:      status,
			})
		}
	}

	if len(tasks) == 0 {
		t.logger.Debug("Nenhuma tarefa encontrada no reasoning")
		return nil
	}

	sig := strings.Join(func() []string {
		parts := make([]string, 0, len(tasks))
		for _, tk := range tasks {
			parts = append(parts, strings.ToLower(strings.TrimSpace(tk.Description)))
		}
		return parts
	}(), "|")

	t.plan = &TaskPlan{
		Tasks:         tasks,
		CurrentTask:   0,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		PlanSignature: sig,
	}

	t.logger.Info("Plano de tarefas criado", zap.Int("total_tasks", len(tasks)))
	return nil
}

func (t *TaskTracker) MarkCurrentAs(status TaskStatus, errorMsg string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plan == nil || len(t.plan.Tasks) == 0 {
		return
	}

	if t.plan.CurrentTask >= len(t.plan.Tasks) {
		return
	}

	t.applyStatusLocked(t.plan.CurrentTask, status, errorMsg)
}

// applyStatusLocked sets the status of the task at idx and keeps the plan's
// bookkeeping consistent (caller holds t.mu, idx is in range): timestamps,
// the failure counter that triggers replanning, the reason kept for
// failed/blocked/cancelled tasks, and the focus cursor — which moves past a
// task that is no longer workable and back onto a task that resumes.
func (t *TaskTracker) applyStatusLocked(idx int, status TaskStatus, reason string) {
	task := t.plan.Tasks[idx]
	task.Status = status
	task.Attempts++

	switch status {
	case TaskInProgress:
		if task.StartedAt.IsZero() {
			task.StartedAt = time.Now()
		}
		task.Error = ""
		t.plan.CurrentTask = idx
	case TaskPending:
		task.Error = ""
		if idx < t.plan.CurrentTask {
			t.plan.CurrentTask = idx
		}
	case TaskCompleted:
		task.CompletedAt = time.Now()
		task.Error = ""
		t.advanceCursorLocked(idx)
	case TaskFailed:
		task.Error = reason
		t.plan.FailureCount++
		if t.plan.FailureCount >= 3 {
			t.plan.NeedsReplan = true
		}
	case TaskBlocked, TaskCancelled:
		task.Error = reason
		t.advanceCursorLocked(idx)
	}

	t.plan.UpdatedAt = time.Now()
}

// advanceCursorLocked moves the focus past idx when idx was the current
// task, landing on the next workable task (or past the end when none is
// left). Caller holds t.mu.
func (t *TaskTracker) advanceCursorLocked(idx int) {
	if idx != t.plan.CurrentTask {
		return
	}
	next := idx + 1
	for next < len(t.plan.Tasks) && !t.plan.Tasks[next].Status.Workable() {
		next++
	}
	t.plan.CurrentTask = next
}

func (t *TaskTracker) GetCurrentTask() *Task {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plan == nil || t.plan.CurrentTask >= len(t.plan.Tasks) {
		return nil
	}

	return t.plan.Tasks[t.plan.CurrentTask]
}

func (t *TaskTracker) GetPlan() *TaskPlan {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.plan
}

func (t *TaskTracker) NeedsReplanning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plan == nil {
		return false
	}

	return t.plan.NeedsReplan
}

func (t *TaskTracker) ResetPlan() {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.plan = nil
	t.logger.Info("Plano de tarefas resetado para replanejamento")
}

// ResetPlanFromReasoning recria o plano a partir de um novo reasoning. Se preserveCompleted=true, tenta preservar como concluídas as tarefas ja concluídas do plano anterior.
func (t *TaskTracker) ResetPlanFromReasoning(reasoningText string, preserveCompleted bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	re := regexp.MustCompile(`(?i)^(\d+)\.?\s*(.+)$`)
	lines := strings.Split(reasoningText, "\n")
	var tasks []*Task
	id := 0

	completed := map[string]bool{}
	if preserveCompleted && t.plan != nil {
		for _, tk := range t.plan.Tasks {
			if tk.Status == TaskCompleted {
				completed[strings.ToLower(strings.TrimSpace(tk.Description))] = true
			}
		}
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) >= 3 {
			id++
			desc := stripCheckbox(matches[2])
			status := TaskPending

			low := strings.ToLower(line)
			if strings.Contains(low, "[x]") || strings.Contains(low, "[✓]") || strings.Contains(low, "[x#]") {
				status = TaskCompleted
			}
			if preserveCompleted && completed[strings.ToLower(strings.TrimSpace(desc))] {
				status = TaskCompleted
			}

			tasks = append(tasks, &Task{ID: id, Description: desc, Status: status})
		}
	}

	if len(tasks) == 0 {
		return nil
	}

	sig := strings.Join(func() []string {
		parts := make([]string, 0, len(tasks))
		for _, tk := range tasks {
			parts = append(parts, strings.ToLower(strings.TrimSpace(tk.Description)))
		}
		return parts
	}(), "|")

	t.plan = &TaskPlan{
		Tasks:         tasks,
		CurrentTask:   0,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		PlanSignature: sig,
	}

	return nil
}

// TaskSpec is the LLM-friendly view of a planned task. It is what the
// @todo plugin uses to talk to the tracker — a flat structure decoupled
// from the internal *Task type's bookkeeping fields (attempts, timestamps).
type TaskSpec struct {
	Description string
	Status      TaskStatus
	// Reason is kept as Task.Error for a failed, blocked or cancelled task.
	Reason string
}

// SetTasks replaces the entire plan with the supplied list. Used by
// the @todo plugin to mirror Claude Code's TodoWrite semantics: the
// LLM emits the full updated list every call; the tracker reconciles
// without preserving prior state.
//
// This is the only public path that lets a caller set per-task status
// explicitly. MarkCurrentAs is for the per-turn ReAct flow (one task
// at a time); SetTasks is for the LLM-driven plan overwrite.
func (t *TaskTracker) SetTasks(specs []TaskSpec) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tasks := make([]*Task, 0, len(specs))
	currentTask := 0
	for i, s := range specs {
		status := s.Status
		if status == "" {
			status = TaskPending
		}
		tk := &Task{
			ID:          i + 1,
			Description: strings.TrimSpace(s.Description),
			Status:      status,
		}
		switch status {
		case TaskInProgress:
			tk.StartedAt = time.Now()
			currentTask = i
		case TaskCompleted:
			tk.CompletedAt = time.Now()
		case TaskFailed, TaskBlocked, TaskCancelled:
			tk.Error = strings.TrimSpace(s.Reason)
		}
		tasks = append(tasks, tk)
	}

	// Advance current marker past any prefix of completed tasks if no
	// task was explicitly marked in_progress. This matches the ReAct
	// loop's intuition that "next pending" is the current focus.
	allInProgress := false
	for _, s := range specs {
		if s.Status == TaskInProgress {
			allInProgress = true
			break
		}
	}
	if !allInProgress {
		currentTask = len(tasks)
		for i, tk := range tasks {
			if tk.Status.Workable() {
				currentTask = i
				break
			}
		}
	}

	sig := strings.Join(func() []string {
		parts := make([]string, 0, len(tasks))
		for _, tk := range tasks {
			parts = append(parts, strings.ToLower(strings.TrimSpace(tk.Description)))
		}
		return parts
	}(), "|")

	t.plan = &TaskPlan{
		Tasks:         tasks,
		CurrentTask:   currentTask,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
		PlanSignature: sig,
	}
}

// AppendTasks adds tasks to the end of the current plan (or starts one),
// keeping every existing task and its status; the new tasks are pending
// unless the spec says otherwise. Returns the ids of the added tasks.
func (t *TaskTracker) AppendTasks(specs []TaskSpec) []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plan == nil {
		t.plan = &TaskPlan{CreatedAt: time.Now()}
	}
	var ids []int
	for _, s := range specs {
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			continue
		}
		status := s.Status
		if status == "" {
			status = TaskPending
		}
		tk := &Task{ID: len(t.plan.Tasks) + 1, Description: desc, Status: status}
		switch status {
		case TaskInProgress:
			tk.StartedAt = time.Now()
		case TaskFailed, TaskBlocked, TaskCancelled:
			tk.Error = strings.TrimSpace(s.Reason)
		}
		t.plan.Tasks = append(t.plan.Tasks, tk)
		ids = append(ids, tk.ID)
	}
	t.plan.UpdatedAt = time.Now()
	return ids
}

// UpdateDescription rewords the task with the given 1-indexed ID, keeping
// its status. Returns false when no such task exists.
func (t *TaskTracker) UpdateDescription(id int, description string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.plan == nil || id < 1 || id > len(t.plan.Tasks) || strings.TrimSpace(description) == "" {
		return false
	}
	t.plan.Tasks[id-1].Description = strings.TrimSpace(description)
	t.plan.UpdatedAt = time.Now()
	return true
}

// MarkByID updates the status of the task with the given 1-indexed ID.
// Returns false when no such task exists. Used by the @todo plugin's
// partial-update path when the LLM wants to flip a single status
// without resending the whole list.
//
// The CurrentTask cursor advances if the marked task was the current
// one and the new status is Completed, mirroring MarkCurrentAs.
func (t *TaskTracker) MarkByID(id int, status TaskStatus, errorMsg string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plan == nil {
		return false
	}
	idx := id - 1
	if idx < 0 || idx >= len(t.plan.Tasks) {
		return false
	}
	t.applyStatusLocked(idx, status, errorMsg)
	return true
}

func (t *TaskTracker) FormatProgress() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plan == nil || len(t.plan.Tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\nAction plan:\n")

	for i, task := range t.plan.Tasks {
		var icon string
		switch task.Status {
		case TaskCompleted:
			icon = "[x]"
		case TaskInProgress:
			icon = "[>]"
		case TaskFailed:
			icon = "[!]"
		case TaskBlocked:
			icon = "[~]"
		case TaskCancelled:
			icon = "[-]"
		default:
			icon = "[ ]"
		}

		currentMarker := "  "
		if i == t.plan.CurrentTask {
			currentMarker = ">"
		}

		fmt.Fprintf(&b, "%s%s %d. %s\n", currentMarker, icon, i+1, task.Description)

		if task.Error != "" {
			switch task.Status {
			case TaskFailed:
				fmt.Fprintf(&b, "    Error: %s\n", task.Error)
			case TaskBlocked:
				fmt.Fprintf(&b, "    Blocked: %s\n", task.Error)
			case TaskCancelled:
				fmt.Fprintf(&b, "    Cancelled: %s\n", task.Error)
			}
		}
	}

	var completed, failed, blocked, cancelled int
	for _, task := range t.plan.Tasks {
		switch task.Status {
		case TaskCompleted:
			completed++
		case TaskFailed:
			failed++
		case TaskBlocked:
			blocked++
		case TaskCancelled:
			cancelled++
		}
	}

	// Cancelled tasks leave the denominator: they are not work left to do.
	fmt.Fprintf(&b, "\nProgress: %d/%d done", completed, len(t.plan.Tasks)-cancelled)
	if failed > 0 {
		fmt.Fprintf(&b, ", %d failed", failed)
	}
	if blocked > 0 {
		fmt.Fprintf(&b, ", %d blocked", blocked)
	}
	if cancelled > 0 {
		fmt.Fprintf(&b, ", %d cancelled", cancelled)
	}
	b.WriteString("\n")

	if t.plan.NeedsReplan {
		b.WriteString("\nWARNING: multiple failures detected. Replanning required!\n")
	}

	return b.String()
}
