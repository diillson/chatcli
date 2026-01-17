package agent

import (
	"strings"

	"go.uber.org/zap"
)

// IntegrateTaskTracking processa o reasoning e atualiza o plano de tarefas
func IntegrateTaskTracking(tracker *TaskTracker, reasoningText string, logger *zap.Logger) {
	if tracker == nil || reasoningText == "" {
		return
	}

	// Se no ha plano ou precisa replanejar, parseia o reasoning
	if tracker.GetPlan() == nil || tracker.NeedsReplanning() {
		if tracker.NeedsReplanning() {
			logger.Info("Replanejamento detectado, criando novo plano")
			tracker.ResetPlan()
		}
		
		if err := tracker.ParseReasoning(reasoningText); err != nil {
			logger.Warn("Erro ao parsear reasoning para tarefas", zap.Error(err))
		}
		return
	}

	// Se ja tem plano, atualiza status baseado no reasoning atual
	// Procura por marcadores de conclusao [x] no texto
	plan := tracker.GetPlan()
	if plan == nil {
		return
	}

	lines := strings.Split(reasoningText, "\n")
	for _, line := range lines {
		line = strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(line, "[x]") || strings.Contains(line, "[☓]") {
			// Marca tarefa atual como concluida (avança automaticamente)
			tracker.MarkCurrentAs(TaskCompleted, "")
		}
	}
}

// MarkTaskFailed marca a tarefa atual como falhada
func MarkTaskFailed(tracker *TaskTracker, errorMsg string) {
	if tracker == nil {
		return
	}
	tracker.MarkCurrentAs(TaskFailed, errorMsg)
}

// MarkTaskCompleted marca a tarefa atual como concluída
func MarkTaskCompleted(tracker *TaskTracker) {
	if tracker == nil {
		return
	}
	tracker.MarkCurrentAs(TaskCompleted, "")
}

// MarkTaskInProgress marca a tarefa atual como em andamento
func MarkTaskInProgress(tracker *TaskTracker) {
	if tracker == nil {
		return
	}
	tracker.MarkCurrentAs(TaskInProgress, "")
}
