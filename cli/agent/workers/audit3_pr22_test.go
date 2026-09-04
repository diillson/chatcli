/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package workers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// overflowThenDoneClient overflows once, then answers with a final text.
type overflowThenDoneClient struct{ calls int }

func (m *overflowThenDoneClient) GetModelName() string { return "mock" }
func (m *overflowThenDoneClient) SendPrompt(_ context.Context, _ string, hist []models.Message, _ int) (string, error) {
	m.calls++
	if m.calls == 1 {
		return "", errors.New("prompt is too long: 250000 tokens > 200000 maximum")
	}
	return "TASK DONE: the worker finished", nil
}

// recordingWindow is a WindowManager that records what the loop asked.
type recordingWindow struct {
	mu        sync.Mutex
	needs     bool
	compacted int
	turns     [][]models.Message
	names     []string
}

func (w *recordingWindow) NeedsCompaction([]models.Message) bool { return w.needs }
func (w *recordingWindow) Compact(_ context.Context, h []models.Message) ([]models.Message, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.compacted++
	return h, nil
}
func (w *recordingWindow) NoteTurn(worker string, turn []models.Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.names = append(w.names, worker)
	w.turns = append(w.turns, turn)
}

func TestRunWorkerReAct_RecoversFromOverflowAndUsesTheSharedWindow(t *testing.T) {
	win := &recordingWindow{needs: true}
	RegisterWorkerWindow(win)
	t.Cleanup(func() { RegisterWorkerWindow(nil) })
	client := &overflowThenDoneClient{}
	config := WorkerReActConfig{MaxTurns: 5, SystemPrompt: "you are a worker"}
	result, err := RunWorkerReAct(context.Background(), config, "task", client, nil, NewSkillSet(), nil, zap.NewNop())
	if err != nil {
		t.Fatalf("the worker must recover from the overflow: %v", err)
	}
	if !strings.Contains(result.Output, "TASK DONE") || client.calls != 2 {
		t.Fatalf("output=%q calls=%d", result.Output, client.calls)
	}
	win.mu.Lock()
	defer win.mu.Unlock()
	if win.compacted == 0 {
		t.Fatal("the shared compactor must run when the window says so")
	}
	if len(win.turns) == 0 || len(win.turns[0]) == 0 {
		t.Fatal("each worker turn must be journaled through NoteTurn")
	}
}

func TestRunWorkerReAct_OverflowRecoveryIsBounded(t *testing.T) {
	RegisterWorkerWindow(nil)
	always := &alwaysOverflowClient{}
	config := WorkerReActConfig{MaxTurns: 10, SystemPrompt: "worker"}
	_, err := RunWorkerReAct(context.Background(), config, "task", always, nil, NewSkillSet(), nil, zap.NewNop())
	if err == nil {
		t.Fatal("a provider that always overflows must fail the worker")
	}
	if always.calls > 4 {
		t.Fatalf("recovery must stay bounded: %d calls", always.calls)
	}
}

type alwaysOverflowClient struct{ calls int }

func (m *alwaysOverflowClient) GetModelName() string { return "mock" }
func (m *alwaysOverflowClient) SendPrompt(_ context.Context, _ string, _ []models.Message, _ int) (string, error) {
	m.calls++
	return "", errors.New("This model's maximum context length is 8192 tokens")
}
