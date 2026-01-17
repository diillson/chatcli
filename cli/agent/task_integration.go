package agent

import (
	"strings"

	"go.uber.org/zap"
)

// IntegrateTaskTracking processa o reasoning e atualiza o plano de tarefas
func IntegrateTaskTracking(tracker *TaskTracker, reasoningText string, logger *zap.Logger) {
	if tracker == nil || strings.TrimSpace(reasoningText) == "" {
		return
	}

	plan := tracker.GetPlan()
	if plan == nil {
		if err := tracker.ParseReasoning(reasoningText); err != nil {
			logger.Warn("Erro ao parsear reasoning para tarefas", zap.Error(err))
		}
		return
	}

	// Detecta mudança na lista (replan) comparando assinaturas
	if plan.PlanSignature != "" {
		if tmp := tracker.ResetPlanFromReasoning(reasoningText, true); tmp == nil {
			if tracker.GetPlan().PlanSignature != plan.PlanSignature {
				logger.Info("Mudança de plano detectada no reasoning, atualizando lista de tarefas")
				return
			}
		}
	}

	// Replan por múltiplas falhas
	if tracker.NeedsReplanning() {
		logger.Info("Replanejamento detectado, criando novo plano")
		tracker.ResetPlan()
		if err := tracker.ParseReasoning(reasoningText); err != nil {
			logger.Warn("Erro ao parsear reasoning para tarefas", zap.Error(err))
		}
		return
	}

	// Atualiza status baseado no reasoning atual
	lines := strings.Split(reasoningText, "\n")
	for _, line := range lines {
		line = strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(line, "[x]") || strings.Contains(line, "[✓]") || strings.Contains(line, "[✔]") {
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
