+++
title = "Receita: Refatoração Segura com Modo Agente"
linkTitle = "Refatoração com Agente"
weight = 40
description = "Use o poder do Modo Agente para planejar e executar uma refatoração de código passo a passo, com segurança e controle total."
+++

Refatorar código, como renomear uma variável importante em múltiplos arquivos, pode ser arriscado. Um erro pode quebrar a aplicação. Esta receita mostra como delegar essa tarefa ao **Modo Agente**, usando seu poder de planejamento e execução controlada para realizar a refatoração com segurança.

### O Problema

Você precisa renomear uma variável de configuração, por exemplo, de `DATABASE_URL` para `POSTGRES_DSN`, em todo o seu projeto. Fazer isso manualmente é tedioso e propenso a erros. Usar `sed` ou `find-and-replace` em massa pode ser perigoso se o nome da variável for uma substring comum.

### Ingredientes

-   Um projeto de código onde você precisa realizar a refatoração.
-   O ChatCLI no modo interativo.

---

### Passo a Passo

#### Passo 1: Delegue a Tarefa ao Agente

Inicie o Modo Agente com uma instrução clara e detalhada. Seja específico sobre o que precisa ser feito.

{{< command >}}
/agent em todo o projeto, encontre todos os arquivos que contêm a string "DATABASE_URL" e a substitua por "POSTGRES_DSN". Mostre-me os arquivos que serão modificados antes de fazer qualquer alteração.
{{< /command >}}

**Desconstruindo o Prompt:**
-   **"em todo o projeto..."**: Define o escopo da operação.
-   **"encontre... e substitua..."**: Descreve a ação de refatoração.
-   **"Mostre-me os arquivos... antes de fazer qualquer alteração"**: Esta é uma instrução de segurança crucial. Estamos pedindo ao agente para criar um plano que nos permita validar o escopo antes de executar a mudança.

---

#### Passo 2: Revise o Plano de Ação

A IA irá analisar sua solicitação e propor um plano. Um bom plano para esta tarefa provavelmente terá duas etapas:

```plaintext
📋 PLANO (visão compacta)
  ⏳ #1: Listar arquivos afetados — grep -rl "DATABASE_URL" .
  ⏳ #2: Executar a substituição — find . -type f -name "*" -exec sed -i 's/DATABASE_URL/POSTGRES_DSN/g' {} +
```
{{< notice "warning" >}}
Ponto de Atenção: O comando  sed -i  modifica arquivos no local. Embora o plano seja lógico, a segunda etapa é destrutiva. É aqui que o controle do Modo Agente se torna vital.
{{< /notice >}}

--------

#### Passo 3: Execute e Valide o Primeiro Passo

Em vez de executar tudo de uma vez, vamos executar apenas o primeiro comando para validar o escopo.

No prompt do agente, digite  1  e pressione Enter.

➤ Sua escolha: 1

###### O ChatCLI executará  grep -rl "DATABASE_URL" .  e mostrará a lista de arquivos que contêm a string.

Exemplo de Saída:
```plaintext
🧾 ÚLTIMO RESULTADO
  ./config/app.go
  ./internal/database/connection.go
  ./docker-compose.yml
```
> Agora você pode revisar esta lista. Se ela contiver um arquivo inesperado (ex:  README.md ), você sabe que a abordagem precisa ser refinada.

--------

#### Passo 4: Edite o Plano para Maior Segurança (Opcional, mas recomendado)

O comando  sed  é poderoso, mas pode ser arriscado. Vamos editar o comando 2 para usar uma extensão de backup, criando cópias dos arquivos originais.

No prompt do agente, digite  `e2`  (editar comando 2). O ChatCLI pedirá o novo comando.
```plaintext
Comando 2/2 (shell): find . -type f -name "*" -exec sed -i'.bak' 's/DATABASE_URL/POSTGRES_DSN/g' {} +
```

Agora, o comando  sed  criará um arquivo  .bak  para cada arquivo modificado, permitindo uma reversão fácil se algo der errado.

--------

#### Passo 5: Execute a Refatoração e Conclua

Com o plano validado e aprimorado, execute o segundo comando digitando  2 .

O agente executará a substituição. Após a conclusão, você pode verificar as mudanças com  git diff  e, se tudo estiver correto, remover os arquivos de backup. Você pode até mesmo pedir ao agente para fazer isso:

➤ Sua escolha: c2  (Continuar após a execução do passo 2)

> A refatoração foi concluída com sucesso. Agora, por favor, crie um comando para encontrar e remover todos os arquivos .bak no projeto.

{{< notice "success" >}}
Resultado: Você realizou uma refatoração em toda a base de código de forma interativa e segura. O Modo Agente atuou como um par programador, permitindo que você válidasse cada etapa crítica antes da execução.
{{< /notice >}}


---