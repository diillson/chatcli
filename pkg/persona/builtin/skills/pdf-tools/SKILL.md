---
name: pdf-tools
description: Manipulate PDFs locally — merge, split, extract pages, rotate, compress, extract text, convert images↔PDF. Uses keyless local CLIs (pdftk/qpdf/pdftotext/poppler/img2pdf/Ghostscript). Use when asked to "merge PDFs", "split this PDF", "extract text from PDF", "compress PDF", "PDF to images".
allowed-tools: ["@coder", "Bash", "@read"]
triggers:
  - merge pdf
  - split pdf
  - extract text from pdf
  - compress pdf
  - pdf to image
  - rotate pdf
  - juntar pdf
  - dividir pdf
  - extrair texto do pdf
  - comprimir pdf
---

# PDF Tools

Local, keyless PDF manipulation. Detect what's installed and use it; nothing leaves the machine.

## Step 0 — Detect tools

- **macOS / Linux**: `command -v qpdf pdftk pdftotext pdfinfo pdftoppm img2pdf gs 2>/dev/null`
- **Windows**: `Get-Command qpdf, pdftk, pdftotext -ErrorAction SilentlyContinue`

Install hints: `brew install qpdf poppler img2pdf ghostscript` / `apt install qpdf poppler-utils img2pdf ghostscript` / `choco install qpdf`.

## Common operations

**Merge** (`qpdf` preferred, `pdftk` fallback):
```
qpdf --empty --pages a.pdf b.pdf -- merged.pdf
pdftk a.pdf b.pdf cat output merged.pdf
```

**Split / extract pages**:
```
qpdf in.pdf --pages in.pdf 2-5 -- pages_2_5.pdf
pdftk in.pdf cat 1 3 5 output odd.pdf
```

**Rotate**:
```
qpdf in.pdf out.pdf --rotate=+90:1-z
```

**Compress** (Ghostscript):
```
gs -sDEVICE=pdfwrite -dPDFSETTINGS=/ebook -o small.pdf in.pdf
```

**Extract text** (then summarize/answer with `@read`):
```
pdftotext -layout in.pdf out.txt
```

**PDF → images** / **images → PDF**:
```
pdftoppm -png -r 150 in.pdf page
img2pdf img1.png img2.jpg -o out.pdf
```

**Inspect**: `pdfinfo in.pdf` (page count, size, encryption).

## Rules

- Work on copies; never overwrite the user's source PDF unless told to.
- For scanned/image PDFs with no text layer, hand off to the `ocr` skill.
- Report output path + page count after each operation.
