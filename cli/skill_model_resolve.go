/*
 * ChatCLI - Skill model/provider resolution (thin wrapper)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Delegates to llm/client.ResolveModelRouting which holds the pure logic.
 * This file only wires in ChatCLI state (cachedModels, manager, logger) and
 * keeps the existing ChatCLI method signature so the rest of the codebase
 * stays unchanged.
 */
package cli

import (
	"context"
	"time"

	"github.com/diillson/chatcli/llm/client"
)

// SkillClientResolution preserves the original type name used by the cli
// package. It is now just a re-export of the shared resolver struct.
type SkillClientResolution = client.ModelRoutingResolution

// resolveSkillClient delegates to client.ResolveModelRouting, passing in
// the ChatCLI's current state (provider, model, active client, cached API
// models for that provider, logger).
//
// Behavior is identical to the previous inline implementation — see
// llm/client/model_router.go for the full pipeline documentation.
func (cli *ChatCLI) resolveSkillClient(hint string) SkillClientResolution {
	// Snapshot the API cache under the read lock so the resolver gets a
	// consistent view even if a background refresh is in flight.
	cli.cachedModelsMu.RLock()
	cache := make([]client.ModelInfo, len(cli.cachedModels))
	copy(cache, cli.cachedModels)
	cli.cachedModelsMu.RUnlock()

	return client.ResolveModelRouting(client.ResolveModelRoutingInput{
		Router:       cli.manager,
		UserProvider: cli.Provider,
		UserModel:    cli.Model,
		UserClient:   cli.Client,
		APICache:     cache,
		Hint:         hint,
		Logger:       cli.logger,
	})
}

// ensureModelCacheWarm triggers a non-blocking refresh of the cached model
// list for the user's current provider if it is empty and the model lister
// interface is supported. Called before resolving a skill hint so the
// api-cached branch has a chance to fire on first use.
//
// Timeout is short (2s) to avoid slowing down the turn if the API is slow —
// we accept stale catalog + heuristic results in that case.
func (cli *ChatCLI) ensureModelCacheWarm() {
	cli.cachedModelsMu.RLock()
	warm := len(cli.cachedModels) > 0
	cli.cachedModelsMu.RUnlock()
	if warm {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		models, err := cli.manager.ListModelsForProvider(ctx, cli.Provider)
		if err != nil || len(models) == 0 {
			return
		}
		cli.cachedModelsMu.Lock()
		if len(cli.cachedModels) == 0 {
			cli.cachedModels = models
		}
		cli.cachedModelsMu.Unlock()
	}()
}
