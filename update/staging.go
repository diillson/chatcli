/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/diillson/chatcli/version"
)

// StagedRecord registra um update aplicado em background (modo auto). Como o
// staging nunca reinicia o processo vivo, o registro é o que permite ao
// PRÓXIMO boot anunciar "atualizado para vX.Y.Z" no welcome.
type StagedRecord struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Method   string    `json:"method"`
	StagedAt time.Time `json:"staged_at"`
}

// stagedRecordTTL descarta registros que nunca se materializaram — ex.: um
// go install que escreveu num GOBIN fora do PATH efetivo do usuário.
const stagedRecordTTL = 7 * 24 * time.Hour

// cacheDirFn resolve o diretório de cache do chatcli — seam para testes.
var cacheDirFn = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chatcli", "cache"), nil
}

func stagedRecordPath() (string, error) {
	dir, err := cacheDirFn()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-staged.json"), nil
}

// SaveStagedRecord persiste o registro de staging (best-effort, atômico via
// temp+rename como o cache de release).
func SaveStagedRecord(rec StagedRecord) {
	path, err := stagedRecordPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-staged-*.json")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
	}
}

// ConsumeStagedRecord devolve o registro pendente quando a versão atual do
// processo JÁ É a versão que o staging instalou — ou seja, este boot é o
// primeiro na versão nova e o welcome deve anunciá-la. O registro consumido
// (ou vencido) é removido; um registro ainda não materializado permanece.
func ConsumeStagedRecord(currentVersion string) (StagedRecord, bool) {
	path, err := stagedRecordPath()
	if err != nil {
		return StagedRecord{}, false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- caminho fixo sob o home do usuário
	if err != nil {
		return StagedRecord{}, false
	}
	var rec StagedRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.To == "" {
		_ = os.Remove(path)
		return StagedRecord{}, false
	}
	if version.ExtractBaseVersion(currentVersion) == version.ExtractBaseVersion(rec.To) {
		_ = os.Remove(path)
		return rec, true
	}
	if time.Since(rec.StagedAt) > stagedRecordTTL {
		_ = os.Remove(path)
	}
	return StagedRecord{}, false
}

// autoLockTTL invalida locks órfãos (processo morto no meio do update).
const autoLockTTL = 15 * time.Minute

// TryAcquireAutoLock serializa o update em background entre processos chatcli
// concorrentes via lock file O_EXCL. Devolve ok=false quando outro processo
// já está atualizando; release remove o lock.
func TryAcquireAutoLock() (release func(), ok bool) {
	dir, err := cacheDirFn()
	if err != nil {
		return nil, false
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	path := filepath.Join(dir, "update.lock")

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, true
		}
		// Lock presente: só rouba se estiver vencido (dono provavelmente morto).
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) <= autoLockTTL {
			return nil, false
		}
		_ = os.Remove(path)
	}
	return nil, false
}
