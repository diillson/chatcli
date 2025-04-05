#!/bin/bash

# Script para testar a API de sessão da OpenAI

# Verifica se a chave API está configurada
if [ -z "$OPENAI_API_KEY" ]; then
    echo "Erro: OPENAI_API_KEY não está definida"
    exit 1
fi

# Definir variáveis de ambiente
export OPENAI_USE_SESSION=true
export LOG_LEVEL=debug
export LLM_PROVIDER=OPENAI

echo "Iniciando ChatCLI com API de Sessão da OpenAI..."
echo "Use as seguintes mensagens para testar:"
echo "1. Olá, como você está?"
echo "2. Lembre do que eu acabei de perguntar. Qual foi minha pergunta anterior?"
echo "3. Muito bem! Agora conte até 3."
echo "4. Continue a contagem até 10."
echo "5. Qual foi o resultado da primeira contagem que você fez?"
echo ""
echo "Estas mensagens testam se o contexto está sendo mantido corretamente pelo servidor."
echo ""

# Iniciar a aplicação
./chatcli