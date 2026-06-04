---
name: presentations
description: Create slide decks from the terminal — Markdown→slides with Marp, programmatic PPTX with python-pptx, or convert/export via LibreOffice. Keyless, local. Use when asked to "make a presentation", "create slides", "build a deck", "PPTX from this outline", "export slides to PDF".
allowed-tools: ["@coder", "Bash", "Write", "@read"]
triggers:
  - make a presentation
  - create slides
  - build a deck
  - powerpoint
  - pptx
  - export slides
  - faça uma apresentação
  - criar slides
  - apresentação de
---

# Presentations / Slides

Local, keyless deck building. Pick the path by what's installed and what the user wants.

## Path A — Markdown → slides (Marp, recommended)

Author plain markdown with `---` slide breaks, then render:
```
marp deck.md -o deck.pdf        # or --pptx, --html
```
Detect/install: `command -v marp` / `npm i -g @marp-team/marp-cli`.
Example `deck.md`:
```
---
marp: true
theme: default
---
# Title
Subtitle

---
## Agenda
- Point one
- Point two
```

## Path B — Programmatic PPTX (python-pptx)

For data-driven decks (charts, tables, generated slides), write a Python script:
```
pip install python-pptx
```
```
from pptx import Presentation
p = Presentation()
s = p.slides.add_slide(p.slide_layouts[0])
s.shapes.title.text = "Quarterly Review"
p.save("deck.pptx")
```

## Path C — Convert / export (LibreOffice headless)

```
soffice --headless --convert-to pdf deck.pptx
soffice --headless --convert-to pptx deck.md
```
Detect: `command -v soffice libreoffice`. Works on Windows/macOS/Linux.

## Rules

- Decide content first (outline → slides); keep ~1 idea per slide, short bullets.
- Report the output file path and slide count.
- For brand themes, ask for/reuse a template (`.pptx` template or Marp theme CSS).
