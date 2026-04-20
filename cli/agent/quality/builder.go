/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package quality

import "go.uber.org/zap"

// BuildPipelineDeps groups the optional callbacks the hooks need.
// nil fields are tolerated: the corresponding hook is just not added.
type BuildPipelineDeps struct {
	// Dispatch routes per-call agent invocations from RefineHook /
	// VerifyHook. nil disables both.
	Dispatch DispatchOne
	// LessonLLM is the small LLM callback ReflexionHook uses to
	// distill a lesson from an attempt.
	LessonLLM LessonLLM
	// PersistLesson writes a lesson to long-term memory. Receives a
	// fresh background ctx (reflexion runs async).
	PersistLesson PersistLessonFunc
}

// BuildPipeline assembles a Pipeline with the hooks selected by cfg.
//
// All deps are optional — passing a zero BuildPipelineDeps yields a
// pipeline with only the always-on machinery (cfg.Enabled gate,
// reasoning auto-enable). This is the path tests usually take.
//
// Wiring per phase:
//   - Phase 5 (Self-Refine): RefineHook (PostHook) — needs Dispatch
//   - Phase 6 (CoVe):        VerifyHook (PostHook) — needs Dispatch
//   - Phase 4 (Reflexion):   ReflexionHook (PostHook) — needs LessonLLM + PersistLesson
//   - Phase 3 (HyDE):        wired in the retriever, not the pipeline
//   - Phase 2 (Plan-First):  wired in agent_mode dispatch, not the pipeline
//   - Phase 7 (Reasoning):   wired inside Pipeline.Run via applyAutoReasoning
func BuildPipeline(cfg Config, logger *zap.Logger, deps BuildPipelineDeps) *Pipeline {
	p := New(cfg, logger)
	if cfg.Refine.Enabled && deps.Dispatch != nil {
		p.AddPost(NewRefineHook(deps.Dispatch, logger))
	}
	if cfg.Verify.Enabled && deps.Dispatch != nil {
		p.AddPost(NewVerifyHook(deps.Dispatch, logger))
	}
	if cfg.Reflexion.Enabled && deps.LessonLLM != nil && deps.PersistLesson != nil {
		p.AddPost(NewReflexionHook(deps.LessonLLM, deps.PersistLesson, logger))
	}
	return p
}
