---
name: huggingface
description: Work with the Hugging Face Hub — search models/datasets, inspect model cards, download weights, and upload artifacts — via the huggingface-cli and the keyless public API. Token only needed for private/gated repos. Use when asked to "find a model for", "download this HF model", "search datasets", "what's on the Hub".
allowed-tools: ["@coder", "Bash", "@webfetch"]
triggers:
  - huggingface
  - hugging face
  - find a model for
  - download model
  - search datasets
  - model card
  - on the hub
  - baixar modelo
  - procurar modelo
---

# Hugging Face Hub

Public Hub browsing is **keyless** (HTTP API); a token (`HF_TOKEN`) is only needed for private or
gated repos and uploads.

## Search (keyless)

```
curl -s "https://huggingface.co/api/models?search=whisper&sort=downloads&limit=10"
curl -s "https://huggingface.co/api/datasets?search=sentiment&limit=10"
```
Read `id`, `downloads`, `likes`, `pipeline_tag`.

## Model / dataset card

```
@webfetch https://huggingface.co/<org>/<model>           # the card
curl -s "https://huggingface.co/api/models/<org>/<model>"  # metadata JSON
```

## Download (huggingface-cli)

```
pip install -U "huggingface_hub[cli]"
huggingface-cli download <org>/<model> --local-dir ./model
# gated/private:
HF_TOKEN=... huggingface-cli download <org>/<model>
```
For a single file: `huggingface-cli download <org>/<model> config.json`.

## Upload (needs token)

```
huggingface-cli login    # or set HF_TOKEN
huggingface-cli upload <org>/<repo> ./local-dir
```

## Choosing a model

1. Search by task (`pipeline_tag`: text-generation, automatic-speech-recognition, etc.).
2. Sort by downloads/likes/recency; read the card for license, size, and intended use.
3. Report id, license, size, and a one-line fit assessment before downloading large weights.

## Rules

- Weights can be huge — confirm before downloading multi-GB repos; mention disk cost.
- Respect model licenses; surface the license from the card.
