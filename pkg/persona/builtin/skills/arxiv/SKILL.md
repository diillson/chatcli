---
name: arxiv
description: Search and retrieve academic papers from arXiv by keyword, author, category, or ID, and read abstracts or full PDFs. Free, keyless. Use when asked to "find papers on", "search arXiv", "summarize this paper", or given an arXiv ID/link.
allowed-tools: ["@coder", "@webfetch", "Bash"]
triggers:
  - arxiv
  - find papers
  - search papers
  - research papers on
  - academic paper
  - summarize this paper
  - artigos sobre
  - papers sobre
  - artigo acadêmico
---

# arXiv Research

Search arXiv via its free REST API — no key, no dependency, just HTTP (via `@coder exec`/Bash
`curl`, or `@webfetch`). The API returns Atom XML.

## Search

```
curl -s "https://export.arxiv.org/api/query?search_query=all:diffusion+models&start=0&max_results=5"
```
Field prefixes: `ti:` title, `au:` author, `abs:` abstract, `cat:` category (e.g. `cs.LG`).
Combine with `+AND+`/`+OR+`. Sort: `&sortBy=submittedDate&sortOrder=descending`.

## Fetch a specific paper

```
curl -s "https://export.arxiv.org/api/query?id_list=2402.03300"
```
Parse `<title>`, `<author><name>`, `<summary>`, and the `<link>` to the PDF with `grep`/`sed`
or pipe through `python3` for clean output.

## Read the paper

- Abstract page → `@webfetch` `https://arxiv.org/abs/<id>`
- Full text → `@webfetch` `https://arxiv.org/pdf/<id>` (then summarize sections the user wants)

## Workflow for "find + summarize"

1. Search → pick the top N by relevance/recency.
2. For each, fetch the abstract; present title, authors, year, one-line summary, link.
3. If the user picks one, `@webfetch` the PDF and give a structured summary
   (problem → method → results → limitations).

## Rules

- Respect arXiv's rate limit: ~1 request / 3s; batch with `max_results` instead of looping fast.
- Always include the arXiv ID and link so the user can verify.
