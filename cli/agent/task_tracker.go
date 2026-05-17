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
)

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

	task := t.plan.Tasks[t.plan.CurrentTask]
	task.Status = status
	task.Attempts++

	switch status {
	case TaskInProgress:
		if task.StartedAt.IsZero() {
			task.StartedAt = time.Now()
		}
	case TaskCompleted:
		task.CompletedAt = time.Now()
		t.plan.CurrentTask++
	case TaskFailed:
		task.Error = errorMsg
		t.plan.FailureCount++
		if t.plan.FailureCount >= 3 {
			t.plan.NeedsReplan = true
		}
	}

	t.plan.UpdatedAt = time.Now()
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
		currentTask = 0
		for i, tk := range tasks {
			if tk.Status != TaskCompleted {
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
	tk := t.plan.Tasks[idx]
	tk.Status = status
	tk.Attempts++
	switch status {
	case TaskInProgress:
		if tk.StartedAt.IsZero() {
			tk.StartedAt = time.Now()
		}
	case TaskCompleted:
		tk.CompletedAt = time.Now()
		if idx == t.plan.CurrentTask {
			t.plan.CurrentTask++
		}
	case TaskFailed:
		tk.Error = errorMsg
		t.plan.FailureCount++
		if t.plan.FailureCount >= 3 {
			t.plan.NeedsReplan = true
		}
	}
	t.plan.UpdatedAt = time.Now()
	return true
}

func (t *TaskTracker) FormatProgress() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.plan == nil || len(t.plan.Tasks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\nPlano de Acao:\n")

	for i, task := range t.plan.Tasks {
		var icon string
		switch task.Status {
		case TaskCompleted:
			icon = "[x]"
		case TaskInProgress:
			icon = "[>]"
		case TaskFailed:
			icon = "[!]"
		default:
			icon = "[ ]"
		}

		currentMarker := "  "
		if i == t.plan.CurrentTask {
			currentMarker = ">"
		}

		fmt.Fprintf(&b, "%s%s %d. %s\n", currentMarker, icon, i+1, task.Description)

		if task.Status == TaskFailed && task.Error != "" {
			fmt.Fprintf(&b, "    Error: %s\n", task.Error)
		}
	}

	completed := 0
	failed := 0
	for _, task := range t.plan.Tasks {
		if task.Status == TaskCompleted {
			completed++
		} else if task.Status == TaskFailed {
			failed++
		}
	}

	fmt.Fprintf(&b, "\nProgresso: %d/%d concluidas", completed, len(t.plan.Tasks))
	if failed > 0 {
		fmt.Fprintf(&b, ", %d falhas", failed)
	}
	b.WriteString("\n")

	if t.plan.NeedsReplan {
		b.WriteString("\nATENCAO: Multiplas falhas detectadas. Replanejamento necessario!\n")
	}

	return b.String()
}
