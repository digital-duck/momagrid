#!/usr/bin/env python3
"""Recipe 03: Two-hub cluster setup and test.

Interactive guide that walks you through setting up two hubs on the LAN,
peering them, joining agents, and verifying task forwarding.

Usage:
    python setup.py status                      # check both hubs
    python setup.py peer                        # peer Hub A <-> Hub B
    python setup.py test                        # submit a task and verify forwarding
    python setup.py full                        # run all steps

Replaces the old setup.sh (kept for reference) with a real, runnable script.
"""
from __future__ import annotations

import time
import uuid

import click
import httpx

DEFAULT_HUB_A = "http://192.168.1.10:9000"
DEFAULT_HUB_B = "http://192.168.1.20:9000"


def _check_hub(url: str) -> dict | None:
    try:
        resp = httpx.get(f"{url}/health", timeout=5.0)
        return resp.json()
    except Exception:
        return None


@click.group()
@click.option("--hub-a", default=DEFAULT_HUB_A, show_default=True, help="Hub A URL")
@click.option("--hub-b", default=DEFAULT_HUB_B, show_default=True, help="Hub B URL")
@click.pass_context
def cli(ctx, hub_a, hub_b):
    """Two-hub cluster setup and test."""
    ctx.ensure_object(dict)
    ctx.obj["hub_a"] = hub_a.rstrip("/")
    ctx.obj["hub_b"] = hub_b.rstrip("/")


@cli.command()
@click.pass_context
def status(ctx):
    """Check health of both hubs and list agents."""
    for label, url in [("Hub A", ctx.obj["hub_a"]), ("Hub B", ctx.obj["hub_b"])]:
        click.echo(f"\n  {label}: {url}")
        health = _check_hub(url)
        if not health:
            click.echo(f"    OFFLINE")
            continue
        click.echo(f"    hub_id={health.get('hub_id')}  agents_online={health.get('agents_online')}")
        try:
            agents = httpx.get(f"{url}/agents", timeout=5.0).json().get("agents", [])
            for a in agents:
                click.echo(f"    - {a.get('name','?'):<12} {a['agent_id'][:12]}..  {a['tier']:<8} {a['status']}")
        except Exception:
            pass
    # Cluster peers
    for label, url in [("Hub A", ctx.obj["hub_a"]), ("Hub B", ctx.obj["hub_b"])]:
        try:
            data = httpx.get(f"{url}/cluster/status", timeout=5.0).json()
            peers = data.get("peers", [])
            if peers:
                click.echo(f"\n  {label} peers:")
                for p in peers:
                    click.echo(f"    - {p.get('hub_id','')} {p.get('hub_url','')} [{p.get('status','')}]")
        except Exception:
            pass


@cli.command()
@click.pass_context
def peer(ctx):
    """Peer Hub A <-> Hub B."""
    hub_a, hub_b = ctx.obj["hub_a"], ctx.obj["hub_b"]
    click.echo(f"\n  Peering {hub_a} <-> {hub_b}")

    for src, dst, label in [(hub_a, hub_b, "A->B"), (hub_b, hub_a, "B->A")]:
        try:
            resp = httpx.post(f"{src}/cluster/peers", json={"url": dst}, timeout=10.0)
            data = resp.json()
            if data.get("accepted"):
                click.echo(f"    {label}: OK (peer_id={data.get('hub_id','')})")
            else:
                click.echo(f"    {label}: {data.get('message','rejected')}")
        except Exception as exc:
            click.echo(f"    {label}: FAILED ({exc})")

    click.echo("\n  Done. Verify with: python setup.py status")


@cli.command()
@click.option("--model", default="llama3", show_default=True)
@click.option("--prompt", default="What is distributed inference? Answer in one sentence.", show_default=True)
@click.pass_context
def test(ctx, model, prompt):
    """Submit a task to Hub A and watch it execute (possibly forwarded to Hub B)."""
    hub_a = ctx.obj["hub_a"]
    task_id = f"cluster-test-{uuid.uuid4().hex[:8]}"

    click.echo(f"\n  Submitting to Hub A: {hub_a}")
    click.echo(f"    task_id={task_id}  model={model}")
    try:
        httpx.post(f"{hub_a}/tasks",
                   json={"task_id": task_id, "model": model, "prompt": prompt, "max_tokens": 256},
                   timeout=10.0).raise_for_status()
    except Exception as exc:
        click.echo(f"    FAILED: {exc}")
        return

    click.echo(f"    Polling...", nl=False)
    deadline = time.monotonic() + 120
    interval = 2.0
    while time.monotonic() < deadline:
        try:
            data = httpx.get(f"{hub_a}/tasks/{task_id}", timeout=5.0).json()
            state = data.get("state", "")
            if state == "COMPLETE":
                r = data.get("result", {})
                click.echo(f" COMPLETE")
                click.echo(f"    agent:  {r.get('agent_id','')}")
                click.echo(f"    tokens: {r.get('input_tokens',0)}+{r.get('output_tokens',0)}")
                click.echo(f"    latency: {r.get('latency_ms',0):.0f}ms")
                click.echo(f"\n    {r.get('content','')[:200]}")
                return
            if state == "FAILED":
                click.echo(f" FAILED: {data.get('result',{}).get('error','')}")
                return
        except Exception:
            pass
        time.sleep(interval)
        interval = min(interval * 1.3, 10.0)
        click.echo(".", nl=False)
    click.echo(" TIMEOUT")


@cli.command()
@click.pass_context
def full(ctx):
    """Run all steps: status -> peer -> test."""
    ctx.invoke(status)
    ctx.invoke(peer)
    ctx.invoke(test)


if __name__ == "__main__":
    cli()
