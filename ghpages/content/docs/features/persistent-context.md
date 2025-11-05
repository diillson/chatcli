+++
title = "Gerenciamento de Contexto Persistente"
linkTitle = "Contextos (/context)"
weight = 20
description = "Domine o gerenciamento de contextos para salvar, anexar e reutilizar snapshots de projetos, tornando sua interação com a IA mais rápida e poderosa."
+++

Enquanto o gerenciamento de sessões (`/session`) salva o histórico da *conversa*, o gerenciamento de contextos (`/context`) salva o *conteúdo do seu ambiente de trabalho*. É a funcionalidade mais poderosa para quem trabalha em múltiplos projetos ou precisa consultar frequentemente a mesma base de código.

Um **Contexto** é um "snapshot" nomeado de um ou mais arquivos e diretórios, processado e salvo em disco para ser reutilizado a qualquer momento.

---

## O Ciclo de Vida de um Contexto

A utilização de contextos segue um fluxo simples e poderoso:

1.  **📦 Criar (`/context create`)**: Você define um conjunto de arquivos e pastas, processa-os com um modo específico (ex: `smart`, `chunked`) e salva o resultado com um nome.
2.  **📌 Anexar (`/context attach`)**: Você "anexa" um ou mais contextos salvos à sua sessão de conversa atual.
3.  **🧠 Usar**: Enquanto estiver anexado, o conteúdo do contexto será **automaticamente** enviado para a IA em todos os seus prompts, fornecendo um conhecimento profundo e contínuo sobre seu projeto.
4.  **✂️ Desanexar (`/context detach`)**: Quando não precisar mais do contexto, você o desanexa para liberar espaço no prompt da IA.

---

## Comandos de Gerenciamento de Contexto

Aqui estão todos os subcomandos disponíveis para gerenciar seus contextos.

#### `create`: Criar um Novo Contexto

Cria e salva um novo contexto a partir de arquivos e diretórios.

**Sintaxe:**
```bash
/context create <nome-do-contexto> <caminho_1> [caminho_2...] [opções]
```
Opções:

-  --mode <modo>  ou  -m : Define como os arquivos serão processados. Os modos são os mesmos do comando  @file :  full ,  summary ,  chunked  ou  smart .
-  --description <texto>  ou  -d : Adiciona uma descrição textual para ajudar a identificar o contexto.
-  --tags <tag1,tag2>  ou  -t : Adiciona tags para facilitar a filtragem e organização.
-  --force ,  -f : Sobrescreve um contexto existente com o mesmo nome.

Exemplo:

###### Cria um contexto 'api-core' com o modo smart, descrição e tags
```bash
/context create api-core ./src/services ./docs/api \
  --mode smart \
  --description "Contexto com o núcleo da API e documentação" \
  --tags golang,api
```
--------

####  `attach`  e  `detach` : Anexar e Desanexar da Sessão

Estes comandos controlam quais contextos estão ativos na sua conversa atual.

-  /context `attach` <nome-do-contexto> : Anexa um contexto à sessão.
-  /context `detach` <nome-do-contexto> : Remove um contexto da sessão.
-  /context `attached` : Lista todos os contextos atualmente anexados.

#### Anexação Avançada de Chunks

Se um contexto foi criado com  --mode=chunked , você pode anexar partes específicas dele:

-  --chunk <N> : Anexa apenas o chunk de número  N .
-  --chunks <N,M,...> : Anexa uma lista de chunks específicos.
-  --priority <num> : Define a ordem em que os contextos anexados são enviados (menor número = maior prioridade).

Exemplo:

Anexa apenas os chunks 1 e 3 do contexto 'legado-db' com alta prioridade
```bash
/context attach legado-db --chunks 1,3 --priority 10
```
--------

###  `list` ,  `show` , e  `inspect` : Visualizar Contextos

Estes comandos ajudam você a entender o que há em seus contextos salvos.

-  /context `list` : Lista todos os contextos disponíveis, com metadados básicos.
-  /context `show` <nome> : Exibe informações detalhadas de um contexto, incluindo a lista completa de arquivos.
-  /context `inspect` <nome> : Fornece uma análise estatística profunda do contexto, como distribuição de tipos de arquivo, contagem de linhas e análise de tamanho dos chunks. Use  --chunk N  para inspecionar um chunk específico.

--------

### Outros Comandos de Gerenciamento

-  /context `delete` <nome> : Deleta um contexto permanentemente.
-  /context `merge` <novo-nome> <ctx1> <ctx2> : Combina múltiplos contextos em um novo, removendo arquivos duplicados.
-  /context `export` <nome> <caminho.json> : Exporta um contexto para um arquivo JSON, facilitando o backup e compartilhamento.
-  /context `import` <caminho.json> : Importa um contexto a partir de um arquivo JSON.
-  /context `metrics` : Mostra estatísticas globais sobre todos os seus contextos (número total, tamanho, etc.).
-  /context `help` : Exibe uma tela de ajuda específica para os comandos de contexto.

--------

## Próximos Passos

Você agora conhece os recursos mais poderosos de automação e gerenciamento de contexto do ChatCLI. Para finalizar, vamos documentar as funcionalidades que garantem a portabilidade e a integração da ferramenta em scripts.

➡️ Próximo: [Modo Não-Interativo e Casos de Uso](/docs/features/non-interactive-mode/)


---