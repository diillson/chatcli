+++
title = "Modo Não-Interativo (One-Shot)"
linkTitle = "Modo One-Shot"
weight = 30
description = "Aprenda a usar o ChatCLI em scripts, automações e pipelines através das flags de linha de comando e do uso de pipes."
+++

O **ChatCLI** não foi projetado apenas para conversas interativas. Ele pode ser uma ferramenta poderosa em seus scripts de automação, pipelines de CI/CD e aliases de shell graças ao seu modo "one-shot".

Neste modo, você pode fazer uma única pergunta, obter a resposta e sair, tudo em um único comando, tornando-o perfeito para integração com outras ferramentas.

---

## Usando as Flags `-p` ou `--prompt`

A maneira mais direta de usar o modo one-shot é com as flags `-p` ou `--prompt`. O ChatCLI executará a pergunta, imprimirá a resposta da IA no `stdout` e encerrará.

**Exemplo:**
```bash
chatcli -p "Qual é o comando para listar todos os processos que consomem mais de 500MB de RAM no Linux?"
```
A saída será apenas a resposta da IA, pronta para ser lida ou processada por outro script.

--------

## Integrando com Pipes ( | )

Uma das formas mais poderosas de usar o modo one-shot é combinando-o com outros comandos de linha de comando através de pipes. O ChatCLI automaticamente detecta se está recebendo dados via  stdin  e os utiliza como contexto.

Exemplo 1: Resumir um  git diff
```bash
git diff | chatcli -p "Baseado neste diff, gere uma sugestão de mensagem de commit seguindo o padrão Conventional Commits."
```
Exemplo 2: Analisar um arquivo de log
```bash
cat /var/log/nginx/error.log | chatcli -p "Encontre o erro mais comum neste log e sugira uma possível causa e solução."
```
--------

## Flags Disponíveis no Modo One-Shot

Você pode customizar o comportamento do modo one-shot com as seguintes flags:
```bash
Flag                  │ Descrição                                                                                               
──────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────────────────
-p ,  --prompt        │ A pergunta ou instrução principal para a IA.                                                            
--provider <nome>     │ Sobrescreve o provedor de LLM padrão (ex:  OPENAI ,  CLAUDEAI ).                                        
--model <nome>        │ Sobrescreve o modelo de IA a ser usado (ex:  gpt-4o ,  claude-3-5-sonnet-20240620 ).                    
--timeout <duração>   │ Define um tempo limite para a requisição (padrão:  5m ). Ex:  10s ,  1m .                               
--max-tokens <num>    │ Sobrescreve o número máximo de tokens na resposta.                                                      
--no-anim             │ Desabilita a animação "Pensando...", ideal para saídas limpas em scripts.                               
--agent-auto-exec     │ No modo agente one-shot, executa o primeiro comando sugerido automaticamente se for considerado seguro.
--realm <nome>        │ (StackSpot) Define o  realm  (tenant) para autenticação.                                                
--agent-id <id>       │ (StackSpot) Define o  Agent ID  a ser usado.
```
Exemplo com Múltiplas Flags:
```bash
cat relatorio.csv | chatcli --provider CLAUDEAI --model claude-3-opus-20240229 -p "Analise estes dados e me dê 3 insights principais."
```
--------

## Modo Agente em One-Shot

Você também pode invocar o Modo Agente de forma não-interativa. Isso é extremamente útil para automações complexas.

Ao usar  `/agent`  ou  `/run`  com a flag  `-p` , o ChatCLI pedirá à IA um plano de ação e o imprimirá. Por padrão, ele não executará os comandos.

Exemplo:
```bash
chatcli -p "/agent encontre todos os arquivos *.tmp no diretório /tmp e os apague."
```
Isso irá exibir o plano sugerido pela IA (ex:  find /tmp -name "*.tmp" -delete ).

Para executar o plano automaticamente, use a flag  `--agent-auto-exec` . A execução só ocorrerá se o validador de segurança interno não detectar comandos perigosos.

A IA irá gerar e, se for seguro, executar o comando para limpar os arquivos temporários.
```bash
chatcli -p "/agent encontre e apague os arquivos .tmp" --agent-auto-exec
```
--------