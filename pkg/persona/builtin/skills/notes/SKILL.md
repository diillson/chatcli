---
name: notes
description: Capture and retrieve quick notes in the OS-native notes app — Apple Notes on macOS, a markdown notes folder on Linux, Sticky Notes/OneNote on Windows. For an Obsidian vault specifically, use the obsidian skill. Use when asked to "make a note", "save this", "jot down", "what did I note about".
allowed-tools: ["@coder", "Bash", "Write", "@read", "@search"]
triggers:
  - make a note
  - save this note
  - jot down
  - quick note
  - note this down
  - what did i note
  - anota isso
  - salva uma nota
  - me lembra que anotei
---

# Notes (OS-native)

Capture quick notes using whatever is native to the OS. For Obsidian, defer to the `obsidian`
skill; this one is for the built-in notes experience.

## macOS — Apple Notes (osascript)

```
osascript -e 'tell application "Notes" to make new note at folder "Notes" with properties {name:"Groceries", body:"milk, eggs"}'
```
List/search:
```
osascript -e 'tell application "Notes" to get name of every note'
```

## Windows — OneNote / plain file

OneNote via PowerShell COM is heavy; the reliable cross-tool path is a markdown notes folder
(`$Env:USERPROFILE\Notes\`): append a timestamped entry to a `.md` and search it. Sticky Notes
has no scripting API — don't promise it.

## Linux — markdown notes folder

Use a `~/Notes/` (or `$NOTES_DIR`) folder of markdown files:
- **Create** → `Write` a new `~/Notes/<slug>.md` with a `# Title` and the body.
- **Append** → `Edit` to add a dated bullet to an existing note.
- **Search** → `@search` over `~/Notes/*.md`.

## Cross-platform default

When unsure, use the markdown notes folder approach (works everywhere, greppable, future-proof)
and tell the user where it lives. This keeps notes portable rather than locked in an app.

## Rules

- Don't overwrite existing notes — append or create.
- Echo back what was saved and where, so the user can find it later.
