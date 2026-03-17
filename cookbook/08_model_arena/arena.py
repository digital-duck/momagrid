#!/usr/bin/env python3
"""Recipe 08: Model Arena -- same prompt to multiple models, side-by-side comparison.

Submit identical prompts to different models and generate a self-contained HTML
report comparing quality, speed, and token efficiency.

Usage:
    python arena.py                                         # default 3 models
    python arena.py --models llama3,mistral,phi3,qwen2.5
    python arena.py --prompt "Explain quantum computing"
    python arena.py --out arena_results.html

Great for evaluating which model to deploy on your LAN grid.
"""
from __future__ import annotations

import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

DEFAULT_MODELS = "llama3,mistral,phi3"
DEFAULT_PROMPT = (
    "Explain the concept of information entropy (Shannon entropy) "
    "and its relationship to thermodynamic entropy. "
    "Include one concrete example. Keep it under 200 words."
)


def submit_and_wait(hub: str, model: str, prompt: str,
                    max_tokens: int, timeout_s: int) -> dict:
    task_id = f"arena-{model}-{uuid.uuid4().hex[:6]}"
    t0 = time.monotonic()
    try:
        httpx.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model, "prompt": prompt,
            "max_tokens": max_tokens,
        }, timeout=10.0).raise_for_status()
    except Exception as exc:
        return {"model": model, "state": "SUBMIT_FAILED", "error": str(exc),
                "content": "", "output_tokens": 0, "latency_s": 0, "tps": 0, "agent_id": ""}

    deadline = time.monotonic() + timeout_s
    interval = 2.0
    with httpx.Client(timeout=5.0) as client:
        while time.monotonic() < deadline:
            try:
                data = client.get(f"{hub}/tasks/{task_id}").json()
                state = data.get("state", "")
                if state == "COMPLETE":
                    r = data.get("result", {})
                    elapsed = time.monotonic() - t0
                    return {"model": model, "state": "COMPLETE",
                            "content": r.get("content", ""),
                            "output_tokens": r.get("output_tokens", 0),
                            "latency_s": round(elapsed, 2),
                            "tps": round(r.get("output_tokens", 0) / max(elapsed, 0.001), 1),
                            "agent_id": r.get("agent_id", "")}
                if state == "FAILED":
                    return {"model": model, "state": "FAILED",
                            "error": data.get("result", {}).get("error", ""),
                            "content": "", "output_tokens": 0, "latency_s": 0, "tps": 0, "agent_id": ""}
            except Exception:
                pass
            time.sleep(interval)
            interval = min(interval * 1.3, 10.0)

    return {"model": model, "state": "TIMEOUT", "content": "", "output_tokens": 0,
            "latency_s": 0, "tps": 0, "agent_id": ""}


_HTML = """\
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Model Arena - {date}</title>
<style>
:root{{--bg:#0f1117;--card:#1a1d27;--accent:#4f8ef7;--text:#e0e0e0;
      --sub:#9ca3af;--border:#2d3148;--ok:#22c55e;--warn:#f59e0b;--gold:#fbbf24}}
*{{box-sizing:border-box;margin:0;padding:0}}
body{{background:var(--bg);color:var(--text);
     font-family:'Segoe UI',system-ui,sans-serif;padding:2rem;max-width:1100px;margin:0 auto}}
h1{{font-size:1.8rem;color:var(--accent);margin-bottom:.25rem}}
.meta{{color:var(--sub);font-size:.85rem;margin-bottom:1rem}}
.prompt-box{{background:#252840;border:1px solid var(--border);border-radius:8px;
            padding:1rem;margin-bottom:2rem;font-size:.9rem;line-height:1.5}}
.prompt-label{{color:var(--accent);font-size:.75rem;text-transform:uppercase;margin-bottom:.3rem}}
.grid{{display:grid;grid-template-columns:repeat(auto-fit,minmax(300px,1fr));gap:1.2rem}}
.card{{background:var(--card);border:1px solid var(--border);border-radius:12px;padding:1.2rem}}
.card-title{{font-size:1rem;font-weight:600;color:var(--accent);margin-bottom:.8rem;
            display:flex;justify-content:space-between;align-items:center}}
.badge{{font-size:.7rem;border-radius:12px;padding:.15rem .5rem}}
.ok{{background:rgba(34,197,94,.15);color:var(--ok)}}
.fail{{background:rgba(245,158,11,.15);color:var(--warn)}}
.response{{font-size:.88rem;line-height:1.6;white-space:pre-wrap;margin-bottom:.8rem;
           max-height:400px;overflow-y:auto}}
.stats{{font-size:.78rem;color:var(--sub);border-top:1px solid var(--border);
       padding-top:.6rem;display:flex;gap:1rem;flex-wrap:wrap}}
.winner{{border-color:var(--gold);box-shadow:0 0 12px rgba(251,191,36,.15)}}
footer{{margin-top:2rem;padding-top:1rem;border-top:1px solid var(--border);
       font-size:.75rem;color:var(--sub);text-align:center}}
</style>
</head>
<body>
<h1>Model Arena</h1>
<p class="meta">Generated {datetime} | {n} model(s) | Hub: {hub}</p>
<div class="prompt-label">Prompt</div>
<div class="prompt-box">{prompt}</div>
<div class="grid">
{cards}
</div>
<footer>Generated by <strong>MoMaHub i-grid</strong> | Digital Duck &amp; Dog Team</footer>
</body>
</html>"""


def build_html(results: list[dict], prompt: str, hub: str) -> str:
    # Find winner (fastest COMPLETE)
    completed = [r for r in results if r["state"] == "COMPLETE"]
    winner = min(completed, key=lambda r: r["latency_s"]) if completed else None

    cards = []
    for r in results:
        state = r["state"]
        cls = "ok" if state == "COMPLETE" else "fail"
        extra_cls = " winner" if r is winner else ""
        trophy = " &#x1F3C6;" if r is winner else ""
        content = r.get("content") or r.get("error", "No result")
        cards.append(f"""<div class="card{extra_cls}">
  <div class="card-title">{r['model']}{trophy}<span class="badge {cls}">{state}</span></div>
  <div class="response">{content}</div>
  <div class="stats">
    <span>Tokens: {r['output_tokens']}</span>
    <span>Latency: {r['latency_s']}s</span>
    <span>TPS: {r['tps']}</span>
    <span>Agent: {r.get('agent_id','')[-12:]}</span>
  </div>
</div>""")

    return _HTML.format(
        date=datetime.now().strftime("%Y-%m-%d"),
        datetime=datetime.now().strftime("%Y-%m-%d %H:%M"),
        n=len(results), hub=hub,
        prompt=prompt, cards="\n".join(cards),
    )


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--models", default=DEFAULT_MODELS, show_default=True,
              help="Comma-separated model names")
@click.option("--prompt", default=DEFAULT_PROMPT, show_default=True)
@click.option("--max-tokens", default=512, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
@click.option("--out", default="", help="Output HTML path (default: auto)")
def main(hub, models, prompt, max_tokens, timeout, out):
    """Run the same prompt through multiple models and compare."""
    model_list = [m.strip() for m in models.split(",") if m.strip()]
    hub = hub.rstrip("/")

    click.echo(f"\n  Model Arena")
    click.echo(f"    Hub:    {hub}")
    click.echo(f"    Models: {model_list}")
    click.echo()

    results = []
    for model in model_list:
        click.echo(f"    [{model}] submitting...", nl=False)
        r = submit_and_wait(hub, model, prompt, max_tokens, timeout)
        results.append(r)
        if r["state"] == "COMPLETE":
            click.echo(f" {r['output_tokens']} tok  {r['latency_s']}s  {r['tps']} tps")
        else:
            click.echo(f" {r['state']}: {r.get('error','')}")

    html = build_html(results, prompt, hub)
    out_path = out or f"arena_{datetime.now().strftime('%Y%m%d_%H%M')}.html"
    Path(out_path).write_text(html, encoding="utf-8")

    # Console summary
    click.echo(f"\n  {'MODEL':<15} {'STATE':<10} {'TOKENS':>8} {'LATENCY':>10} {'TPS':>8}")
    click.echo(f"  {'-'*55}")
    for r in results:
        click.echo(f"  {r['model']:<15} {r['state']:<10} {r['output_tokens']:>8} "
                    f"{r['latency_s']:>9.1f}s {r['tps']:>7.1f}")

    click.echo(f"\n  Report: {out_path}")
    click.echo(f"  Open in browser for dark-mode side-by-side comparison.\n")


if __name__ == "__main__":
    main()
