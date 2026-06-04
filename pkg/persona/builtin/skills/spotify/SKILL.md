---
name: spotify
description: Control music playback — play/pause, skip, volume, now-playing — on the local Spotify app via playerctl (Linux/MPRIS) or AppleScript (macOS), keyless. Use when asked to "play music", "pause", "skip song", "what's playing", "next track".
allowed-tools: ["@coder", "Bash"]
triggers:
  - play music
  - pause the music
  - skip song
  - next track
  - previous track
  - what's playing
  - spotify
  - tocar música
  - pausar música
  - pular música
  - que música é essa
---

# Spotify / Media Playback (local control)

Control the **local Spotify desktop app** without any API key or OAuth, using the OS media
control layer. (The Web API exists but needs OAuth — prefer local control for play/pause/skip.)

## macOS — AppleScript

```
osascript -e 'tell application "Spotify" to playpause'
osascript -e 'tell application "Spotify" to next track'
osascript -e 'tell application "Spotify" to set sound volume to 60'
osascript -e 'tell application "Spotify" to (name of current track) & " — " & (artist of current track)'
osascript -e 'tell application "Spotify" to play track "spotify:track:TRACKID"'
```

## Linux — playerctl (MPRIS; works with Spotify, and most players)

```
playerctl -p spotify play-pause
playerctl -p spotify next
playerctl -p spotify metadata --format '{{ artist }} — {{ title }}'
playerctl -p spotify volume 0.6
```
Detect: `command -v playerctl`. Install: `apt install playerctl`.

## Windows

The Windows Spotify app exposes media keys via the System Media Transport Controls — simplest is
to send media-key virtual inputs from PowerShell, or use `spotify-cli`. If neither is available,
say so rather than improvising.

## Search / play a specific song (needs Web API)

Playing an arbitrary search result requires the Spotify Web API with the user's OAuth token
(`SPOTIFY_TOKEN`). If the user wants that, explain the one-time auth; otherwise stick to
controlling whatever is already queued.

## Rules

- Detect the OS first; the control mechanism differs entirely.
- For "what's playing", read metadata and report `artist — title`.
