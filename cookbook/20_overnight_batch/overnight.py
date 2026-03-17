#!/usr/bin/env python3
"""Recipe 20: Overnight Batch — submit N tasks at night, check results in the morning.

Fire a large batch of tasks, let all GPUs work through the night,
and collect a comprehensive results report by morning.

Usage:
    python overnight.py --tasks 100 --model llama3
    python overnight.py --tasks 200 --model mistral --hub http://192.168.1.10:9000
    python overnight.py --prompts my_prompts.txt --tasks 500
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

DEFAULT_PROMPTS = [
    "Summarise the key principles of thermodynamics.",
    "What is the difference between supervised and unsupervised learning?",
    "Explain how the internet works in three paragraphs.",
    "What are the main causes of climate change?",
    "Describe the human digestive system.",
    "What is blockchain technology and how does it work?",
    "Explain the concept of recursion with an example.",
    "What are the principles of object-oriented programming?",
    "Describe the water cycle and its importance.",
    "What is the difference between machine learning and deep learning?",
    "Explain how a neural network learns.",
    "What is quantum computing and what problems can it solve?",
    "Describe the structure of DNA.",
    "What is the significance of the Turing test?",
    "Explain the concept of entropy in information theory.",
    "What are the main differences between TCP and UDP?",
    "Describe how a compiler works.",
    "What is the difference between a process and a thread?",
    "Explain the CAP theorem in distributed systems.",
    "What is the Big Bang theory?",
]


async def submit_task(client: httpx.AsyncClient, hub: str,
                      prompt: str, model: str, max_tokens: int) -> str:
    task_id = f"overnight-{uuid.uuid4().hex[:8]}"
    await client.post(f"{hub}/tasks", json={
        "task_id": task_id, "model": model,
        "prompt": prompt, "max_tokens": max_tokens,
    })
    return task_id


async def poll_task(client: httpx.AsyncClient, hub: str,
                    task_id: str, timeout_s: int) -> dict:
    t0 = time.monotonic()
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
                return {"task_id": task_id, "state": "COMPLETE",
                        "agent_id": result.get("agent_id", ""),
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "tps": round(result.get("output_tokens", 0) / max(elapsed, 0.001), 1)}
            if state == "FAILED":
                return {"task_id": task_id, "state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "latency_s": 0, "output_tokens": 0, "tps": 0}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.3, 15.0)
    return {"task_id": task_id, "state": "TIMEOUT", "latency_s": 0, "output_tokens": 0, "tps": 0}


async def run_batch(hub: str, prompts: list[str], model: str,
                    max_tokens: int, concurrency: int, timeout_s: int) -> tuple[list[dict], float]:
    sem = asyncio.Semaphore(concurrency)
    results = []
    completed_count = 0

    async def bounded(client, tid, prompt_idx):
        nonlocal completed_count
        async with sem:
            result = await poll_task(client, hub, tid, timeout_s)
            completed_count += 1
            state = result["state"]
            if completed_count % 10 == 0 or state != "COMPLETE":
                ts = datetime.now().strftime("%H:%M:%S")
                click.echo(f"  [{ts}] {completed_count:>4}/{len(prompts)}  "
                           f"{state:<10}  {result.get('latency_s', 0):>5.1f}s  "
                           f"{result.get('output_tokens', 0):>5} tok")
            return result

    wall_start = time.monotonic()
    async with httpx.AsyncClient(timeout=httpx.Timeout(120.0)) as client:
        # Submit all tasks first
        click.echo(f"  Submitting {len(prompts)} tasks...")
        task_ids = []
        for i, prompt in enumerate(prompts):
            try:
                tid = await submit_task(client, hub, prompt, model, max_tokens)
                task_ids.append((tid, i))
                if (i + 1) % 20 == 0:
                    click.echo(f"    Submitted {i+1}/{len(prompts)}...")
            except Exception as exc:
                click.echo(f"    Submit failed at {i+1}: {exc}")
                task_ids.append((None, i))

        click.echo(f"  All {len(task_ids)} tasks submitted. Waiting for completion...")
        click.echo(f"  (Progress shown every 10 completions)\n")

        # Poll all in parallel
        results = await asyncio.gather(
            *[bounded(client, tid, i) for tid, i in task_ids if tid]
        )

    wall_time = time.monotonic() - wall_start
    return list(results), wall_time


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--tasks", default=100, show_default=True, type=int,
              help="Total number of tasks to submit")
@click.option("--model", default="llama3", show_default=True)
@click.option("--max-tokens", default=256, show_default=True, type=int)
@click.option("--concurrency", default=30, show_default=True, type=int)
@click.option("--timeout", default=600, show_default=True, type=int,
              help="Per-task timeout in seconds")
@click.option("--prompts", "prompts_file", default=None, type=click.Path(exists=True),
              help="Text file with one prompt per line")
@click.option("--out", default="", help="Output JSON file path")
def main(hub, tasks, model, max_tokens, concurrency, timeout, prompts_file, out):
    """Submit a large batch of tasks overnight across all GPU agents."""
    hub = hub.rstrip("/")

    # Load prompts
    if prompts_file:
        lines = Path(prompts_file).read_text().splitlines()
        base_prompts = [l.strip() for l in lines if l.strip() and not l.startswith("#")]
    else:
        base_prompts = DEFAULT_PROMPTS

    # Repeat prompts to fill task count
    prompts = [base_prompts[i % len(base_prompts)] for i in range(tasks)]

    start_time = datetime.now()
    click.echo(f"\n  Overnight Batch")
    click.echo(f"    Hub:         {hub}")
    click.echo(f"    Tasks:       {tasks}")
    click.echo(f"    Model:       {model}")
    click.echo(f"    Concurrency: {concurrency}")
    click.echo(f"    Started:     {start_time.strftime('%Y-%m-%d %H:%M:%S')}")

    try:
        agents = httpx.get(f"{hub}/agents", timeout=3.0).json().get("agents", [])
        online = [a for a in agents if a["status"] == "ONLINE"]
        click.echo(f"    Agents:      {len(online)} online")
    except Exception:
        pass

    click.echo()

    results, wall_time = asyncio.run(
        run_batch(hub, prompts, model, max_tokens, concurrency, timeout)
    )

    # Summary
    completed = [r for r in results if r["state"] == "COMPLETE"]
    failed = [r for r in results if r["state"] == "FAILED"]
    timed_out = [r for r in results if r["state"] == "TIMEOUT"]
    total_tokens = sum(r.get("output_tokens", 0) for r in completed)
    throughput = total_tokens / wall_time if wall_time > 0 else 0

    agent_dist: dict[str, int] = {}
    for r in completed:
        aid = r.get("agent_id", "unknown")
        agent_dist[aid] = agent_dist.get(aid, 0) + 1

    click.echo(f"\n  {'='*60}")
    click.echo(f"  OVERNIGHT BATCH COMPLETE")
    click.echo(f"  Started:    {start_time.strftime('%Y-%m-%d %H:%M:%S')}")
    click.echo(f"  Finished:   {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    click.echo(f"  Wall time:  {wall_time/3600:.2f}h ({wall_time:.0f}s)")
    click.echo(f"  Completed:  {len(completed)}/{tasks}")
    click.echo(f"  Failed:     {len(failed)}")
    click.echo(f"  Timed out:  {len(timed_out)}")
    click.echo(f"  Tokens:     {total_tokens:,}")
    click.echo(f"  Throughput: {throughput:.1f} tokens/s")
    if completed:
        avg_lat = sum(r["latency_s"] for r in completed) / len(completed)
        click.echo(f"  Avg latency:{avg_lat:.1f}s")
    click.echo(f"\n  Agent distribution:")
    for aid, cnt in sorted(agent_dist.items(), key=lambda x: -x[1]):
        pct = cnt / max(len(completed), 1) * 100
        bar = "█" * int(pct / 5)
        click.echo(f"    ..{aid[-14:]:<16} {cnt:>4} tasks ({pct:.0f}%)  {bar}")

    # Save results
    summary = {
        "started": start_time.isoformat(), "finished": datetime.now().isoformat(),
        "hub": hub, "model": model, "tasks": tasks,
        "completed": len(completed), "failed": len(failed), "timed_out": len(timed_out),
        "wall_time_s": round(wall_time, 2), "total_tokens": total_tokens,
        "throughput_tps": round(throughput, 2),
        "agent_distribution": agent_dist,
    }
    out_path = out or str(SCRIPT_DIR / f"overnight_{start_time.strftime('%Y%m%d_%H%M')}.json")
    Path(out_path).write_text(json.dumps(summary, indent=2))
    click.echo(f"\n  Report: {out_path}\n")


if __name__ == "__main__":
    main()
