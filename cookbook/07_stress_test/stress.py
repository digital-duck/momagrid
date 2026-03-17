#!/usr/bin/env python3
"""Recipe 07: Stress Test -- fire N tasks rapidly, watch them fan out.

Demonstrates dispatcher load balancing across all agents on the grid.
Great for the LAN weekend: see all 3 GPUs light up simultaneously.

Usage:
    python stress.py                          # 20 tasks, default settings
    python stress.py -n 50 --model mistral    # 50 tasks with mistral
    python stress.py -n 10 --hub http://192.168.1.10:9000

Tip: run 'mg agents' in another terminal to watch agent status live.
     run 'mg logs -f' to watch pulse logs in real time.
"""
from __future__ import annotations

import asyncio
import os
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

SCRIPT_DIR = Path(__file__).resolve().parent


def load_prompts(path: Path) -> list[str]:
    """Load prompts from a text file (one per line, blanks/comments skipped)."""
    lines = path.read_text(encoding="utf-8").splitlines()
    return [l.strip() for l in lines if l.strip() and not l.strip().startswith("#")]


async def submit_and_wait(client: httpx.AsyncClient, hub: str, task_id: str,
                          prompt: str, model: str, max_tokens: int,
                          timeout_s: int) -> dict:
    t0 = time.monotonic()
    await client.post(f"{hub}/tasks", json={
        "task_id": task_id, "model": model, "prompt": prompt, "max_tokens": max_tokens,
    })
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
                return {"task_id": task_id, "state": "COMPLETE", "prompt": prompt,
                        "agent_id": result.get("agent_id", ""),
                        "output_tokens": result.get("output_tokens", 0),
                        "latency_s": round(elapsed, 2),
                        "tps": round(result.get("output_tokens", 0) / max(elapsed, 0.001), 1)}
            if state == "FAILED":
                return {"task_id": task_id, "state": "FAILED", "prompt": prompt,
                        "agent_id": "", "output_tokens": 0, "latency_s": 0, "tps": 0}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"task_id": task_id, "state": "TIMEOUT", "prompt": prompt,
            "agent_id": "", "output_tokens": 0, "latency_s": 0, "tps": 0}


def write_report(results: list[dict], hub: str, n: int, model: str,
                 max_tokens: int, concurrency: int, wall_time: float) -> str:
    """Write results to a markdown report file. Returns the file path."""
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = SCRIPT_DIR / f"results-{ts}.md"

    completed = [r for r in results if r["state"] == "COMPLETE"]
    failed = [r for r in results if r["state"] == "FAILED"]
    timed_out = [r for r in results if r["state"] == "TIMEOUT"]
    total_tokens = sum(r["output_tokens"] for r in completed)

    agent_counts: dict[str, int] = {}
    for r in completed:
        aid = r.get("agent_id", "unknown")
        agent_counts[aid] = agent_counts.get(aid, 0) + 1

    lines = [
        f"# Stress Test Results",
        f"",
        f"**Date**: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}",
        f"",
        f"## Configuration",
        f"",
        f"| Parameter | Value |",
        f"|-----------|-------|",
        f"| Hub | {hub} |",
        f"| Tasks | {n} |",
        f"| Model | {model} |",
        f"| Max tokens | {max_tokens} |",
        f"| Concurrency | {concurrency} |",
        f"",
        f"## Summary",
        f"",
        f"| Metric | Value |",
        f"|--------|-------|",
        f"| Completed | {len(completed)}/{n} |",
        f"| Failed | {len(failed)} |",
        f"| Timeout | {len(timed_out)} |",
        f"| Wall time | {wall_time:.1f}s |",
        f"| Total tokens | {total_tokens:,} |",
    ]

    if completed:
        avg_lat = sum(r["latency_s"] for r in completed) / len(completed)
        avg_tps = sum(r["tps"] for r in completed) / len(completed)
        throughput = total_tokens / wall_time
        lines += [
            f"| Avg latency | {avg_lat:.1f}s |",
            f"| Avg TPS | {avg_tps:.1f} |",
            f"| Grid throughput | {throughput:.1f} tokens/s |",
        ]

    if agent_counts:
        lines += [
            f"",
            f"## Agent Distribution",
            f"",
            f"| Agent | Tasks |",
            f"|-------|-------|",
        ]
        for aid, cnt in sorted(agent_counts.items(), key=lambda x: -x[1]):
            lines.append(f"| `{aid}` | {cnt} |")

    lines += [
        f"",
        f"## Task Details",
        f"",
        f"| # | State | Latency | Tokens | TPS | Agent | Prompt |",
        f"|---|-------|---------|--------|-----|-------|--------|",
    ]
    for i, r in enumerate(results, 1):
        prompt_preview = r.get("prompt", "")[:60]
        agent_short = r.get("agent_id", "")[-16:] or "-"
        lines.append(
            f"| {i} | {r['state']} | {r['latency_s']}s | {r['output_tokens']} "
            f"| {r['tps']} | `..{agent_short}` | {prompt_preview} |"
        )

    lines.append("")
    path.write_text("\n".join(lines), encoding="utf-8")
    return str(path)


async def run_stress(hub: str, n: int, model: str, max_tokens: int,
                     concurrency: int, timeout_s: int, prompts_file: str):
    prompt_path = Path(prompts_file) if prompts_file else SCRIPT_DIR / "prompts.txt"
    if not prompt_path.exists():
        click.echo(f"Prompts file not found: {prompt_path}", err=True)
        raise SystemExit(1)

    all_prompts = load_prompts(prompt_path)
    if not all_prompts:
        click.echo(f"No prompts found in {prompt_path}", err=True)
        raise SystemExit(1)

    prompts = [all_prompts[i % len(all_prompts)] for i in range(n)]
    task_ids = [f"stress-{uuid.uuid4().hex[:8]}" for _ in range(n)]

    click.echo(f"\n  Stress Test")
    click.echo(f"    Hub:         {hub}")
    click.echo(f"    Tasks:       {n}")
    click.echo(f"    Model:       {model}")
    click.echo(f"    Concurrency: {concurrency}")
    click.echo(f"    Max tokens:  {max_tokens}")
    click.echo(f"    Prompts:     {prompt_path} ({len(all_prompts)} unique)")
    click.echo()

    wall_start = time.monotonic()
    results = []
    sem = asyncio.Semaphore(concurrency)

    async def bounded(client, tid, prompt):
        async with sem:
            return await submit_and_wait(client, hub, tid, prompt, model, max_tokens, timeout_s)

    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        tasks = [bounded(client, tid, p) for tid, p in zip(task_ids, prompts)]
        for coro in asyncio.as_completed(tasks):
            r = await coro
            results.append(r)
            status = r["state"]
            agent = r.get("agent_id", "")[-12:] or "?"
            click.echo(f"    [{len(results):>3}/{n}] {status:<8} {r['latency_s']:>6.1f}s  "
                        f"{r['output_tokens']:>4} tok  {r['tps']:>5.1f} tps  agent=..{agent}")

    wall_time = time.monotonic() - wall_start

    # Summary
    completed = [r for r in results if r["state"] == "COMPLETE"]
    failed = [r for r in results if r["state"] == "FAILED"]
    timed_out = [r for r in results if r["state"] == "TIMEOUT"]
    total_tokens = sum(r["output_tokens"] for r in completed)

    click.echo(f"\n  {'='*60}")
    click.echo(f"  Results:")
    click.echo(f"    Completed: {len(completed)}/{n}  |  Failed: {len(failed)}  |  Timeout: {len(timed_out)}")
    click.echo(f"    Wall time: {wall_time:.1f}s")
    click.echo(f"    Total tokens: {total_tokens:,}")
    if completed:
        avg_lat = sum(r["latency_s"] for r in completed) / len(completed)
        avg_tps = sum(r["tps"] for r in completed) / len(completed)
        throughput = total_tokens / wall_time
        click.echo(f"    Avg latency: {avg_lat:.1f}s  |  Avg TPS: {avg_tps:.1f}")
        click.echo(f"    Grid throughput: {throughput:.1f} tokens/s")

    # Agent distribution
    agent_counts: dict[str, int] = {}
    for r in completed:
        aid = r.get("agent_id", "unknown")
        agent_counts[aid] = agent_counts.get(aid, 0) + 1
    if agent_counts:
        click.echo(f"\n  Agent distribution:")
        for aid, cnt in sorted(agent_counts.items(), key=lambda x: -x[1]):
            bar = "#" * cnt
            click.echo(f"    {aid[-16:]:<18} {cnt:>3} tasks  {bar}")

    # Persist results
    report_path = write_report(results, hub, n, model, max_tokens, concurrency, wall_time)
    click.echo(f"\n  Report saved to {report_path}")
    click.echo()


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("-n", "--num-tasks", default=20, show_default=True, type=int, help="Number of tasks")
@click.option("--model", default="llama3", show_default=True)
@click.option("--max-tokens", default=150, show_default=True, type=int)
@click.option("--concurrency", default=10, show_default=True, type=int,
              help="Max parallel submissions")
@click.option("--timeout", default=300, show_default=True, type=int, help="Per-task timeout (s)")
@click.option("--prompts", default="", help="Path to prompts file (default: prompts.txt alongside script)")
def main(hub, num_tasks, model, max_tokens, concurrency, timeout, prompts):
    """Fire N tasks at the grid and measure throughput."""
    asyncio.run(run_stress(hub, num_tasks, model, max_tokens, concurrency, timeout, prompts))


if __name__ == "__main__":
    main()
