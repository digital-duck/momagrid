#!/usr/bin/env python3
"""Recipe 22: Rewards Report — pretty-print the reward ledger after a run.

Shows total tokens generated, credits earned, and per-agent breakdown.
Makes the reward economy tangible after an overnight batch or stress test.

Usage:
    python rewards_report.py
    python rewards_report.py --hub http://192.168.1.10:9000
    python rewards_report.py --out report.html
"""
from __future__ import annotations

from datetime import datetime
from pathlib import Path

import click
import httpx


def fetch_rewards(hub: str) -> list[dict]:
    resp = httpx.get(f"{hub}/rewards", timeout=5.0)
    resp.raise_for_status()
    return resp.json().get("summary", [])


def fetch_agents(hub: str) -> dict[str, dict]:
    try:
        agents = httpx.get(f"{hub}/agents", timeout=5.0).json().get("agents", [])
        return {a["operator_id"]: a for a in agents}
    except Exception:
        return {}


def fetch_tasks(hub: str, limit: int = 1000) -> list[dict]:
    try:
        return httpx.get(f"{hub}/tasks?limit={limit}", timeout=5.0).json().get("tasks", [])
    except Exception:
        return []


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--out", default="", help="Output HTML report path")
@click.option("--limit", default=500, show_default=True, type=int,
              help="Max tasks to analyse for per-model breakdown")
def main(hub, out, limit):
    """Display the full reward ledger with per-operator and grid totals."""
    hub = hub.rstrip("/")

    try:
        rewards = fetch_rewards(hub)
    except Exception as exc:
        raise click.ClickException(f"Cannot reach hub: {exc}")

    if not rewards:
        click.echo("  No rewards recorded yet. Run some tasks first.")
        return

    agents_by_op = fetch_agents(hub)
    tasks = fetch_tasks(hub, limit)

    # Totals
    total_tasks   = sum(r.get("total_tasks", 0) for r in rewards)
    total_tokens  = sum(r.get("total_tokens", 0) for r in rewards)
    total_credits = sum(r.get("total_credits", 0.0) for r in rewards)

    # Per-model breakdown from task history
    model_stats: dict[str, dict] = {}
    for t in tasks:
        if t.get("state") != "COMPLETE":
            continue
        m = t.get("model", "unknown")
        if m not in model_stats:
            model_stats[m] = {"tasks": 0, "tokens": 0, "latency_ms": []}
        model_stats[m]["tasks"] += 1
        model_stats[m]["tokens"] += t.get("output_tokens", 0)
        if t.get("latency_ms"):
            model_stats[m]["latency_ms"].append(t["latency_ms"])

    # Print
    click.echo(f"\n  Reward Economy Report")
    click.echo(f"    Hub:   {hub}")
    click.echo(f"    Time:  {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    click.echo()
    click.echo(f"  {'─'*50}")
    click.echo(f"  GRID TOTALS")
    click.echo(f"  {'─'*50}")
    click.echo(f"  Total tasks:    {total_tasks:>10,}")
    click.echo(f"  Total tokens:   {total_tokens:>10,}")
    click.echo(f"  Total credits:  {total_credits:>10.4f}")
    click.echo(f"  Credit rate:    1 credit per 1,000 output tokens (PoC)")
    click.echo()
    click.echo(f"  {'─'*50}")
    click.echo(f"  BY OPERATOR")
    click.echo(f"  {'─'*50}")
    click.echo(f"  {'Operator':<20} {'Tasks':>8} {'Tokens':>12} {'Credits':>10}")
    click.echo(f"  {'-'*52}")
    for r in sorted(rewards, key=lambda x: -x.get("total_credits", 0)):
        op = r.get("operator_id", "?")
        a = agents_by_op.get(op, {})
        tier = a.get("tier", "")
        label = f"{op} ({tier})" if tier else op
        click.echo(f"  {label:<20} {r.get('total_tasks', 0):>8,} "
                   f"{r.get('total_tokens', 0):>12,} "
                   f"{r.get('total_credits', 0.0):>10.4f}")

    if model_stats:
        click.echo()
        click.echo(f"  {'─'*50}")
        click.echo(f"  BY MODEL (last {limit} tasks)")
        click.echo(f"  {'─'*50}")
        click.echo(f"  {'Model':<25} {'Tasks':>8} {'Tokens':>10} {'Avg Lat':>10}")
        click.echo(f"  {'-'*55}")
        for m, s in sorted(model_stats.items(), key=lambda x: -x[1]["tasks"]):
            avg_lat = (sum(s["latency_ms"]) / len(s["latency_ms"])
                       if s["latency_ms"] else 0)
            click.echo(f"  {m:<25} {s['tasks']:>8,} {s['tokens']:>10,} "
                       f"{avg_lat:>9.0f}ms")

    click.echo()
    click.echo(f"  Note: Full reward economy (redemption, transfer, billing)")
    click.echo(f"        coming in Phase 9. Credits are currently indicative.\n")

    # HTML report
    if out or True:
        ts = datetime.now().strftime("%Y%m%d-%H%M%S")
        out_path = out or str(Path(__file__).parent / f"rewards_{ts}.html")
        Path(out_path).write_text(
            _build_html(rewards, model_stats, total_tasks, total_tokens,
                        total_credits, hub, agents_by_op),
            encoding="utf-8"
        )
        click.echo(f"  Report: {out_path}\n")


def _build_html(rewards, model_stats, total_tasks, total_tokens,
                total_credits, hub, agents_by_op) -> str:
    op_rows = ""
    for r in sorted(rewards, key=lambda x: -x.get("total_credits", 0)):
        op = r.get("operator_id", "?")
        a = agents_by_op.get(op, {})
        tier = a.get("tier", "")
        op_rows += (f"<tr><td>{op}</td><td>{tier}</td>"
                    f"<td>{r.get('total_tasks', 0):,}</td>"
                    f"<td>{r.get('total_tokens', 0):,}</td>"
                    f"<td>{r.get('total_credits', 0.0):.4f}</td></tr>")

    model_rows = ""
    for m, s in sorted(model_stats.items(), key=lambda x: -x[1]["tasks"]):
        avg_lat = (sum(s["latency_ms"]) / len(s["latency_ms"]) if s["latency_ms"] else 0)
        model_rows += (f"<tr><td>{m}</td><td>{s['tasks']:,}</td>"
                       f"<td>{s['tokens']:,}</td><td>{avg_lat:.0f}ms</td></tr>")

    return f"""<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>i-grid Reward Report</title>
<style>body{{background:#0f1117;color:#e0e0e0;font-family:system-ui;padding:2rem;max-width:900px;margin:0 auto}}
h1{{color:#4f8ef7}}.meta{{color:#9ca3af;font-size:.85rem;margin-bottom:1.5rem}}
.metrics{{display:grid;grid-template-columns:repeat(3,1fr);gap:1rem;margin:1.5rem 0}}
.metric{{background:#1a1d27;border:1px solid #2d3148;border-radius:8px;padding:1rem;text-align:center}}
.metric-value{{font-size:1.8rem;font-weight:700;color:#4f8ef7}}
.metric-label{{color:#9ca3af;font-size:.8rem;margin-top:.2rem}}
h2{{color:#4f8ef7;margin-top:2rem}}
table{{width:100%;border-collapse:collapse;margin-top:.5rem}}
th,td{{padding:.5rem .8rem;border:1px solid #2d3148;text-align:left}}
th{{background:#1a1d27;color:#4f8ef7}}</style>
</head><body>
<h1>i-grid Reward Economy Report</h1>
<p class="meta">{datetime.now().strftime("%Y-%m-%d %H:%M")} | Hub: {hub}</p>
<div class="metrics">
<div class="metric"><div class="metric-value">{total_tasks:,}</div><div class="metric-label">Total Tasks</div></div>
<div class="metric"><div class="metric-value">{total_tokens:,}</div><div class="metric-label">Total Tokens</div></div>
<div class="metric"><div class="metric-value">{total_credits:.4f}</div><div class="metric-label">Credits Earned</div></div>
</div>
<h2>By Operator</h2>
<table><tr><th>Operator</th><th>Tier</th><th>Tasks</th><th>Tokens</th><th>Credits</th></tr>
{op_rows}</table>
<h2>By Model</h2>
<table><tr><th>Model</th><th>Tasks</th><th>Tokens</th><th>Avg Latency</th></tr>
{model_rows}</table>
<p style="color:#9ca3af;margin-top:2rem;font-size:.8rem">
Phase 9 will add credit redemption, transfer, and operator billing.
</p>
</body></html>"""


if __name__ == "__main__":
    main()
