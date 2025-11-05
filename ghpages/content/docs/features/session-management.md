+++
title = "Gerenciamento de Sessões"
linkTitle = "Sessões"
weight = 10
description = "Aprenda a salvar, carregar e gerenciar múltiplas sessões de conversa para organizar seu trabalho e manter o histórico de diferentes tarefas."
+++

Trabalhar em múltiplos projetos ou tarefas pode ser desafiador, especialmente quando cada um possui um contexto de conversa diferente. O **ChatCLI** resolve isso com um sistema simples e poderoso de gerenciamento de sessões.

Uma sessão é essencialmente um "salvamento" completo do seu histórico de conversa, permitindo que você a retome exatamente de onde parou.

---

## Comandos de Sessão

Todos os comandos de gerenciamento de sessão começam com `/session`.

#### /session save <nome>

Salva a conversa atual (todo o histórico de prompts e respostas) com um nome de sua escolha.

```bash
# Salva a conversa atual na sessão "debug-api-pagamentos"
/session save debug-api-pagamentos

│ ✅ Sessão 'debug-api-pagamentos' salva com sucesso.

│ Dica: Após salvar, o nome da sessão aparecerá no seu prompt (ex:  debug-api-pagamentos ❯ ), indicando que você está trabalhando nela.
```
--------

####  /session load <nome>

Carrega uma sessão salva anteriormente. A conversa atual é substituída pelo histórico da sessão carregada.

# Carrega a sessão para continuar o trabalho no projeto do site
```bash
/session load documentacao-site

│ ✅ Sessão 'documentacao-site' carregada. A conversa anterior foi restaurada.
```
--------

####  /session list

Lista todas as sessões que você salvou no disco.
```bash
/session list

│ Sessões salvas:
│
│ • debug-api-pagamentos
│ • documentacao-site
│ • refatoracao-legado
```
--------

####  /session delete <nome>

Remove permanentemente uma sessão salva do disco. Esta ação não pode ser desfeita.
```bash
/session delete refatoracao-legado

│ ✅ Sessão 'refatoracao-legado' deletada com sucesso do disco.
```
Se você deletar a sessão que está ativa no momento, seu histórico atual será limpo e você começará uma nova conversa.

--------

####  /session new  (ou  /newsession )

Limpa o histórico atual e inicia uma conversa completamente nova. É perfeito para começar uma tarefa do zero sem estar atrelado a nenhuma sessão nomeada.
```bash
/session new

│ Nova sessão de conversa iniciada; histórico foi limpo.
```
--------

## Próximos Passos

O gerenciamento de sessões é ótimo para salvar o histórico da conversa, mas e quanto ao contexto dos arquivos? Para isso, o ChatCLI oferece um sistema ainda mais poderoso.

➡️ Próximo: [Gerenciamento de Contexto Persistente `/context`](/docs/features/persistent-context/)


---