---
name: jupyter
description: Run Python data-science work through Jupyter — execute notebooks headlessly, run cells against a live kernel, and convert notebooks to scripts/HTML. Keyless, local. Use when asked to "run this notebook", "execute these cells", "analyze this dataset in Python", "convert notebook to".
allowed-tools: ["@coder", "Bash", "@read", "Write"]
triggers:
  - jupyter
  - run this notebook
  - execute notebook
  - run these cells
  - analyze dataset
  - convert notebook
  - rodar o notebook
  - executar notebook
  - analisar os dados
---

# Jupyter / Data Science

Local, keyless notebook execution. Detect: `command -v jupyter python3` /
`Get-Command jupyter, python -ErrorAction SilentlyContinue`.
Install: `pipx install jupyter` or `pip install jupyterlab nbconvert ipykernel`.

## Execute a notebook headlessly

```
jupyter nbconvert --to notebook --execute --inplace analysis.ipynb
jupyter nbconvert --to html --execute report.ipynb        # produce a shareable HTML
```
Read results with `@read` on the output, or inspect the executed `.ipynb` cells.

## Run ad-hoc Python (no notebook)

For quick analysis, write a `.py` and run it via `@coder exec` `python3 script.py` — manage deps
in a venv: `python3 -m venv .venv && . .venv/bin/activate && pip install pandas matplotlib`.

## Convert

```
jupyter nbconvert --to script notebook.ipynb     # → .py
jupyter nbconvert --to pdf notebook.ipynb         # needs LaTeX (see paper-writing skill)
```

## Live kernel (interactive sessions)

For a persistent kernel across steps, start `jupyter console` or use `jupyter run`. Keep the
kernel alive only as long as needed; one-shot `--execute` is simpler for batch work.

## Rules

- Use a project venv; don't pollute the system Python.
- For plots, save to a file (`plt.savefig`) and report the path — terminals can't show inline plots.
- State which packages you installed; never assume pandas/numpy are present without checking.
