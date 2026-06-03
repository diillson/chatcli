---
name: reminders
description: Create and list reminders / to-dos and time-based nudges using native tooling (AppleScript Reminders on macOS, or ChatCLI's own @scheduler for a timed message back to the user). Use when asked to "remind me", "set a reminder", "add a to-do", "don't let me forget".
allowed-tools: ["@scheduler", "@send", "@coder", "Bash"]
triggers:
  - remind me
  - set a reminder
  - add a to-do
  - add a todo
  - don't let me forget
  - me lembre
  - cria um lembrete
  - adicione uma tarefa
  - não me deixe esquecer
---

# Reminders

Two distinct things hide behind "remind me" — pick the right one:

1. **A reminder/to-do in the user's reminders app** (persists in their ecosystem, syncs to
   phone). Use native tooling.
2. **A timed nudge from ChatCLI itself** — "ping me in 2h", "message me tomorrow at 9 to do X".
   Use ChatCLI's built-in `@scheduler` (+ `@send` to deliver), no external app needed. This is
   the self-contained path and works on every OS.

If the user is talking to ChatCLI over a gateway (Telegram/WhatsApp) and wants to *be messaged*,
prefer path 2. If they want it in Apple/Google Reminders, use path 1.

## Path 1 — Native reminders app (OS-specific)

ChatCLI is multi-OS — pick by platform:
- **macOS** → AppleScript `Reminders.app` (no install):
  ```
  osascript -e 'tell application "Reminders" to make new reminder with properties {name:"Call the dentist", due date:(date "2026-06-04 09:00")}'
  ```
  List open items:
  ```
  osascript -e 'tell application "Reminders" to get name of (reminders whose completed is false)'
  ```
- **Windows** → a scheduled Toast reminder via PowerShell `ScheduledTasks`, or simply use
  **Path 2** (the ChatCLI scheduler) which is cleaner and OS-agnostic. Detect with
  `Get-Command remind -ErrorAction SilentlyContinue`.
- **Linux** with `remind(1)` installed (`command -v remind`) → append to `~/.reminders` and
  query with `remind ~/.reminders`. If nothing suitable is installed, fall back to **Path 2**.

When unsure what's installed, prefer **Path 2** — it needs no external app and works identically
on Windows, macOS, and Linux.

## Path 2 — ChatCLI scheduler (timed nudge, any OS)

Schedule a job that fires later and delivers a message. Resolve "now" first if the delay is
relative — **macOS / Linux**: `date '+%Y-%m-%d %H:%M %Z'`; **Windows**:
`Get-Date -Format "yyyy-MM-dd HH:mm zzz"`. Then use `@scheduler` to register the job, pairing it with `@send` so the reminder reaches the
user on their channel. Consult the `@scheduler` tool schema for the exact `when`/`every`/payload
fields (one-shot vs recurring), and confirm the resolved absolute time back to the user.

Example intent → "remind me in 2 hours to submit the report":
1. `date` → compute the absolute fire time.
2. Register a one-shot `@scheduler` job at that time whose action sends
   "⏰ Reminder: submit the report" via `@send` to the user's channel.
3. Confirm: "Okay — I'll ping you at 16:42 to submit the report."

## Rules

- **Always anchor relative times** ("in 2h", "tomorrow") to the output of `date` / `Get-Date`,
  and echo the absolute time back so the user can catch mistakes.
- Recurring reminders ("every weekday at 9") → recurring `@scheduler` job; say the cadence plainly.
- Don't silently pick a path — if it's ambiguous whether they want an app reminder or a ChatCLI
  nudge, ask briefly.
