# Como Contribuir para o ChatCLI

Olá! Primeiramente, obrigado pelo seu interesse em contribuir para o **ChatCLI**. Estamos entusiasmados em receber ajuda da comunidade para tornar esta ferramenta ainda melhor.

Toda contribuição é bem-vinda, desde a correção de um simples erro de digitação na documentação até a implementação de uma nova funcionalidade complexa.

## Como Começar

Para garantir um processo tranquilo, siga estes passos para configurar seu ambiente de desenvolvimento:

1.  **Faça um Fork do Repositório**:
    Clique no botão "Fork" no canto superior direito da página do repositório no GitHub para criar uma cópia em sua própria conta.

2.  **Clone seu Fork**:
    Clone o repositório para sua máquina local. Substitua `seu-usuario` pelo seu nome de usuário do GitHub.
    ```bash
    git clone https://github.com/seu-usuario/chatcli.git
    cd chatcli
    ```

3.  **Configure as Dependências**:
    O projeto usa Go Modules. Para baixar todas as dependências, execute:
    ```bash
    go mod tidy
    ```

4.  **Crie seu Arquivo `.env`**:
    Para testar localmente, você precisará de um arquivo `.env` com suas chaves de API. Copie o arquivo de exemplo ou crie um novo na raiz do projeto:
    ```env
    # .env
    LLM_PROVIDER=OPENAI
    OPENAI_API_KEY="sk-xxxxxxxxxx"
    ```

5.  **Compile e Execute Localmente**:
    Para compilar um binário de desenvolvimento, use o comando `go build`:
    ```bash
    go build -o chatcli .
    ```
    Agora você pode executar sua versão local com `./chatcli`.

6.  **Crie uma Nova Branch**:
    Crie uma branch descritiva para sua alteração.
    ```bash
    # Para uma nova funcionalidade:
    git checkout -b feat/adicionar-suporte-ao-modelo-xyz

    # Para uma correção de bug:
    git checkout -b fix/corrigir-erro-no-comando-git
    ```

---

## Estilo de Código e Convenções

*   **Formatação**: Todo o código Go deve ser formatado com `gofmt`. Execute `gofmt -w .` antes de commitar suas alterações.
*   **Mensagens de Commit**: Siga o padrão [Conventional Commits](https://www.conventionalcommits.org/). Isso nos ajuda a automatizar o versionamento e a gerar changelogs.
    *   `feat:` para novas funcionalidades.
    *   `fix:` para correções de bugs.
    *   `docs:` para alterações na documentação.
    *   `style:` para mudanças de formatação que não alteram a lógica.
    *   `refactor:` para refatorações de código.
    *   `test:` para adicionar ou corrigir testes.

---

## Submetendo Alterações (Pull Request)

1.  **Faça suas alterações**: Implemente sua funcionalidade ou correção de bug.
2.  **Adicione Testes**: Se você adicionou uma nova funcionalidade, por favor, inclua testes unitários para garantir que ela funcione como esperado e para evitar regressões no futuro.
3.  **Verifique se Todos os Testes Passam**: Antes de submeter, rode a suíte completa de testes:
    ```bash
    go test ./...
    ```
4.  **Faça o Commit das Suas Alterações**: Use uma mensagem de commit clara e descritiva.
    ```bash
    git commit -m "feat: Adiciona suporte para o comando @docker"
    ```
5.  **Envie para o seu Fork**:
    ```bash
    git push origin feat/adicionar-suporte-ao-modelo-xyz
    ```
6.  **Abra um Pull Request (PR)**: Vá para o repositório original do `diillson/chatcli` no GitHub. Você verá um botão para criar um Pull Request a partir da sua branch. Preencha o template do PR, descrevendo o que você fez e por quê.

--------