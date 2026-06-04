---
name: linear
description: Manage Linear issues from the terminal via the official GraphQL API and the user's API key — list/search issues, create issues, update status/assignee, add comments. Use when asked to "create a Linear issue", "what's assigned to me in Linear", "move this issue to done", "comment on LIN-123".
allowed-tools: ["@coder", "Bash"]
triggers:
  - linear
  - create a linear issue
  - my linear issues
  - assigned to me in linear
  - move issue to
  - linear issue
  - criar issue no linear
  - minhas issues do linear
---

# Linear

Drive Linear through its **GraphQL API** with the user's own key — opt-in, no middleman.

## Setup

Set `LINEAR_API_KEY` (Settings → API → Personal API keys). All calls:
```
curl -s https://api.linear.app/graphql -H "Authorization: $LINEAR_API_KEY" \
  -H "Content-Type: application/json" -d '{"query":"..."}'
```

## My open issues

```
{"query":"{ viewer { assignedIssues(filter:{state:{type:{neq:\"completed\"}}}) { nodes { identifier title state { name } } } } }"}
```

## Search issues

```
{"query":"query($q:String!){ searchIssues(term:$q){ nodes{ identifier title url } } }","variables":{"q":"login bug"}}
```

## Create an issue

First resolve the team id (`{ teams { nodes { id key name } } }`), then:
```
{"query":"mutation($t:String!,$ti:String!,$d:String){ issueCreate(input:{teamId:$t,title:$ti,description:$d}){ success issue{ identifier url } } }","variables":{"t":"TEAM_ID","ti":"Fix X","d":"details"}}
```

## Update status / assignee / comment

- Resolve workflow state ids (`{ workflowStates { nodes { id name } } }`), then `issueUpdate(input:{id, stateId})`.
- Comment: `commentCreate(input:{issueId, body})`.

## Rules

- If `LINEAR_API_KEY` is unset, explain how to create one and stop.
- Resolve human references (team key, status name, `LIN-123` identifier) to ids before mutating.
- Echo the issue identifier + URL after creating/updating.
