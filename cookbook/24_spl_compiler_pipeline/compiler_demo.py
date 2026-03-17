#!/usr/bin/env python3
"""Recipe 24: SPL Compiler Pipeline — 5-step chain demonstrating the MoMa Compiler.

Simulates the MoMa Compiler front-end → mid-end → back-end pipeline:
  Step 1: Translate input (non-English → English)
  Step 2: Extract key concepts (front-end parsing)
  Step 3: Optimise the query (mid-end rewriting)
  Step 4: Generate the response (back-end execution)
  Step 5: Format the output (post-processing)

Each step dispatched to the grid. Steps 2+3 run in parallel after Step 1.
Steps 4+5 run after 2+3 complete.

Usage:
    python compiler_demo.py "What is machine learning?"
    python compiler_demo.py "机器学习是什么？"   # Chinese input
    python compiler_demo.py "Qu'est-ce que l'IA?" --translate-model mistral
    python compiler_demo.py --demo
"""
from __future__ import annotations

import asyncio
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

STEP_SYSTEMS = {
    "translate": (
        "You are a translation assistant. If the input is already in English, "
        "output it unchanged. Otherwise translate it to English, preserving meaning."
    ),
    "concepts": (
        "You are an NLP analyst. Extract 3-5 key concepts from this query as a "
        "comma-separated list. Output only the concepts, nothing else."
    ),
    "optimise": (
        "You are a prompt engineer. Rewrite this query to be clearer, more specific, "
        "and more likely to get a precise answer. Output only the rewritten query."
    ),
    "generate": (
        "You are a knowledgeable assistant. Answer the question clearly and accurately "
        "in 3-5 sentences."
    ),
    "format": (
        "You are a technical writer. Take this AI response and format it as a clean, "
        "readable summary with: (1) a one-sentence TL;DR, (2) the full explanation."
    ),
}


async def run_step(client: httpx.AsyncClient, hub: str,
                   step_name: str, prompt: str, model: str,
                   max_tokens: int, timeout_s: int) -> dict:
    task_id = f"compiler-{step_name}-{uuid.uuid4().hex[:6]}"
    t0 = time.monotonic()
    system = STEP_SYSTEMS[step_name]

    click.echo(f"  → [{step_name:<10}] {model:<20} ...", nl=False)
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": prompt, "system": system,
            "max_tokens": max_tokens,
        })
    except Exception as exc:
        click.echo(f" SUBMIT_FAILED: {exc}")
        return {"step": step_name, "state": "SUBMIT_FAILED", "content": "", "error": str(exc)}

    deadline = time.monotonic() + timeout_s
    interval = 1.5
    while time.monotonic() < deadline:
        try:
            r = await client.get(f"{hub}/tasks/{task_id}")
            data = r.json()
            state = data.get("state", "")
            if state == "COMPLETE":
                result = data.get("result", {})
                elapsed = time.monotonic() - t0
                click.echo(f" ✓ {elapsed:.1f}s  {result.get('output_tokens', 0)} tok  "
                           f"agent=..{result.get('agent_id', '')[-10:]}")
                return {"step": step_name, "state": "COMPLETE",
                        "content": result.get("content", ""),
                        "model": result.get("model", model),
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                err = data.get("result", {}).get("error", "")
                click.echo(f" ✗ FAILED: {err}")
                return {"step": step_name, "state": "FAILED", "content": "", "error": err}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    click.echo(" TIMEOUT")
    return {"step": step_name, "state": "TIMEOUT", "content": ""}


async def run_pipeline(hub: str, query: str,
                       translate_model: str, analysis_model: str,
                       generate_model: str, format_model: str,
                       max_tokens: int, timeout_s: int) -> None:
    click.echo(f"\n  MoMa Compiler Pipeline Demo")
    click.echo(f"    Hub:     {hub}")
    click.echo(f"    Input:   {query[:80]}")
    click.echo()
    click.echo(f"  Front-end:")

    wall_start = time.monotonic()
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        # Step 1: Translate (sequential — all subsequent steps need English)
        step1 = await run_step(client, hub, "translate", query,
                               translate_model, 256, timeout_s)
        if step1["state"] != "COMPLETE":
            click.echo(f"  Pipeline aborted at Step 1: {step1.get('error', '')}")
            return
        english_query = step1["content"].strip()

        # Steps 2+3 in parallel (mid-end)
        click.echo(f"\n  Mid-end (parallel):")
        step2, step3 = await asyncio.gather(
            run_step(client, hub, "concepts", english_query,
                     analysis_model, 128, timeout_s),
            run_step(client, hub, "optimise", english_query,
                     analysis_model, 256, timeout_s),
        )
        optimised_query = step3["content"].strip() if step3["state"] == "COMPLETE" else english_query

        # Step 4: Generate (back-end)
        click.echo(f"\n  Back-end:")
        step4 = await run_step(client, hub, "generate", optimised_query,
                               generate_model, max_tokens, timeout_s)
        if step4["state"] != "COMPLETE":
            click.echo(f"  Pipeline aborted at Step 4.")
            return
        raw_response = step4["content"].strip()

        # Step 5: Format output
        step5 = await run_step(client, hub, "format",
                               f"Query: {english_query}\n\nResponse: {raw_response}",
                               format_model, max_tokens, timeout_s)

    wall_time = time.monotonic() - wall_start

    # Pipeline summary
    steps = [step1, step2, step3, step4, step5]
    agents_used = {s.get("agent_id", "")[-14:] for s in steps if s.get("agent_id")}

    click.echo(f"\n  {'='*60}")
    click.echo(f"  PIPELINE RESULTS")
    click.echo(f"  {'-'*60}")
    click.echo(f"  Original query:  {query}")
    click.echo(f"  English:         {english_query}")
    if step2["state"] == "COMPLETE":
        click.echo(f"  Key concepts:    {step2['content'].strip()}")
    click.echo(f"  Optimised query: {optimised_query}")
    click.echo(f"\n  FINAL OUTPUT:")
    click.echo(f"  {'-'*60}")
    final = step5["content"] if step5["state"] == "COMPLETE" else raw_response
    click.echo(f"  {final}")
    click.echo(f"\n  {'='*60}")
    click.echo(f"  Wall time:   {wall_time:.1f}s")
    click.echo(f"  Steps:       5 (1 sequential + 2 parallel + 2 sequential)")
    click.echo(f"  Agents used: {len(agents_used)}")
    total_tokens = sum(s.get("output_tokens", 0) for s in steps)
    click.echo(f"  Total tokens:{total_tokens}")
    click.echo()

    # Save HTML
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    out = Path(__file__).parent / f"compiler_demo_{ts}.html"
    out.write_text(_build_html(query, steps, wall_time, hub), encoding="utf-8")
    click.echo(f"  Report: {out}\n")


def _build_html(query, steps, wall_time, hub) -> str:
    step_labels = {
        "translate": "Step 1: Translate [front-end]",
        "concepts":  "Step 2: Extract Concepts [mid-end, parallel]",
        "optimise":  "Step 3: Optimise Query [mid-end, parallel]",
        "generate":  "Step 4: Generate Response [back-end]",
        "format":    "Step 5: Format Output [post-process]",
    }
    cards = ""
    for s in steps:
        label = step_labels.get(s["step"], s["step"])
        status_color = "#4ade80" if s["state"] == "COMPLETE" else "#f87171"
        cards += f"""<div class="card">
<div class="card-title">{label}
<span style="color:{status_color};font-size:.75rem">{s['state']}</span>
<span class="stat">{s.get('latency_s',0):.1f}s | {s.get('output_tokens',0)} tok | {s.get('model','')}</span>
</div>
<div class="content">{(s.get('content','') or s.get('error','')).replace('<','&lt;').replace('>','&gt;')}</div>
</div>"""

    return f"""<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>MoMa Compiler Pipeline Demo</title>
<style>body{{background:#0f1117;color:#e0e0e0;font-family:system-ui;padding:2rem;max-width:960px;margin:0 auto}}
h1{{color:#4f8ef7}}.meta{{color:#9ca3af;font-size:.85rem;margin-bottom:1.5rem}}
.card{{background:#1a1d27;border:1px solid #2d3148;border-radius:8px;padding:1rem;margin:1rem 0}}
.card-title{{font-weight:600;color:#4f8ef7;margin-bottom:.5rem;display:flex;gap:.8rem;align-items:baseline}}
.stat{{color:#9ca3af;font-size:.75rem;margin-left:auto}}.content{{white-space:pre-wrap;font-size:.88rem;line-height:1.6}}</style>
</head><body>
<h1>MoMa Compiler Pipeline Demo</h1>
<p class="meta">{datetime.now().strftime("%Y-%m-%d %H:%M")} | Hub: {hub} | Wall: {wall_time:.1f}s</p>
<div class="card"><div class="card-title">Input Query</div>
<div class="content">{query}</div></div>
{cards}
</body></html>"""


DEMO_QUERIES = [
    "What is distributed inference and why does it matter?",
    "机器学习和深度学习有什么区别？",  # Chinese
    "Qu'est-ce qu'un réseau de neurones?",  # French
]


@click.command()
@click.argument("query", default="")
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--translate-model", default="llama3", show_default=True)
@click.option("--analysis-model", default="llama3", show_default=True,
              help="Model for concept extraction + query optimisation")
@click.option("--generate-model", default="llama3", show_default=True)
@click.option("--format-model", default="llama3", show_default=True)
@click.option("--demo", is_flag=True, help="Run all 3 demo queries")
@click.option("--max-tokens", default=512, show_default=True, type=int)
@click.option("--timeout", default=180, show_default=True, type=int)
def main(query, hub, translate_model, analysis_model, generate_model,
         format_model, demo, max_tokens, timeout):
    """5-step compiler pipeline: translate → analyse → optimise → generate → format."""
    hub = hub.rstrip("/")
    queries = DEMO_QUERIES if demo else [query or DEMO_QUERIES[0]]
    for q in queries:
        asyncio.run(run_pipeline(hub, q, translate_model, analysis_model,
                                 generate_model, format_model, max_tokens, timeout))


if __name__ == "__main__":
    main()
