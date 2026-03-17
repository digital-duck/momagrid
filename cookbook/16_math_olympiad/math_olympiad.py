#!/usr/bin/env python3
"""Recipe 16: Math Olympiad — benchmark math models on a problem set.

Routes problems to mathstral and qwen2-math, scores accuracy,
and compares TPS and latency across models.

Usage:
    python math_olympiad.py
    python math_olympiad.py --models mathstral,qwen2-math,llama3
    python math_olympiad.py --hub http://192.168.1.10:9000 --difficulty hard
"""
from __future__ import annotations

import asyncio
import re
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

PROBLEMS = {
    "easy": [
        {"q": "What is 15% of 240?",                           "answer": "36"},
        {"q": "Simplify: (x^2 - 9) / (x - 3)",               "answer": "x+3"},
        {"q": "What is the sum of interior angles of a hexagon?", "answer": "720"},
        {"q": "Solve: 3x + 7 = 22",                           "answer": "5"},
        {"q": "What is 12! / 10!?",                            "answer": "132"},
    ],
    "medium": [
        {"q": "Find the derivative of f(x) = x^3 - 4x^2 + 2x - 1", "answer": "3x^2-8x+2"},
        {"q": "Solve the quadratic: x^2 - 5x + 6 = 0",        "answer": "2,3"},
        {"q": "What is the probability of rolling two sixes with two fair dice?", "answer": "1/36"},
        {"q": "Integrate: ∫(2x + 3)dx",                       "answer": "x^2+3x+C"},
        {"q": "If log₂(x) = 5, what is x?",                   "answer": "32"},
    ],
    "hard": [
        {"q": "Prove that √2 is irrational. Give the key step of the proof.", "answer": "contradiction"},
        {"q": "Find the sum of the infinite geometric series: 1 + 1/2 + 1/4 + 1/8 + ...", "answer": "2"},
        {"q": "How many ways can 8 people be seated in a circle?", "answer": "5040"},
        {"q": "What is the Euler's formula relating e, π, i, 1, and 0?", "answer": "e^(iπ)+1=0"},
        {"q": "Find all prime numbers p such that p^2 + 2 is also prime.", "answer": "3"},
    ],
}

SYSTEM = (
    "You are a precise mathematics solver. Show your work briefly, "
    "then state the final answer clearly prefixed with 'Answer:'. "
    "Be concise but accurate."
)


def check_answer(response: str, expected: str) -> bool:
    """Loose check: expected string appears somewhere in the response."""
    response_lower = response.lower()
    expected_lower = expected.lower()
    # Direct match
    if expected_lower in response_lower:
        return True
    # Numeric equivalence for simple numbers
    try:
        exp_num = float(expected.replace(",", ""))
        nums = re.findall(r"-?\d+\.?\d*", response)
        return any(abs(float(n) - exp_num) < 0.01 for n in nums)
    except ValueError:
        pass
    return False


async def solve_one(client: httpx.AsyncClient, hub: str, problem: dict,
                    model: str, max_tokens: int, timeout_s: int) -> dict:
    task_id = f"math-{uuid.uuid4().hex[:8]}"
    t0 = time.monotonic()
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": problem["q"], "system": SYSTEM,
            "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"model": model, "q": problem["q"], "expected": problem["answer"],
                "state": "SUBMIT_FAILED", "error": str(exc),
                "correct": False, "latency_s": 0, "output_tokens": 0, "tps": 0}

    deadline = time.monotonic() + timeout_s
    interval = 1.5
    while time.monotonic() < deadline:
        try:
            r = await client.get(f"{hub}/tasks/{task_id}")
            data = r.json()
            state = data.get("state", "")
            if state == "COMPLETE":
                result = data.get("result", {})
                content = result.get("content", "")
                elapsed = time.monotonic() - t0
                correct = check_answer(content, problem["answer"])
                return {"model": model, "q": problem["q"][:60],
                        "expected": problem["answer"],
                        "response": content[:200],
                        "correct": correct, "state": "COMPLETE",
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "tps": round(result.get("output_tokens", 0) / max(elapsed, 0.001), 1),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                return {"model": model, "q": problem["q"][:60],
                        "expected": problem["answer"], "correct": False,
                        "state": "FAILED", "error": data.get("result", {}).get("error", ""),
                        "latency_s": 0, "output_tokens": 0, "tps": 0}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"model": model, "q": problem["q"][:60], "expected": problem["answer"],
            "correct": False, "state": "TIMEOUT",
            "latency_s": 0, "output_tokens": 0, "tps": 0}


async def run(hub: str, models: list[str], difficulty: str,
              max_tokens: int, timeout_s: int) -> None:
    problems = PROBLEMS.get(difficulty, PROBLEMS["medium"])
    click.echo(f"\n  Math Olympiad — {difficulty.upper()}")
    click.echo(f"    Hub:       {hub}")
    click.echo(f"    Models:    {models}")
    click.echo(f"    Problems:  {len(problems)}")
    click.echo()

    # Submit all (model x problem) combinations in parallel
    combos = [(m, p) for m in models for p in problems]
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        results = await asyncio.gather(
            *[solve_one(client, hub, p, m, max_tokens, timeout_s)
              for m, p in combos]
        )

    # Group by model
    by_model: dict[str, list[dict]] = {m: [] for m in models}
    for r in results:
        by_model[r["model"]].append(r)

    # Per-problem results
    click.echo(f"  {'Problem':<45} " + "  ".join(f"{m[:12]:<12}" for m in models))
    click.echo(f"  {'-'*45} " + "  ".join("-"*12 for _ in models))
    for i, prob in enumerate(problems):
        row = f"  {prob['q'][:44]:<45}"
        for m in models:
            r = by_model[m][i]
            if r["state"] == "COMPLETE":
                mark = "✓" if r["correct"] else "✗"
                row += f"  {mark} {r['latency_s']:>4.1f}s {r['tps']:>5.1f}tps"
            else:
                row += f"  {r['state']:<12}"
        click.echo(row)

    # Model summary
    click.echo(f"\n  {'Model':<20} {'Score':>7} {'Avg Lat':>9} {'Avg TPS':>9} {'Tokens':>8}")
    click.echo(f"  {'-'*57}")
    for m in models:
        res = by_model[m]
        done = [r for r in res if r["state"] == "COMPLETE"]
        correct = sum(1 for r in done if r["correct"])
        score = f"{correct}/{len(problems)}"
        avg_lat = sum(r["latency_s"] for r in done) / max(len(done), 1)
        avg_tps = sum(r["tps"] for r in done) / max(len(done), 1)
        total_tok = sum(r["output_tokens"] for r in done)
        click.echo(f"  {m:<20} {score:>7} {avg_lat:>8.1f}s {avg_tps:>9.1f} {total_tok:>8}")

    # Save HTML report
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    out = Path(__file__).parent / f"math_olympiad_{difficulty}_{ts}.html"
    out.write_text(_build_html(results, models, problems, difficulty, hub), encoding="utf-8")
    click.echo(f"\n  Report: {out}\n")


def _build_html(results: list[dict], models: list[str],
                problems: list[dict], difficulty: str, hub: str) -> str:
    by_model: dict[str, list[dict]] = {m: [] for m in models}
    for r in results:
        by_model[r["model"]].append(r)

    rows = ""
    for i, prob in enumerate(problems):
        rows += f"<tr><td>{prob['q']}</td>"
        for m in models:
            r = by_model[m][i]
            if r["state"] == "COMPLETE":
                color = "#4ade80" if r["correct"] else "#f87171"
                mark = "✓" if r["correct"] else "✗"
                rows += (f"<td style='color:{color}'>{mark} {r['latency_s']:.1f}s "
                         f"<small>{r['tps']:.0f}tps</small></td>")
            else:
                rows += f"<td style='color:#9ca3af'>{r['state']}</td>"
        rows += "</tr>"

    headers = "".join(f"<th>{m}</th>" for m in models)
    return f"""<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Math Olympiad — {difficulty}</title>
<style>body{{background:#0f1117;color:#e0e0e0;font-family:system-ui;padding:2rem;max-width:1100px;margin:0 auto}}
h1{{color:#4f8ef7}}table{{width:100%;border-collapse:collapse;margin-top:1rem}}
th,td{{padding:.5rem .8rem;border:1px solid #2d3148;text-align:left}}
th{{background:#1a1d27;color:#4f8ef7}}tr:hover{{background:#1a1d27}}</style>
</head><body>
<h1>Math Olympiad — {difficulty.title()}</h1>
<p style="color:#9ca3af">{datetime.now().strftime("%Y-%m-%d %H:%M")} | Hub: {hub}</p>
<table><tr><th>Problem</th>{headers}</tr>{rows}</table>
</body></html>"""


@click.command()
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--models", default="mathstral,qwen2-math", show_default=True,
              help="Comma-separated model names")
@click.option("--difficulty", default="medium",
              type=click.Choice(["easy", "medium", "hard"]), show_default=True)
@click.option("--max-tokens", default=512, show_default=True, type=int)
@click.option("--timeout", default=180, show_default=True, type=int)
def main(hub, models, difficulty, max_tokens, timeout):
    """Benchmark math models on a problem set and compare accuracy + TPS."""
    model_list = [m.strip() for m in models.split(",") if m.strip()]
    asyncio.run(run(hub.rstrip("/"), model_list, difficulty, max_tokens, timeout))


if __name__ == "__main__":
    main()
