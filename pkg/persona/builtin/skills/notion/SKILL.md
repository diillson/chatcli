---
name: notion
description: Read and write Notion — search pages/databases, query a database, create pages, append blocks — via the official Notion REST API and the user's own integration token. Use when asked to "add to Notion", "find in my Notion", "create a Notion page", "update my Notion database".
allowed-tools: ["@coder", "Bash", "@webfetch"]
triggers:
  - notion
  - add to notion
  - find in notion
  - create a notion page
  - update notion
  - no notion
  - adicionar no notion
  - criar página no notion
---

# Notion

Drive Notion through its **official REST API** with the user's own integration token — no
third-party middleman. This needs a token, so it's opt-in (the user's key, generic API).

## Setup (one-time)

1. User creates an internal integration at notion.so/my-integrations and copies the secret.
2. Set `NOTION_TOKEN` (env). Share the relevant pages/databases with that integration.

All calls send `Authorization: Bearer $NOTION_TOKEN` and `Notion-Version: 2022-06-28`.

## Search

```
curl -s -X POST https://api.notion.com/v1/search \
  -H "Authorization: Bearer $NOTION_TOKEN" -H "Notion-Version: 2022-06-28" \
  -H "Content-Type: application/json" \
  -d '{"query":"roadmap"}'
```

## Query a database

```
curl -s -X POST "https://api.notion.com/v1/databases/$DB_ID/query" \
  -H "Authorization: Bearer $NOTION_TOKEN" -H "Notion-Version: 2022-06-28" \
  -H "Content-Type: application/json" -d '{"page_size":20}'
```

## Create a page

```
curl -s -X POST https://api.notion.com/v1/pages \
  -H "Authorization: Bearer $NOTION_TOKEN" -H "Notion-Version: 2022-06-28" \
  -H "Content-Type: application/json" -d '{
    "parent": {"database_id": "'"$DB_ID"'"},
    "properties": {"Name": {"title": [{"text": {"content": "New task"}}]}}
  }'
```

## Append blocks to a page

`PATCH /v1/blocks/$PAGE_ID/children` with a `children` array of block objects (paragraph,
heading_2, to_do, etc.).

## Rules

- If `NOTION_TOKEN` is unset, explain the integration setup and stop — don't guess.
- The integration only sees pages explicitly shared with it; a 404 usually means "not shared".
- Resolve names→ids (search first) before writing; confirm the target database/page.
