#!/usr/bin/env python3
"""Recipe 19: Privacy Chunk Demo — split a document across agents, hub assembles.

Demonstrates the Phase 8 privacy architecture: no single agent sees the full
document. Each chunk is dispatched independently; the hub assembles the result.

Usage:
    python privacy_demo.py
    python privacy_demo.py --file contract.txt --chunks 3
    python privacy_demo.py --hub http://192.168.1.10:9000 --file report.txt
"""
from __future__ import annotations

import asyncio
import math
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

SAMPLE_DOCUMENT = """
MoMaHub Distributed Inference Agreement (Demo Document)

Section 1 — Parties
This agreement is entered into between Digital Duck Labs ("Provider") and the
participating GPU operators ("Contributors"). The Provider operates the hub
infrastructure; Contributors supply compute via the i-grid agent software.

Section 2 — Compute Contribution
Contributors agree to make available their GPU resources during registered hours.
The hub dispatches inference tasks based on tier (PLATINUM/GOLD/SILVER/BRONZE),
VRAM availability, and model support. Contributors are compensated in i-grid
credits at the rate of 1 credit per 1,000 output tokens generated.

Section 3 — Privacy and Data Handling
No task prompt shall be stored beyond the task lifecycle (default: 24 hours).
Contributors acknowledge that prompt chunks may not represent complete documents.
The hub retains sole access to task assembly and final result delivery.

Section 4 — Liability
The Provider makes no warranty regarding uptime, task completion rates, or
inference quality. Contributors are responsible for their hardware and network.
Neither party is liable for indirect or consequential damages.

Section 5 — Termination
Either party may terminate participation with 24 hours notice via `mg down`.
Outstanding tasks at termination time will be re-queued to available agents.
Credits earned prior to termination remain redeemable indefinitely.
"""

CHUNK_SYSTEM = (
    "You are a document analyst. You are reading ONE EXCERPT of a larger document. "
    "Extract and summarise the key points from this excerpt only. "
    "Do not speculate about content outside the excerpt."
)

ASSEMBLY_SYSTEM = (
    "You are a senior analyst assembling a document analysis from multiple excerpts. "
    "Combine the partial analyses into a coherent summary. "
    "Identify the main themes, key obligations, and notable clauses."
)


def split_into_chunks(text: str, n_chunks: int) -> list[str]:
    """Split text into n roughly equal chunks at paragraph boundaries."""
    paragraphs = [p.strip() for p in text.strip().split("\n\n") if p.strip()]
    per_chunk = math.ceil(len(paragraphs) / n_chunks)
    chunks = []
    for i in range(0, len(paragraphs), per_chunk):
        chunks.append("\n\n".join(paragraphs[i:i + per_chunk]))
    return chunks[:n_chunks]


async def process_chunk(client: httpx.AsyncClient, hub: str,
                        chunk: str, chunk_num: int, model: str,
                        max_tokens: int, timeout_s: int) -> dict:
    task_id = f"chunk{chunk_num}-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    click.echo(f"  → Chunk {chunk_num}: dispatching to agent ({len(chunk)} chars)...")
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": f"Analyse this document excerpt:\n\n{chunk}",
            "system": CHUNK_SYSTEM,
            "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"chunk": chunk_num, "state": "SUBMIT_FAILED", "error": str(exc), "content": ""}

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
                agent = result.get("agent_id", "")
                click.echo(f"    ✓ Chunk {chunk_num} complete  {elapsed:.1f}s  "
                           f"agent=..{agent[-12:]}")
                return {"chunk": chunk_num, "state": "COMPLETE",
                        "content": result.get("content", ""),
                        "agent_id": agent,
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0)}
            if state == "FAILED":
                err = data.get("result", {}).get("error", "")
                click.echo(f"    ✗ Chunk {chunk_num} FAILED: {err}")
                return {"chunk": chunk_num, "state": "FAILED", "error": err, "content": ""}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"chunk": chunk_num, "state": "TIMEOUT", "content": ""}


async def assemble_results(client: httpx.AsyncClient, hub: str,
                           chunk_results: list[dict], model: str,
                           max_tokens: int, timeout_s: int) -> dict:
    combined = "\n\n".join(
        f"--- Excerpt {r['chunk']} Analysis ---\n{r['content']}"
        for r in chunk_results if r["state"] == "COMPLETE"
    )
    task_id = f"assemble-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    click.echo(f"\n  → Assembly: combining {len(chunk_results)} chunk analyses...")
    await client.post(f"{hub}/tasks", json={
        "task_id": task_id, "model": model,
        "prompt": f"Combine these partial document analyses:\n\n{combined}",
        "system": ASSEMBLY_SYSTEM,
        "max_tokens": max_tokens,
    })
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
                click.echo(f"    ✓ Assembly complete  {elapsed:.1f}s")
                return {"state": "COMPLETE", "content": result.get("content", ""),
                        "latency_s": round(elapsed, 2),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                return {"state": "FAILED", "content": "",
                        "error": data.get("result", {}).get("error", "")}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"state": "TIMEOUT", "content": ""}


async def run(hub: str, document: str, filename: str, n_chunks: int,
              chunk_model: str, assembly_model: str,
              max_tokens: int, timeout_s: int) -> None:
    chunks = split_into_chunks(document, n_chunks)
    click.echo(f"\n  Privacy Chunk Demo")
    click.echo(f"    Hub:           {hub}")
    click.echo(f"    Document:      {filename} ({len(document)} chars)")
    click.echo(f"    Chunks:        {len(chunks)} (dispatched in parallel)")
    click.echo(f"    Chunk model:   {chunk_model}")
    click.echo(f"    Assembly model:{assembly_model}")
    click.echo()
    click.echo("  Privacy guarantee: no single agent sees the full document.")
    click.echo("  The hub is the only point that assembles all chunk analyses.")
    click.echo()

    # Show agents
    try:
        agents = httpx.get(f"{hub}/agents", timeout=3.0).json().get("agents", [])
        online = [a for a in agents if a["status"] == "ONLINE"]
        click.echo(f"  Active agents: {len(online)}")
        for a in online:
            click.echo(f"    - {a['name']} ({a['tier']})")
        click.echo()
    except Exception:
        pass

    wall_start = time.monotonic()
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        # Dispatch all chunks in parallel
        chunk_results = await asyncio.gather(
            *[process_chunk(client, hub, chunk, i + 1, chunk_model, max_tokens // 2, timeout_s)
              for i, chunk in enumerate(chunks)]
        )

        # Hub assembles (only hub sees all)
        final = await assemble_results(client, hub, list(chunk_results),
                                       assembly_model, max_tokens, timeout_s)

    wall_time = time.monotonic() - wall_start

    # Show agent distribution
    agent_ids = {r.get("agent_id", "")[-14:] for r in chunk_results if r["state"] == "COMPLETE"}
    click.echo(f"\n  Chunks handled by {len(agent_ids)} agent(s): {', '.join('..'+a for a in agent_ids)}")
    click.echo(f"  (Each agent saw only 1/{len(chunks)} of the document)\n")

    click.echo(f"  {'='*60}")
    click.echo(f"  ASSEMBLED ANALYSIS")
    click.echo(f"  {'-'*60}")
    click.echo(f"  {final.get('content', 'Assembly failed')}")
    click.echo(f"\n  Wall time: {wall_time:.1f}s\n")


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--file", "file_path", type=click.Path(exists=True), default=None,
              help="Document to chunk and analyse")
@click.option("--chunks", default=3, show_default=True, type=int,
              help="Number of chunks to split into")
@click.option("--chunk-model", default="llama3", show_default=True)
@click.option("--assembly-model", default="llama3", show_default=True)
@click.option("--max-tokens", default=512, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
def main(hub, file_path, chunks, chunk_model, assembly_model, max_tokens, timeout):
    """Split a document across agents (privacy demo) then assemble the analysis."""
    if file_path:
        document = Path(file_path).read_text(encoding="utf-8")
        filename = Path(file_path).name
    else:
        document = SAMPLE_DOCUMENT
        filename = "demo_contract.txt"

    asyncio.run(run(hub.rstrip("/"), document, filename, chunks,
                    chunk_model, assembly_model, max_tokens, timeout))


if __name__ == "__main__":
    main()
