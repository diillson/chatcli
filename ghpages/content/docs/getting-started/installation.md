+++
title = "Instalação e Configuração"
linkTitle = "Instalação"
weight = 10
description = "Aprenda a instalar o ChatCLI e a configurar suas chaves de API para começar a usar."
+++

## Pré-requisitos

Antes de começar, certifique-se de que você tem os seguintes requisitos instalados e configurados em seu sistema:

1.  **Go (versão 1.25 ou superior)**: O ChatCLI é uma aplicação Go. Você precisará do Go para compilar e instalar a ferramenta.
    *   Para verificar sua versão, execute: `go version`
2.  **Git**: O Go utiliza o Git para baixar dependências de repositórios públicos.
3.  **Chave de API ou conta OAuth**: Você precisará de uma chave de API de pelo menos um provedor de LLM suportado (ex: OpenAI, Anthropic, Google, etc.), **ou** uma conta com plano ativo (ChatGPT Plus/Codex, Claude Pro) para autenticação via OAuth.

---

## 1. Instalação

A maneira mais recomendada de instalar o `chatcli` é através do comando `go install`. Isso irá baixar o código-fonte, compilar o binário e movê-lo para o seu diretório `$GOPATH/bin`.

Abra seu terminal e execute o seguinte comando:

{{< command >}}
go install github.com/diillson/chatcli@latest
{{< /command >}}
#### Adicionando ao seu PATH

Para que o comando  chatcli  seja acessível de qualquer lugar no seu terminal, certifique-se de que o diretório de binários do Go está no  PATH  do seu sistema.

Adicione a seguinte linha ao seu arquivo de configuração de shell (como  .bashrc ,  .zshrc , etc.):

export PATH=$PATH:$(go env GOPATH)/bin

Após adicionar a linha, recarregue seu terminal ou execute  source ~/.your_shell_rc_file .

--------

## 2. Configuração Inicial

O ChatCLI é configurado principalmente através de um arquivo  .env  na raiz do diretório onde você executa o comando.

1. Crie o arquivo  .env :

Na sua pasta de usuário ( HOME ) ou na pasta do seu projeto, crie um arquivo chamado  .env .

2. Adicione sua Chave de API:

Abra o arquivo  .env  e adicione a configuração para o provedor que você deseja usar. A estrutura é  LLM_PROVIDER  e a chave  _API_KEY  correspondente.

### Veja um exemplo para configurar a OpenAI:

1. Defina o provedor principal que você deseja usar
LLM_PROVIDER=OPENAI

2. Adicione a chave de API para o provedor escolhido
OPENAI_API_KEY="sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

###### (Opcional) Você pode definir um modelo específico. Se não, o padrão será usado (ex: gpt-4o-mini).
###### OPENAI_MODEL="gpt-4o"

> **Nota:** Você só precisa configurar as chaves para os provedores que pretende usar. O ChatCLI saberá quais estão disponíveis com base nas chaves que encontrar.

#### Alternativa: Autenticação via OAuth

Se você possui um plano **ChatGPT Plus/Codex** ou **Claude Pro**, pode autenticar diretamente pelo terminal sem precisar de API keys:

```bash
# Inicie o ChatCLI (funciona sem credenciais)
chatcli

# Faça login via OAuth
/auth login openai-codex    # Para OpenAI
/auth login anthropic       # Para Anthropic
```

O navegador abrirá automaticamente. Após autorizar, o provedor estará disponível no `/switch`. Consulte a [documentação de OAuth](/docs/features/oauth-authentication/) para mais detalhes.

--------

## 3. Verificando a Instalação

Para garantir que tudo foi instalado e configurado corretamente, execute os seguintes comandos:

1. Verifique a versão:
Este comando confirma que o binário está no seu  PATH  e é executável.
{{< command >}}chatcli --version{{< /command >}}

2. Faça sua primeira pergunta:
Execute  chatcli  sem argumentos para entrar no modo interativo e faça uma pergunta simples para testar a conexão com a API.
{{< command >}}O que é a equação de Dirac?{{< /command >}}


###### Se você receber uma resposta da IA, {{< notice "success">}}parabéns! O ChatCLI está pronto para ser usado.{{< /notice >}}

--------

## Próximos Passos

Agora que a ferramenta está funcionando, aprenda sobre os recursos essenciais:

➡️ Próximo: [**Uso Básico e Comandos Principais**](/docs/core-concepts/basic-usage/)


---
