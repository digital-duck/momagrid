#!/usr/bin/env python3
"""Recipe 09: Document Pipeline -- PDF -> extract -> grid summarize -> format output.

End-to-end demo combining dd-extract, the i-grid, and dd-format:
  1. Extract text from a PDF (local file or URL) using dd-extract
  2. Submit the text to the grid for LLM summarization
  3. Format the summary into HTML, DOCX, or PDF using dd-format

Usage:
    python pipeline.py paper.pdf                          # summarize a local PDF
    python pipeline.py https://arxiv.org/pdf/2312.12345   # from URL
    python pipeline.py paper.pdf --format docx --out summary.docx
    python pipeline.py paper.pdf --model mistral --engine docling

Prerequisites:
    pip install dd-extract dd-format
    pip install dd-extract[docling]    # for layout-aware extraction
"""
from __future__ import annotations

import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

SYSTEM = (
    "You are an expert document analyst. Produce clear, well-structured summaries. "
    "Be specific and cite details from the text."
)

PROMPT_TPL = """\
## Document text ({chars:,} characters)

{text}

---

Produce a structured summary with:

### Overview
What is this document about? (2-3 sentences)

### Key Points
The most important facts, findings, or arguments (bullet list).

### Details
Expand on the key points with specific details from the text.

### Conclusion
What are the main takeaways?
"""


def extract_text(source: str, engine: str, max_chars: int) -> str:
    """Extract text from PDF file or URL."""
    from dd_extract import PDFExtractor
    extractor = PDFExtractor(engine=engine, max_chars=max_chars)
    if source.startswith("http://") or source.startswith("https://"):
        return extractor.from_url(source)
    return extractor.from_file(source)


def submit_and_wait(hub: str, text: str, model: str,
                    max_tokens: int, timeout_s: int) -> dict:
    task_id = f"doc-{uuid.uuid4().hex[:8]}"
    prompt = PROMPT_TPL.format(text=text, chars=len(text))

    httpx.post(f"{hub}/tasks", json={
        "task_id": task_id, "model": model, "prompt": prompt,
        "system": SYSTEM, "max_tokens": max_tokens,
    }, timeout=10.0).raise_for_status()

    deadline = time.monotonic() + timeout_s
    interval = 2.0
    with httpx.Client(timeout=5.0) as client:
        while time.monotonic() < deadline:
            try:
                data = client.get(f"{hub}/tasks/{task_id}").json()
                state = data.get("state", "")
                if state == "COMPLETE":
                    r = data.get("result", {})
                    return {"state": "COMPLETE", "content": r.get("content", ""),
                            "output_tokens": r.get("output_tokens", 0),
                            "latency_ms": r.get("latency_ms", 0),
                            "agent_id": r.get("agent_id", "")}
                if state == "FAILED":
                    return {"state": "FAILED", "error": data.get("result", {}).get("error", "")}
            except Exception:
                pass
            time.sleep(interval)
            interval = min(interval * 1.3, 10.0)
    return {"state": "TIMEOUT"}


def format_output(md_text: str, out_path: str, fmt: str, title: str):
    """Write summary to the chosen format."""
    if fmt == "html":
        from dd_format import markdown_to_html
        markdown_to_html(md_text, out_path, title=title)
    elif fmt == "docx":
        from dd_format import markdown_to_docx
        markdown_to_docx(md_text, out_path, title=title)
    elif fmt == "pdf":
        from dd_format import markdown_to_pdf
        markdown_to_pdf(md_text, out_path, title=title)
    else:
        # Plain markdown
        Path(out_path).write_text(md_text, encoding="utf-8")


@click.command()
@click.argument("source")
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--model", default="llama3", show_default=True)
@click.option("--engine", type=click.Choice(["pypdf", "docling"]), default="pypdf", show_default=True)
@click.option("--max-chars", default=12000, show_default=True, type=int,
              help="Max chars to extract from PDF")
@click.option("--max-tokens", default=4096, show_default=True, type=int)
@click.option("--format", "fmt", type=click.Choice(["html", "docx", "pdf", "md"]),
              default="html", show_default=True)
@click.option("--out", default="", help="Output path (default: auto)")
@click.option("--title", default="", help="Document title")
@click.option("--timeout", default=300, show_default=True, type=int)
def main(source, hub, model, engine, max_chars, max_tokens, fmt, out, title, timeout):
    """Extract PDF, summarize on the grid, format output.

    SOURCE can be a local PDF path or a URL to a PDF.
    """
    hub = hub.rstrip("/")
    doc_title = title or Path(source).stem if not source.startswith("http") else "Document Summary"

    click.echo(f"\n  Document Pipeline")
    click.echo(f"    Source:  {source}")
    click.echo(f"    Hub:    {hub}")
    click.echo(f"    Model:  {model}")
    click.echo(f"    Engine: {engine}")
    click.echo()

    # Step 1: Extract
    click.echo(f"  [1/3] Extracting text ({engine})...", nl=False)
    try:
        text = extract_text(source, engine, max_chars)
        click.echo(f" {len(text):,} chars")
    except Exception as exc:
        click.echo(f" FAILED: {exc}")
        return

    # Step 2: Summarize on grid
    click.echo(f"  [2/3] Summarizing on grid ({model})...", nl=False)
    result = submit_and_wait(hub, text, model, max_tokens, timeout)
    if result["state"] == "COMPLETE":
        click.echo(f" {result['output_tokens']} tokens  {result['latency_ms']:.0f}ms")
        summary = result["content"]
    else:
        click.echo(f" {result['state']}: {result.get('error','')}")
        return

    # Step 3: Format output
    out_path = out or f"summary_{datetime.now().strftime('%Y%m%d_%H%M')}.{fmt}"
    click.echo(f"  [3/3] Formatting ({fmt})...", nl=False)
    try:
        format_output(summary, out_path, fmt, doc_title)
        click.echo(f" {out_path}")
    except Exception as exc:
        click.echo(f" FAILED: {exc}")
        return

    click.echo(f"\n  Done! Open {out_path} to view.\n")


if __name__ == "__main__":
    main()
