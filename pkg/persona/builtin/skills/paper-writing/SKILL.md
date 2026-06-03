---
name: paper-writing
description: Write and typeset formal documents — academic papers, reports, articles — using Markdown + Pandoc and LaTeX. Convert between formats (md↔pdf↔docx↔tex), manage citations with BibTeX/CSL. Keyless, local. Use when asked to "write a paper", "format this as LaTeX", "convert to PDF/Word", "add citations".
allowed-tools: ["@coder", "Bash", "Write", "@read", "@webfetch"]
triggers:
  - write a paper
  - latex
  - convert to pdf
  - convert to docx
  - format as a report
  - add citations
  - bibliography
  - escrever um artigo
  - converter para pdf
  - formatar como relatório
---

# Paper / Document Writing

Local, keyless typesetting with **Pandoc** (+ LaTeX for PDF). Detect:
`command -v pandoc pdflatex xelatex 2>/dev/null`. Install: `brew install pandoc basictex` /
`apt install pandoc texlive-xetex` / `choco install pandoc miktex`.

## Author in Markdown

Write the document as markdown with a YAML metadata block:
```
---
title: A Study of X
author: Jane Doe
date: 2026-06-03
bibliography: refs.bib
---
# Introduction
As shown by prior work [@smith2024] ...
```

## Convert

```
pandoc paper.md -o paper.pdf --pdf-engine=xelatex     # PDF (needs LaTeX)
pandoc paper.md -o paper.docx                          # Word
pandoc paper.md -o paper.tex                           # LaTeX source
pandoc report.docx -o report.md                        # reverse: docx → markdown
```

## Citations

- Keep references in `refs.bib` (BibTeX). Cite with `[@key]`.
- Render with `--citeproc` and a style: `pandoc paper.md --citeproc --csl=ieee.csl -o paper.pdf`.
- Fetch BibTeX for a DOI/arXiv id via `@webfetch` (e.g. `https://doi.org/<doi>` with
  `Accept: application/x-bibtex`), then append to `refs.bib`.

## Workflow

1. Draft structure (sections), then prose.
2. Insert citations as you go; collect them in `refs.bib`.
3. Render to the requested format; if LaTeX errors, read the log and fix the offending markup.

## Rules

- For PDF you need a LaTeX engine — if absent, offer DOCX/HTML instead of failing.
- Report the output path; for papers, do a final pass for figure/table/citation references.
