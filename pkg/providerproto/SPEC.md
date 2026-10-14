# chatcli provider plugin protocol — version 1

A provider plugin is an executable that adds an LLM provider to chatcli. Once
installed it behaves like a built-in provider: `/switch`, `/provider`, the
command palette, cost tracking, failover chains and model routing all see it.

This document is the contract. Go authors can use `pkg/providersdk`, which
implements it; authors in any other language implement it directly. Run
`chatcli provider test <plugin>` to check an implementation.

## 1. Transport

- The host starts the plugin as a child process and keeps it running.
- Messages are JSON-RPC 2.0 objects, **one per line**, UTF-8, on stdin (host →
  plugin) and stdout (plugin → host). JSON encoders escape newlines inside
  strings, so a message never spans lines. Maximum message size: 32 MiB.
- **stdout carries protocol messages only.** Anything else on stdout is a
  protocol violation. Write logs to stderr (the host keeps a tail of it) or
  send `$/log` notifications.
- Requests may be answered in any order; the plugin may serve them
  concurrently. Notifications are processed in arrival order.

## 2. Lifecycle

```
host                                   plugin
 │ ── initialize ──────────────────────▶ │   must be the first request
 │ ◀───────────────────── result ─────── │
 │ ── initialized (notification) ──────▶ │
 │        … auth / models / chat …       │
 │ ── shutdown ────────────────────────▶ │   finish earlier requests, refuse later ones
 │ ◀───────────────────── result ─────── │
 │ ── exit (notification) ─────────────▶ │   exit with status 0
```

- Any request before `initialize` fails with `-32600`.
- Requests that **arrived** before `shutdown` are still served; later ones fail
  with `-32600`.
- After `exit` (or when stdin closes) the process terminates. The host kills a
  plugin that does not exit within 5 seconds.
- Either side may send `$/cancelRequest` `{"id": <request id>}`. The receiver
  should stop the work and answer the request with `-33007` (or not at all).

### initialize

```json
→ {"jsonrpc":"2.0","id":1,"method":"initialize","params":{
     "protocolVersions":["1"],
     "host":{"name":"chatcli","version":"1.240.0"},
     "capabilities":{"interactiveAuth":true}}}
← {"jsonrpc":"2.0","id":1,"result":{
     "protocolVersion":"1",
     "provider":{"name":"ACME","displayName":{"en":"Acme Gateway","pt-BR":"Gateway Acme"},"version":"1.2.0"},
     "mode":"dialect",
     "dialect":{"api":"openai-chat","baseURL":"https://llm.acme.example/v1"},
     "capabilities":{"auth":{"login":true,"token":true,"logout":false},"models":true,
                     "chat":{"stream":false,"tools":false,"images":false,"thinking":false},
                     "countTokens":false},
     "authEnv":["ACME_CLIENT_ID","ACME_CLIENT_SECRET"]}}
```

- `protocolVersion` must be one of the offered versions; otherwise fail with
  `-32600`.
- `provider.name` must match `^[A-Z][A-Z0-9_]{1,31}$` and must not be the name
  of a built-in provider (`OPENAI`, `CLAUDEAI`, `BEDROCK`, …). It is the value
  users pass to `LLM_PROVIDER` and `/switch`.
- `displayName.en` is required; other BCP 47 tags are optional.
- `capabilities` must be truthful: the host never calls a method whose
  capability is false.
- `authEnv` lists the environment variables the plugin reads. The host passes
  only those, plus a minimal base environment, and treats their values as
  secrets.

## 3. Modes

| Mode | The plugin implements | The host provides |
|---|---|---|
| `dialect` | `auth/*`, `models/list` | Chat through its built-in client for `dialect.api` against `dialect.baseURL`, sending the headers from `auth/token`. Prompt caching, native tool calling, streaming and usage parsing come from that client. |
| `full` | `auth/*` (optional), `models/list`, `chat/send`, optionally `tokens/count` | Everything above the wire: history, tools execution, memory, compaction, cost, failover. |

`dialect.api` is one of `openai-chat`, `openai-responses`,
`anthropic-messages` or `gemini`. Prefer `dialect` whenever the backend speaks
one of these formats; it gives the best feature parity for the least code.

## 4. Authentication

The plugin never stores secrets. It returns a **credential** (a flat
string→string object) at the end of the login. The host encrypts and stores
it, and sends it back on every call that needs it.

### auth/login and auth/continue

Login is a small state machine. The plugin never touches the terminal: it
returns steps and the host renders them. The same flow then works in the CLI,
the web UI, IDE clients and messaging channels.

```json
→ {"method":"auth/login","params":{"inputs":{"environment":"prod"}}}
← {"result":{"kind":"prompt","session":"s1",
             "fields":[{"id":"client_id","label":{"en":"Client ID"}},
                       {"id":"client_secret","label":{"en":"Client secret"},"secret":true},
                       {"id":"environment","label":{"en":"Environment"},"choices":["dev","prod"]}]}}
→ {"method":"auth/continue","params":{"session":"s1","inputs":{"client_id":"…","client_secret":"…"}}}
← {"result":{"kind":"done","account":"app-123","credential":{"client_id":"…","client_secret":"…","environment":"prod"}}}
```

| Step `kind` | Meaning | Host action |
|---|---|---|
| `prompt` | Ask for `fields` | Collect the values, call `auth/continue` |
| `open_url` | Open `url` in a browser (OAuth) | Show or open it, then call `auth/continue` (a loopback redirect can complete it) |
| `device_code` | Show `code` and `url` | Call `auth/continue` every `pollMs` until `done` |
| `done` | Finished | Store `credential` |

`inputs` on `auth/login` pre-answers fields by id, so unattended hosts (CI,
servers) can log in without a person. When `interactiveAuth` is false and
input is missing, fail with `-33001`.

### auth/token

```json
→ {"method":"auth/token","params":{"credential":{…}}}
← {"result":{"headers":{"Authorization":"Bearer eyJ…","X-Client-Id":"app-123"},
             "expiresAt":"2026-10-14T18:00:00Z"}}
```

The host caches the result and calls again before `expiresAt`, or after a
`401`/`403` from the backend. Return `credential` as well to replace the
stored one (a rotated refresh token, for example).

### auth/status and auth/logout

`auth/status` answers `{"state":"logged_in"|"logged_out"|"expired"}`.
Optional fields: `account`, `expiresAt`. With no credential it must not
report `logged_in`. `auth/logout` revokes the credential upstream when the
backend supports it.

## 5. models/list

```json
← {"result":{"models":[
     {"id":"claude-sonnet-4-6","displayName":"Claude Sonnet 4.6",
      "upstream":{"provider":"CLAUDEAI","model":"claude-sonnet-4-6"},
      "headers":{"X-Upstream":"anthropic"}},
     {"id":"gpt-5.4-mini","contextWindow":400000,"maxOutputTokens":128000,
      "pricing":{"input":0.25,"output":2.0,"cacheRead":0.025},
      "capabilities":["tools","vision","prompt_cache"]}]}}
```

- `upstream` names the built-in provider and model a gateway model fronts.
  The host then reuses what it already knows about that model: context
  window, output limit, pricing and capabilities. Explicit fields win.
- `pricing` is USD per million tokens.
- `headers` are added to dialect requests for that model only, for gateways
  that route by header. `dialect` overrides the plugin-level API per model.

## 6. chat/send (full mode)

```json
→ {"id":7,"method":"chat/send","params":{
     "credential":{…},"model":"m1","maxTokens":4096,"stream":true,
     "messages":[
       {"role":"system","content":[{"type":"text","text":"…","cacheBreakpoint":true}]},
       {"role":"user","content":[{"type":"text","text":"Read main.go"}]},
       {"role":"assistant","toolCalls":[{"id":"c1","name":"read","arguments":"{\"file\":\"main.go\"}"}]},
       {"role":"tool","toolCallId":"c1","content":[{"type":"text","text":"package main…"}]}],
     "tools":[{"name":"read","description":"…","parameters":{"type":"object"}}],
     "hints":{"cacheTTL":"1h","reasoningEffort":"high"}}}
← {"method":"chat/delta","params":{"requestId":7,"text":"The file "}}
← {"method":"chat/delta","params":{"requestId":7,"text":"declares…"}}
← {"id":7,"result":{
     "message":{"role":"assistant","content":[{"type":"text","text":"The file declares…"}]},
     "stopReason":"end",
     "usage":{"inputTokens":1200,"outputTokens":85,"cacheReadTokens":9000,"costUSD":0.0042}}}
```

- Part types: `text`, `image` (`mediaType` plus base64 `data` or `url`), and
  `thinking` (`text`, with `signature`/`redacted` to replay verbatim).
- `cacheBreakpoint: true` marks the end of a cacheable prefix. Map it to the
  backend's cache marker when there is one; ignore it otherwise.
- `hints` are optional. Apply the ones the backend supports and ignore the rest.
- When `stream` is true and `capabilities.chat.stream` is true, send
  `chat/delta` notifications before the result. The concatenated `text`
  deltas must equal the final text.
- `stopReason` is one of `end`, `max_tokens`, `tool_use` or `refusal`.
- `usage.inputTokens` excludes cached tokens. `costUSD`, when present, is
  treated as authoritative.

## 7. tokens/count (optional)

Same `model`, `messages` and `tools` as `chat/send`; it answers
`{"tokens": n}`. Without it the host calibrates its estimate from the usage
of each reply.

## 8. Errors

Errors are JSON-RPC error objects. Pick the code that matches the cause: the
host drives retries, failover and re-login from it.

| Code | Meaning | Host reaction |
|---|---|---|
| `-33001` | Auth required | Run the login, or fail with a login hint when unattended |
| `-33002` | Rate limited (`data.retryAfterMs`) | Back off and retry |
| `-33003` | Context too long | Compact the history and retry |
| `-33004` | Unknown model | Fail and suggest `/switch --model` |
| `-33005` | Upstream unavailable | Retry, then fail over |
| `-33006` | Bad request | Fail, no retry |
| `-33007` | Cancelled | — |
| `-33008` | Refused by policy | Fail with the message |
| `-32600` / `-32601` / `-32602` / `-32603` | JSON-RPC: invalid request, unknown method, invalid params, internal | Fail |

`data.httpStatus` may carry the upstream status for diagnostics.

## 9. Security expectations

- Distribute signed binaries. chatcli verifies plugin signatures with the same
  trusted-key mechanism as tool plugins (`chatcli plugin sign|verify|trust`).
- Read credentials only from the `credential` the host sends or from the
  variables declared in `authEnv`. Never write secrets to stdout or stderr.
- The plugin receives the user's conversations. Users are told so when they
  install it.

## 10. Minimal skeleton (Python)

```python
import json, sys

def reply(id, result=None, error=None):
    msg = {"jsonrpc": "2.0", "id": id}
    if error is not None:
        msg["error"] = error
    else:
        msg["result"] = result
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()

initialized = False
for line in sys.stdin:
    msg = json.loads(line)
    method, id = msg.get("method"), msg.get("id")
    if method == "exit":
        sys.exit(0)
    if id is None:          # other notifications
        continue
    if method == "initialize":
        initialized = True
        reply(id, {"protocolVersion": "1",
                   "provider": {"name": "ACME", "displayName": {"en": "Acme"}, "version": "0.1.0"},
                   "mode": "dialect",
                   "dialect": {"api": "openai-chat", "baseURL": "https://llm.acme.example/v1"},
                   "capabilities": {"auth": {"login": True, "token": True}, "models": True,
                                    "chat": {}, "countTokens": False}})
    elif not initialized:
        reply(id, error={"code": -32600, "message": "initialize must come first"})
    elif method == "shutdown":
        reply(id, None)
    # … auth/login, auth/continue, auth/token, auth/status, models/list …
    else:
        reply(id, error={"code": -32601, "message": f"{method} not supported"})
```

A production plugin should serve requests concurrently (a slow `auth/token`
must not block `models/list`) and honor `$/cancelRequest`.
