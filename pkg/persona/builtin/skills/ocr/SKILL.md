---
name: ocr
description: Extract text from images and scanned PDFs using local OCR (Tesseract). Keyless, offline. Use when asked to "read text from this image", "OCR this", "extract text from a screenshot/scan", or given an image with text.
allowed-tools: ["@coder", "Bash", "@read"]
triggers:
  - ocr
  - read text from image
  - extract text from image
  - text from screenshot
  - scan to text
  - ler texto da imagem
  - extrair texto da imagem
  - texto do print
---

# OCR (image / scan → text)

Local, offline OCR via **Tesseract** — no API, no upload.

## Step 0 — Detect

- **macOS / Linux**: `command -v tesseract pdftoppm 2>/dev/null; tesseract --list-langs 2>/dev/null`
- **Windows**: `Get-Command tesseract -ErrorAction SilentlyContinue`

Install: `brew install tesseract` / `apt install tesseract-ocr` / `choco install tesseract`.
Language packs: `tesseract-ocr-por` (Portuguese), `-eng` (English), etc.

## Image → text

```
tesseract input.png stdout -l eng
tesseract input.jpg stdout -l por+eng     # multi-language
```
Write to a file with `tesseract input.png out` → `out.txt`, then read with `@read`.

## Scanned PDF → text

Rasterize pages first, then OCR each:
```
pdftoppm -png -r 300 scan.pdf page
for f in page-*.png; do tesseract "$f" stdout -l por+eng; done > scan.txt
```
(On Windows, loop with PowerShell `Get-ChildItem page-*.png | % { tesseract $_.Name stdout -l por+eng }`.)

## Tips for accuracy

- Use 300 DPI for scans; higher helps small text.
- `-l` must match the document language — pick `por` for Portuguese, or it mangles accents.
- For skewed/noisy scans, mention that pre-processing (deskew/threshold via ImageMagick) improves results.

## Rules

- Choose the language pack from the document, not the UI locale.
- Present the extracted text, then offer to summarize/translate/feed it to another skill.
