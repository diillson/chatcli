---
name: skill-authoring
description: How and when to author or evolve your own skills with the @skill tool — capturing reusable procedures, project conventions, and workflows the user repeats so they auto-activate later. Activates when you notice something worth remembering as a repeatable procedure, or when the user says "remember how to", "make a skill", "next time do it like this".
allowed-tools: ["@skill", "@memory"]
triggers:
  - make a skill
  - create a skill
  - remember how to
  - next time do it like this
  - save this workflow
  - turn this into a skill
  - crie uma skill
  - lembre como fazer
  - salve esse procedimento
  - da próxima vez faça assim
---

# Skill Authoring (self-evolving skills)

You can grow your own capabilities. When you learn something **reusable**, capture it as a
skill with `@skill` so it auto-activates the next time it's relevant — in this session and every
future one. This is how you get better over time instead of relearning the same thing.

## @memory vs @skill — pick the right store

- **`@memory`** = durable **facts** about the user/project: "the user prefers tabs", "prod is
  in us-east-1", "the user's name is Ana". Short, declarative.
- **`@skill`** = a reusable **procedure or body of knowledge** with **triggers**: "how to deploy
  this project", "the team's PR conventions", "how to run the data pipeline". Multi-step,
  activated by keywords.

If it's a one-line fact → `@memory`. If it's "how to do X" that you'd want to follow again → `@skill`.

## When to author a skill

Author (or update) a skill when you notice:
- A **workflow the user repeated** 2+ times, or asked you to "always do it this way".
- A **project convention** you discovered (build/test/deploy commands, branch naming, lint rules).
- A **non-obvious procedure** you worked out (a fix, a setup, a sequence of tool calls).
- The user explicitly says "make a skill" / "remember how to do this".

Don't author for one-offs, trivia, or things already covered by an existing skill (update that
one instead — check `@skill list` / `@skill show` first).

## How to write a good skill

```
<tool_call name="@skill" args='{"cmd":"create","args":{
  "name":"deploy-acme-api",
  "description":"How to deploy the Acme API. Use when asked to deploy/ship the Acme API.",
  "triggers":["deploy acme","ship the api","release acme"],
  "content":"# Deploy Acme API\n\n1. Run the tests: make test\n2. Tag: git tag vX.Y.Z\n3. ...",
  "allowed_tools":["@coder","Bash"]
}}' />
```

Quality bar:
- **name**: kebab-case, specific (`deploy-acme-api`, not `deploy`).
- **description**: one line that says *what it is* and *when to use it* — this is what decides
  whether the skill gets activated, so make it precise.
- **triggers**: the natural phrases a user would say. Include both languages if the user mixes.
- **content**: concrete, step-by-step, with the actual commands/paths. Write it so a fresh
  session could follow it. Prefer the OS-independent native tools where possible.

## Evolving a skill

When a skill turns out to be wrong or incomplete, `@skill update` it (don't pile up duplicates).
`@skill show <name>` first to see the current content, then update with the corrected version.

## Rules

- Confirm with the user before saving a skill that encodes a preference you only inferred once.
- Never store secrets/tokens in a skill — those are environment/credential concerns.
- Keep skills focused: one procedure per skill, not a kitchen sink.
