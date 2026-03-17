#!/usr/bin/env python3
"""Recipe 11: Batch Translate -- one text to N languages in parallel.

Fan out translations across all grid agents simultaneously.
Visually impressive: all GPUs working on the same content in parallel.

Usage:
    python translate.py "Hello, world! AI is changing everything."
    python translate.py --file input.txt --languages fr,de,zh,es
    python translate.py "The quick brown fox" --hub http://192.168.1.10:9000

Default languages: French, German,  Chinese, Spanish.
"""
from __future__ import annotations

import asyncio
import time
import uuid
from datetime import datetime
from pathlib import Path

import click
import httpx

DEFAULT_LANGUAGES = "French,German,Chinese,Spanish"

SYSTEM = (
    "You are a professional translator. Translate accurately while preserving "
    "tone, nuance, and meaning. Output ONLY the translation, no commentary."
)

PROMPT_TPL = "Translate the following text into {language}:\n\n{text}"


async def translate_one(client: httpx.AsyncClient, hub: str, text: str,
                        language: str, model: str, max_tokens: int,
                        timeout_s: int) -> dict:
    task_id = f"translate-{language[:3].lower()}-{uuid.uuid4().hex[:6]}"
    t0 = time.monotonic()

    try:
        await client.post(f"{hub}/tasks", json={
            "task_id": task_id, "model": model,
            "prompt": PROMPT_TPL.format(language=language, text=text),
            "system": SYSTEM, "max_tokens": max_tokens,
        })
    except Exception as exc:
        return {"language": language, "state": "SUBMIT_FAILED", "error": str(exc),
                "translation": "", "output_tokens": 0, "latency_s": 0, "agent_id": ""}

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
                return {"language": language, "state": "COMPLETE",
                        "translation": result.get("content", ""),
                        "output_tokens": result.get("output_tokens", 0),
                        "latency_s": round(elapsed, 2),
                        "agent_id": result.get("agent_id", "")}
            if state == "FAILED":
                return {"language": language, "state": "FAILED",
                        "error": data.get("result", {}).get("error", ""),
                        "translation": "", "output_tokens": 0, "latency_s": 0, "agent_id": ""}
        except Exception:
            pass
        await asyncio.sleep(interval)
        interval = min(interval * 1.2, 8.0)

    return {"language": language, "state": "TIMEOUT", "translation": "",
            "output_tokens": 0, "latency_s": 0, "agent_id": ""}


async def run_translations(hub: str, text: str, languages: list[str],
                           model: str, max_tokens: int, timeout_s: int) -> list[dict]:
    async with httpx.AsyncClient(timeout=httpx.Timeout(timeout_s + 30.0)) as client:
        tasks = [translate_one(client, hub, text, lang, model, max_tokens, timeout_s)
                 for lang in languages]
        results = []
        for coro in asyncio.as_completed(tasks):
            r = await coro
            results.append(r)
            if r["state"] == "COMPLETE":
                click.echo(f"    {r['language']:<12} {r['output_tokens']:>4} tok  "
                            f"{r['latency_s']:>5.1f}s  agent=..{r.get('agent_id','')[-12:]}")
            else:
                click.echo(f"    {r['language']:<12} {r['state']}: {r.get('error','')}")
    return results


_HTML = """\
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Batch Translation - {date}</title>
<style>
:root{{--bg:#0f1117;--card:#1a1d27;--accent:#4f8ef7;--text:#e0e0e0;
      --sub:#9ca3af;--border:#2d3148}}
*{{box-sizing:border-box;margin:0;padding:0}}
body{{background:var(--bg);color:var(--text);
     font-family:'Segoe UI',system-ui,sans-serif;padding:2rem;max-width:900px;margin:0 auto}}
h1{{font-size:1.6rem;color:var(--accent);margin-bottom:.25rem}}
.meta{{color:var(--sub);font-size:.85rem;margin-bottom:1.5rem}}
.original{{background:#252840;border:1px solid var(--border);border-radius:8px;
          padding:1rem;margin-bottom:2rem;font-size:.95rem;line-height:1.5}}
.label{{color:var(--accent);font-size:.72rem;text-transform:uppercase;margin-bottom:.3rem}}
.card{{background:var(--card);border:1px solid var(--border);border-radius:10px;
      padding:1rem;margin-bottom:1rem}}
.lang{{font-weight:600;color:var(--accent);margin-bottom:.4rem;display:flex;
      justify-content:space-between}}
.translation{{font-size:.92rem;line-height:1.6}}
.stats{{font-size:.75rem;color:var(--sub);margin-top:.5rem}}
footer{{margin-top:2rem;font-size:.75rem;color:var(--sub);text-align:center}}
</style>
</head>
<body>
<h1>Batch Translation</h1>
<p class="meta">{datetime} | {n} language(s) | Model: {model} | Hub: {hub}</p>
<div class="label">Original</div>
<div class="original">{original}</div>
{cards}
<footer>Generated by <strong>MoMaHub i-grid</strong> | Digital Duck &amp; Dog Team</footer>
</body>
</html>"""


def build_html(results: list[dict], original: str, model: str, hub: str) -> str:
    cards = []
    for r in sorted(results, key=lambda x: x["language"]):
        if r["state"] != "COMPLETE":
            continue
        cards.append(f"""<div class="card">
  <div class="lang">{r['language']}<span class="stats">{r['output_tokens']} tok | {r['latency_s']}s</span></div>
  <div class="translation">{r['translation']}</div>
</div>""")
    return _HTML.format(
        date=datetime.now().strftime("%Y-%m-%d"),
        datetime=datetime.now().strftime("%Y-%m-%d %H:%M"),
        n=len(results), model=model, hub=hub,
        original=original, cards="\n".join(cards),
    )


@click.command()
@click.argument("text", default="")
@click.option("--file", "file_path", type=click.Path(exists=True), default=None,
              help="Read text from file instead")
@click.option("--hub", default="http://localhost:9000", show_default=True)
@click.option("--model", default="llama3", show_default=True)
@click.option("--languages", default=DEFAULT_LANGUAGES, show_default=True,
              help="Comma-separated target languages")
@click.option("--max-tokens", default=1024, show_default=True, type=int)
@click.option("--timeout", default=300, show_default=True, type=int)
@click.option("--out", default="", help="Output HTML path (default: auto)")
def main(text, file_path, hub, model, languages, max_tokens, timeout, out):
    """Translate text into multiple languages in parallel on the grid."""
    if file_path:
        text = Path(file_path).read_text(encoding="utf-8").strip()
    if not text:
        raise click.UsageError("Provide text as an argument or via --file.")

    hub = hub.rstrip("/")
    lang_list = [l.strip() for l in languages.split(",") if l.strip()]

    click.echo(f"\n  Batch Translate")
    click.echo(f"    Hub:       {hub}")
    click.echo(f"    Model:     {model}")
    click.echo(f"    Languages: {lang_list}")
    click.echo(f"    Text:      {text[:80]}{'...' if len(text) > 80 else ''}")
    click.echo()

    wall_start = time.monotonic()
    results = asyncio.run(run_translations(hub, text, lang_list, model, max_tokens, timeout))
    wall_time = time.monotonic() - wall_start

    completed = [r for r in results if r["state"] == "COMPLETE"]
    click.echo(f"\n  {'='*50}")
    click.echo(f"  {len(completed)}/{len(lang_list)} translations complete  wall={wall_time:.1f}s")

    # Print translations
    for r in sorted(results, key=lambda x: x["language"]):
        if r["state"] == "COMPLETE":
            click.echo(f"\n  [{r['language']}]")
            click.echo(f"  {r['translation'][:200]}")

    # HTML report
    html = build_html(results, text, model, hub)
    out_path = out or f"translations_{datetime.now().strftime('%Y%m%d_%H%M')}.html"
    Path(out_path).write_text(html, encoding="utf-8")
    click.echo(f"\n  Report: {out_path}\n")


if __name__ == "__main__":
    main()
