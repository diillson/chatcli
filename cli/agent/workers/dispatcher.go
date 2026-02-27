package workers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// DispatcherConfig holds configuration for the async dispatcher.
type DispatcherConfig struct {
	MaxWorkers    int           // max concurrent worker goroutines
	ParallelMode  bool          // whether parallel dispatch is enabled
	Provider      string        // LLM provider for worker instances
	Model         string        // LLM model for worker instances
	WorkerTimeout time.Duration // timeout per individual worker
}

// DefaultMaxWorkers is the default number of concurrent worker goroutines.
const DefaultMaxWorkers = 4

// DefaultWorkerTimeout is the default timeout for a single worker.
const DefaultWorkerTimeout = 5 * time.Minute

// Dispatcher orchestrates parallel agent execution.
type Dispatcher struct {
	registry *Registry
	lockMgr  *FileLockManager
	llmMgr   manager.LLMManager
	config   DispatcherConfig
	logger   *zap.Logger
}

// NewDispatcher creates a Dispatcher with the given dependencies.
func NewDispatcher(
	registry *Registry,
	llmMgr manager.LLMManager,
	config DispatcherConfig,
	logger *zap.Logger,
) *Dispatcher {
	if config.MaxWorkers <= 0 {
		config.MaxWorkers = DefaultMaxWorkers
	}
	if config.WorkerTimeout <= 0 {
		config.WorkerTimeout = DefaultWorkerTimeout
	}
	return &Dispatcher{
		registry: registry,
		lockMgr:  NewFileLockManager(),
		llmMgr:   llmMgr,
		config:   config,
		logger:   logger,
	}
}

// MaxWorkers returns the maximum number of concurrent worker goroutines.
func (d *Dispatcher) MaxWorkers() int {
	return d.config.MaxWorkers
}

// Dispatch executes a batch of agent calls, respecting parallelism settings.
// Independent calls run concurrently (up to MaxWorkers), dependent calls run sequentially.
// Returns results in the same order as the input calls.
func (d *Dispatcher) Dispatch(ctx context.Context, calls []AgentCall) []AgentResult {
	if len(calls) == 0 {
		return nil
	}
	if !d.config.ParallelMode || len(calls) == 1 {
		return d.dispatchSequential(ctx, calls)
	}
	return d.dispatchParallel(ctx, calls)
}

// dispatchSequential executes agent calls one by one.
func (d *Dispatcher) dispatchSequential(ctx context.Context, calls []AgentCall) []AgentResult {
	results := make([]AgentResult, len(calls))

	for i, call := range calls {
		d.logger.Info("Dispatching agent (sequential)",
			zap.String("agent", string(call.Agent)),
			zap.String("task", truncateStr(call.Task, 80)),
			zap.String("callID", call.ID),
		)

		result := d.executeAgent(ctx, call)
		results[i] = result

		d.logger.Info("Agent completed (sequential)",
			zap.String("agent", string(call.Agent)),
			zap.String("callID", call.ID),
			zap.Duration("duration", result.Duration),
			zap.Bool("hasError", result.Error != nil),
		)
	}

	return results
}

// dispatchParallel executes agent calls concurrently with a semaphore.
func (d *Dispatcher) dispatchParallel(ctx context.Context, calls []AgentCall) []AgentResult {
	results := make([]AgentResult, len(calls))
	sem := make(chan struct{}, d.config.MaxWorkers) // semaphore
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, ac AgentCall) {
			defer wg.Done()

			// Acquire semaphore slot
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = AgentResult{
					CallID: ac.ID,
					Agent:  ac.Agent,
					Task:   ac.Task,
					Error:  ctx.Err(),
				}
				return
			}

			d.logger.Info("Dispatching agent (parallel)",
				zap.String("agent", string(ac.Agent)),
				zap.String("task", truncateStr(ac.Task, 80)),
				zap.String("callID", ac.ID),
				zap.Int("workerSlot", idx),
			)

			result := d.executeAgent(ctx, ac)
			results[idx] = result

			d.logger.Info("Agent completed (parallel)",
				zap.String("agent", string(ac.Agent)),
				zap.String("callID", ac.ID),
				zap.Duration("duration", result.Duration),
				zap.Bool("hasError", result.Error != nil),
			)
		}(i, call)
	}

	wg.Wait()
	return results
}

// executeAgent runs a single agent call with timeout and error handling.
func (d *Dispatcher) executeAgent(ctx context.Context, call AgentCall) AgentResult {
	startTime := time.Now()

	// Look up the agent
	agent, ok := d.registry.Get(call.Agent)
	if !ok {
		return AgentResult{
			CallID:   call.ID,
			Agent:    call.Agent,
			Task:     call.Task,
			Error:    fmt.Errorf("unknown agent type: %s", call.Agent),
			Duration: time.Since(startTime),
		}
	}

	// Create a fresh LLM client for this worker
	llmClient, err := d.llmMgr.GetClient(d.config.Provider, d.config.Model)
	if err != nil {
		return AgentResult{
			CallID:   call.ID,
			Agent:    call.Agent,
			Task:     call.Task,
			Error:    fmt.Errorf("failed to create LLM client for worker: %w", err),
			Duration: time.Since(startTime),
		}
	}

	// Create worker context with timeout
	workerCtx, cancel := context.WithTimeout(ctx, d.config.WorkerTimeout)
	defer cancel()

	deps := &WorkerDeps{
		LLMClient: llmClient,
		LockMgr:   d.lockMgr,
		Logger:    d.logger.With(zap.String("agent", string(call.Agent)), zap.String("callID", call.ID)),
	}

	// Execute the agent
	result, execErr := agent.Execute(workerCtx, call.Task, deps)
	if result == nil {
		result = &AgentResult{}
	}

	result.CallID = call.ID
	result.Agent = call.Agent
	result.Task = call.Task
	result.Duration = time.Since(startTime)
	if execErr != nil && result.Error == nil {
		result.Error = execErr
	}

	return *result
}

// FormatResults formats a slice of AgentResult into a feedback string
// for injection into the orchestrator's LLM history.
func FormatResults(results []AgentResult) string {
	var b strings.Builder
	b.WriteString("--- Agent Results ---\n\n")

	for i, r := range results {
		fmt.Fprintf(&b, "[%s] (call %s, %s)\n", r.Agent, r.CallID, r.Duration.Round(time.Millisecond))
		fmt.Fprintf(&b, "Task: %s\n", r.Task)
		if r.Error != nil {
			fmt.Fprintf(&b, "Status: FAILED — %v\n", r.Error)
		} else {
			b.WriteString("Status: OK\n")
		}
		if r.Output != "" {
			b.WriteString("Output:\n")
			b.WriteString(r.Output)
			if !strings.HasSuffix(r.Output, "\n") {
				b.WriteString("\n")
			}
		}
		if i < len(results)-1 {
			b.WriteString("\n---\n\n")
		}
	}

	return b.String()
}

// truncateStr truncates a string to maxLen and appends "..." if needed.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
