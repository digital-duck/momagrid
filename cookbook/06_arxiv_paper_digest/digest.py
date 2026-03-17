#!/usr/bin/env python3
"""
Recipe 06: Paper Digest — CLI version (Click).

Submit a list of papers (arxiv or any PDF URL) before bed. The i-grid
analyses them in parallel overnight. Wake up to a self-contained digest.

Usage:
    python digest.py 2312.12345 2401.99999 https://arxiv.org/abs/2409.11111
    python digest.py https://example.com/paper.pdf
    python digest.py --urls-file urls.txt --model llama3 --hub http://localhost:9000
    python digest.py --urls-file urls.txt --out ~/Desktop/my_digest.html
    python digest.py 2312.12345 --format docx --out digest.docx

Output: digest_YYYYMMDD_HHMM.html  (dark-mode, self-contained HTML) by default.
        --format docx/pdf for alternative outputs (requires: pip install mg-hub[format])
"""

from __future__ import annotations

import re
import sys
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

from dd_extract import PDFExtractor

# ---------------------------------------------------------------------------
# Config defaults
# ---------------------------------------------------------------------------

DEFAULT_HUB = "http://localhost:9000"
DEFAULT_MODEL = "llama3"
DEFAULT_MAX_TOKENS = 4096
DEFAULT_MAX_CHARS = 12_000

ARXIV_PDF_BASE = "https://arxiv.org/pdf"
ARXIV_ABS_BASE = "https://arxiv.org/abs"

_ARXIV_ID_RE = re.compile(
    r"(?:arxiv\.org/(?:abs|pdf)/)?(\d{4}\.\d{4,5}(?:v\d+)?)"
)

# ---------------------------------------------------------------------------
# Analysis prompt
# ---------------------------------------------------------------------------

SYSTEM = (
    "You are a rigorous academic reviewer who produces clear, structured paper digests. "
    "Write precisely — avoid vague phrases. Ground every claim in the provided text."
)

PROMPT_TPL = """\
## Paper text (first {chars:,} characters extracted from PDF)

{text}

---

Produce a structured digest with these sections:

### 1. Title & Core Claim
What does this paper claim to contribute? (1–2 sentences)

### 2. Problem Statement
What specific problem does it address and why does it matter?

### 3. Methodology
Key technical approach and design choices.

### 4. Key Results
Most important quantitative or qualitative findings.

### 5. Limitations & Open Questions
What does the paper leave unresolved? Honest weaknesses.

### 6. Relevance
Who benefits most from reading this? Practical or research implications.

### 7. One-Line Summary
A single crisp sentence suitable for a literature review citation.
"""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def parse_arxiv_id(raw: str) -> str | None:
    m = _ARXIV_ID_RE.search(raw.strip())
    return m.group(1) if m else None


def classify_url(raw: str) -> tuple[str, str | None]:
    """Return (kind, arxiv_id_or_none).

    kind is one of: 'arxiv', 'pdf', 'unsupported'.
    """
    raw = raw.strip()
    arxiv_id = parse_arxiv_id(raw)
    if arxiv_id:
        return "arxiv", arxiv_id
    if raw.startswith("http://") or raw.startswith("https://"):
        if raw.lower().endswith(".pdf"):
            return "pdf", None
        # Try HEAD to check content-type
        try:
            resp = httpx.head(raw, timeout=10.0, follow_redirects=True)
            ct = resp.headers.get("content-type", "")
            if "application/pdf" in ct:
                return "pdf", None
        except Exception:
            pass
        return "unsupported", None
    return "unsupported", None


def fetch_meta(arxiv_id: str) -> dict:
    meta = {"title": arxiv_id, "authors": ""}
    try:
        resp = httpx.get(f"{ARXIV_ABS_BASE}/{arxiv_id}", timeout=10.0, follow_redirects=True)
        html = resp.text
        m = re.search(r'<h1 class="title[^"]*">\s*(?:<span[^>]*>[^<]*</span>\s*)?([^<]+)', html)
        if m:
            meta["title"] = m.group(1).strip()
        m = re.search(r'<div class="authors">(.*?)</div>', html, re.DOTALL)
        if m:
            meta["authors"] = re.sub(r"\s+", " ", re.sub(r"<[^>]+>", "", m.group(1))).strip()
    except Exception:
        pass
    return meta


def submit_task(hub: str, paper_id: str, text: str, model: str, max_tokens: int) -> str:
    task_id = f"digest-{paper_id[:20].replace('.', '-').replace('/', '-')}-{uuid.uuid4().hex[:6]}"
    payload = {
        "task_id": task_id,
        "model": model,
        "prompt": PROMPT_TPL.format(text=text, chars=len(text)),
        "system": SYSTEM,
        "max_tokens": max_tokens,
        "temperature": 0.3,
    }
    with httpx.Client(timeout=10.0) as client:
        client.post(f"{hub}/tasks", json=payload).raise_for_status()
    return task_id


def poll_task(hub: str, task_id: str, timeout_s: int = 900) -> dict:
    deadline = time.monotonic() + timeout_s
    interval = 5.0
    with httpx.Client(timeout=5.0) as client:
        while time.monotonic() < deadline:
            try:
                r = client.get(f"{hub}/tasks/{task_id}")
                data = r.json()
                if data.get("state") in ("COMPLETE", "FAILED"):
                    return data
            except Exception:
                pass
            time.sleep(interval)
            interval = min(interval * 1.2, 20.0)
    return {"state": "TIMEOUT", "result": None}

# ---------------------------------------------------------------------------
# HTML output
# ---------------------------------------------------------------------------

_HTML = """\
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Paper Digest — {date}</title>
<style>
:root{{--bg:#0f1117;--card:#1a1d27;--accent:#4f8ef7;--text:#e0e0e0;
      --sub:#9ca3af;--border:#2d3148;--ok:#22c55e;--warn:#f59e0b}}
*{{box-sizing:border-box;margin:0;padding:0}}
body{{background:var(--bg);color:var(--text);
     font-family:'Segoe UI',system-ui,sans-serif;padding:2rem;max-width:960px;margin:0 auto}}
h1{{font-size:1.8rem;color:var(--accent);margin-bottom:.25rem}}
.meta{{color:var(--sub);font-size:.85rem;margin-bottom:2.5rem}}
.card{{background:var(--card);border:1px solid var(--border);border-radius:12px;
       padding:1.5rem;margin-bottom:1.8rem}}
.card-header{{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:1rem}}
.paper-title{{font-size:1.1rem;font-weight:600;color:var(--accent)}}
.paper-authors{{font-size:.8rem;color:var(--sub);margin-top:.2rem}}
.source-link{{font-size:.78rem;color:var(--sub);text-decoration:none;
             border:1px solid var(--border);border-radius:6px;padding:.2rem .5rem;white-space:nowrap}}
.source-link:hover{{color:var(--accent);border-color:var(--accent)}}
.digest{{font-size:.92rem;line-height:1.65;white-space:pre-wrap}}
.digest h3{{color:var(--accent);font-size:.9rem;margin:1rem 0 .3rem}}
.badge{{display:inline-block;font-size:.72rem;border-radius:20px;padding:.15rem .5rem;margin-left:.5rem}}
.ok{{background:rgba(34,197,94,.15);color:var(--ok)}}
.fail{{background:rgba(245,158,11,.15);color:var(--warn)}}
.stats{{font-size:.78rem;color:var(--sub);margin-top:1rem;border-top:1px solid var(--border);
        padding-top:.75rem;display:flex;gap:1.5rem;flex-wrap:wrap}}
footer{{margin-top:3rem;padding-top:1rem;border-top:1px solid var(--border);
        font-size:.75rem;color:var(--sub);text-align:center}}
</style>
</head>
<body>
<h1>Paper Digest</h1>
<p class="meta">Generated {datetime} &nbsp;·&nbsp; {n} paper(s) &nbsp;·&nbsp;
Model: {model} &nbsp;·&nbsp; Hub: {hub}</p>
{cards}
<footer>Generated by <strong>MoMaHub i-grid</strong> &nbsp;·&nbsp; Digital Duck &amp; Dog Team</footer>
</body>
</html>"""

_CARD = """\
<div class="card">
<div class="card-header">
  <div>
    <div class="paper-title">{title}<span class="badge {cls}">{state}</span></div>
    <div class="paper-authors">{authors}</div>
  </div>
  <a class="source-link" href="{source_url}" target="_blank">{source_label} ↗</a>
</div>
<div class="digest">{digest}</div>
<div class="stats"><span>Tokens: {tokens:,}</span><span>Latency: {lat:.1f}s</span></div>
</div>"""


def fmt_digest(text: str) -> str:
    out = []
    for line in text.split("\n"):
        if line.startswith("### "):
            out.append(f"<h3>{line[4:]}</h3>")
        else:
            out.append(line)
    return "\n".join(out)


def build_html(papers: list[dict], model: str, hub: str) -> str:
    cards = []
    for p in papers:
        state = p.get("state", "UNKNOWN")
        cls = "ok" if state == "COMPLETE" else "fail"
        content = p.get("content") or p.get("error") or "No result"
        r = p.get("result_data") or {}
        tokens = (r.get("input_tokens") or 0) + (r.get("output_tokens") or 0)
        lat_ms = r.get("latency_ms") or 0
        cards.append(_CARD.format(
            title=p.get("title", p["paper_id"]),
            cls=cls, state=state,
            authors=p.get("authors", ""),
            source_url=p.get("source_url", ""),
            source_label=p.get("source_label", p["paper_id"]),
            digest=fmt_digest(content),
            tokens=tokens, lat=lat_ms / 1000,
        ))
    return _HTML.format(
        date=datetime.now().strftime("%Y-%m-%d"),
        datetime=datetime.now().strftime("%Y-%m-%d %H:%M UTC"),
        n=len(papers), model=model, hub=hub,
        cards="\n".join(cards),
    )

# ---------------------------------------------------------------------------
# Build markdown digest (for docx/pdf output)
# ---------------------------------------------------------------------------

def build_markdown(papers: list[dict], model: str, hub: str) -> str:
    parts = [f"# Paper Digest\n\nGenerated {datetime.now().strftime('%Y-%m-%d %H:%M UTC')} "
             f"| {len(papers)} paper(s) | Model: {model}\n"]
    for p in papers:
        parts.append(f"\n## {p.get('title', p['paper_id'])}\n")
        if p.get("authors"):
            parts.append(f"*{p['authors']}*\n")
        content = p.get("content") or p.get("error") or "No result"
        parts.append(content)
        parts.append("")
    return "\n".join(parts)

# ---------------------------------------------------------------------------
# Click CLI
# ---------------------------------------------------------------------------

@click.command()
@click.argument("urls", nargs=-1)
@click.option("--urls-file", type=click.Path(exists=True), help="File with one URL/ID per line")
@click.option("--hub", default=DEFAULT_HUB, show_default=True, help="Hub URL")
@click.option("--model", default=DEFAULT_MODEL, show_default=True, help="Ollama model")
@click.option("--max-tokens", default=DEFAULT_MAX_TOKENS, type=int, show_default=True)
@click.option("--max-chars", default=DEFAULT_MAX_CHARS, type=int, show_default=True,
              help="PDF text chars per paper")
@click.option("--engine", type=click.Choice(["pypdf", "docling"]), default="pypdf",
              show_default=True, help="PDF extraction engine")
@click.option("--out", default="", help="Output file path (default: auto-generated)")
@click.option("--format", "out_fmt", type=click.Choice(["html", "docx", "pdf"]),
              default="html", show_default=True, help="Output format")
def digest(urls, urls_file, hub, model, max_tokens, max_chars, engine, out, out_fmt):
    """Digest papers from arxiv IDs, arxiv URLs, or any PDF URL.

    Examples:\n
      python digest.py 2312.12345 2401.99999\n
      python digest.py https://arxiv.org/abs/2409.11111\n
      python digest.py https://example.com/paper.pdf\n
      python digest.py --urls-file urls.txt --format docx
    """
    # Collect raw inputs
    raw = list(urls)
    if urls_file:
        raw += Path(urls_file).read_text().splitlines()
    raw = [r.strip() for r in raw if r.strip()]
    if not raw:
        raise click.UsageError("Provide at least one URL or arxiv ID.")

    extractor = PDFExtractor(engine=engine, max_chars=max_chars)

    click.echo(f"\nPaper Digest")
    click.echo(f"   Hub:    {hub}")
    click.echo(f"   Model:  {model}")
    click.echo(f"   Engine: {engine}")
    click.echo(f"   Papers: {len(raw)}")
    click.echo()

    # Phase 1: classify, fetch, extract, submit
    papers: list[dict] = []
    for entry in raw:
        kind, arxiv_id = classify_url(entry)

        if kind == "arxiv":
            click.echo(f"  [{arxiv_id}] fetching metadata...", nl=False)
            meta = fetch_meta(arxiv_id)
            click.echo(f" {meta['title'][:60]}")

            click.echo(f"  [{arxiv_id}] fetching PDF...", nl=False)
            try:
                pdf_url = f"{ARXIV_PDF_BASE}/{arxiv_id}"
                text = extractor.from_url(pdf_url)
                click.echo(f" {len(text):,} chars")
            except Exception as exc:
                click.echo(f" FAILED: {exc}")
                papers.append({"paper_id": arxiv_id, **meta, "task_id": None,
                               "state": "FAILED", "error": str(exc),
                               "source_url": f"https://arxiv.org/abs/{arxiv_id}",
                               "source_label": f"arxiv:{arxiv_id}"})
                continue

            click.echo(f"  [{arxiv_id}] submitting to grid...", nl=False)
            try:
                task_id = submit_task(hub, arxiv_id, text, model, max_tokens)
                click.echo(f" task_id={task_id}")
                papers.append({"paper_id": arxiv_id, **meta, "task_id": task_id,
                               "state": "PENDING",
                               "source_url": f"https://arxiv.org/abs/{arxiv_id}",
                               "source_label": f"arxiv:{arxiv_id}"})
            except Exception as exc:
                click.echo(f" FAILED: {exc}")
                papers.append({"paper_id": arxiv_id, **meta, "task_id": None,
                               "state": "FAILED", "error": str(exc),
                               "source_url": f"https://arxiv.org/abs/{arxiv_id}",
                               "source_label": f"arxiv:{arxiv_id}"})

        elif kind == "pdf":
            # Generic PDF URL
            short = entry[:60]
            click.echo(f"  [{short}] fetching PDF...", nl=False)
            try:
                text = extractor.from_url(entry)
                click.echo(f" {len(text):,} chars")
            except Exception as exc:
                click.echo(f" FAILED: {exc}")
                papers.append({"paper_id": entry, "title": entry, "authors": "",
                               "task_id": None, "state": "FAILED", "error": str(exc),
                               "source_url": entry, "source_label": "PDF"})
                continue

            click.echo(f"  [{short}] submitting to grid...", nl=False)
            try:
                task_id = submit_task(hub, entry, text, model, max_tokens)
                click.echo(f" task_id={task_id}")
                papers.append({"paper_id": entry, "title": entry, "authors": "",
                               "task_id": task_id, "state": "PENDING",
                               "source_url": entry, "source_label": "PDF"})
            except Exception as exc:
                click.echo(f" FAILED: {exc}")
                papers.append({"paper_id": entry, "title": entry, "authors": "",
                               "task_id": None, "state": "FAILED", "error": str(exc),
                               "source_url": entry, "source_label": "PDF"})

        else:
            click.echo(f"  [{entry[:60]}] web extraction not yet supported — skipping")

    if not papers:
        click.echo("No valid papers to process.", err=True)
        sys.exit(1)

    # Phase 2: poll for results
    pending = sum(1 for p in papers if p.get("task_id"))
    click.echo(f"\nWaiting for {pending} analyses...\n")
    for p in papers:
        if not p.get("task_id"):
            continue
        click.echo(f"  [{p['paper_id'][:40]}] polling...", nl=False)
        result = poll_task(hub, p["task_id"])
        state = result.get("state", "TIMEOUT")
        p["state"] = state
        r = result.get("result") or {}
        if state == "COMPLETE":
            p["content"] = r.get("content", "")
            p["result_data"] = r
            tokens = (r.get("input_tokens") or 0) + (r.get("output_tokens") or 0)
            click.echo(f" done ({tokens:,} tokens)")
        else:
            p["content"] = ""
            p["error"] = r.get("error", f"State: {state}")
            p["result_data"] = {}
            click.echo(f" FAILED: {p['error']}")

    # Phase 3: write output
    if out_fmt == "html":
        output_text = build_html(papers, model, hub)
        ext = ".html"
    elif out_fmt == "docx":
        ext = ".docx"
    elif out_fmt == "pdf":
        ext = ".pdf"
    else:
        ext = ".html"

    out_path = out or f"digest_{datetime.now().strftime('%Y%m%d_%H%M')}{ext}"

    if out_fmt == "html":
        Path(out_path).write_text(output_text, encoding="utf-8")
    elif out_fmt == "docx":
        from dd_format import markdown_to_docx
        md = build_markdown(papers, model, hub)
        markdown_to_docx(md, out_path, title="Paper Digest")
    elif out_fmt == "pdf":
        from dd_format import markdown_to_pdf
        md = build_markdown(papers, model, hub)
        markdown_to_pdf(md, out_path, title="Paper Digest")

    ok = sum(1 for p in papers if p.get("state") == "COMPLETE")
    click.echo(f"\nDigest written to: {out_path}  ({ok}/{len(papers)} papers analysed)")
    if out_fmt == "html":
        click.echo("   Open in your browser — dark mode, self-contained HTML.\n")


if __name__ == "__main__":
    digest()
