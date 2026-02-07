package auth

import "go.uber.org/zap"

// TEMPORARY: stub para destravar o build. A versão anterior deste arquivo ficou corrompida (bytes não-UTF-8) durante escrita em base64 e quebrou a compilação.
// Vamos reimplementar o sync Claude Code / Codex CLI em passos pequenos com write valido.
func SyncExternalCliCreds(logger *zap.Logger) (bool, error) {
	return false, nil
}
