---
name: airtable
description: Read and write Airtable bases via the official REST API and the user's personal access token — list/filter records, create, update, delete. Use when asked to "add a row to Airtable", "find records in my base", "update this Airtable record".
allowed-tools: ["@coder", "Bash"]
triggers:
  - airtable
  - add to airtable
  - find in airtable
  - update airtable
  - airtable record
  - adicionar no airtable
  - registro no airtable
---

# Airtable

Drive Airtable through its **REST API** with the user's personal access token — opt-in.

## Setup

Set `AIRTABLE_TOKEN` (a personal access token with `data.records:read/write` scopes on the base).
You also need the **base id** (`appXXXX`, from the API docs of the base) and **table name**.
All calls send `Authorization: Bearer $AIRTABLE_TOKEN`.

## List / filter records

```
curl -s "https://api.airtable.com/v0/$BASE_ID/Tasks?maxRecords=20" \
  -H "Authorization: Bearer $AIRTABLE_TOKEN"
# with a formula filter:
curl -s -G "https://api.airtable.com/v0/$BASE_ID/Tasks" \
  -H "Authorization: Bearer $AIRTABLE_TOKEN" \
  --data-urlencode 'filterByFormula={Status}="Open"'
```

## Create a record

```
curl -s -X POST "https://api.airtable.com/v0/$BASE_ID/Tasks" \
  -H "Authorization: Bearer $AIRTABLE_TOKEN" -H "Content-Type: application/json" \
  -d '{"fields":{"Name":"Ship release","Status":"Open"}}'
```

## Update / delete

```
curl -s -X PATCH "https://api.airtable.com/v0/$BASE_ID/Tasks/$REC_ID" \
  -H "Authorization: Bearer $AIRTABLE_TOKEN" -H "Content-Type: application/json" \
  -d '{"fields":{"Status":"Done"}}'
curl -s -X DELETE "https://api.airtable.com/v0/$BASE_ID/Tasks/$REC_ID" \
  -H "Authorization: Bearer $AIRTABLE_TOKEN"
```

## Rules

- If `AIRTABLE_TOKEN`/base id are unset, explain how to get them and stop.
- Field names are case-sensitive and must match the table exactly — list a record first to learn them.
- Echo the record id after create/update; confirm before delete.
