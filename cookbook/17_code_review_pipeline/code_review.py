#!/usr/bin/env python3
"""Recipe 17: Code Review Pipeline — multi-step, multi-model, multi-agent.

Step 1: deepseek-coder-v2 reviews code for bugs, security, improvements.
Step 2: llama3 summarises the review into a concise action list.
Step 3: qwen2.5-coder suggests a refactored version.

Each step dispatched to the grid independently.

Usage:
    python code_review.py --file mycode.py
    python code_review.py --stdin < mycode.py
    python code_review.py --hub http://192.168.1.10:9000 --file app.py
    cat mycode.py | python code_review.py --stdin
"""
from __future__ import annotations

import asyncio
import sys
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

REVIEW_SYSTEM = (
    "You are a senior software engineer conducting a thorough code review. "
    "Identify bugs, security vulnerabilities, performance issues, and style problems. "
    "Be specific — cite line numbers or variable names where possible."
)

SUMMARY_SYSTEM = (
    "You are a technical lead summarising a code review. "
    "Produce a numbered action list of the top issues to fix, ordered by severity. "
    "Be concise — one line per item."
)

REFACTOR_SYSTEM = (
    "You are an expert programmer. Based on the code and the review, "
    "suggest the most important refactoring. Show the improved code snippet only, "
    "with a brief comment explaining each change."
)


async def submit_and_wait(client: httpx.AsyncClient, hub: str,
                          prompt: str, system: str, model: str,
                          max_tokens: int, timeout_s: int,
                          label: str) -> dict:
    task_id = f"cr-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    click.echo(f"  → [{label}] submitting to {model}...")
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": prompt, "system": system,
            "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"label": label, "state": "SUBMIT_FAILED", "error": str(exc), "content": ""}

    deadline = time.monotonic() + timeout_s
    interval = 2.0
    while time.monotonic() < deadline:
        try:
            r = await client.get(f"{hub}/tasks/{task_id}")
            data = r.json()
            state = data.get("state", "")
            if state == "COMPLETE":
                result = data.get("result", {})
                elapsed = time.monotonic() - t0
                click.echo(f"    ✓ [{label}] done in {elapsed:.1f}s  "
                           f"{result.get('output_tokens', 0)} tokens  "
                           f"agent=..{result.get('agent_id', '')[-12:]}")
                return {"label": label, "state": "COMPLETE",
                        "content": result.get("content", ""),
                        "model": result.get("model", model),
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                err = data.get("result", {}).get("error", "")
                click.echo(f"    ✗ [{label}] FAILED: {err}")
                return {"label": label, "state": "FAILED", "error": err, "content": ""}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"label": label, "state": "TIMEOUT", "content": ""}


async def run(hub: str, code: str, filename: str,
              reviewer_model: str, summariser_model: str, refactor_model: str,
              max_tokens: int, timeout_s: int) -> None:
    click.echo(f"\n  Code Review Pipeline")
    click.echo(f"    Hub:      {hub}")
    click.echo(f"    File:     {filename} ({len(code)} chars)")
    click.echo(f"    Step 1:   {reviewer_model} → review")
    click.echo(f"    Step 2:   {summariser_model} → summarise")
    click.echo(f"    Step 3:   {refactor_model} → refactor")
    click.echo()

    wall_start = time.monotonic()
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        # Step 1: Review (sequential — each step depends on the previous)
        review_result = await submit_and_wait(
            client, hub,
            prompt=f"Review this code:\n\n```\n{code}\n```",
            system=REVIEW_SYSTEM,
            model=reviewer_model,
            max_tokens=max_tokens,
            timeout_s=timeout_s,
            label="Review",
        )
        if review_result["state"] != "COMPLETE":
            click.echo(f"  Pipeline aborted at Step 1: {review_result.get('error', '')}")
            return

        # Step 2 + 3 in parallel (both use the review output)
        review_text = review_result["content"]
        summary_result, refactor_result = await asyncio.gather(
            submit_and_wait(
                client, hub,
                prompt=f"Summarise this code review as a numbered action list:\n\n{review_text}",
                system=SUMMARY_SYSTEM,
                model=summariser_model,
                max_tokens=512,
                timeout_s=timeout_s,
                label="Summary",
            ),
            submit_and_wait(
                client, hub,
                prompt=(f"Original code:\n```\n{code[:3000]}\n```\n\n"
                        f"Review findings:\n{review_text[:2000]}\n\n"
                        "Suggest the most important refactoring:"),
                system=REFACTOR_SYSTEM,
                model=refactor_model,
                max_tokens=max_tokens,
                timeout_s=timeout_s,
                label="Refactor",
            ),
        )

    wall_time = time.monotonic() - wall_start

    # Print results
    click.echo(f"\n  {'='*60}")
    click.echo(f"  STEP 1 — Code Review ({reviewer_model})")
    click.echo(f"  {'-'*60}")
    click.echo(f"  {review_result['content']}\n")

    if summary_result["state"] == "COMPLETE":
        click.echo(f"  STEP 2 — Action List ({summariser_model})")
        click.echo(f"  {'-'*60}")
        click.echo(f"  {summary_result['content']}\n")

    if refactor_result["state"] == "COMPLETE":
        click.echo(f"  STEP 3 — Refactoring Suggestion ({refactor_model})")
        click.echo(f"  {'-'*60}")
        click.echo(f"  {refactor_result['content']}\n")

    click.echo(f"  {'='*60}")
    click.echo(f"  Total wall time: {wall_time:.1f}s  (steps 2+3 ran in parallel)")

    # Save HTML
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    out = Path(__file__).parent / f"code_review_{ts}.html"
    out.write_text(_build_html(filename, code, review_result, summary_result,
                               refactor_result, wall_time, hub), encoding="utf-8")
    click.echo(f"  Report: {out}\n")


def _build_html(filename, code, review, summary, refactor, wall_time, hub) -> str:
    def card(title, model, content, latency):
        return f"""<div class="card">
<div class="card-title">{title} <span class="model">{model}</span>
<span class="stat">{latency:.1f}s</span></div>
<div class="content">{content.replace('<','&lt;').replace('>','&gt;')}</div></div>"""

    return f"""<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Code Review: {filename}</title>
<style>body{{background:#0f1117;color:#e0e0e0;font-family:system-ui;padding:2rem;max-width:960px;margin:0 auto}}
h1{{color:#4f8ef7}}.card{{background:#1a1d27;border:1px solid #2d3148;border-radius:8px;padding:1rem;margin:1rem 0}}
.card-title{{font-weight:600;color:#4f8ef7;margin-bottom:.5rem}}.model{{color:#9ca3af;font-size:.8rem;margin-left:.5rem}}
.stat{{float:right;color:#9ca3af;font-size:.8rem}}.content{{white-space:pre-wrap;font-size:.88rem;line-height:1.6}}
.code{{background:#0a0d14;padding:1rem;border-radius:6px;font-family:monospace;white-space:pre-wrap;font-size:.82rem}}</style>
</head><body>
<h1>Code Review Pipeline</h1>
<p style="color:#9ca3af">{datetime.now().strftime("%Y-%m-%d %H:%M")} | {filename} | Hub: {hub} | Wall: {wall_time:.1f}s</p>
<div class="card"><div class="card-title">Source Code</div><div class="code">{code[:4000].replace('<','&lt;').replace('>','&gt;')}</div></div>
{card("Step 1: Review", review.get("model",""), review.get("content",""), review.get("latency_s",0))}
{card("Step 2: Action List", summary.get("model",""), summary.get("content",""), summary.get("latency_s",0))}
{card("Step 3: Refactoring", refactor.get("model",""), refactor.get("content",""), refactor.get("latency_s",0))}
</body></html>"""


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--file", "file_path", type=click.Path(exists=True), default=None,
              help="Python/code file to review")
@click.option("--stdin", "from_stdin", is_flag=True, help="Read code from stdin")
@click.option("--reviewer", default="deepseek-coder-v2", show_default=True)
@click.option("--summariser", default="llama3", show_default=True)
@click.option("--refactor-model", default="qwen2.5-coder", show_default=True)
@click.option("--max-tokens", default=1024, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
def main(hub, file_path, from_stdin, reviewer, summariser, refactor_model, max_tokens, timeout):
    """Multi-step code review: review → summarise → refactor, across multiple models."""
    if from_stdin:
        code = sys.stdin.read()
        filename = "stdin"
    elif file_path:
        code = Path(file_path).read_text(encoding="utf-8")
        filename = Path(file_path).name
    else:
        # Demo: review the dispatcher itself
        demo = Path(__file__).parents[2] / "igrid/hub/dispatcher.py"
        if demo.exists():
            code = demo.read_text(encoding="utf-8")[:4000]
            filename = "dispatcher.py (demo)"
        else:
            raise click.UsageError("Provide --file or --stdin, or ensure igrid/ is present.")

    asyncio.run(run(hub.rstrip("/"), code, filename,
                    reviewer, summariser, refactor_model, max_tokens, timeout))


if __name__ == "__main__":
    main()
