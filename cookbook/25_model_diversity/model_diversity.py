"""Recipe 25 — Model Diversity Benchmark.

Runs the same set of benchmark prompts across all 14 Ollama models on the grid.
Measures latency, tokens/s, and captures output for side-by-side quality comparison.

Models tested:
    llama3, llama3.1, mistral, mathstral, qwen3, qwen2.5, qwen2.5-coder,
    qwen2-math, deepseek-r1, deepseek-coder-v2, gemma3, phi4, phi4-mini, phi3

Usage:
    # Full benchmark (all models, all prompts):
    python model_diversity.py

    # Subset of models:
    python model_diversity.py --models llama3.1,qwen3,gemma3,phi4,phi4-mini,deepseek-r1

    # Single probe prompt to quickly check which models are alive:
    python model_diversity.py --probe

    # Save results:
    python model_diversity.py --out results.json --report diversity.html
"""
from __future__ import annotations

import json
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

# ── All 14 models ─────────────────────────────────────────────────────────────
ALL_MODELS = [
    "llama3",
    "llama3.1",
    "mistral",
    "mathstral",
    "qwen3",
    "qwen2.5",
    "qwen2.5-coder",
    "qwen2-math",
    "deepseek-r1",
    "deepseek-coder-v2",
    "gemma3",
    "phi4",
    "phi4-mini",
    "phi3",
]

# ── Benchmark prompts — one per capability domain ─────────────────────────────
BENCHMARKS = [
    {
        "id": "general",
        "domain": "General knowledge",
        "prompt": "Explain why the sky is blue in exactly three sentences.",
        "system": "You are a concise science communicator.",
        "max_tokens": 200,
    },
    {
        "id": "reasoning",
        "domain": "Logical reasoning",
        "prompt": (
            "Alice is taller than Bob. Bob is taller than Carol. "
            "Is Alice taller than Carol? Explain your reasoning."
        ),
        "system": "You are a logical reasoning assistant. Be brief and precise.",
        "max_tokens": 150,
    },
    {
        "id": "math",
        "domain": "Mathematics",
        "prompt": (
            "Solve: A train travels 120 km at 60 km/h, then 80 km at 40 km/h. "
            "What is the average speed for the whole journey? Show working."
        ),
        "system": "You are a mathematics tutor. Show step-by-step working.",
        "max_tokens": 300,
    },
    {
        "id": "code",
        "domain": "Code generation",
        "prompt": (
            "Write a Python function `flatten(lst)` that recursively flattens "
            "a nested list of arbitrary depth. Include a docstring and two examples."
        ),
        "system": "You are an expert Python developer. Output only code and docstring.",
        "max_tokens": 350,
    },
    {
        "id": "multilingual",
        "domain": "Multilingual",
        "prompt": "Translate to French, Spanish, and Japanese: 'Distributed AI inference makes powerful models accessible to everyone.'",
        "system": "You are a professional translator. Output each translation on its own line labelled with the language.",
        "max_tokens": 200,
    },
    {
        "id": "summarise",
        "domain": "Summarisation",
        "prompt": (
            "Summarise in one sentence: Transformer models use self-attention mechanisms "
            "to weigh the relevance of each token in a sequence relative to every other "
            "token, enabling them to capture long-range dependencies more effectively "
            "than recurrent neural networks."
        ),
        "system": "You are a technical writer. Produce exactly one sentence.",
        "max_tokens": 100,
    },
]

PROBE_PROMPT = {
    "id": "probe",
    "domain": "Probe",
    "prompt": "Reply with exactly: 'Model online.'",
    "system": "",
    "max_tokens": 20,
}


# ── HTTP helpers ──────────────────────────────────────────────────────────────

def submit_task(hub: str, model: str, prompt: str, system: str,
                max_tokens: int, client: httpx.Client) -> str:
    task_id = f"div-{uuid.uuid4().hex[:10]}"
    client.post(f"{hub}/tasks", json={
        "task_id": task_id,
        "model": model,
        "prompt": prompt,
        "system": system,
        "max_tokens": max_tokens,
        "temperature": 0.3,
    }, timeout=10.0).raise_for_status()
    return task_id


def poll_task(hub: str, task_id: str, timeout_s: int,
              client: httpx.Client) -> dict:
    deadline = time.monotonic() + timeout_s
    interval = 2.0
    while time.monotonic() < deadline:
        try:
            data = client.get(f"{hub}/tasks/{task_id}", timeout=5.0).json()
            state = data.get("state", "")
            if state == "COMPLETE":
                return data.get("result", {})
            if state == "FAILED":
                return {"error": data.get("result", {}).get("error", "failed")}
        except Exception:
            pass
        time.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"error": "timeout"}


# ── Single model × single benchmark ──────────────────────────────────────────

def run_one(hub: str, model: str, bench: dict, timeout_s: int) -> dict:
    t0 = time.monotonic()
    with httpx.Client(timeout=10.0) as client:
        try:
            task_id = submit_task(
                hub, model,
                bench["prompt"], bench.get("system", ""),
                bench["max_tokens"], client,
            )
        except Exception as exc:
            return {
                "model": model, "benchmark_id": bench["id"],
                "domain": bench["domain"],
                "error": str(exc), "wall_s": 0,
                "output_tokens": 0, "tps": 0.0, "content": "",
            }
        result = poll_task(hub, task_id, timeout_s, client)

    wall_s = time.monotonic() - t0
    out_tokens = int(result.get("output_tokens") or 0)
    latency_ms = float(result.get("latency_ms") or 0)
    tps = (out_tokens / (latency_ms / 1000)) if latency_ms > 0 else 0.0

    return {
        "model": model,
        "benchmark_id": bench["id"],
        "domain": bench["domain"],
        "content": result.get("content", ""),
        "input_tokens": int(result.get("input_tokens") or 0),
        "output_tokens": out_tokens,
        "latency_ms": latency_ms,
        "tps": round(tps, 1),
        "wall_s": round(wall_s, 2),
        "agent_id": result.get("agent_id", ""),
        "error": result.get("error", ""),
    }


# ── HTML report ───────────────────────────────────────────────────────────────

def _status_color(r: dict) -> str:
    if r.get("error"):
        return "#fdd"
    if r["tps"] >= 30:
        return "#dfd"
    if r["tps"] >= 10:
        return "#ffd"
    return "#fff"


def generate_html(results: list[dict], models: list[str],
                  benchmarks: list[dict], ts: str) -> str:
    # Build lookup: model -> benchmark_id -> result
    lookup: dict[str, dict[str, dict]] = {}
    for r in results:
        lookup.setdefault(r["model"], {})[r["benchmark_id"]] = r

    # Per-model summary
    def model_summary(m: str) -> dict:
        rows = list(lookup.get(m, {}).values())
        ok = [r for r in rows if not r.get("error")]
        errors = len(rows) - len(ok)
        avg_tps = sum(r["tps"] for r in ok) / len(ok) if ok else 0
        total_tok = sum(r["output_tokens"] for r in ok)
        return {"ok": len(ok), "errors": errors, "avg_tps": avg_tps, "total_tok": total_tok}

    rows_html = ""
    for m in models:
        s = model_summary(m)
        for b in benchmarks:
            r = lookup.get(m, {}).get(b["id"], {})
            bg = _status_color(r) if r else "#eee"
            content_preview = (r.get("content") or r.get("error") or "—")[:200].replace("<", "&lt;")
            tps_str = f"{r['tps']:.1f}" if r.get("tps") else "—"
            lat_str = f"{r['latency_ms']:.0f}" if r.get("latency_ms") else "—"
            tok_str = str(r.get("output_tokens", "—")) if r else "—"
            rows_html += f"""
            <tr style="background:{bg}">
                <td><b>{m}</b></td>
                <td>{b['domain']}</td>
                <td style="font-size:0.85em;max-width:400px;white-space:pre-wrap">{content_preview}</td>
                <td style="text-align:right">{tps_str}</td>
                <td style="text-align:right">{lat_str}</td>
                <td style="text-align:right">{tok_str}</td>
            </tr>"""

    summary_rows = ""
    for m in models:
        s = model_summary(m)
        bg = "#dfd" if s["errors"] == 0 else "#fdd"
        summary_rows += f"""
        <tr style="background:{bg}">
            <td><b>{m}</b></td>
            <td style="text-align:right">{s['ok']}/{s['ok']+s['errors']}</td>
            <td style="text-align:right">{s['avg_tps']:.1f}</td>
            <td style="text-align:right">{s['total_tok']}</td>
            <td style="text-align:right">{s['errors']}</td>
        </tr>"""

    return f"""<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Model Diversity Benchmark — {ts}</title>
<style>
  body {{ font-family: sans-serif; padding: 20px; }}
  h1 {{ color: #333; }}
  table {{ border-collapse: collapse; width: 100%; margin-bottom: 2em; }}
  th {{ background: #444; color: #fff; padding: 6px 10px; text-align: left; }}
  td {{ border: 1px solid #ccc; padding: 5px 8px; vertical-align: top; }}
  .section {{ margin-top: 2em; }}
</style>
</head>
<body>
<h1>Model Diversity Benchmark</h1>
<p>Generated: {ts} &nbsp;|&nbsp; Models: {len(models)} &nbsp;|&nbsp; Benchmarks: {len(benchmarks)}</p>

<div class="section">
<h2>Summary (per model)</h2>
<table>
  <tr><th>Model</th><th>Passed</th><th>Avg TPS</th><th>Total tokens</th><th>Errors</th></tr>
  {summary_rows}
</table>
</div>

<div class="section">
<h2>Full Results</h2>
<table>
  <tr><th>Model</th><th>Domain</th><th>Response (first 200 chars)</th>
      <th>TPS</th><th>Latency (ms)</th><th>Out tokens</th></tr>
  {rows_html}
</table>
</div>
</body>
</html>"""


# ── CLI ───────────────────────────────────────────────────────────────────────

@click.command()
@click.option("--hub",     default="http://localhost:9000", show_default=True)
@click.option("--models",  default=",".join(ALL_MODELS), show_default=False,
              help="Comma-separated list of models to test")
@click.option("--timeout", default=180, show_default=True, type=int,
              help="Per-task timeout in seconds")
@click.option("--probe",   is_flag=True,
              help="Quick probe only — one short prompt per model")
@click.option("--skip-errors", is_flag=True,
              help="Continue if a model errors; report at end")
@click.option("--out",    default="", help="Save raw results to JSON file")
@click.option("--report", default="diversity.html", show_default=True,
              help="HTML report output path")
def main(hub: str, models: str, timeout: int, probe: bool,
         skip_errors: bool, out: str, report: str) -> None:
    model_list = [m.strip() for m in models.split(",") if m.strip()]
    benchmarks = [PROBE_PROMPT] if probe else BENCHMARKS

    total = len(model_list) * len(benchmarks)
    click.echo(f"Model diversity benchmark")
    click.echo(f"  Hub:        {hub}")
    click.echo(f"  Models:     {len(model_list)}")
    click.echo(f"  Benchmarks: {len(benchmarks)} ({'probe only' if probe else 'full suite'})")
    click.echo(f"  Tasks:      {total}")
    click.echo(f"  Timeout:    {timeout}s per task")
    click.echo()

    results: list[dict] = []
    done = 0

    for model in model_list:
        click.echo(f"[{model}]")
        for bench in benchmarks:
            click.echo(f"  {bench['domain']:<25} ", nl=False)
            r = run_one(hub, model, bench, timeout)
            done += 1

            if r.get("error"):
                click.echo(f"ERROR  {r['error'][:60]}")
                if not skip_errors and not probe:
                    results.append(r)
                    click.echo(f"  Skipping remaining benchmarks for {model} (use --skip-errors to continue)")
                    break
            else:
                click.echo(f"ok  {r['tps']:5.1f} TPS  {r['latency_ms']:6.0f}ms  {r['output_tokens']} tok")

            results.append(r)

        click.echo()

    # ── Summary table ──────────────────────────────────────────────────────────
    click.echo("=" * 60)
    click.echo(f"{'MODEL':<22} {'PASS':>4} {'AVG TPS':>8} {'TOKENS':>8} {'ERRORS':>7}")
    click.echo("-" * 60)
    for model in model_list:
        rows = [r for r in results if r["model"] == model]
        ok_rows = [r for r in rows if not r.get("error")]
        errors  = len(rows) - len(ok_rows)
        avg_tps = sum(r["tps"] for r in ok_rows) / len(ok_rows) if ok_rows else 0
        tot_tok = sum(r["output_tokens"] for r in ok_rows)
        status  = "OK" if errors == 0 else f"{errors} ERR"
        click.echo(f"{model:<22} {len(ok_rows):>4}/{len(benchmarks):<3} {avg_tps:>7.1f} {tot_tok:>8} {status:>7}")
    click.echo("=" * 60)

    # ── Save JSON ──────────────────────────────────────────────────────────────
    ts = datetime.now().strftime("%Y-%m-%dT%H:%M:%S")
    payload = {
        "timestamp": ts,
        "hub": hub,
        "models": model_list,
        "benchmarks": [b["id"] for b in benchmarks],
        "results": results,
    }
    if out:
        Path(out).write_text(json.dumps(payload, indent=2))
        click.echo(f"\nJSON saved to {out}")

    # ── HTML report ────────────────────────────────────────────────────────────
    html = generate_html(results, model_list, benchmarks, ts)
    Path(report).write_text(html)
    click.echo(f"HTML report saved to {report}")


if __name__ == "__main__":
    main()
