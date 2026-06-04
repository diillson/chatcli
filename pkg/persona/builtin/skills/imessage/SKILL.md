---
name: imessage
description: Send and read iMessage/SMS on macOS via the Messages app (AppleScript). macOS-only. Use when asked to "send an iMessage", "text someone on iMessage", "message X on my Mac". For Telegram/WhatsApp/Discord/Slack, use the send-message skill instead.
allowed-tools: ["@coder", "Bash"]
triggers:
  - imessage
  - send an imessage
  - text on imessage
  - message on my mac
  - mandar imessage
---

# iMessage (macOS)

Send and read iMessage/SMS through the macOS **Messages.app** via `osascript`. This is
**macOS-only** — check `uname -s` returns `Darwin` first; on Windows/Linux there is no iMessage,
so route the user to the `send-message` skill (Telegram/WhatsApp/etc.) instead.

## Preconditions

- Messages.app is signed in to the user's Apple ID.
- First send may require Automation permission (System Settings → Privacy & Security →
  Automation) — if osascript errors with "not allowed", tell the user to grant it.

## Send

```
osascript -e 'tell application "Messages"
  set svc to 1st service whose service type = iMessage
  set buddy to buddy "+15551234567" of svc
  send "On my way 🚗" to buddy
end tell'
```
To send to an existing chat by name, target `text chat` instead of `buddy`.

## Read recent messages

Messages stores history in `~/Library/Messages/chat.db` (SQLite). Read-only query:
```
sqlite3 ~/Library/Messages/chat.db \
  "SELECT datetime(date/1000000000 + 978307200,'unixepoch'), text FROM message ORDER BY date DESC LIMIT 10;"
```
(Requires Full Disk Access for the terminal; mention this if it errors.)

## Rules

- Confirm recipient + text before sending — messages can't be unsent.
- Phone numbers in E.164 (`+countrycode...`) are most reliable.
- This is the one skill that is genuinely OS-locked; be explicit when the user isn't on macOS.
