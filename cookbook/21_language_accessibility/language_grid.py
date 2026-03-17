#!/usr/bin/env python3
"""Recipe 21: Language Accessibility — same question in 10 languages across the grid.

Demonstrates that the i-grid makes AI accessible regardless of language.
All queries dispatched in parallel; results show the grid handling global workloads.

Usage:
    python language_grid.py "What is artificial intelligence?"
    python language_grid.py --topic climate --hub http://192.168.1.10:9000
    python language_grid.py --all-languages "How does the internet work?"
"""
from __future__ import annotations

import asyncio
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

LANGUAGES = {
    "English":    {"code": "en", "q": "What is artificial intelligence?"},
    "Chinese":    {"code": "zh", "q": "人工智能是什么？"},
    "Spanish":    {"code": "es", "q": "¿Qué es la inteligencia artificial?"},
    "French":     {"code": "fr", "q": "Qu'est-ce que l'intelligence artificielle?"},
    "Arabic":     {"code": "ar", "q": "ما هو الذكاء الاصطناعي؟"},
    "Hindi":      {"code": "hi", "q": "कृत्रिम बुद्धिमत्ता क्या है?"},
    "Portuguese": {"code": "pt", "q": "O que é inteligência artificial?"},
    "Russian":    {"code": "ru", "q": "Что такое искусственный интеллект?"},
    "Japanese":   {"code": "ja", "q": "人工知能とは何ですか？"},
    "German":     {"code": "de", "q": "Was ist künstliche Intelligenz?"},
}

TOPICS = {
    "ai":       "What is artificial intelligence? Explain in 2-3 sentences.",
    "climate":  "What is climate change and why does it matter? Explain briefly.",
    "internet": "How does the internet work? Give a simple explanation.",
    "health":   "What is the most important thing for staying healthy? Be concise.",
    "math":     "What is the Pythagorean theorem? Give an example.",
}

SYSTEM = (
    "You are a helpful assistant. Answer the question in the same language it was asked. "
    "Be clear and concise — 2-4 sentences maximum."
)


async def ask_one(client: httpx.AsyncClient, hub: str,
                  language: str, question: str, model: str,
                  max_tokens: int, timeout_s: int) -> dict:
    task_id = f"lang-{language[:3].lower()}-{uuid.uuid4().hex[:6]}"
    t0 = time.monotonic()
    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": question, "system": SYSTEM,
            "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"language": language, "question": question, "state": "SUBMIT_FAILED",
                "error": str(exc), "answer": "", "latency_s": 0, "output_tokens": 0}

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
                return {"language": language, "question": question,
                        "state": "COMPLETE",
                        "answer": result.get("content", ""),
                        "latency_s": round(elapsed, 2),
                        "output_tokens": result.get("output_tokens", 0),
                        "tps": round(result.get("output_tokens", 0) / max(elapsed, 0.001), 1),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                return {"language": language, "question": question,
                        "state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "answer": "", "latency_s": 0, "output_tokens": 0}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)
    return {"language": language, "question": question, "state": "TIMEOUT",
            "answer": "", "latency_s": 0, "output_tokens": 0}


async def run(hub: str, custom_question: str, topic: str, languages: list[str],
              model: str, max_tokens: int, timeout_s: int) -> None:
    # Build query set
    selected = {k: v for k, v in LANGUAGES.items() if k in languages}
    if custom_question:
        queries = {lang: custom_question for lang in selected}
    elif topic:
        base = TOPICS.get(topic, TOPICS["ai"])
        queries = {lang: base for lang in selected}
    else:
        queries = {lang: data["q"] for lang, data in selected.items()}

    click.echo(f"\n  Language Accessibility Demo")
    click.echo(f"    Hub:       {hub}")
    click.echo(f"    Model:     {model}")
    click.echo(f"    Languages: {len(queries)}")
    click.echo()

    wall_start = time.monotonic()
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        tasks = [ask_one(client, hub, lang, q, model, max_tokens, timeout_s)
                 for lang, q in queries.items()]
        results = []
        for coro in asyncio.as_completed(tasks):
            r = await coro
            results.append(r)
            if r["state"] == "COMPLETE":
                click.echo(f"    {r['language']:<12} {r['output_tokens']:>4} tok  "
                           f"{r['latency_s']:>5.1f}s")
            else:
                click.echo(f"    {r['language']:<12} {r['state']}")

    wall_time = time.monotonic() - wall_start

    # Print answers
    click.echo(f"\n  {'='*60}")
    completed = [r for r in results if r["state"] == "COMPLETE"]
    for r in sorted(completed, key=lambda x: x["language"]):
        click.echo(f"\n  [{r['language']}]")
        click.echo(f"  Q: {r['question'][:80]}")
        click.echo(f"  A: {r['answer'][:300]}")

    click.echo(f"\n  {'-'*60}")
    click.echo(f"  {len(completed)}/{len(queries)} languages answered in {wall_time:.1f}s wall time")
    click.echo(f"  (All dispatched in parallel — wall time ≈ slowest single response)\n")

    # HTML report
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    out = Path(__file__).parent / f"language_grid_{ts}.html"
    out.write_text(_build_html(results, model, hub), encoding="utf-8")
    click.echo(f"  Report: {out}\n")


def _build_html(results: list[dict], model: str, hub: str) -> str:
    cards = ""
    for r in sorted(results, key=lambda x: x["language"]):
        if r["state"] != "COMPLETE":
            continue
        cards += f"""<div class="card">
<div class="lang">{r['language']}
<span class="stat">{r['output_tokens']} tok | {r['latency_s']}s</span></div>
<div class="question">{r['question']}</div>
<div class="answer">{r['answer']}</div>
</div>"""

    return f"""<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Language Grid — i-grid Accessibility Demo</title>
<style>body{{background:#0f1117;color:#e0e0e0;font-family:system-ui;padding:2rem;max-width:960px;margin:0 auto}}
h1{{color:#4f8ef7}}.meta{{color:#9ca3af;font-size:.85rem;margin-bottom:1.5rem}}
.card{{background:#1a1d27;border:1px solid #2d3148;border-radius:10px;padding:1rem;margin-bottom:1rem}}
.lang{{font-weight:600;color:#4f8ef7;margin-bottom:.4rem;display:flex;justify-content:space-between}}
.stat{{color:#9ca3af;font-size:.75rem}}.question{{color:#9ca3af;font-size:.82rem;margin-bottom:.4rem}}
.answer{{font-size:.92rem;line-height:1.6}}
footer{{margin-top:2rem;font-size:.75rem;color:#9ca3af;text-align:center}}</style>
</head><body>
<h1>Language Accessibility Demo</h1>
<p class="meta">{datetime.now().strftime("%Y-%m-%d %H:%M")} | Model: {model} | Hub: {hub}</p>
<p style="color:#9ca3af;margin-bottom:1.5rem">
All queries dispatched in parallel across the i-grid. AI accessible in any language.
</p>
{cards}
<footer>Generated by <strong>MoMaHub i-grid</strong></footer>
</body></html>"""


@click.command()
@click.argument("question", default="")
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--model", default="llama3", show_default=True)
@click.option("--topic", default="",
              type=click.Choice(["", "ai", "climate", "internet", "health", "math"]),
              show_default=True, help="Use a preset question in all languages")
@click.option("--languages", default=",".join(LANGUAGES.keys()),
              help="Comma-separated languages (default: all 10)")
@click.option("--max-tokens", default=256, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
def main(question, hub, model, topic, languages, max_tokens, timeout):
    """Ask the same question in 10 languages simultaneously on the grid."""
    lang_list = [l.strip() for l in languages.split(",") if l.strip() in LANGUAGES]
    if not lang_list:
        lang_list = list(LANGUAGES.keys())
    asyncio.run(run(hub.rstrip("/"), question, topic, lang_list,
                    model, max_tokens, timeout))


if __name__ == "__main__":
    main()
