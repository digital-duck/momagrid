#!/usr/bin/env python3
"""Recipe 10: Chain Relay -- multi-step reasoning across grid nodes.

Demonstrates sequential task dependencies where each step's output feeds
into the next. Tasks may land on different agents, showing the grid
acting as a collaborative reasoning pipeline.

Pipeline:
    Step 1: RESEARCH   -- gather facts about a topic
    Step 2: ANALYZE    -- find patterns and insights from the research
    Step 3: SUMMARIZE  -- produce a concise executive summary

Usage:
    python chain.py "quantum computing"
    python chain.py "distributed AI inference" --model mistral
    python chain.py "climate change solutions" --hub http://192.168.1.10:9000

Watch 'mg logs -f' to see tasks hop between agents.
"""
from __future__ import annotations

import time
import uuid

import click
import httpx

STEPS = [
    {
        "name": "Research",
        "system": "You are a thorough researcher. Gather comprehensive facts and data.",
        "prompt": (
            "Research the following topic thoroughly. List key facts, "
            "important developments, major players, and current state of the art. "
            "Be specific with names, dates, and numbers where possible.\n\n"
            "Topic: {topic}"
        ),
    },
    {
        "name": "Analyze",
        "system": "You are an analytical thinker. Find patterns, connections, and insights.",
        "prompt": (
            "Based on the following research, provide a deep analysis:\n\n"
            "{prev_output}\n\n"
            "---\n\n"
            "Identify:\n"
            "1. Key trends and patterns\n"
            "2. Strengths and weaknesses\n"
            "3. Opportunities and risks\n"
            "4. Surprising connections or insights\n"
            "5. What most people get wrong about this topic"
        ),
    },
    {
        "name": "Summarize",
        "system": "You are a concise executive writer. Distill complex analysis into clear summaries.",
        "prompt": (
            "Based on the following research and analysis, write an executive summary:\n\n"
            "{prev_output}\n\n"
            "---\n\n"
            "Write a clear, actionable executive summary with:\n"
            "- One-paragraph overview\n"
            "- 3-5 key takeaways (bullet points)\n"
            "- Recommended next steps\n"
            "- One-sentence bottom line"
        ),
    },
]


def submit_and_wait(hub: str, task_id: str, system: str, prompt: str,
                    model: str, max_tokens: int, timeout_s: int) -> dict:
    httpx.post(f"{hub}/tasks", json={
        "task_id": task_id, "model": model, "prompt": prompt,
        "system": system, "max_tokens": max_tokens,
    }, timeout=10.0).raise_for_status()

    deadline = time.monotonic() + timeout_s
    interval = 2.0
    with httpx.Client(timeout=5.0) as client:
        while time.monotonic() < deadline:
            try:
                data = client.get(f"{hub}/tasks/{task_id}").json()
                state = data.get("state", "")
                if state == "COMPLETE":
                    r = data.get("result", {})
                    return {"state": "COMPLETE", "content": r.get("content", ""),
                            "output_tokens": r.get("output_tokens", 0),
                            "latency_ms": r.get("latency_ms", 0),
                            "agent_id": r.get("agent_id", "")}
                if state == "FAILED":
                    return {"state": "FAILED", "error": data.get("result", {}).get("error", "")}
            except Exception:
                pass
            time.sleep(interval)
            interval = min(interval * 1.3, 10.0)
    return {"state": "TIMEOUT"}


@click.command()
@click.argument("topic")
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--model", default="llama3", show_default=True)
@click.option("--max-tokens", default=2048, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
def main(topic, hub, model, max_tokens, timeout):
    """Run a multi-step reasoning chain on the grid.

    Each step feeds its output into the next step's prompt.
    """
    hub = hub.rstrip("/")
    click.echo(f"\n  Chain Relay")
    click.echo(f"    Topic:  {topic}")
    click.echo(f"    Hub:    {hub}")
    click.echo(f"    Model:  {model}")
    click.echo(f"    Steps:  {' -> '.join(s['name'] for s in STEPS)}")
    click.echo()

    prev_output = ""
    total_tokens = 0
    total_ms = 0.0
    agents_used = []

    for i, step in enumerate(STEPS, 1):
        task_id = f"chain-{step['name'].lower()}-{uuid.uuid4().hex[:6]}"
        prompt = step["prompt"].format(topic=topic, prev_output=prev_output)

        click.echo(f"  [{i}/{len(STEPS)}] {step['name']}...", nl=False)
        t0 = time.monotonic()
        result = submit_and_wait(hub, task_id, step["system"], prompt,
                                 model, max_tokens, timeout)
        wall = time.monotonic() - t0

        if result["state"] == "COMPLETE":
            prev_output = result["content"]
            total_tokens += result["output_tokens"]
            total_ms += result["latency_ms"]
            agent = result.get("agent_id", "")
            agents_used.append(agent)
            click.echo(f" {result['output_tokens']} tok  {wall:.1f}s  agent=..{agent[-12:]}")
        else:
            click.echo(f" {result['state']}: {result.get('error','')}")
            click.echo("\n  Chain broken. Stopping.\n")
            return

    # Final output
    click.echo(f"\n  {'='*60}")
    click.echo(f"  Chain complete!")
    click.echo(f"    Total tokens: {total_tokens:,}")
    click.echo(f"    Total latency: {total_ms:.0f}ms")
    unique_agents = set(agents_used)
    click.echo(f"    Agents used: {len(unique_agents)} ({', '.join(a[-12:] for a in unique_agents)})")
    click.echo(f"\n  --- Final Summary ---\n")
    click.echo(prev_output)
    click.echo()


if __name__ == "__main__":
    main()
