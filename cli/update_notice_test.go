/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/update"
	"github.com/diillson/chatcli/version"
)

func TestBackgroundUpdateFlowQueuesNoticeInNotifyMode(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	// default: notify — o refresh descobre a versão nova depois da welcome.

	cli.backgroundUpdateFlow(context.Background())

	out := captureStdout(t, cli.drainUpdateNotice)
	if !strings.Contains(out, "1.6.0") || !strings.Contains(out, "/update") {
		t.Fatalf("aviso mid-session deve anunciar a versão e apontar o /update, saída: %q", out)
	}

	// Consumo único: o turno seguinte não repete o aviso.
	out = captureStdout(t, cli.drainUpdateNotice)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("aviso deve ser consumido uma única vez, saída: %q", out)
	}
}

func TestUpdateNoticeSuppressedWhenWelcomeAlreadyAnnounced(t *testing.T) {
	cli := minimalCLI(t)

	cli.markUpdateAnnounced("1.6.0")
	cli.queueUpdateNotice("1.6.0")

	out := captureStdout(t, cli.drainUpdateNotice)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("welcome já anunciou esta versão; aviso seria redundante, saída: %q", out)
	}

	// Uma versão AINDA mais nova descoberta depois volta a ser anunciada.
	cli.queueUpdateNotice("1.7.0")
	out = captureStdout(t, cli.drainUpdateNotice)
	if !strings.Contains(out, "1.7.0") {
		t.Fatalf("versão diferente da anunciada deve gerar aviso, saída: %q", out)
	}
}

func TestUpdateNoticeDroppedWhenWelcomeAnnouncesAfterQueue(t *testing.T) {
	cli := minimalCLI(t)

	// Corrida benigna: o refresh enfileira antes de a welcome imprimir.
	cli.queueUpdateNotice("1.6.0")
	cli.markUpdateAnnounced("1.6.0")

	out := captureStdout(t, cli.drainUpdateNotice)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("welcome anunciou depois do queue; aviso deve ser descartado, saída: %q", out)
	}
}

func TestBackgroundUpdateFlowQueuesNoticeWhenAutoStagingFails(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", errors.New("proxy fora do ar"))
	t.Setenv("CHATCLI_AUTO_UPDATE", "auto")

	cli.backgroundUpdateFlow(context.Background())

	out := captureStdout(t, cli.drainUpdateNotice)
	if !strings.Contains(out, "1.6.0") {
		t.Fatalf("staging falhou — o usuário precisa saber que há versão nova, saída: %q", out)
	}
}

func TestBackgroundUpdateFlowNoNoticeWhenStagingSucceeds(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	t.Setenv("CHATCLI_AUTO_UPDATE", "auto")

	cli.backgroundUpdateFlow(context.Background())

	out := captureStdout(t, cli.drainUpdateNotice)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("staging bem-sucedido é anunciado no próximo boot, não em aviso, saída: %q", out)
	}
}

// TestWelcomeAnnouncesUpdateFromExpiredCache reproduz o cenário que motivou
// o desacoplamento exibição/TTL: o cache diz que há release nova, mas já
// venceu. O banner do welcome deve aparecer mesmo assim — antes, o cache
// vencido contava como ausente e o usuário só descobria a atualização um
// boot depois de rodar /version.
func TestWelcomeAnnouncesUpdateFromExpiredCache(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(home, ".chatcli", "cache", "latest-release.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := fmt.Sprintf(`{"fetched_at":%q,"release":{"tag_name":"v1.6.0"}}`,
		time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339))
	if err := os.WriteFile(cachePath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() { cli.PrintWelcomeScreen() })

	if !strings.Contains(out, "1.6.0") || !strings.Contains(out, "/update") {
		t.Fatalf("cache vencido ainda é o último dado conhecido; welcome deve anunciar, saída: %q", out)
	}
}

// TestPreWelcomeCheckAnnouncesReleaseOnFirstBoot pina o requisito de
// imediatismo: sem cache nenhum (primeiro boot após uma release), o check
// síncrono pré-welcome popula o cache e a PRÓPRIA welcome anuncia a versão
// nova com o convite completo do /version — linha amarela + comando do canal.
// Antes, o usuário só descobria no próximo input ou no próximo boot.
func TestPreWelcomeCheckAnnouncesReleaseOnFirstBoot(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodHomebrew, "1.5.0", "1.6.0", nil)

	cli.preWelcomeUpdateCheck(context.Background())
	out := captureStdout(t, func() { cli.PrintWelcomeScreen() })

	if !strings.Contains(out, "1.6.0") || !strings.Contains(out, "/update") {
		t.Fatalf("welcome deve anunciar a release recém-descoberta, saída: %q", out)
	}
	if !strings.Contains(out, "brew upgrade diillson/chatcli/chatcli") {
		t.Fatalf("welcome deve mostrar o comando do canal de instalação, saída: %q", out)
	}

	// A welcome anunciou — o fluxo em background não pode repetir no drain.
	cli.backgroundUpdateFlow(context.Background())
	if drain := captureStdout(t, cli.drainUpdateNotice); strings.TrimSpace(drain) != "" {
		t.Fatalf("aviso mid-session seria redundante após o banner, saída: %q", drain)
	}
}

// countingFetch instala um contador sobre o seam de fetch de release; o
// cleanup do withUpdateSeams restaura o original.
func countingFetch(t *testing.T) *atomic.Int32 {
	t.Helper()
	calls := &atomic.Int32{}
	orig := version.FetchLatestReleaseImpl
	version.FetchLatestReleaseImpl = func(ctx context.Context) (version.ReleaseInfo, error) {
		calls.Add(1)
		return orig(ctx)
	}
	return calls
}

func TestPreWelcomeCheckSkipsNetworkWhenOff(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	t.Setenv("CHATCLI_AUTO_UPDATE", "off")
	calls := countingFetch(t)

	cli.preWelcomeUpdateCheck(context.Background())

	if calls.Load() != 0 {
		t.Fatalf("política off não pode custar rede no boot, fetches: %d", calls.Load())
	}
}

func TestPreWelcomeCheckFreshCacheSkipsNetwork(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)

	cli.preWelcomeUpdateCheck(context.Background()) // popula o cache
	calls := countingFetch(t)
	cli.preWelcomeUpdateCheck(context.Background())

	if calls.Load() != 0 {
		t.Fatalf("cache fresco dispensa nova consulta no boot, fetches: %d", calls.Load())
	}
}

func TestBackgroundUpdateFlowNoNoticeInOffMode(t *testing.T) {
	cli := minimalCLI(t)
	withUpdateSeams(t, update.MethodGoInstall, "1.5.0", "1.6.0", nil)
	t.Setenv("CHATCLI_AUTO_UPDATE", "off")

	cli.backgroundUpdateFlow(context.Background())

	out := captureStdout(t, cli.drainUpdateNotice)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("política off suprime qualquer aviso, saída: %q", out)
	}
}
