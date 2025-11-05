+++
title = "Receita: Depurando Logs de Erro com Pipes"
linkTitle = "Depurar Logs de Erro"
weight = 20
description = "Aprenda a usar pipes do shell e o modo one-shot para analisar logs complexos e encontrar a causa raiz de erros rapidamente."
+++

Analisar arquivos de log extensos pode ser uma tarefa demorada e frustrante. Esta receita mostrará como usar o **ChatCLI** em conjunto com `cat`, `grep` ou `kubectl logs` para transformar a IA em seu analista de logs pessoal.

### O Problema

Você está enfrentando um bug em produção. O arquivo de log tem milhares de linhas com `INFO`, `WARN` e `ERROR`. Encontrar a exceção relevante e entender a cascata de eventos que levou ao erro é como procurar uma agulha no palheiro.

### Ingredientes

-   Acesso a um arquivo de log (ex: `app.log`, `/var/log/syslog`).
-   Ou acesso a um comando que gera logs (ex: `kubectl logs <pod-name>`).

---

### Passo a Passo

#### Passo 1: Canalize (Pipe) a Saída do Log para o ChatCLI

A mágica acontece ao usar o operador pipe (`|`) do seu shell para enviar a saída de um comando diretamente para o `chatcli` no modo one-shot (`-p`).

**Cenário 1: Analisando um arquivo de log completo**

Vamos pedir à IA para identificar o erro mais frequente e explicar sua causa.

{{< command >}}
cat /var/log/nginx/error.log | chatcli -p "Analise este log do Nginx, identifique o erro mais recorrente, explique a causa provável e sugira um comando para corrigi-lo."
{{< /command >}}

**Cenário 2: Analisando logs de um container Kubernetes**

Se você é um engenheiro de DevOps, pode fazer isso diretamente com `kubectl`.

{{< command >}}
kubectl logs my-app-pod-xyz-123 | chatcli -p "Este é o log de um pod que está reiniciando. Encontre a exceção fatal (panic) e me diga em qual arquivo e linha do código-fonte ela provavelmente ocorreu."
{{< /command >}}

---

#### Passo 2: Refinando a Análise com `grep`

Se o log for gigantesco, enviar tudo pode ser ineficiente. Você pode pré-filtrar as linhas relevantes com `grep` e enviar apenas o contexto do erro para a IA.

Vamos supor que você sabe que o erro está relacionado a uma `NullPointerException`.

{{< command >}}
grep -C 20 "NullPointerException" application.log | chatcli -p "O erro principal é uma NullPointerException. Com base nas 20 linhas de contexto antes e depois, qual é a sequência de chamadas que levou a este erro?"
{{< /command >}}

**Desconstruindo o Comando:**
-   `grep -C 20 "NullPointerException"`: Encontra a linha com o erro e captura **20 linhas de contexto** (`C`) antes e depois.
-   `| chatcli -p "..."`: Envia esse trecho focado para a IA com uma pergunta muito específica, resultando em uma análise mais rápida e precisa.

---

#### Passo 3: Use a Resposta para Agir

A IA retornará uma análise estruturada, economizando seu tempo de leitura e interpretação.

**Exemplo de Resposta da IA (para o Cenário 2):**
```markdown
A análise do log indica uma exceção fatal (panic) originada por um ponteiro nulo.

**Causa Raiz Provável:**
A exceção `panic: runtime error: invalid memory address or nil pointer dereference` ocorre na seguinte pilha de chamadas:
- `main.go:152`: na função `processPayment()`
- `main.go:98`: na função `handleRequest()`

Isso sugere que a variável `dbConnection` na função `processPayment` estava nula ao ser chamada.

**Sugestão de Correção:**
Verifique se a conexão com o banco de dados foi inicializada corretamente antes da chamada a `processPayment()` na linha 98 do arquivo `main.go`. Adicione uma verificação de nulidade:

`if dbConnection == nil { return fmt.Errorf("conexão com o banco de dados não está ativa") }`
```

{{< notice "success" >}}
Resultado: Em vez de gastar minutos (ou horas) lendo logs, você obteve a causa raiz, a localização no código e uma sugestão de correção em segundos.
{{< /notice >}}

---