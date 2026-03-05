+++
title = "Migracao de Configuracao"
linkTitle = "Migracao de Config"
weight = 79
description = "Sistema versionado de migracao de configuracao com backup automatico, rollback e execucao sequencial de migracoes."
icon = "sync_alt"
+++

O ChatCLI inclui um sistema de **migracao versionada de configuracao** que garante upgrades seguros e automaticos entre versoes. Quando o schema de configuracao muda, as migracoes sao aplicadas sequencialmente, com backup completo antes de qualquer alteracao.

---

## Como Funciona

Cada versao do ChatCLI define um `CurrentConfigVersion`. Quando o usuario atualiza o binario, o sistema compara a versao salva com a versao atual e aplica as migracoes necessarias:

```
v0 (sem arquivo de versao)
  ↓ migracao v0→v1
v1 (CurrentConfigVersion)
  ↓ futuras migracoes
v2, v3, ...
```

### Ciclo de Migracao

1. **Detecta necessidade**: Compara versao atual com `CurrentConfigVersion`
2. **Cria backup**: Salva configuracao completa em `~/.chatcli/backups/config_backup_<timestamp>.json`
3. **Aplica migracoes**: Executa cada migracao na sequencia (v0→v1, v1→v2, ...)
4. **Atualiza versao**: Grava a nova versao em `~/.chatcli/config_version.json`
5. **Em caso de falha**: Preserva os valores originais e o backup permanece disponivel

---

## Migracao v0 → v1

A migracao incluida normaliza configuracoes legadas:

| Transformacao | Antes | Depois |
|:-------------|:------|:-------|
| Normaliza provider | `openai` | `OPENAI` |
| Renomeia API key | `CHATCLI_API_KEY` | `OPENAI_API_KEY` |
| Renomeia modelo | `CHATCLI_MODEL` | `OPENAI_MODEL` |
| Default skills | (ausente) | `CHATCLI_SKILLS_ENABLED=true` |
| Default safety | (ausente) | `CHATCLI_SAFETY_ENABLED=false` |
| Default tool use | (ausente) | `CHATCLI_NATIVE_TOOL_USE=true` |

---

## API

### Verificar Necessidade de Migracao

```go
registry := config.NewMigrationRegistry(configDir, logger)
needs, current, target, err := registry.NeedsMigration()
if needs {
    fmt.Printf("Migracao necessaria: v%d → v%d\n", current, target)
}
```

### Executar Migracao

```go
values := map[string]interface{}{
    "LLM_PROVIDER":    "openai",
    "CHATCLI_API_KEY": "sk-test",
}

result, err := registry.Migrate(values)
// result["LLM_PROVIDER"] == "OPENAI"
// result["OPENAI_API_KEY"] == "sk-test"
// result["CHATCLI_SKILLS_ENABLED"] == "true"
```

### Backup e Rollback

```go
// Backup manual
path, err := registry.Backup(values)
fmt.Println("Backup salvo em:", path)

// Rollback
err = registry.Rollback(backupPath)
```

### Registrar Migracao Custom

```go
registry.Register(1, func(v map[string]interface{}) (map[string]interface{}, error) {
    // Migracao v1 → v2
    v["NEW_FEATURE_ENABLED"] = "true"
    return v, nil
})
```

---

## Arquivo de Versao

A versao e armazenada em `~/.chatcli/config_version.json`:

```json
{
  "version": 1,
  "migrated_at": "2026-03-04T14:30:00Z"
}
```

---

## Backups

Os backups sao armazenados em `~/.chatcli/backups/` com timestamp:

```
~/.chatcli/backups/
├── config_backup_20260304_143000.json
├── config_backup_20260301_100000.json
└── ...
```

Cada backup contem o snapshot completo dos valores de configuracao antes da migracao.

---

## Seguranca

- **Atomicidade**: Cada migracao e aplicada sobre uma copia dos valores. Se falhar, os originais sao preservados
- **Sequencialidade**: Migracoes sao executadas estritamente em ordem crescente de versao
- **Backup obrigatorio**: Um backup e criado automaticamente antes de qualquer migracao
- **Idempotencia**: Se ja estiver na versao atual, `Migrate()` retorna os valores inalterados
