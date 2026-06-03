---
name: youtube
description: Work with YouTube videos from the terminal using yt-dlp — fetch metadata, download audio/video, and pull transcripts/subtitles to summarize. Keyless. Use when asked to "summarize this YouTube video", "download audio from", "get the transcript of", or given a YouTube link.
allowed-tools: ["@coder", "Bash", "@webfetch", "@read"]
triggers:
  - youtube
  - summarize this video
  - download audio from
  - get the transcript
  - transcript of
  - resumir esse vídeo
  - baixar áudio de
  - transcrição do vídeo
---

# YouTube

Keyless YouTube access via **yt-dlp** (no API key). Detect: `command -v yt-dlp ffmpeg` (Unix) /
`Get-Command yt-dlp -ErrorAction SilentlyContinue` (Windows). Install: `brew install yt-dlp ffmpeg`
/ `pipx install yt-dlp` / `choco install yt-dlp`.

## Metadata (no download)

```
yt-dlp --dump-json --no-warnings "URL" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['title'],'-',d['uploader'],'-',d['duration_string'])"
```

## Transcript / subtitles (best for summarizing)

```
yt-dlp --skip-download --write-auto-subs --sub-langs "en,pt" --sub-format vtt -o "%(id)s" "URL"
```
This writes a `.vtt`; read it with `@read`, strip timestamps, and summarize. Prefer manual subs
(`--write-subs`) when available, auto-subs (`--write-auto-subs`) otherwise.

## Download

```
yt-dlp -f "bestaudio" -x --audio-format mp3 "URL"     # audio only
yt-dlp -f "bv*+ba/b" -o "%(title)s.%(ext)s" "URL"      # best video+audio
```

## "Summarize this video" workflow

1. Get metadata (title/author/length).
2. Pull the transcript (subs). If none exist, say so — don't fabricate.
3. Read the transcript and produce a structured summary (key points + timestamps if useful).

## Rules

- Respect copyright/ToS: download only what the user is entitled to; default to transcript-only
  for summarization.
- `ffmpeg` is required for audio extraction/merging — check it in Step 0.
