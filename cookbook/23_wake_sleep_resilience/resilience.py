#!/usr/bin/env python3
"""Recipe 23: Wake/Sleep Resilience — grid adapts as agents join and leave.

Continuously submits tasks while agents join/leave dynamically.
Verifies the grid routes correctly to available agents at all times.
Perfect for the "sleeping gamer" narrative — the grid keeps working as
nodes come and go.

Usage:
    python resilience.py --duration 300
    python resilience.py --hub http://192.168.1.10:9000 --duration 600
"""
from __future__ import annotations

import asyncio
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

PROMPTS = [
    "What is resilience in distributed systems?",
    "Name the planets in order from the sun.",
    "What is 17 * 23?",
    "Explain what a load balancer does.",
    "What is the speed of light?",
    "What is a hash function?",
    "Explain the concept of idempotency.",
    "What is latency in networking?",
]


async def submit_and_wait(client: httpx.AsyncClient, hub: str,
                          prompt: str, model: str, max_tokens: int,
                          timeout_s: int) -> dict:
    task_id = f"resilience-{uuid.uuid4().hex[:8]}"
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
                        "output_tokens": result.get("output_tokens", 0)}
            if state == "FAILED":
                return {"state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "latency_s": round(time.monotonic() - t0, 2),
                        "output_tokens": 0, "agent_id": ""}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"state": "TIMEOUT", "latency_s": timeout_s, "output_tokens": 0, "agent_id": ""}


async def watch_agents(hub: str, agent_log: list[dict]) -> None:
    """Continuously poll agent status and log changes."""
    prev_agents: dict[str, str] = {}
    while True:
        try:
            agents = httpx.get(f"{hub}/agents", timeout=2.0).json().get("agents", [])
            current = {a["agent_id"]: (a["status"], a.get("name", a["agent_id"][:12]))
                       for a in agents}
            for aid, (status, name) in current.items():
                prev_status = prev_agents.get(aid)
                if prev_status != status:
                    ts = datetime.now().strftime("%H:%M:%S")
                    if status == "OFFLINE" and prev_status is not None:
                        msg = f"[{ts}] AGENT OFFLINE: {name}"
                        click.echo(f"\n  ⚠  {msg}")
                        agent_log.append({"ts": ts, "event": "OFFLINE", "agent": name})
                    elif status == "ONLINE" and prev_status == "OFFLINE":
                        msg = f"[{ts}] AGENT ONLINE: {name}"
                        click.echo(f"\n  ✓  {msg}")
                        agent_log.append({"ts": ts, "event": "ONLINE", "agent": name})
                    prev_agents[aid] = status
            # New agents
            for aid, (status, name) in current.items():
                if aid not in prev_agents:
                    ts = datetime.now().strftime("%H:%M:%S")
                    click.echo(f"\n  +  [{ts}] NEW AGENT: {name} ({status})")
                    agent_log.append({"ts": ts, "event": "JOINED", "agent": name})
                    prev_agents[aid] = status
        except Exception:
            pass
        await asyncio.sleep(5)


async def run(hub: str, duration_s: int, model: str, max_tokens: int,
              interval_s: float, timeout_s: int) -> None:
    click.echo(f"\n  Wake/Sleep Resilience Test")
    click.echo(f"    Hub:      {hub}")
    click.echo(f"    Duration: {duration_s}s")
    click.echo(f"    Model:    {model}")
    click.echo(f"    Interval: {interval_s}s between tasks")
    click.echo()
    click.echo("  Continuously submitting tasks. Bring agents online/offline to test.")
    click.echo("  (`mg join` on another machine, or `mg down` to stop an agent)")
    click.echo()

    agent_log: list[dict] = []
    results: list[dict] = []
    task_count = 0
    wall_start = time.monotonic()

    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        # Start agent watcher
        watcher = asyncio.create_task(watch_agents(hub, agent_log))

        deadline = time.monotonic() + duration_s
        pending: list[asyncio.Task] = []

        while time.monotonic() < deadline:
            prompt = PROMPTS[task_count % len(PROMPTS)]
            task = asyncio.create_task(
                submit_and_wait(client, hub, prompt, model, max_tokens, timeout_s)
            )
            pending.append(task)
            task_count += 1

            ts = datetime.now().strftime("%H:%M:%S")
            click.echo(f"  [{ts}] Task {task_count:>4} submitted  "
                       f"({int(deadline - time.monotonic())}s remaining)", nl=False)

            # Collect any completed tasks
            still_pending = []
            for t in pending:
                if t.done():
                    r = await t
                    results.append(r)
                    state = r["state"]
                    agent = r.get("agent_id", "")[-12:] or "?"
                    click.echo(f"  → {state}  ..{agent}  {r['latency_s']:.1f}s")
                else:
                    still_pending.append(t)
            pending = still_pending
            click.echo()

            await asyncio.sleep(interval_s)

        # Wait for remaining tasks
        if pending:
            click.echo(f"\n  Waiting for {len(pending)} in-flight tasks...")
            remaining = await asyncio.gather(*pending)
            results.extend(remaining)

        watcher.cancel()
        try:
            await watcher
        except asyncio.CancelledError:
            pass

    wall_time = time.monotonic() - wall_start
    completed = [r for r in results if r["state"] == "COMPLETE"]
    failed = [r for r in results if r["state"] != "COMPLETE"]

    agent_dist: dict[str, int] = {}
    for r in completed:
        aid = r.get("agent_id", "unknown")
        agent_dist[aid] = agent_dist.get(aid, 0) + 1

    click.echo(f"\n  {'='*60}")
    click.echo(f"  Resilience Test Complete")
    click.echo(f"  Duration:  {wall_time:.0f}s")
    click.echo(f"  Tasks:     {len(results)} submitted")
    click.echo(f"  Completed: {len(completed)}")
    click.echo(f"  Failed:    {len(failed)}")
    click.echo(f"  Success:   {len(completed)/max(len(results),1)*100:.1f}%")

    if agent_log:
        click.echo(f"\n  Agent events during test:")
        for e in agent_log:
            click.echo(f"    [{e['ts']}] {e['event']:<10} {e['agent']}")

    click.echo(f"\n  Task distribution across agents:")
    for aid, cnt in sorted(agent_dist.items(), key=lambda x: -x[1]):
        bar = "█" * cnt
        click.echo(f"    ..{aid[-14:]:<16} {cnt:>4} tasks  {bar}")

    if len(completed) == len(results):
        click.echo(f"\n  PASS: 100% tasks completed despite {len(agent_log)} agent event(s).")
    elif len(completed) / max(len(results), 1) >= 0.95:
        click.echo(f"\n  PASS: ≥95% tasks completed. Grid proved resilient.")
    else:
        click.echo(f"\n  PARTIAL: {len(completed)}/{len(results)} tasks completed.")
    click.echo()


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--duration", default=300, show_default=True, type=int,
              help="Test duration in seconds")
@click.option("--model", default="llama3", show_default=True)
@click.option("--max-tokens", default=64, show_default=True, type=int)
@click.option("--interval", default=3.0, show_default=True, type=float,
              help="Seconds between task submissions")
@click.option("--timeout", default=120, show_default=True, type=int,
              help="Per-task timeout in seconds")
def main(hub, duration, model, max_tokens, interval, timeout):
    """Continuously submit tasks while agents come and go — verify resilience."""
    asyncio.run(run(hub.rstrip("/"), duration, model, max_tokens, interval, timeout))


if __name__ == "__main__":
    main()
