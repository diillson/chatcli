---
name: webhooks
description: Call outbound webhooks and integrate with automation platforms — POST JSON to a URL, trigger Slack/Discord incoming webhooks, fire Zapier/Make/n8n hooks, and verify signatures. Keyless (uses the URLs/secrets the user already has). Use when asked to "trigger a webhook", "POST to this URL", "notify via webhook", "call my automation".
allowed-tools: ["@coder", "Bash", "@webfetch", "@send"]
triggers:
  - trigger a webhook
  - call a webhook
  - post to this url
  - notify via webhook
  - fire the hook
  - zapier
  - n8n
  - disparar webhook
  - chamar webhook
---

# Webhooks / Automation

Fire outbound webhooks to integrate ChatCLI with anything that accepts HTTP — Slack/Discord
incoming webhooks, Zapier/Make/n8n, custom endpoints. Keyless: it uses the webhook URLs/secrets
the user provides.

## Generic JSON POST

```
curl -s -X POST "$WEBHOOK_URL" -H "Content-Type: application/json" \
  -d '{"event":"build.done","status":"green"}'
```

## Slack / Discord incoming webhooks

```
# Slack
curl -s -X POST "$SLACK_WEBHOOK_URL" -H "Content-Type: application/json" \
  -d '{"text":"Deploy finished ✅"}'
# Discord
curl -s -X POST "$DISCORD_WEBHOOK_URL" -H "Content-Type: application/json" \
  -d '{"content":"Deploy finished ✅"}'
```
(If you just need to message a chat the gateway already manages, prefer the `@send` tool / the
`send-message` skill instead of a raw webhook.)

## Zapier / Make / n8n

These expose a "catch hook" URL — POST your payload as JSON; the platform's scenario does the rest.
Match the field names the scenario expects.

## Signed webhooks (verify inbound)

When the user receives webhooks (e.g. via ChatCLI's gateway webhook adapter), verify the HMAC:
compute `HMAC-SHA256(secret, raw_body)` and compare to the signature header in constant time.
Document which header the sender uses (`X-Hub-Signature-256`, `X-Signature`, etc.).

## Scheduled / recurring

For "fire this webhook every morning" or "after X happens", pair with `@scheduler` (timing) and
this skill (the HTTP call).

## Rules

- Never log full secrets/URLs; treat webhook URLs as credentials.
- Confirm the destination before POSTing to an unfamiliar URL — webhooks trigger real actions.
- Check the response status; report success/failure with the HTTP code.
