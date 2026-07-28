/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Aviso de atualização descoberto DEPOIS da welcome screen: o refresh de
 * release roda em goroutine no boot, então quando ele detecta uma versão
 * nova a tela de boas-vindas já foi impressa. Em vez de esperar o próximo
 * boot, o aviso fica pendente e é drenado no próximo tick do executor —
 * o único ponto onde escrever no stdout não disputa com o redraw do
 * go-prompt (mesmo padrão de memNotices).
 */
package cli

import (
	"fmt"

	"github.com/diillson/chatcli/i18n"
)

// markUpdateAnnounced registra a versão que a welcome screen anunciou no
// banner, para o aviso mid-session não repetir a mesma informação. Também
// descarta um aviso pendente da mesma versão que tenha chegado primeiro
// (corrida benigna entre o refresh e a impressão da welcome).
func (cli *ChatCLI) markUpdateAnnounced(latest string) {
	cli.updateNoticeMu.Lock()
	defer cli.updateNoticeMu.Unlock()
	cli.welcomeUpdateShown = latest
	if cli.pendingUpdateNotice == latest {
		cli.pendingUpdateNotice = ""
	}
}

// queueUpdateNotice agenda o aviso de versão nova para o próximo turno,
// exceto quando a welcome já anunciou exatamente essa versão neste boot.
func (cli *ChatCLI) queueUpdateNotice(latest string) {
	if latest == "" {
		return
	}
	cli.updateNoticeMu.Lock()
	defer cli.updateNoticeMu.Unlock()
	if cli.welcomeUpdateShown == latest {
		return
	}
	cli.pendingUpdateNotice = latest
}

// drainUpdateNotice imprime (uma única vez) o aviso pendente de versão
// nova. Chamado no início do executor, entre a submissão do usuário e o
// processamento do input.
func (cli *ChatCLI) drainUpdateNotice() {
	cli.updateNoticeMu.Lock()
	latest := cli.pendingUpdateNotice
	cli.pendingUpdateNotice = ""
	cli.updateNoticeMu.Unlock()

	if latest == "" {
		return
	}
	fmt.Println(colorize("  "+i18n.T("update.notice.available", latest), ColorYellow))
}
