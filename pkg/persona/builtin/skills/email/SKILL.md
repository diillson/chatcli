---
name: email
description: Send and read email from the terminal using whatever native CLI is installed (himalaya, msmtp/mutt/neomutt on Linux, or AppleScript Mail on macOS). Use when asked to "send an email", "email someone", "check my inbox", "read my email", "reply to".
allowed-tools: ["@coder", "Bash", "Read"]
triggers:
  - send an email
  - send email
  - email
  - check my inbox
  - read my email
  - reply to the email
  - mandar email
  - enviar email
  - ler meus emails
  - verificar email
---

# Email

Operate the user's email through their **own installed mail tooling** — the hermes way:
a skill (this knowledge) + the terminal (`@coder exec`) + a native CLI. ChatCLI does not
bundle an SMTP/IMAP client; you detect what exists and drive it. Never hardcode
credentials or paste passwords into commands.

## Step 0 — Identify the OS first (this is a multi-OS CLI)

Detection and tooling differ by platform. ChatCLI runs on **Windows, macOS, and Linux** —
never assume a Unix shell. Pick the detection probe for the host:

- **macOS / Linux** (`@coder exec` runs `sh`):
  ```
  command -v himalaya msmtp mutt neomutt mail 2>/dev/null; uname -s
  ```
- **Windows** (`@coder exec` runs PowerShell):
  ```
  Get-Command himalaya, mutt -ErrorAction SilentlyContinue | Select-Object Name
  ```

## Step 1 — Choose the tool

Preference order:
1. **`himalaya`** — modern, scriptable, JSON output, **cross-platform** (Windows/macOS/Linux
   via `cargo install himalaya`). Best for both read and send. Prefer it everywhere.
2. **`msmtp`** (+ a heredoc) — reliable send-only (Unix).
3. **`mutt` / `neomutt`** — send and read (Unix).
4. **macOS with none of the above** → AppleScript `Mail.app` via `osascript`.
5. **Windows with none of the above** → Outlook via PowerShell COM (`New-Object -ComObject Outlook.Application`).
6. None found → tell the user what to install (`cargo install himalaya`, `brew install himalaya`,
   `apt install himalaya`) and stop. Do not invent a workaround.

## Step 2 — Send

**himalaya** (preferred):
```
himalaya message send <<'EOF'
To: alice@example.com
Subject: Lunch?
From: me@example.com

Hey Alice — free for lunch Thursday?
EOF
```
(Use `himalaya -a <account>` to pick a configured account; `himalaya account list` to see them.)

**msmtp** (send-only, uses `~/.msmtprc`):
```
printf 'To: alice@example.com\nSubject: Lunch?\n\nHey Alice — free Thursday?\n' | msmtp alice@example.com
```

**macOS Mail (osascript)** — no extra install:
```
osascript -e 'tell application "Mail"
  set m to make new outgoing message with properties {subject:"Lunch?", content:"Hey Alice — free Thursday?", visible:false}
  tell m to make new to recipient at end of to recipients with properties {address:"alice@example.com"}
  send m
end tell'
```

**Windows Outlook (PowerShell COM)** — no extra install when Outlook is present:
```
$ol = New-Object -ComObject Outlook.Application
$mail = $ol.CreateItem(0)
$mail.To = "alice@example.com"
$mail.Subject = "Lunch?"
$mail.Body = "Hey Alice -- free Thursday?"
$mail.Send()
```

## Step 3 — Read / search

**himalaya**:
```
himalaya envelope list            # recent inbox
himalaya envelope list --folder Sent
himalaya message read <ID>        # full message
```
Parse the table (or add `-o json`) and summarize for the user — don't dump raw output.

**macOS Mail**: read counts/subjects via `osascript` against `Mail`'s `inbox`'s `messages`.

**Windows Outlook**: enumerate via PowerShell COM —
`$ol.GetNamespace("MAPI").GetDefaultFolder(6).Items` (folder 6 = Inbox); read `.Subject`/`.SenderName`.

## Rules

- **Confirm before sending** unless the user clearly already authorized this exact send
  (recipient + intent are unambiguous). Email is outward-facing and hard to unsend.
- Quote the recipient, subject, and a one-line body summary back to the user before/after sending.
- Respect i18n: write the email in the language the user is communicating in.
- If multiple accounts exist and the user didn't specify, ask which to send from.
