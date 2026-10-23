#!/usr/bin/env python3
"""A local OpenAI-compatible endpoint backed by chatcli (Claude through the
user's own login).

The LongMemEval-V2 harness talks to its reader and its judge through
`/v1/chat/completions`. Neither the reader (Qwen3.5-9B) nor the judge (gpt-5.2)
is reachable here, and the Anthropic login lives inside chatcli, so this
server answers those two calls by running chatcli's chat mode once per request:

  * the system prompt, the text parts and the images of the request become one
    message on chatcli's stdin (images are written to temp files and named as
    `@file <path>`, which chatcli attaches);
  * the reply text is the chat completion;
  * every request runs in its own empty `CHATCLI_STATE_DIR`, so no memory,
    history or skill of the user reaches the answer.

It is a measurement tool, not a model server: sampling parameters
(temperature, top_p, top_k, ...) are accepted and ignored, and a score produced
through it is NOT comparable with the leaderboard, which fixes the reader and
the judge.

    CHATCLI_BIN=/path/to/chatcli python3 claude_bridge.py --port 8023 \
        --model claude-sonnet-5-5 --workers 6
"""
from __future__ import annotations

import argparse
import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

ANSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")
EXT = {"image/png": ".png", "image/jpeg": ".jpg", "image/jpg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}


class Bridge:
    def __init__(self, binary: str, model: str, provider: str, workers: int, timeout: int, log: Path | None):
        self.binary, self.model, self.provider, self.timeout = binary, model, provider, timeout
        self.sem = threading.Semaphore(workers)
        self.log = log
        self.lock = threading.Lock()
        self.stats = {"requests": 0, "errors": 0, "cost_usd": 0.0, "input_tokens": 0, "output_tokens": 0}

    def render(self, messages: list[dict], tmp: Path) -> str:
        parts: list[str] = []
        n = 0
        for m in messages:
            role = m.get("role", "user")
            content = m.get("content")
            chunks: list[str] = []
            if isinstance(content, str):
                chunks.append(content)
            elif isinstance(content, list):
                for part in content:
                    if not isinstance(part, dict):
                        continue
                    if part.get("type") == "text":
                        chunks.append(part.get("text", ""))
                    elif part.get("type") == "image_url":
                        url = (part.get("image_url") or {}).get("url", "")
                        mm = re.match(r"data:([^;]+);base64,(.*)", url, re.S)
                        if not mm:
                            continue
                        n += 1
                        f = tmp / f"img{n:03d}{EXT.get(mm.group(1).lower(), '.png')}"
                        f.write_bytes(base64.b64decode(mm.group(2)))
                        chunks.append(f"\n@file {f}\n")
            text = "".join(chunks)
            parts.append(text if role == "user" else f"[{role.upper()} INSTRUCTIONS]\n{text}\n[END {role.upper()} INSTRUCTIONS]\n")
        return "\n".join(parts)

    def complete(self, req: dict) -> dict:
        model = req.get("model") or self.model
        max_tokens = req.get("max_tokens") or req.get("max_completion_tokens")
        with self.sem, tempfile.TemporaryDirectory(prefix="bridge-") as td:
            tmp = Path(td)
            state = tmp / "state"
            state.mkdir()
            (tmp / "empty.env").write_text("")
            record = tmp / "record.json"
            prompt = self.render(req.get("messages", []), tmp)
            env = {
                "HOME": os.environ.get("HOME", ""), "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
                "TMPDIR": os.environ.get("TMPDIR", "/tmp"), "USER": os.environ.get("USER", ""),
                "LANG": "en_US.UTF-8", "CHATCLI_LANG": "en",
                "CHATCLI_STATE_DIR": str(state), "CHATCLI_PARK_DIR": str(state / "parked"),
                "CHATCLI_AUTH_DIR": os.environ.get("CHATCLI_AUTH_DIR", str(Path.home() / ".chatcli")),
                "CHATCLI_DOTENV": str(tmp / "empty.env"), "CHATCLI_HOOKS_ENABLED": "false",
                "CHATCLI_DISABLE_VERSION_CHECK": "true", "CHATCLI_SESSION_AUTOSAVE": "false",
                "CHATCLI_DISABLE_HISTORY": "true", "CHATCLI_EVAL_RECORD": str(record),
            }
            cmd = [self.binary, "-p", "Respond to the message on stdin.", "--provider", self.provider,
                   "--model", model, "--raw", "--no-anim", "--timeout", f"{self.timeout}s"]
            if max_tokens:
                cmd += ["--max-tokens", str(int(max_tokens))]
            t0 = time.time()
            proc = subprocess.run(cmd, cwd=str(tmp), env=env, input=prompt, capture_output=True, text=True)
            text = ANSI.sub("", proc.stdout).strip()
            rec = {}
            try:
                rec = json.loads(record.read_text())
            except (OSError, ValueError):
                pass
            ptok, ctok = int(rec.get("input_tokens") or 0), int(rec.get("output_tokens") or 0)
            cost = float(rec.get("cost_usd") or rec.get("cost") or 0.0)
            with self.lock:
                self.stats["requests"] += 1
                self.stats["cost_usd"] += cost
                self.stats["input_tokens"] += ptok
                self.stats["output_tokens"] += ctok
                if proc.returncode != 0 or not text:
                    self.stats["errors"] += 1
                if self.log:
                    with self.log.open("a") as f:
                        f.write(json.dumps({"t": round(time.time() - t0, 2), "rc": proc.returncode, "in": ptok, "out": ctok,
                                            "cost": cost, "chars": len(prompt)}) + "\n")
            if proc.returncode != 0 or not text:
                raise RuntimeError(f"chatcli exit {proc.returncode}: {ANSI.sub('', proc.stderr)[-400:]}")
        return {
            "id": "chatcmpl-" + uuid.uuid4().hex[:24], "object": "chat.completion", "created": int(time.time()), "model": model,
            "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": text}}],
            "usage": {"prompt_tokens": ptok, "completion_tokens": ctok, "total_tokens": ptok + ctok},
        }


def make_handler(bridge: Bridge):
    class H(BaseHTTPRequestHandler):
        def log_message(self, *a):  # quiet
            return

        def _send(self, code: int, body: dict) -> None:
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            if self.path.rstrip("/") in ("/v1/models", "/models"):
                self._send(200, {"object": "list", "data": [{"id": bridge.model, "object": "model"}]})
            elif self.path.rstrip("/") == "/stats":
                self._send(200, bridge.stats)
            else:
                self._send(404, {"error": {"message": "not found"}})

        def do_POST(self):
            if not self.path.rstrip("/").endswith("/chat/completions"):
                return self._send(404, {"error": {"message": "not found"}})
            try:
                n = int(self.headers.get("Content-Length", "0"))
                req = json.loads(self.rfile.read(n))
                self._send(200, bridge.complete(req))
            except Exception as exc:  # the client retries on 5xx
                self._send(500, {"error": {"message": str(exc)[:600], "type": "bridge_error"}})

    return H


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=8023)
    ap.add_argument("--model", default="claude-sonnet-5-5")
    ap.add_argument("--provider", default="CLAUDEAI")
    ap.add_argument("--workers", type=int, default=6)
    ap.add_argument("--timeout", type=int, default=900)
    ap.add_argument("--log", default=None)
    a = ap.parse_args()
    binary = os.environ.get("CHATCLI_BIN", "chatcli")
    b = Bridge(binary, a.model, a.provider, a.workers, a.timeout, Path(a.log) if a.log else None)
    srv = ThreadingHTTPServer(("127.0.0.1", a.port), make_handler(b))
    print(f"claude_bridge on http://127.0.0.1:{a.port}/v1 -> {binary} ({a.provider}/{a.model}), {a.workers} workers", flush=True)
    srv.serve_forever()


if __name__ == "__main__":
    sys.exit(main())
