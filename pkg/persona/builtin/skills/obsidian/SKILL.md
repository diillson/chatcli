---
name: obsidian
description: Read, search, create, append, and link notes in an Obsidian vault (plain markdown on the filesystem). Use when asked to "add a note", "find my notes about", "append to my daily note", "create a note in my vault", or anything about Obsidian.
allowed-tools: ["@read", "@search", "@coder", "Read", "Write", "Edit", "Bash"]
triggers:
  - obsidian
  - my vault
  - add a note
  - daily note
  - find my notes
  - note about
  - cria uma nota
  - minha vault
  - anotação
  - nota sobre
---

# Obsidian Vault

Filesystem-first Obsidian work: the vault is just markdown files, so use ChatCLI's file
tools (`@read`, `@search`, `Read`/`Write`/`Edit`) over shell `cat`/`find` — they give line
numbers, pagination, and handle paths with spaces.

## Resolve the vault path first

Order of resolution:
1. `OBSIDIAN_VAULT_PATH` env var, if set.
2. Common defaults: `~/Documents/Obsidian Vault` (macOS/Windows), `~/obsidian`, `~/vault`.
3. If unknown, ask the user for the path.

File tools don't expand `$OBSIDIAN_VAULT_PATH` — resolve it to a concrete absolute path first
(`@coder exec` `echo $OBSIDIAN_VAULT_PATH` on Unix, `$Env:OBSIDIAN_VAULT_PATH` on Windows),
then pass the literal path.

## Read / list / search

- **Read a note** → `@read` with the absolute path.
- **List notes** → `@search` for files matching `*.md` under the vault.
- **Search content** → `@search` (ripgrep-backed) for the term across the vault. Summarize hits
  as `note title — matching line`, don't dump raw output.

## Create a note

Write a new `<vault>/<folder>/<Title>.md`. Start with frontmatter when the vault uses it:
```
---
created: 2026-06-03
tags: [meeting]
---

# Title

...body...
```

## Append (e.g. daily note)

Resolve today's daily note path (often `<vault>/Daily/2026-06-03.md`), then `Edit` to append a
section — never overwrite an existing note's body.

## Wikilinks

Link notes with `[[Note Title]]`. When creating a note that references others, add the
`[[...]]` links so the graph stays connected. Tags are `#tag` inline or in frontmatter.

## Rules

- Prefer file tools; only shell out to resolve the vault path.
- Never clobber an existing note — append or create a new one unless told to replace.
- Keep the user's existing frontmatter/tag conventions; mirror what neighboring notes do.
