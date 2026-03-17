#!/usr/bin/env python3
"""Recipe 18: Smart Router — auto-route prompts to the right model by type.

Detects prompt intent (math, code, general) and routes to the optimal model.
Prototype of the MoMa Compiler back-end targeting pass.

Usage:
    python smart_router.py "What is the integral of x^2?"
    python smart_router.py "Write a Python function to reverse a linked list"
    python smart_router.py "Explain the French Revolution"
    python smart_router.py --interactive
    python smart_router.py --batch prompts.txt
"""
from __future__ import annotations

import asyncio
import re
import sys
import time
import uuid
from pathlib import Path

import click
import httpx

# Routing table: keyword patterns → model
ROUTES = [
    {"type": "math",    "model": "mathstral",       "patterns": [
        r"\b(integral|derivative|calculus|theorem|proof|equation|solve|factor|matrix|eigen|prime|factorial|probability|statistics|algebra|geometry|trigonometry|logarithm|limit|series|vector|tensor)\b",
        r"\d+\s*[\+\-\*\/\^]\s*\d+",  # arithmetic expression
        r"\bx\^?\d+\b",               # polynomial
    ]},
    {"type": "code",    "model": "qwen2.5-coder",   "patterns": [
        r"\b(function|class|def |import |algorithm|code|program|script|debug|refactor|implement|api|sql|query|regex|json|http|async|thread|recursion|sort|search)\b",
        r"```",
        r"\bwrite a\b.*\b(in python|in javascript|in go|in rust|in java|in c\+\+)\b",
    ]},
    {"type": "general", "model": "llama3",          "patterns": []},  # fallback
]


def detect_type(prompt: str) -> dict:
    """Return the best route for a prompt."""
    prompt_lower = prompt.lower()
    for route in ROUTES[:-1]:  # skip fallback
        for pattern in route["patterns"]:
            if re.search(pattern, prompt_lower):
                return route
    return ROUTES[-1]  # fallback: general


async def route_and_run(client: httpx.AsyncClient, hub: str,
                        prompt: str, override_model: str | None,
                        max_tokens: int, timeout_s: int) -> dict:
    route = detect_type(prompt)
    model = override_model or route["model"]
    task_type = route["type"]

    task_id = f"route-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": prompt, "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"prompt": prompt, "type": task_type, "model": model,
                "state": "SUBMIT_FAILED", "error": str(exc)}

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
                return {"prompt": prompt[:80], "type": task_type, "model": model,
                        "state": "COMPLETE",
                        "content": result.get("content", ""),
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "tps": round(result.get("output_tokens", 0) / max(elapsed, 0.001), 1),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                return {"prompt": prompt[:80], "type": task_type, "model": model,
                        "state": "FAILED",
                        "error": data.get("result", {}).get("error", "")}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"prompt": prompt[:80], "type": task_type, "model": model, "state": "TIMEOUT"}


async def run_prompts(hub: str, prompts: list[str], override_model: str | None,
                      max_tokens: int, timeout_s: int, show_response: bool) -> None:
    # Show routing decisions first
    click.echo(f"\n  Smart Router")
    click.echo(f"    Hub: {hub}")
    click.echo(f"    Prompts: {len(prompts)}")
    click.echo()
    click.echo(f"  Routing decisions:")
    for p in prompts:
        route = detect_type(p)
        model = override_model or route["model"]
        click.echo(f"    [{route['type']:<8}] → {model:<20}  {p[:60]}")
    click.echo()

    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        results = await asyncio.gather(
            *[route_and_run(client, hub, p, override_model, max_tokens, timeout_s)
              for p in prompts]
        )

    click.echo(f"  Results:")
    for r in results:
        if r["state"] == "COMPLETE":
            click.echo(f"\n  [{r['type']:<8}] {r['model']:<20} "
                       f"{r['latency_s']:>5.1f}s {r['tps']:>5.1f}tps")
            click.echo(f"  Q: {r['prompt']}")
            if show_response:
                click.echo(f"  A: {r['content'][:300]}")
        else:
            click.echo(f"  [{r['type']:<8}] {r['model']:<20} {r['state']}: "
                       f"{r.get('error', '')}")

    completed = [r for r in results if r["state"] == "COMPLETE"]
    click.echo(f"\n  {len(completed)}/{len(prompts)} completed")
    by_type: dict[str, int] = {}
    for r in completed:
        by_type[r["type"]] = by_type.get(r["type"], 0) + 1
    for t, cnt in sorted(by_type.items()):
        click.echo(f"    {t}: {cnt} tasks")
    click.echo()


DEMO_PROMPTS = [
    "What is the derivative of sin(x) * cos(x)?",
    "Write a Python function to find the longest common subsequence.",
    "Explain the causes of World War I in three sentences.",
    "Solve: 3x^2 - 12x + 9 = 0",
    "Implement a binary search tree insert method in Python.",
    "What is quantum entanglement?",
]


@click.command()
@click.argument("prompt", default="")
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--batch", "batch_file", type=click.Path(exists=True), default=None,
              help="File with one prompt per line")
@click.option("--interactive", is_flag=True, help="Interactive mode: type prompts one by one")
@click.option("--demo", is_flag=True, help="Run built-in demo prompts")
@click.option("--model", default="", help="Override auto-routing with a specific model")
@click.option("--max-tokens", default=512, show_default=True, type=int)
@click.option("--timeout", default=180, show_default=True, type=int)
@click.option("--show-response", is_flag=True, default=True, show_default=True,
              help="Print model responses")
def main(prompt, hub, batch_file, interactive, demo, model, max_tokens, timeout, show_response):
    """Auto-route prompts to the best model (math→mathstral, code→qwen-coder, general→llama3)."""
    override = model.strip() or None
    hub = hub.rstrip("/")

    if demo:
        prompts = DEMO_PROMPTS
    elif batch_file:
        lines = Path(batch_file).read_text().splitlines()
        prompts = [l.strip() for l in lines if l.strip() and not l.startswith("#")]
    elif interactive:
        prompts = []
        click.echo("Enter prompts (empty line to finish):")
        while True:
            p = click.prompt("  >", default="", show_default=False)
            if not p:
                break
            prompts.append(p)
        if not prompts:
            return
    elif prompt:
        prompts = [prompt]
    else:
        click.echo("No prompt given — running demo. Use --demo, --batch, or pass a prompt.")
        prompts = DEMO_PROMPTS

    asyncio.run(run_prompts(hub, prompts, override, max_tokens, timeout, show_response))


if __name__ == "__main__":
    main()
