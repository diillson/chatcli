---
name: ascii-art
description: Render ASCII/terminal art — big text banners (figlet/toilet), colorized output (lolcat), and image-to-ASCII/terminal previews (chafa/jp2a). Keyless, local. Use when asked to "make a banner", "ascii art of", "big text", "show this image in the terminal".
allowed-tools: ["@coder", "Bash"]
triggers:
  - ascii art
  - make a banner
  - big text
  - figlet
  - show image in terminal
  - arte ascii
  - banner de texto
  - imagem no terminal
---

# ASCII / Terminal Art

Local, keyless terminal art. Detect tools first:
`command -v figlet toilet lolcat chafa jp2a 2>/dev/null` (Unix) / `Get-Command figlet, chafa` (Windows).
Install: `brew install figlet lolcat chafa jp2a` / `apt install figlet toilet lolcat chafa jp2a`.

## Big text banners

```
figlet "Deploy OK"
figlet -f slant "ChatCLI"          # other fonts: big, banner, standard
toilet -f mono12 -F metal "BUILD"  # toilet adds colors/filters
```
List figlet fonts: `figlet -I2` shows the font directory; `showfigfonts` previews them.

## Colorize

```
figlet "Done" | lolcat            # rainbow gradient
```

## Image → ASCII / terminal preview

```
chafa image.png                   # high-fidelity terminal graphics (truecolor/sixel)
chafa --size 60x30 image.png
jp2a --colors image.jpg           # classic ASCII conversion
```

## Rules

- Banners are great for CLI/agent output; in messaging gateways (Telegram/WhatsApp), monospaced
  ASCII often breaks on phones — prefer plain text there, or send a rendered image.
- Keep banners short; wide figlet output wraps badly in narrow terminals.
