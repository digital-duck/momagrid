#!/usr/bin/env python3
"""Recipe 15: Agent Failover — verify tasks re-queue when an agent goes offline.

Submits a stream of tasks, then instructs you to kill an agent mid-run.
The script detects the failover and confirms all tasks eventually complete
on the remaining agents.

Usage:
    python failover.py
    python failover.py --hub http://192.168.1.10:9000 -n 30
    python failover.py --watch-only   # just watch task states, no submit
"""
from __future__ import annotations

import asyncio
import time
import uuid
from datetime import datetime

import click
import httpx

PROMPTS = [
    "Explain the concept of fault tolerance.",
    "What is load balancing in distributed systems?",
    "Describe the CAP theorem.",
    "What is a circuit breaker pattern?",
    "Explain eventual consistency.",
    "What is a heartbeat in distributed systems?",
    "Describe leader election.",
    "What is consensus in distributed computing?",
    "Explain the two-phase commit protocol.",
    "What is a distributed hash table?",
]


async def submit_task(client: httpx.AsyncClient, hub: str, prompt: str,
                      model: str, max_tokens: int) -> str:
    task_id = f"failover-{uuid.uuid4().hex[:8]}"
    await client.post(f"{hub}/tasks", json={
        "task_id": task_id, "model": model,
        "prompt": prompt, "max_tokens": max_tokens,
    })
    return task_id


async def poll_task(client: httpx.AsyncClient, hub: str, task_id: str,
                    timeout_s: int) -> dict:
    t0 = time.monotonic()
    deadline = time.monotonic() + timeout_s
    interval = 1.5
    while time.monotonic() < deadline:
        try:
            r = await client.get(f"{hub}/tasks/{task_id}")
            data = r.json()
            state = data.get("state", "")
            if state == "COMPLETE":
                result = data.get("result", {})
                return {"task_id": task_id, "state": "COMPLETE",
                        "agent_id": result.get("agent_id", ""),
                        "latency_s": round(time.monotonic() - t0, 2),
                        "output_tokens": result.get("output_tokens", 0)}
            if state == "FAILED":
                return {"task_id": task_id, "state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "agent_id": "", "latency_s": 0, "output_tokens": 0}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"task_id": task_id, "state": "TIMEOUT", "agent_id": "", "latency_s": 0, "output_tokens": 0}


async def watch_agents(hub: str, stop_event: asyncio.Event) -> None:
    seen_agents: set[str] = set()
    while not stop_event.is_set():
        try:
            agents = httpx.get(f"{hub}/agents", timeout=2.0).json().get("agents", [])
            current = {a["agent_id"]: a["status"] for a in agents}
            # Detect new offline agents
            for aid, status in current.items():
                key = f"{aid}-{status}"
                if key not in seen_agents:
                    seen_agents.add(key)
                    name = next((a["name"] for a in agents if a["agent_id"] == aid), aid[:16])
                    ts = datetime.now().strftime("%H:%M:%S")
                    if status == "OFFLINE":
                        click.echo(f"\n  [{ts}] ALERT: Agent {name} went OFFLINE — tasks will re-queue")
                    elif status == "ONLINE" and any(f"{aid}-OFFLINE" in s for s in seen_agents):
                        click.echo(f"\n  [{ts}] Agent {name} back ONLINE")
        except Exception:
            pass
        await asyncio.sleep(3)


async def run(hub: str, n: int, model: str, max_tokens: int,
              timeout_s: int, submit_delay: float) -> None:
    click.echo(f"\n  Agent Failover Test")
    click.echo(f"    Hub:    {hub}")
    click.echo(f"    Tasks:  {n}")
    click.echo(f"    Model:  {model}")
    click.echo()

    # Show initial agents
    try:
        agents = httpx.get(f"{hub}/agents", timeout=3.0).json().get("agents", [])
        click.echo(f"  Initial agents ({len(agents)} online):")
        for a in agents:
            click.echo(f"    - {a['name']:<20} {a['tier']:<10} {a['status']}")
    except Exception as e:
        click.echo(f"  Warning: {e}")

    click.echo()
    click.echo("  Submitting tasks... Kill an agent mid-run to test failover.")
    click.echo("  (Use `mg down` on the agent machine, or stop the agent process)")
    click.echo()

    stop_event = asyncio.Event()
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        # Start agent watcher
        watcher = asyncio.create_task(watch_agents(hub, stop_event))

        # Submit tasks with slight delay to spread across time window
        task_ids = []
        prompts = [PROMPTS[i % len(PROMPTS)] for i in range(n)]
        for i, prompt in enumerate(prompts):
            try:
                tid = await submit_task(client, hub, prompt, model, max_tokens)
                task_ids.append((tid, prompt))
                click.echo(f"  Submitted [{i+1:>3}/{n}] {tid}")
                await asyncio.sleep(submit_delay)
            except Exception as exc:
                click.echo(f"  Submit failed: {exc}")

        click.echo(f"\n  All {n} tasks submitted. Waiting for completion...\n")

        # Poll all tasks concurrently
        results = await asyncio.gather(
            *[poll_task(client, hub, tid, timeout_s) for tid, _ in task_ids]
        )

        stop_event.set()
        await watcher

    # Summary
    completed = [r for r in results if r["state"] == "COMPLETE"]
    failed = [r for r in results if r["state"] == "FAILED"]
    timed_out = [r for r in results if r["state"] == "TIMEOUT"]

    agent_dist: dict[str, int] = {}
    for r in completed:
        aid = r.get("agent_id", "unknown")
        agent_dist[aid] = agent_dist.get(aid, 0) + 1

    click.echo(f"  {'='*60}")
    click.echo(f"  Completed: {len(completed)}/{n}")
    click.echo(f"  Failed:    {len(failed)}")
    click.echo(f"  Timed out: {len(timed_out)}")
    click.echo(f"\n  Agent distribution (post-failover):")
    for aid, cnt in sorted(agent_dist.items(), key=lambda x: -x[1]):
        click.echo(f"    ..{aid[-14:]:<16} {cnt:>3} tasks")

    if len(completed) == n:
        click.echo(f"\n  PASS: All {n} tasks completed despite any failover events.")
    else:
        click.echo(f"\n  PARTIAL: {len(completed)}/{n} tasks completed.")
    click.echo()


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("-n", "--num-tasks", default=20, show_default=True, type=int)
@click.option("--model", default="llama3", show_default=True)
@click.option("--max-tokens", default=64, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
@click.option("--submit-delay", default=1.0, show_default=True, type=float,
              help="Seconds between submissions (spread tasks over time)")
def main(hub, num_tasks, model, max_tokens, timeout, submit_delay):
    """Submit tasks and verify resilience when an agent goes offline mid-run."""
    asyncio.run(run(hub.rstrip("/"), num_tasks, model, max_tokens, timeout, submit_delay))


if __name__ == "__main__":
    main()
