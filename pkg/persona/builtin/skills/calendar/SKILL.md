---
name: calendar
description: Create, list, and manage calendar events / meetings from the terminal using whatever native CLI is installed (gcalcli for Google Calendar, khal for CalDAV, or AppleScript Calendar on macOS). Use when asked to "schedule a meeting", "create an event", "what's on my calendar", "am I free", "book".
allowed-tools: ["@coder", "Bash", "Read"]
triggers:
  - schedule a meeting
  - create an event
  - add to my calendar
  - what's on my calendar
  - am i free
  - book a meeting
  - agendar reunião
  - marcar reunião
  - criar evento
  - minha agenda
  - estou livre
---

# Calendar

Manage the user's calendar through their **own installed tooling** — skill + terminal +
native CLI. ChatCLI bundles no calendar client; detect what exists and drive it.

## Step 0 — Identify the OS first (this is a multi-OS CLI)

ChatCLI runs on **Windows, macOS, and Linux**. Probe accordingly:
- **macOS / Linux** (`sh`): `command -v gcalcli khal 2>/dev/null; uname -s`
- **Windows** (PowerShell): `Get-Command gcalcli -ErrorAction SilentlyContinue | Select-Object Name`

## Step 1 — Choose the tool

Preference order:
1. **`gcalcli`** — Google Calendar, scriptable, **cross-platform** (pip). The common case; prefer it.
2. **`khal`** — CalDAV/local (privacy-friendly, self-hosted; Unix).
3. **macOS with neither** → AppleScript `Calendar.app` via `osascript`.
4. **Windows with neither** → Outlook Calendar via PowerShell COM.
5. None → tell the user what to install (`pipx install gcalcli` / `apt install khal`) and stop.

## Step 2 — Always resolve "now" before scheduling

Relative times ("tomorrow 3pm", "next Tuesday") need an anchor. Get it first:
- **macOS / Linux**: `date '+%Y-%m-%d %H:%M %Z (%A)'`
- **Windows**: `Get-Date -Format "yyyy-MM-dd HH:mm zzz (dddd)"`

Then compute the absolute start/end and confirm it back to the user.

## Step 3 — Create an event

**gcalcli**:
```
gcalcli add --title "Sync with Alice" --when "2026-06-05 15:00" --duration 30 --where "Meet" --noprompt
```
Add `--calendar "<name>"` to target a specific calendar; `gcalcli list` shows them.

**khal**:
```
khal new 2026-06-05 15:00 16:00 "Sync with Alice"
```

**macOS Calendar (osascript)**:
```
osascript -e 'tell application "Calendar" to tell calendar "Home"
  make new event with properties {summary:"Sync with Alice", start date:(date "2026-06-05 15:00"), end date:(date "2026-06-05 15:30")}
end tell'
```

**Windows Outlook (PowerShell COM)**:
```
$ol = New-Object -ComObject Outlook.Application
$appt = $ol.CreateItem(1)   # 1 = AppointmentItem
$appt.Subject = "Sync with Alice"
$appt.Start = [datetime]"2026-06-05 15:00"
$appt.Duration = 30
$appt.Save()
```

## Step 4 — List / check availability

```
gcalcli agenda today tomorrow          # upcoming
gcalcli agenda "2026-06-05" "2026-06-06"
khal list today
```
On Windows, enumerate `$ol.GetNamespace("MAPI").GetDefaultFolder(9).Items` (folder 9 = Calendar).
Summarize the agenda; for "am I free at X?", check for overlaps and answer yes/no with the conflict.

## Video meetings (Meet/Zoom)

- `gcalcli add ... --conference` (where supported) attaches a Google Meet link.
- If the user wants a Meet/Zoom link and the CLI can't add one, create the event and tell the
  user to add the conferencing in the calendar UI — don't fabricate a link.

## Rules

- **Confirm the resolved absolute date/time, duration, and attendees** before creating.
- Creating/deleting events is outward-facing (invites go out) — get a clear go-ahead.
- Work in the user's timezone (from `date` / `Get-Date`); state it explicitly when there's any ambiguity.
- If multiple calendars exist and none was specified, ask which one.
