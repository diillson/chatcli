---
name: web-research
description: Conduct multi-source web research — search the web, open and read the most relevant pages, cross-check claims, and synthesize a cited answer. Use when asked to "research", "look up", "find information about", "what's the latest on", "compare X and Y" using current web info.
allowed-tools: ["@websearch", "@webfetch"]
triggers:
  - research
  - look up
  - find information about
  - what's the latest
  - search the web for
  - compare
  - pesquise sobre
  - procure na web
  - últimas notícias sobre
  - o que há de novo em
---

# Web Research

Native, keyless deep research using ChatCLI's own tools — `@websearch` to discover sources and
`@webfetch` to read them. No third-party paid search middleman required; the configured search
chain handles providers.

## Method (search → read → synthesize)

1. **Decompose** the question into 2–4 sub-queries (different angles/keywords).
2. **Search** each with `@websearch`; collect the most relevant, recent, authoritative URLs.
3. **Read** the top sources with `@webfetch` — don't answer from snippets alone; open the pages.
4. **Cross-check** any non-trivial claim against at least two independent sources. Flag conflicts.
5. **Synthesize** a structured answer with inline citations (URL or source name per claim).

## Quality bar

- Prefer primary sources (official docs, papers, vendor pages) over aggregators.
- Note publication dates; for "latest"/"current" questions, prefer the most recent.
- Distinguish fact from opinion/marketing. If sources disagree, say so and show both.
- If the web doesn't clearly answer it, say what's uncertain rather than guessing.

## Output

End with a short **Sources** list (title + URL). Keep the body tight; lead with the answer,
then the supporting detail.

## When to escalate

For long-running monitoring ("watch this topic and tell me when X changes"), pair this skill
with `@scheduler` (periodic re-run) and `@send` (notify the user on their channel).
