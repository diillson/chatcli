---
name: send-message
description: Proactively send a message to the user (or a chat) over a connected messaging platform — Telegram, WhatsApp, Discord, Slack, or a generic webhook. Use when asked to "notify", "ping", "message", "tell", "send", "let me know on Telegram/WhatsApp", or to deliver a result to a chat.
allowed-tools: ["@send"]
triggers:
  - send a message
  - notify me
  - ping me
  - message me
  - tell me on
  - let me know on
  - mande mensagem
  - me avise
  - me notifique
  - manda no telegram
  - manda no whatsapp
---

# Send Message

Deliver a message to a person/chat through ChatCLI's **gateway platform adapters** —
the same Telegram/WhatsApp/Discord/Slack/webhook integrations the gateway daemon uses,
now reachable to *initiate* a message. This is the native, no-gambiarra path: do **not**
shell out to `curl`/`pywhatkit`/browser automation when `@send` covers the platform.

## The tool

`@send` has two subcommands. Always pass the JSON envelope on one line:

```
<tool_call name="@send" args='{"cmd":"send","args":{"to":"telegram","message":"Build is green ✅"}}' />
<tool_call name="@send" args='{"cmd":"list"}' />
```

### `send {to, message}`
- `to` is either:
  - a **bare platform** — `"telegram"`, `"whatsapp"`, `"discord"`, `"slack"`, `"webhook"` —
    which delivers to that platform's configured *home channel*
    (`CHATCLI_<PLATFORM>_HOME_CHANNEL`); or
  - a **`platform:chat_id`** target — e.g. `"telegram:-1001234567890"`,
    `"whatsapp:+5511999999999"`, `"slack:C0123ABC"`, `"discord:<channel_id>"`.
    Anything after the first `:` is passed verbatim, so thread/suffix forms like
    `"telegram:-100123:42"` are preserved.
- `message` is plain text. Messaging apps render plain text best — avoid tables,
  ASCII banners, and code fences unless the user explicitly wants code.

### `list`
Reports which platforms are configured and whether each has a home channel.
Run it first when you're unsure what's available or which `to` to use.

## How to decide

1. **Which platform?** If the user named one ("manda no Telegram"), use it. If a
   gateway conversation is in progress, prefer the platform you're already on. If
   unsure, run `@send list` and pick the only configured one, or ask.
2. **Which target?** If the user means "me" and a home channel is set, use the bare
   platform. To reach a specific group/chat, use `platform:chat_id`.
3. **Write for the channel.** Short, natural, plain text. One message unless asked.

## Failure handling

- `"... is not configured"` → that platform has no credentials. Run `@send list` and
  use a configured one, or tell the user which env vars to set
  (e.g. `CHATCLI_TELEGRAM_BOT_TOKEN`).
- `"No target for <platform>"` → no `chat_id` given and no home channel set. Either
  pass `platform:chat_id` or tell the user to set `CHATCLI_<PLATFORM>_HOME_CHANNEL`.
- Delivery errors are reported verbatim from the platform API — surface them; don't retry blindly.

## Scope

`@send` sends **text**. Outbound media (images/audio/files) is not yet supported by the
gateway adapters — if asked, say so plainly rather than improvising a workaround.
