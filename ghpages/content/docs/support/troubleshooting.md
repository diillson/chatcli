+++
title = "Solução de Problemas (Troubleshooting)"
linkTitle = "Troubleshooting"
weight = 10
description = "Encontre soluções rápidas para os problemas e erros mais comuns ao usar o ChatCLI."
+++

Encontrou um problema? Sem pânico. Esta página lista os erros mais comuns e como resolvê-los rapidamente.

---

### **Problema: Comando `chatcli` não encontrado**

> **Sintomas:** Ao executar `chatcli` no terminal, você recebe uma mensagem como `bash: chatcli: command not found` ou `zsh: command not found: chatcli`.

> **Solução:** Isso significa que o diretório de binários do Go não está no `PATH` do seu sistema.
>
> 1.  Abra seu arquivo de configuração de shell (`~/.bashrc`, `~/.zshrc`, etc.).
> 2.  Adicione a seguinte linha ao final do arquivo:
>     ```bash
>     export PATH=$PATH:$(go env GOPATH)/bin
>     ```
> 3.  Reinicie seu terminal ou execute `source ~/.your_shell_rc_file` para aplicar as alterações.

---

### **Problema: Erro "Nenhum provedor LLM está configurado"**

> **Sintomas:** O ChatCLI encerra imediatamente após iniciar, exibindo uma mensagem sobre provedores ausentes.

> **Solução:** Você não configurou nenhuma chave de API no seu arquivo `.env`.
>
> 1.  Crie ou abra o arquivo `.env` no diretório onde você executa o `chatcli` (ou no seu diretório `HOME`).
> 2.  Adicione as credenciais para pelo menos um provedor. Exemplo para OpenAI:
>     ```env
>     LLM_PROVIDER=OPENAI
>     OPENAI_API_KEY="sk-sua-chave-secreta-aqui"
>     ```
> 3.  Salve o arquivo e execute `chatcli` novamente.

---

### **Problema: Alterações no `.env` não têm efeito**

> **Sintomas:** Você alterou o `LLM_PROVIDER` ou outro valor no `.env`, mas o ChatCLI continua usando a configuração antiga.

> **Solução:** O ChatCLI carrega as configurações durante a inicialização. Se você alterar o arquivo `.env` enquanto a aplicação está rodando, é preciso recarregar as configurações.
>
> Use o comando `/reload`:
> ```bash
> ❯ /reload
> ```
> O ChatCLI irá recarregar todas as variáveis do seu arquivo `.env` instantaneamente.

---

### **Problema: Comando `@file` não encontra um arquivo/diretório**

> **Sintomas:** Você recebe um erro como "o arquivo não existe" ou "caminho não encontrado".

> **Solução:** Verifique o caminho que você forneceu.
>
> *   **Caminhos Relativos**: Os caminhos são relativos ao diretório onde você executou o `chatcli`. Se você estiver em `/home/user` e o arquivo estiver em `/home/user/project/src/main.go`, o comando correto é `@file ./project/src/main.go`.
> *   **Caminho Home (`~`)**: Você pode usar `~` como um atalho para seu diretório `HOME`. Ex: `@file ~/documentos/notas.txt`.
> *   **Permissões**: Certifique-se de que você tem permissão de leitura para o arquivo e de execução para os diretórios no caminho. Use `ls -l <caminho>` para verificar.

---

### **Problema: O Modo Agente (`/agent`) mostra o plano, mas não executa nada**

> **Sintomas:** A IA apresenta um plano de ação, mas o programa apenas aguarda sua entrada sem executar os comandos.

> **Solução:** Isso é o comportamento esperado. O Modo Agente é interativo e exige sua aprovação explícita para executar qualquer comando.
>
> *   Para executar **um único comando**, digite seu número (ex: `1`) e pressione Enter.
> *   Para executar **todos os comandos** em sequência, digite `a` e pressione Enter.

---

### **Problema: O provedor Ollama não é detectado**

> **Sintomas:** Mesmo com `OLLAMA_ENABLED=true` no seu `.env`, a aplicação informa que o provedor não está disponível.

> **Solução:** O `chatcli` precisa se comúnicar com o servidor Ollama, que deve estar rodando localmente.
>
> 1.  **Servidor Ollama**: Certifique-se de que o servidor do Ollama está em execução. Em outro terminal, rode `ollama serve`.
> 2.  **Modelo Local**: Verifique se você tem pelo menos um modelo baixado. Rode `ollama list` para ver os modelos disponíveis. Se não tiver nenhum, baixe um (ex: `ollama pull llama3`).
> 3.  **URL Base**: Se o seu servidor Ollama não está rodando no endereço padrão (`http://localhost:11434`), defina a variável `OLLAMA_BASE_URL` no seu arquivo `.env`.

---