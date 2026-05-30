/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - gateway_runtime_model.go
 *
 * The gateway daemon is a SEPARATE detached process (see gateway_command.go):
 * it snapshots provider/model at spawn time and never shared memory with the
 * interactive REPL. So `/switch --model …` (or `/model …`) in the REPL was
 * invisible to a running daemon, and starting the daemon after a switch still
 * booted the .env default — both surfaced as "the gateway ignores my current
 * model".
 *
 * The fix is a tiny shared runtime-state file (~/.chatcli/runtime_model.json)
 * that the interactive process WRITES whenever the live model changes (switch,
 * provider switch, and right before spawning the daemon) and the daemon READS
 * before each inbound message. The file is the single cross-process source of
 * truth for "the operator's current model"; absence means "no override — keep
 * the env-derived snapshot", so non-gateway use is entirely unaffected.
 *
 * Only the interactive process writes; the daemon is read-only here. That keeps
 * the daemon from clobbering the operator's choice with its own boot snapshot.
 */
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"

	"go.uber.org/zap"
)

// runtimeModelState is the cross-process snapshot of the operator's live model
// choice, persisted by the interactive REPL and mirrored by the gateway daemon.
type runtimeModelState struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// runtimeModelStatePath returns ~/.chatcli/runtime_model.json (same state dir
// the gateway pidfile/log live in, so the daemon and REPL agree on location).
func runtimeModelStatePath() string {
	return gatewayStatePath("runtime_model.json")
}

// writeRuntimeModelState records the current provider/model so a detached
// gateway daemon mirrors the REPL's live choice. Called only from the
// interactive process (switch handlers + daemon spawn); failures are best-effort
// and logged, never fatal — a missing file simply means "no override".
func (cli *ChatCLI) writeRuntimeModelState() {
	if cli.Provider == "" || cli.Model == "" {
		return
	}
	path := runtimeModelStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		cli.logger.Warn("gateway: could not create runtime-model state dir", zap.Error(err))
		return
	}
	data, err := json.Marshal(runtimeModelState{Provider: cli.Provider, Model: cli.Model})
	if err != nil {
		cli.logger.Warn("gateway: could not marshal runtime-model state", zap.Error(err))
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cli.logger.Warn("gateway: could not write runtime-model state", zap.Error(err))
	}
}

// readRuntimeModelState loads the persisted snapshot. ok=false when the file is
// absent or malformed (no override → caller keeps its current model).
func readRuntimeModelState() (runtimeModelState, bool) {
	data, err := os.ReadFile(runtimeModelStatePath()) // #nosec G304 -- daemon-scoped state dir
	if err != nil {
		return runtimeModelState{}, false
	}
	var s runtimeModelState
	if err := json.Unmarshal(data, &s); err != nil || s.Provider == "" || s.Model == "" {
		return runtimeModelState{}, false
	}
	return s, true
}

// refreshGatewayModel re-reads the shared runtime-model state and rebuilds the
// LLM client when the interactive session has switched models since the daemon
// last looked. The daemon calls it at startup and before every inbound message
// (under the run lock in gatewayAgentFunc), so a long-lived gateway always
// answers with the operator's current model. A GetClient failure keeps the
// current model rather than dropping the daemon.
func (cli *ChatCLI) refreshGatewayModel() {
	s, ok := readRuntimeModelState()
	if !ok {
		return
	}
	if s.Provider == cli.Provider && s.Model == cli.Model {
		return
	}
	client, err := cli.manager.GetClient(s.Provider, s.Model)
	if err != nil {
		cli.logger.Warn("gateway: could not adopt runtime model; keeping current",
			zap.String("provider", s.Provider), zap.String("model", s.Model), zap.Error(err))
		return
	}
	cli.logger.Info("gateway: adopting runtime model from interactive session",
		zap.String("from_provider", cli.Provider), zap.String("from_model", cli.Model),
		zap.String("to_provider", s.Provider), zap.String("to_model", s.Model))
	cli.Client = client
	cli.Provider = s.Provider
	cli.Model = s.Model
}
