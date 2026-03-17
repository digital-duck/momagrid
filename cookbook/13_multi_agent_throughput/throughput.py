#!/usr/bin/env python3
"""Recipe 13: Multi-Agent Throughput — measure scaling across 1, 2, 3 agents.

Fires N tasks per run and records wall-clock time and tokens/s.
Run once per agent configuration to build the scaling chart for the MoMa paper.

Usage:
    python throughput.py                          # 30 tasks, default model
    python throughput.py -n 60 --model mistral
    python throughput.py --label "3-agents" -n 60 --hub http://192.168.1.10:9000

Tip: run with 1 agent active, then 2, then 3, comparing the --label outputs.
"""
from __future__ import annotations

import asyncio
import json
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

SCRIPT_DIR = Path(__file__).resolve().parent

PROMPTS = [
    "What is Newton's first law of motion?",
    "Explain the water cycle in two sentences.",
    "What is machine learning?",
    "Name three programming languages and their main use cases.",
    "What is the capital of Australia?",
    "Explain what an API is.",
    "What is the difference between RAM and storage?",
    "Describe the greenhouse effect briefly.",
    "What is recursion in computer science?",
    "Explain supply and demand in economics.",
]


async def submit_and_wait(client: httpx.AsyncClient, hub: str,
                          prompt: str, model: str, max_tokens: int,
                          timeout_s: int) -> dict:
    task_id = f"tput-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": prompt, "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"state": "SUBMIT_FAILED", "error": str(exc),
                "latency_s": 0, "output_tokens": 0, "agent_id": ""}

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
                return {"state": "COMPLETE",
                        "agent_id": result.get("agent_id", ""),
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "tps": round(result.get("output_tokens", 0) / max(elapsed, 0.001), 1)}
            if state == "FAILED":
                return {"state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "latency_s": 0, "output_tokens": 0, "tps": 0, "agent_id": ""}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"state": "TIMEOUT", "latency_s": 0, "output_tokens": 0, "tps": 0, "agent_id": ""}


async def run_batch(hub: str, n: int, model: str, max_tokens: int,
                    concurrency: int, timeout_s: int) -> tuple[list[dict], float]:
    prompts = [PROMPTS[i % len(PROMPTS)] for i in range(n)]
    sem = asyncio.Semaphore(concurrency)

    async def bounded(client, prompt):
        async with sem:
            return await submit_and_wait(client, hub, prompt, model, max_tokens, timeout_s)

    wall_start = time.monotonic()
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        results = await asyncio.gather(*[bounded(client, p) for p in prompts])
    wall_time = time.monotonic() - wall_start
    return list(results), wall_time


def print_summary(results: list[dict], wall_time: float, label: str, n: int,
                  model: str, hub: str) -> dict:
    completed = [r for r in results if r["state"] == "COMPLETE"]
    total_tokens = sum(r["output_tokens"] for r in completed)
    throughput = total_tokens / wall_time if wall_time > 0 else 0

    agent_dist: dict[str, int] = {}
    for r in completed:
        aid = r.get("agent_id", "unknown")[-16:]
        agent_dist[aid] = agent_dist.get(aid, 0) + 1

    click.echo(f"\n  {'='*60}")
    click.echo(f"  Label:      {label}")
    click.echo(f"  Completed:  {len(completed)}/{n}")
    click.echo(f"  Wall time:  {wall_time:.1f}s")
    click.echo(f"  Tokens:     {total_tokens:,}")
    click.echo(f"  Throughput: {throughput:.1f} tokens/s  ← KEY METRIC")
    if completed:
        avg_lat = sum(r["latency_s"] for r in completed) / len(completed)
        avg_tps = sum(r["tps"] for r in completed) / len(completed)
        click.echo(f"  Avg latency:{avg_lat:.1f}s  |  Avg TPS/agent: {avg_tps:.1f}")
    click.echo(f"\n  Agent distribution:")
    for aid, cnt in sorted(agent_dist.items(), key=lambda x: -x[1]):
        bar = "█" * cnt
        click.echo(f"    ..{aid:<18} {cnt:>3} tasks  {bar}")

    return {
        "label": label, "hub": hub, "model": model, "n": n,
        "completed": len(completed), "failed": n - len(completed),
        "wall_time_s": round(wall_time, 2),
        "total_tokens": total_tokens,
        "throughput_tps": round(throughput, 2),
        "avg_latency_s": round(sum(r["latency_s"] for r in completed) / max(len(completed), 1), 2),
        "agent_distribution": agent_dist,
        "timestamp": datetime.now().isoformat(),
    }


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("-n", "--num-tasks", default=30, show_default=True, type=int,
              help="Number of tasks to fire")
@click.option("--model", default="llama3", show_default=True)
@click.option("--max-tokens", default=128, show_default=True, type=int)
@click.option("--concurrency", default=20, show_default=True, type=int,
              help="Max parallel submissions")
@click.option("--timeout", default=180, show_default=True, type=int)
@click.option("--label", default="", help="Run label e.g. '1-agent', '3-agents'")
@click.option("--out", default="", help="Append result to this JSON file")
def main(hub, num_tasks, model, max_tokens, concurrency, timeout, label, out):
    """Measure grid throughput — run with different agent counts to build scaling chart."""
    hub = hub.rstrip("/")
    run_label = label or f"run-{datetime.now().strftime('%H%M%S')}"

    # Show active agents
    try:
        agents = httpx.get(f"{hub}/agents", timeout=3.0).json().get("agents", [])
        online = [a for a in agents if a["status"] == "ONLINE"]
        click.echo(f"\n  Multi-Agent Throughput")
        click.echo(f"    Hub:         {hub}")
        click.echo(f"    Agents:      {len(online)} online")
        click.echo(f"    Tasks:       {num_tasks}")
        click.echo(f"    Model:       {model}")
        click.echo(f"    Concurrency: {concurrency}")
        click.echo(f"    Label:       {run_label}")
        for a in online:
            click.echo(f"      - {a['name']} ({a['tier']})")
    except Exception as e:
        click.echo(f"  Warning: could not fetch agents: {e}")

    click.echo()
    results, wall_time = asyncio.run(
        run_batch(hub, num_tasks, model, max_tokens, concurrency, timeout)
    )

    summary = print_summary(results, wall_time, run_label, num_tasks, model, hub)

    # Append to output file for comparison across runs
    if out:
        path = Path(out)
        runs = json.loads(path.read_text()) if path.exists() else []
        runs.append(summary)
        path.write_text(json.dumps(runs, indent=2))
        click.echo(f"\n  Result appended to {out}")
        # Print comparison if multiple runs
        if len(runs) > 1:
            click.echo(f"\n  Scaling comparison:")
            click.echo(f"  {'Label':<20} {'Agents':>7} {'Tokens/s':>10} {'Wall(s)':>8} {'Done':>6}")
            click.echo(f"  {'-'*55}")
            for r in runs:
                n_agents = len(r.get("agent_distribution", {}))
                click.echo(f"  {r['label']:<20} {n_agents:>7} "
                           f"{r['throughput_tps']:>10.1f} "
                           f"{r['wall_time_s']:>8.1f} "
                           f"{r['completed']:>6}")
    else:
        ts = datetime.now().strftime("%Y%m%d-%H%M%S")
        path = SCRIPT_DIR / f"throughput-{run_label}-{ts}.json"
        path.write_text(json.dumps(summary, indent=2))
        click.echo(f"\n  Result saved to {path}")
    click.echo()


if __name__ == "__main__":
    main()
