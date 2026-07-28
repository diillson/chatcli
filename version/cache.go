/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// releaseCacheTTL limita a consulta à API do GitHub a no máximo uma por dia
// por máquina; dentro da janela, os metadados servem do disco.
const releaseCacheTTL = 24 * time.Hour

// releaseCacheEntry é o formato persistido em disco.
type releaseCacheEntry struct {
	FetchedAt time.Time   `json:"fetched_at"`
	Release   ReleaseInfo `json:"release"`
}

// releaseCachePath resolve o arquivo de cache sob o diretório padrão do
// chatcli. Erro só quando o home do usuário é indeterminável.
func releaseCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chatcli", "cache", "latest-release.json"), nil
}

// saveReleaseCache persiste os metadados da release de forma atômica
// (temp + rename, para um leitor concorrente nunca ver JSON truncado).
// Best-effort: falhas de disco degradam para "sem cache", nunca para erro.
func saveReleaseCache(release ReleaseInfo) {
	path, err := releaseCachePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(releaseCacheEntry{FetchedAt: time.Now().UTC(), Release: release})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".latest-release-*.json")
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

// loadReleaseCacheEntry devolve a entrada cacheada quando existe e é
// parseável, junto com a idade dela — SEM aplicar o TTL. Validade de
// exibição e throttle de fetch são decisões separadas: um cache vencido
// ainda é o melhor dado conhecido para exibir, mas obriga um novo fetch.
// Cache corrompido ou sem tag conta como ausente.
func loadReleaseCacheEntry() (releaseCacheEntry, bool) {
	path, err := releaseCachePath()
	if err != nil {
		return releaseCacheEntry{}, false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- caminho fixo sob o home do usuário
	if err != nil {
		return releaseCacheEntry{}, false
	}
	var entry releaseCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return releaseCacheEntry{}, false
	}
	if entry.Release.TagName == "" {
		return releaseCacheEntry{}, false
	}
	return entry, true
}

// fresh informa se a entrada ainda dispensa um novo fetch (dentro do TTL).
func (e releaseCacheEntry) fresh() bool {
	return time.Since(e.FetchedAt) <= releaseCacheTTL
}

// loadReleaseCache devolve os metadados cacheados quando existem, mesmo que
// vencidos — exibição confia no último dado conhecido. Quem precisa decidir
// se refaz o fetch usa loadReleaseCacheEntry + fresh.
func loadReleaseCache() (ReleaseInfo, bool) {
	entry, ok := loadReleaseCacheEntry()
	if !ok {
		return ReleaseInfo{}, false
	}
	return entry.Release, true
}

// OfflineReport monta o relatório só com dados locais: build info resolvido e,
// quando há cache da release (mesmo vencido — é o último dado conhecido), o
// mesmo enriquecimento de commit/data do GetReport — sem nenhuma chamada de
// rede. É o que a tela de boas-vindas usa para mostrar o hash de um build
// go install e o banner de atualização sem atrasar o boot.
func OfflineReport() Report {
	rep := Report{Current: GetCurrentVersion()}
	release, ok := loadReleaseCache()
	if !ok {
		return rep
	}
	rep.Latest = normalizeTag(release.TagName)
	rep.NeedsUpdate = NeedsUpdate(ExtractBaseVersion(rep.Current.Version), rep.Latest)
	rep.Current = rep.Current.enrichedFromRelease(rep.Latest, release)
	if rep.NeedsUpdate {
		rep.Notes = release.Body
		rep.ReleaseURL = release.HTMLURL
	}
	return rep
}

// RefreshReleaseCacheIfStale renova o cache quando vencido/ausente, limitada
// por um deadline próprio sobre o ctx do chamador — desenhada para rodar em
// goroutine no boot sem segurar nada. O TTL aqui é só throttle de rede
// (≤1 consulta/dia à API do GitHub); a exibição usa o cache em qualquer
// idade. Respeita CHATCLI_DISABLE_VERSION_CHECK e nunca retorna erro: a
// próxima exibição simplesmente usa o que houver.
func RefreshReleaseCacheIfStale(ctx context.Context) {
	if versionCheckDisabled() {
		return
	}
	if entry, ok := loadReleaseCacheEntry(); ok && entry.fresh() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = GetReport(ctx)
}
