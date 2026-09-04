/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

/*
 * project_env.go
 *
 * Per-project environment overlay for surfaces that only learn about the
 * user's project after boot — today the ACP server, which receives it as
 * session/new's cwd.
 *
 * The overlay is FILL-ONLY (config.ApplyProjectDotenv): a project .env can
 * add settings the user has not set, and can never take over one that is
 * already in effect. It also runs at most once per process: the first
 * project announced wins, so two editor windows can never fight over the
 * process-wide environment.
 */
package cli

import (
	"strings"
	"sync"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/llm/manager"
	"go.uber.org/zap"
)

// projectEnvOnce guards the overlay: applying it twice would be confusing
// (the second project's values would be silently ignored anyway, since the
// first pass already exported them) and would rebuild the manager under a
// live session for nothing.
var projectEnvOnce sync.Once

// ApplyProjectEnv layers <dir>/.env over the process environment and, when
// that actually contributed variables, rebuilds the LLM manager so providers
// the project unlocked (a per-repository AWS_PROFILE, a provider key) become
// available. Returns the rebuilt manager, or nil when nothing changed.
//
// It never runs while an operation is in flight: swapping the manager under a
// running prompt would race the client it is already using.
func (cli *ChatCLI) ApplyProjectEnv(dir string) (manager.LLMManager, config.ProjectDotenvReport) {
	var (
		mgr manager.LLMManager
		rep config.ProjectDotenvReport
	)
	projectEnvOnce.Do(func() {
		rep = config.ApplyProjectDotenv(dir)
		if rep.Err != nil {
			cli.logger.Warn("project .env ignored", zap.String("path", rep.Path), zap.Error(rep.Err))
			return
		}
		if !rep.Present {
			cli.logger.Debug("no project .env", zap.String("dir", rep.Dir))
			return
		}
		cli.logger.Info("project .env applied",
			zap.String("path", rep.Path),
			zap.String("mode", string(rep.Mode)),
			zap.Strings("applied", rep.Applied),
			zap.Strings("already_set", rep.Skipped),
			zap.Strings("refused", rep.Refused))
		if len(rep.Applied) == 0 {
			return
		}
		if cli.IsExecuting() {
			// Values are exported already — the provider factories read the
			// environment per call, so most of the overlay takes effect
			// anyway. Only the manager rebuild is skipped.
			cli.logger.Warn("project .env applied mid-operation; manager rebuild deferred",
				zap.String("path", rep.Path))
			return
		}
		config.Global.Reload(cli.logger)
		rebuilt, err := manager.NewLLMManager(cli.logger)
		if err != nil {
			cli.logger.Error("failed to rebuild the LLM manager after the project .env", zap.Error(err))
			return
		}
		cli.manager = rebuilt
		mgr = rebuilt
		cli.adoptProviderAfterOverlay(rep)
	})
	return mgr, rep
}

// adoptProviderAfterOverlay keeps the session on its current provider/model
// when the rebuilt manager still serves them, and only re-resolves from the
// environment when the overlay is what defined them (or the current pair no
// longer resolves).
func (cli *ChatCLI) adoptProviderAfterOverlay(rep config.ProjectDotenvReport) {
	if cli.Provider != "" {
		if client, err := cli.manager.GetClient(cli.Provider, cli.Model); err == nil {
			cli.Client = client
			return
		}
	}
	cli.configureProviderAndModel()
	client, err := cli.manager.GetClient(cli.Provider, cli.Model)
	if err != nil {
		cli.logger.Warn("no client after the project .env overlay",
			zap.String("provider", cli.Provider), zap.String("model", cli.Model), zap.Error(err))
		return
	}
	cli.Client = client
	cli.logger.Info("provider re-resolved after the project .env overlay",
		zap.String("provider", cli.Provider),
		zap.String("model", cli.Model),
		zap.String("source", strings.Join(rep.Applied, ",")))
}

// ResetProjectEnvOnceForTest re-arms the one-shot overlay. Test-only.
func ResetProjectEnvOnceForTest() {
	projectEnvOnce = sync.Once{}
}
