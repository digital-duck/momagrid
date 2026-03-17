#!/usr/bin/env python3
"""Recipe 12: Tier-Aware Dispatch — submit tasks with VRAM hints, verify routing.

Demonstrates that the dispatcher honours min_vram_gb and model requirements,
routing each task to the correct tier agent.

Usage:
    python tier_dispatch.py
    python tier_dispatch.py --hub http://192.168.1.10:9000
    python tier_dispatch.py --vram 8 --model llama3
"""
from __future__ import annotations

import asyncio
import time
import uuid
from datetime import datetime

import click
import httpx

TASKS = [
    {"label": "No VRAM constraint",  "model": "llama3",   "min_vram_gb": 0,  "prompt": "Name the planets in our solar system."},
    {"label": "VRAM >= 4 GB",        "model": "llama3",   "min_vram_gb": 4,  "prompt": "What is photosynthesis?"},
    {"label": "VRAM >= 8 GB",        "model": "llama3",   "min_vram_gb": 8,  "prompt": "Explain gradient descent in one paragraph."},
    {"label": "VRAM >= 10 GB",       "model": "llama3",   "min_vram_gb": 10, "prompt": "What is a transformer architecture?"},
]


async def submit_and_wait(client: httpx.AsyncClient, hub: str, task: dict,
                          max_tokens: int, timeout_s: int) -> dict:
    task_id = f"tier-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id,
            "model": task["model"],
            "prompt": task["prompt"],
            "max_tokens": max_tokens,
            "min_vram_gb": task["min_vram_gb"],
        })
    except Exception as exc:
        return {**task, "state": "SUBMIT_FAILED", "error": str(exc),
                "agent_id": "", "latency_s": 0, "output_tokens": 0}

    deadline = time.monotonic() + timeout_s
    interval = 1.5
    while time.monotonic() < deadline:
        try:
            r = await client.get(f"{hub}/tasks/{task_id}")
            data = r.json()
            state = data.get("state", "")
            if state == "COMPLETE":
                result = data.get("result", {})
                return {**task, "state": "COMPLETE",
                        "agent_id": result.get("agent_id", ""),
                        "latency_s": round(time.monotonic() - t0, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "content": result.get("content", "")[:120]}
            if state == "FAILED":
                return {**task, "state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "agent_id": "", "latency_s": 0, "output_tokens": 0}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)

    return {**task, "state": "TIMEOUT", "agent_id": "", "latency_s": 0, "output_tokens": 0}


async def run(hub: str, max_tokens: int, timeout_s: int, custom_vram: float,
              custom_model: str) -> None:
    tasks = TASKS.copy()
    if custom_vram >= 0:
        tasks = [{"label": f"Custom VRAM >= {custom_vram} GB",
                  "model": custom_model, "min_vram_gb": custom_vram,
                  "prompt": "Explain distributed AI inference in two sentences."}]

    click.echo(f"\n  Tier-Aware Dispatch")
    click.echo(f"    Hub:   {hub}")
    click.echo(f"    Tasks: {len(tasks)}")
    click.echo()

    # Fetch agents to show available tiers
    try:
        agents_data = httpx.get(f"{hub}/agents", timeout=3.0).json().get("agents", [])
        click.echo("  Available agents:")
        for a in agents_data:
            import json
            gpus = json.loads(a.get("gpus") or "[]")
            vram = gpus[0]["vram_gb"] if gpus else 0
            click.echo(f"    {a['name']:<20} tier={a['tier']:<10} vram={vram:.0f}GB  "
                       f"status={a['status']}")
        click.echo()
    except Exception:
        pass

    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        results = await asyncio.gather(
            *[submit_and_wait(client, hub, t, max_tokens, timeout_s) for t in tasks]
        )

    click.echo(f"  {'Label':<30} {'VRAM':>6}  {'State':<10} {'Agent':>20}  {'Lat':>6}  {'Tok':>5}")
    click.echo(f"  {'-'*85}")
    for r in results:
        agent_short = (".."+r.get("agent_id","")[-14:]) if r.get("agent_id") else "-"
        click.echo(f"  {r['label']:<30} {r['min_vram_gb']:>5.0f}G  "
                   f"{r['state']:<10} {agent_short:>20}  "
                   f"{r.get('latency_s',0):>5.1f}s  {r.get('output_tokens',0):>5}")

    passed = [r for r in results if r["state"] == "COMPLETE"]
    failed = [r for r in results if r["state"] != "COMPLETE"]
    click.echo(f"\n  {len(passed)}/{len(tasks)} dispatched successfully")
    if failed:
        for r in failed:
            click.echo(f"  FAILED: {r['label']} — {r.get('error', r['state'])}")
    click.echo()


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--max-tokens", default=128, show_default=True, type=int)
@click.option("--timeout", default=120, show_default=True, type=int)
@click.option("--vram", default=-1.0, type=float,
              help="Run a single custom task with this VRAM requirement (GB)")
@click.option("--model", default="llama3", show_default=True,
              help="Model for --vram custom task")
def main(hub, max_tokens, timeout, vram, model):
    """Submit tasks with VRAM hints and verify tier-aware routing."""
    asyncio.run(run(hub.rstrip("/"), max_tokens, timeout, vram, model))


if __name__ == "__main__":
    main()
