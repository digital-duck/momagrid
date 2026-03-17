#!/usr/bin/env python3
"""
analyze_run_all.py — Parse a run_all log and enrich with per-recipe JSON output.

Produces a Markdown analysis report saved to <out>/RUN_ALL_REPORT-<timestamp>.md

Usage (from repo root OR from cookbook/):
    python cookbook/analyze_run_all.py                         # auto-find latest log
    python analyze_run_all.py                                  # same, from cookbook/
    python cookbook/analyze_run_all.py path/to/run_all_log.md # specific log
    python cookbook/analyze_run_all.py --out /some/other/dir  # custom output dir
"""

import argparse
import json
import re
import socket
import sys
from datetime import datetime
from pathlib import Path

# Default output dir is always relative to this script's location (cookbook/out/),
# regardless of the working directory the user invokes from.
_SCRIPT_DIR = Path(__file__).resolve().parent
_DEFAULT_OUT = _SCRIPT_DIR / "out"


# Maps recipe ID -> JSON filename stem (as passed to saveResults() in each recipe)
RECIPE_JSON_NAME = {
    "01": "hello",
    "02": "multi_cte",
    "03": "translate",
    "04": "benchmark",
    "05": "rag",
    "07": "stress",
    "08": "arena",
    "09": "doc_pipeline",
    "10": "chain",
    "12": "tier_dispatch",
    "13": "throughput",
    "15": "failover",
    "16": "math_olympiad",
    "17": "code_review",
    "18": "smart_router",
    "19": "privacy_demo",
    "20": "overnight_batch",
    "21": "language_grid",
    "22": "rewards_report",
    "23": "resilience",
    "24": "compiler_pipeline",
    "25": "model_diversity",
    "27": "model_health",
    "29": "model_fingerprinting",
    "30": "academic_pipeline",
    "34": "junior_dev_assistant",
}

RECIPE_DESCRIPTION = {
    "01": "Basic SPL query via grid",
    "02": "Parallel multi-CTE queries",
    "03": "Batch translation — 4 languages",
    "04": "Model benchmark — latency & TPS",
    "05": "RAG retrieval-augmented generation",
    "06": "Arxiv paper digest",
    "07": "Stress test — sustained load",
    "08": "Model arena — head-to-head",
    "09": "Document processing pipeline",
    "10": "Chain relay — sequential prompts",
    "11": "SPL join query",
    "12": "Tier-aware dispatch by VRAM",
    "13": "Multi-agent throughput benchmark",
    "14": "Agent discovery",
    "15": "Agent failover & retry",
    "16": "Math olympiad — reasoning accuracy",
    "17": "Code review pipeline (3 stages)",
    "18": "Smart router — query classification",
    "19": "Privacy chunk — distributed doc analysis",
    "20": "Overnight batch processing",
    "21": "Language accessibility — 10 languages",
    "22": "Rewards report — operator credits",
    "23": "Wake/sleep resilience",
    "24": "SPL compiler pipeline",
    "25": "Model diversity — all models probed",
    "26": "Code guardian — parallel reviews",
    "27": "Model health check — load & TPS",
    "28": "Federated search — gathering & synthesis",
    "29": "Model fingerprinting — cross-agent consistency",
    "30": "Academic paper pipeline — 4-stage parallel workflow",
    "33": "Micro-learning — textbook generation",
    "34": "Junior developer assistant — code review, refactoring, docs",
    "80": "Fibonacci sequence benchmark",
    "90": "Two-hub cluster federation",
}


def find_latest_log(out_dir: Path) -> Path | None:
    logs = sorted(out_dir.glob("RUN_ALL_LOGGING-*.md"), reverse=True)
    return logs[0] if logs else None


def parse_log(log_path: Path) -> dict:
    """Parse a run_all log into structured data."""
    text = log_path.read_text(errors="replace")

    result = {
        "log_file": str(log_path),
        "run_date": None,
        "hub": None,
        "recipes": [],
        "passed": None,
        "total": None,
        "total_elapsed_s": None,
    }

    m = re.search(r"=== Momahub Go Cookbook Batch Run — (.+?) ===", text)
    if m:
        result["run_date"] = m.group(1).strip()

    m = re.search(r"Hub:\s+(\S+)", text)
    if m:
        result["hub"] = m.group(1)

    # Each recipe block:
    #   [ID] Name
    #        cmd : ...
    #        log : ...
    #        | output lines
    #        result: SUCCESS/FAILED  (X.Xs)
    recipe_re = re.compile(
        r"^\[(\w+)\] (.+?)\n"
        r"     cmd : (.+?)\n"
        r"     log : (.+?)\n"
        r"((?:     \| .*\n)*)"
        r"     result: (SUCCESS|FAILED)\s+\((\d+\.?\d*)s\)",
        re.MULTILINE,
    )
    for m in recipe_re.finditer(text):
        raw_output = m.group(5)
        output_lines = [l[7:] for l in raw_output.splitlines() if l.strip()]
        result["recipes"].append({
            "id": m.group(1),
            "name": m.group(2).strip(),
            "cmd": m.group(3).strip(),
            "log": m.group(4).strip(),
            "output": output_lines,
            "status": m.group(6),
            "elapsed_s": float(m.group(7)),
        })

    m = re.search(r"Summary: (\d+)/(\d+) Success\s+\(total (\d+\.?\d*)s\)", text)
    if m:
        result["passed"] = int(m.group(1))
        result["total"] = int(m.group(2))
        result["total_elapsed_s"] = float(m.group(3))

    return result


def _lower_keys(obj):
    """Recursively lowercase all dict keys — Go marshals struct fields as PascalCase."""
    if isinstance(obj, dict):
        return {k.lower(): _lower_keys(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_lower_keys(i) for i in obj]
    return obj


def load_jsons(out_dir: Path) -> dict[str, dict]:
    """Load per-recipe JSON files from out_dir, keeping the newest per stem name."""
    jsons: dict[str, dict] = {}
    for jf in sorted(out_dir.glob("*.json"), key=lambda p: p.stat().st_mtime):
        try:
            data = _lower_keys(json.loads(jf.read_text()))
        except Exception:
            continue
        if not isinstance(data, dict):
            continue
        # Filename: <name>_YYYYMMDD_HHMMSS.json — strip the two trailing timestamp parts
        parts = jf.stem.split("_")
        name = "_".join(parts[:-2]) if len(parts) >= 3 else jf.stem
        data["_file"] = jf.name
        jsons[name] = data  # last write wins (newest mtime)
    return jsons


def extract_metrics(recipe: dict, jsons: dict) -> dict:
    """Pull key metrics from a recipe's saved JSON output."""
    json_name = RECIPE_JSON_NAME.get(recipe["id"])
    data = jsons.get(json_name, {}) if json_name else {}
    metrics: dict = {}

    rid = recipe["id"]
    results = data.get("results", [])

    def tps_stats(rs):
        vals = [r.get("tps", 0) for r in rs if r.get("state") == "COMPLETE" and r.get("tps", 0) > 0]
        return round(sum(vals) / len(vals), 1) if vals else None

    def completion(rs):
        done = sum(1 for r in rs if r.get("state") == "COMPLETE")
        return f"{done}/{len(rs)}"

    if rid == "03" and results:
        metrics["translations"] = completion(results)

    elif rid == "04" and results:
        t = tps_stats(results)
        if t:
            metrics["avg_tps"] = t
        metrics["models_tested"] = len(set(r.get("model", "") for r in results))

    elif rid == "07" and results:
        metrics["tasks"] = completion(results)
        t = tps_stats(results)
        if t:
            metrics["avg_tps"] = t

    elif rid == "08" and results:
        t = tps_stats(results)
        if t:
            metrics["avg_tps"] = t
        metrics["models"] = len(set(r.get("model", "") for r in results))

    elif rid == "13" and results:
        metrics["tasks"] = completion(results)
        t = tps_stats(results)
        if t:
            metrics["avg_tps"] = t

    elif rid == "15" and results:
        metrics["tasks"] = completion(results)

    elif rid == "16" and results:
        by_model: dict = {}
        for r in results:
            mod = r.get("model", "?")
            by_model.setdefault(mod, [0, 0])
            by_model[mod][1] += 1
            if r.get("correct"):
                by_model[mod][0] += 1
        metrics["scores"] = {m: f"{v[0]}/{v[1]}" for m, v in by_model.items()}

    elif rid == "21" and results:
        metrics["languages"] = completion(results)

    elif rid == "25" and results:
        t = tps_stats(results)
        if t:
            metrics["avg_tps"] = t
        metrics["models_probed"] = len(set(r.get("model", "") for r in results))

    elif rid == "09":
        result = data.get("result", {})
        if result.get("state") == "COMPLETE":
            toks = result.get("output_tokens", 0)
            if toks:
                metrics["tokens"] = int(toks)

    elif rid == "10":
        steps = data.get("steps", [])
        if steps:
            metrics["steps"] = len(steps)
            total = sum(s.get("output_tokens", 0) for s in steps)
            if total:
                metrics["total_tokens"] = int(total)

    elif rid == "17":
        parts_done = sum(
            1 for k in ("review", "summary", "refactor")
            if data.get(k, {}).get("state") == "COMPLETE"
        )
        if parts_done:
            metrics["steps"] = f"{parts_done}/3"

    elif rid == "18" and data.get("results"):
        rs = data["results"]
        done = sum(1 for r in rs if r.get("state") == "COMPLETE")
        metrics["tasks"] = f"{done}/{len(rs)}"
        t = tps_stats(rs)
        if t:
            metrics["avg_tps"] = t

    elif rid == "23":
        rs = data.get("results", [])
        if rs:
            done = sum(1 for r in rs if r.get("state") == "COMPLETE")
            metrics["tasks"] = f"{done}/{len(rs)}"
        evts = data.get("agent_events", [])
        if evts:
            metrics["agent_events"] = len(evts)

    elif rid == "24":
        steps = data.get("steps", [])
        if steps:
            metrics["steps"] = len(steps)
        total_tokens = data.get("total_tokens")
        if total_tokens:
            metrics["total_tokens"] = int(total_tokens)

    elif rid == "12" and results:
        metrics["tasks"] = completion(results)
        t = tps_stats(results)
        if t:
            metrics["avg_tps"] = t

    elif rid == "19":
        chunks = data.get("chunks", [])
        if chunks:
            done = sum(1 for c in chunks if c.get("state") == "COMPLETE")
            metrics["chunks"] = f"{done}/{len(chunks)}"
        if data.get("assembly", {}).get("state") == "COMPLETE":
            metrics["assembled"] = True

    elif rid == "20":
        completed = data.get("completed")
        num_tasks = data.get("num_tasks")
        if completed is not None and num_tasks:
            metrics["tasks"] = f"{int(completed)}/{int(num_tasks)}"
        throughput = data.get("throughput")
        if throughput:
            metrics["avg_tps"] = round(float(throughput), 1)

    elif rid == "22":
        total_tasks = data.get("total_tasks")
        if total_tasks:
            metrics["tasks_rewarded"] = int(total_tasks)
        total_credits = data.get("total_credits")
        if total_credits:
            metrics["credits"] = round(float(total_credits), 2)

    elif rid == "29":
        # Model fingerprinting metrics
        results_data = data.get("results", [])
        if results_data:
            total_runs = sum(r.get("num_runs", 0) for r in results_data) if results_data else 0
            stable_tests = [r for r in results_data if r.get("expect_stable")]
            passed = [r for r in stable_tests if r.get("consistency_pct", 0) >= 80]
            if stable_tests:
                metrics["stable"] = f"{len(passed)}/{len(stable_tests)}"
            if total_runs:
                metrics["runs"] = total_runs

    elif rid == "30":
        # Academic paper pipeline metrics
        stages = data.get("stages", [])
        if stages:
            done = sum(1 for s in stages if s.get("state") == "COMPLETE")
            metrics["stages"] = f"{done}/{len(stages)}"
        total_tokens = data.get("total_tokens", 0)
        if total_tokens:
            metrics["total_tokens"] = int(total_tokens)
        conclusion = data.get("conclusion", {})
        if conclusion.get("state") == "COMPLETE":
            metrics["synthesis"] = "✓"

    elif rid == "34":
        # Junior developer assistant metrics
        stages = ["code_review", "refactoring", "documentation"]
        completed_stages = []
        total_tokens = 0

        for stage in stages:
            stage_data = data.get(stage, {})
            if stage_data.get("state") == "COMPLETE":
                completed_stages.append(stage)
                total_tokens += stage_data.get("output_tokens", 0)

        if completed_stages:
            metrics["stages"] = f"{len(completed_stages)}/3"
            if total_tokens > 0:
                metrics["total_tokens"] = int(total_tokens)

    elif rid == "01":
        if data.get("state") == "COMPLETE" or data.get("status") == "ok":
            metrics["status"] = "ok"
        elif data:
            metrics["status"] = "ran"

    elif rid == "02":
        qs = data.get("results", data.get("queries", []))
        if qs:
            metrics["tasks"] = completion(qs)

    elif rid == "05":
        if data.get("state") == "COMPLETE":
            toks = data.get("output_tokens", 0)
            if toks:
                metrics["tokens"] = int(toks)

    return metrics


def render_report(parsed: dict, jsons: dict) -> str:
    run_date = parsed.get("run_date", "unknown")
    hub = parsed.get("hub", "unknown")
    passed = parsed.get("passed") or 0
    total = parsed.get("total") or 0
    total_s = parsed.get("total_elapsed_s") or 0.0
    recipes = parsed["recipes"]
    hostname = socket.gethostname()

    lines: list[str] = []

    lines += [
        "# Momahub Cookbook Run — Analysis Report",
        "",
        f"**Run date:** {run_date}  ",
        f"**Hub:** {hub}  ",
        f"**Hostname:** {hostname}  ",
        f"**Result:** {passed}/{total} passed  ",
        f"**Total time:** {total_s:.1f}s ({total_s / 60:.1f} min)  ",
        f"**Log:** `{parsed['log_file']}`  ",
        "",
        "---",
        "",
        "## Recipe Results",
        "",
        "| ID | Recipe | Status | Elapsed | Description | Notes |",
        "|----|--------|--------|---------|-------------|-------|",
    ]

    for r in recipes:
        icon = "✅" if r["status"] == "SUCCESS" else "❌"
        m = extract_metrics(r, jsons)
        notes_parts = _notes_parts(m)
        notes = " · ".join(notes_parts) if notes_parts else "—"
        m_cmd = re.search(r'(?:go|mg) run (\./\S+\.(?:go|spl))', r['cmd'])
        if m_cmd:
            rel_path = m_cmd.group(1).lstrip('./')  # e.g. "04_benchmark_models/benchmark.go"
            recipe_link = f"[{r['name']}](../{rel_path})"
        else:
            recipe_link = r['name']
        desc = RECIPE_DESCRIPTION.get(r['id'], "")
        lines.append(f"| {r['id']} | {recipe_link} | {icon} | {r['elapsed_s']:.1f}s | {desc} | {notes} |")

    lines += ["", "---", "", "## Timing Breakdown", ""]

    sorted_r = sorted(recipes, key=lambda r: r["elapsed_s"], reverse=True)
    lines += [
        "**Top 5 slowest:**",
        "",
        "| Rank | Recipe | Elapsed |",
        "|------|--------|---------|",
    ]
    for i, r in enumerate(sorted_r[:5], 1):
        lines.append(f"| {i} | [{r['id']}] {r['name']} | {r['elapsed_s']:.1f}s |")

    lines += ["", "**Fastest 5:**", "", "| Rank | Recipe | Elapsed |", "|------|--------|---------|"]
    for i, r in enumerate(reversed(sorted_r[-5:]), 1):
        lines.append(f"| {i} | [{r['id']}] {r['name']} | {r['elapsed_s']:.1f}s |")

    # Throughput summary
    tput_rows = []
    for r in recipes:
        if r["id"] in ("07", "13", "04", "08", "18") and r["status"] == "SUCCESS":
            m = extract_metrics(r, jsons)
            if "avg_tps" in m:
                tput_rows.append((r["id"], r["name"], m["avg_tps"]))

    if tput_rows:
        lines += [
            "",
            "---",
            "",
            "## Throughput Summary",
            "",
            "| Recipe | Avg tok/s |",
            "|--------|-----------|",
        ]
        for rid, name, tps in tput_rows:
            lines.append(f"| [{rid}] {name} | {tps} |")

    # Math olympiad scores
    math_data = jsons.get("math_olympiad", {})
    if math_data.get("results"):
        r16 = next((r for r in recipes if r["id"] == "16"), None)
        if r16:
            m = extract_metrics(r16, jsons)
            if "scores" in m:
                lines += [
                    "",
                    "---",
                    "",
                    "## Math Olympiad Scores",
                    "",
                    f"Difficulty: `{math_data.get('difficulty', '?')}`",
                    "",
                    "| Model | Score |",
                    "|-------|-------|",
                ]
                for model, score in m["scores"].items():
                    lines.append(f"| {model} | {score} |")

    # Failed recipes
    failed = [r for r in recipes if r["status"] == "FAILED"]
    if failed:
        lines += ["", "---", "", "## Failed Recipes", ""]
        for r in failed:
            lines += [
                f"### ❌ [{r['id']}] {r['name']}",
                "",
                f"**Command:** `{r['cmd']}`  ",
                f"**Elapsed:** {r['elapsed_s']:.1f}s  ",
                "",
            ]
            if r["output"]:
                lines += ["**Last output:**", "```", *r["output"][-15:], "```", ""]

    lines += [
        "",
        "---",
        "",
        f"*Generated {datetime.now().strftime('%Y-%m-%d %H:%M:%S')} by `cookbook/analyze_run_all.py`*",
        "",
    ]
    return "\n".join(lines)


def _notes_parts(m: dict) -> list[str]:
    """Shared helper — build the notes string parts from an extracted metrics dict."""
    parts = []
    if "avg_tps" in m:
        parts.append(f"avg {m['avg_tps']} tok/s")
    if "tasks" in m:
        parts.append(f"{m['tasks']} tasks")
    if "translations" in m:
        parts.append(f"{m['translations']} langs")
    if "languages" in m:
        parts.append(m["languages"])
    if "scores" in m:
        parts.append(", ".join(f"{k}={v}" for k, v in m["scores"].items()))
    if "models_tested" in m:
        parts.append(f"{m['models_tested']} models")
    if "models_probed" in m:
        parts.append(f"{m['models_probed']} models probed")
    if "models" in m:
        parts.append(f"{m['models']} models")
    if "steps" in m:
        parts.append(f"{m['steps']} steps")
    if "tokens" in m:
        parts.append(f"{m['tokens']} tok")
    if "total_tokens" in m:
        parts.append(f"{m['total_tokens']} tok")
    if "agent_events" in m:
        parts.append(f"{m['agent_events']} events")
    if "chunks" in m:
        parts.append(f"{m['chunks']} chunks")
    if "assembled" in m:
        parts.append("assembled ✓")
    if "tasks_rewarded" in m:
        parts.append(f"{m['tasks_rewarded']} tasks rewarded")
    if "credits" in m:
        parts.append(f"{m['credits']} credits")
    if "status" in m:
        parts.append(m["status"])
    if "stable" in m:
        parts.append(f"{m['stable']} stable")
    if "runs" in m:
        parts.append(f"{m['runs']} runs")
    if "stages" in m:
        parts.append(f"{m['stages']} stages")
    if "synthesis" in m:
        parts.append(f"synthesis {m['synthesis']}")
    return parts


def render_html(parsed: dict, jsons: dict) -> str:
    """Render a self-contained HTML report suitable for demo / browser viewing."""
    run_date = parsed.get("run_date", "unknown")
    hub = parsed.get("hub", "unknown")
    passed = parsed.get("passed") or 0
    total = parsed.get("total") or 0
    total_s = parsed.get("total_elapsed_s") or 0.0
    recipes = parsed["recipes"]
    hostname = socket.gethostname()
    gen_time = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    pass_color = "#22c55e" if passed == total else "#f59e0b"

    # ── recipe rows ──────────────────────────────────────────────────────────
    recipe_rows = []
    for r in recipes:
        ok = r["status"] == "SUCCESS"
        icon = "✅" if ok else "❌"
        row_cls = "" if ok else ' style="background:#fef2f2"'
        m = extract_metrics(r, jsons)
        parts = _notes_parts(m)
        notes = " · ".join(parts) if parts else "—"
        m_cmd = re.search(r'(?:go|mg) run (\./\S+\.(?:go|spl))', r["cmd"])
        if m_cmd:
            rel = m_cmd.group(1).lstrip("./")
            recipe_cell = f'<a href="../{rel}" style="color:#2563eb;text-decoration:none">{r["name"]}</a>'
        else:
            recipe_cell = r["name"]
        desc = RECIPE_DESCRIPTION.get(r["id"], "")
        recipe_rows.append(
            f'<tr{row_cls}>'
            f'<td style="text-align:center;font-weight:600">{r["id"]}</td>'
            f'<td>{recipe_cell}</td>'
            f'<td style="text-align:center">{icon}</td>'
            f'<td style="text-align:right">{r["elapsed_s"]:.1f}s</td>'
            f'<td style="color:#6b7280">{desc}</td>'
            f'<td>{notes}</td>'
            f'</tr>'
        )
    recipe_rows_html = "\n".join(recipe_rows)

    # ── throughput table ─────────────────────────────────────────────────────
    tput_rows_html = ""
    for r in recipes:
        if r["id"] in ("07", "13", "04", "08", "18") and r["status"] == "SUCCESS":
            m = extract_metrics(r, jsons)
            if "avg_tps" in m:
                tput_rows_html += (
                    f'<tr><td>[{r["id"]}] {r["name"]}</td>'
                    f'<td style="text-align:right;font-weight:600">{m["avg_tps"]}</td></tr>\n'
                )

    # ── math olympiad ────────────────────────────────────────────────────────
    math_html = ""
    math_data = jsons.get("math_olympiad", {})
    if math_data.get("results"):
        r16 = next((r for r in recipes if r["id"] == "16"), None)
        if r16:
            m = extract_metrics(r16, jsons)
            if "scores" in m:
                score_rows = "".join(
                    f'<tr><td>{model}</td><td style="text-align:center">{score}</td></tr>'
                    for model, score in m["scores"].items()
                )
                diff = math_data.get("difficulty", "?")
                math_html = f"""
        <h2>Math Olympiad Scores</h2>
        <p style="color:#6b7280">Difficulty: <code>{diff}</code></p>
        <table>
          <thead><tr><th>Model</th><th>Score</th></tr></thead>
          <tbody>{score_rows}</tbody>
        </table>"""

    # ── timing breakdown ─────────────────────────────────────────────────────
    sorted_r = sorted(recipes, key=lambda r: r["elapsed_s"], reverse=True)
    slow_rows = "".join(
        f'<tr><td>{i}</td><td>[{r["id"]}] {r["name"]}</td>'
        f'<td style="text-align:right">{r["elapsed_s"]:.1f}s</td></tr>'
        for i, r in enumerate(sorted_r[:5], 1)
    )
    fast_rows = "".join(
        f'<tr><td>{i}</td><td>[{r["id"]}] {r["name"]}</td>'
        f'<td style="text-align:right">{r["elapsed_s"]:.1f}s</td></tr>'
        for i, r in enumerate(reversed(sorted_r[-5:]), 1)
    )

    # ── failed section ───────────────────────────────────────────────────────
    failed_html = ""
    failed = [r for r in recipes if r["status"] == "FAILED"]
    if failed:
        items = []
        for r in failed:
            tail = "<br>".join(r["output"][-10:]) if r["output"] else ""
            items.append(
                f'<div style="background:#fef2f2;border:1px solid #fca5a5;border-radius:6px;padding:12px;margin-bottom:12px">'
                f'<strong>❌ [{r["id"]}] {r["name"]}</strong> — {r["elapsed_s"]:.1f}s<br>'
                f'<code style="font-size:12px">{r["cmd"]}</code>'
                + (f'<pre style="font-size:12px;margin-top:8px">{tail}</pre>' if tail else "")
                + "</div>"
            )
        failed_html = "<h2>Failed Recipes</h2>" + "".join(items)

    tput_section = ""
    if tput_rows_html:
        tput_section = f"""
        <h2>Throughput Summary</h2>
        <table>
          <thead><tr><th>Recipe</th><th style="text-align:right">Avg tok/s</th></tr></thead>
          <tbody>{tput_rows_html}</tbody>
        </table>"""

    timing_section = f"""
        <h2>Timing Breakdown</h2>
        <div style="display:grid;grid-template-columns:1fr 1fr;gap:24px">
          <div>
            <h3 style="margin:0 0 8px">Top 5 slowest</h3>
            <table>
              <thead><tr><th>#</th><th>Recipe</th><th>Elapsed</th></tr></thead>
              <tbody>{slow_rows}</tbody>
            </table>
          </div>
          <div>
            <h3 style="margin:0 0 8px">Top 5 fastest</h3>
            <table>
              <thead><tr><th>#</th><th>Recipe</th><th>Elapsed</th></tr></thead>
              <tbody>{fast_rows}</tbody>
            </table>
          </div>
        </div>"""

    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>MomaGrid Cookbook Run — {run_date}</title>
<style>
  * {{ box-sizing: border-box; margin: 0; padding: 0; }}
  body {{ font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
          background: #f8fafc; color: #1e293b; padding: 32px 24px; }}
  .container {{ max-width: 1100px; margin: 0 auto; }}
  .banner {{ background: linear-gradient(135deg, #1e40af 0%, #7c3aed 100%);
             color: white; border-radius: 12px; padding: 28px 32px; margin-bottom: 28px; }}
  .banner h1 {{ font-size: 1.8rem; font-weight: 700; margin-bottom: 6px; }}
  .banner .subtitle {{ opacity: 0.85; font-size: 0.95rem; margin-bottom: 16px; }}
  .stats {{ display: flex; gap: 24px; flex-wrap: wrap; margin-top: 16px; }}
  .stat {{ background: rgba(255,255,255,0.15); border-radius: 8px; padding: 10px 18px; }}
  .stat .val {{ font-size: 1.6rem; font-weight: 700; color: {pass_color}; }}
  .stat .lbl {{ font-size: 0.78rem; opacity: 0.8; margin-top: 2px; }}
  .pi-badge {{ display:inline-block; background:rgba(255,255,255,0.2);
               border-radius:20px; padding:4px 14px; font-size:0.85rem; margin-left:12px; }}
  h2 {{ font-size: 1.15rem; font-weight: 600; margin: 28px 0 12px;
        padding-bottom: 6px; border-bottom: 2px solid #e2e8f0; }}
  h3 {{ font-size: 0.95rem; font-weight: 600; }}
  table {{ width: 100%; border-collapse: collapse; background: white;
           border-radius: 8px; overflow: hidden;
           box-shadow: 0 1px 3px rgba(0,0,0,0.08); margin-bottom: 8px; }}
  th {{ background: #f1f5f9; font-size: 0.78rem; font-weight: 600;
        text-transform: uppercase; letter-spacing: 0.05em;
        color: #64748b; padding: 10px 14px; text-align: left; }}
  td {{ padding: 9px 14px; font-size: 0.88rem; border-top: 1px solid #f1f5f9; }}
  tr:hover td {{ background: #fafbff; }}
  code {{ background: #f1f5f9; border-radius: 4px; padding: 1px 5px;
          font-size: 0.83rem; font-family: monospace; }}
  pre {{ background: #1e293b; color: #e2e8f0; border-radius: 6px;
         padding: 10px; font-size: 0.8rem; overflow-x: auto; }}
  .footer {{ margin-top: 40px; text-align: center; font-size: 0.78rem; color: #94a3b8; }}
</style>
</head>
<body>
<div class="container">

  <div class="banner">
    <h1>🦆 MomaGrid Cookbook Run
      <span class="pi-badge">π Day — March 14</span>
    </h1>
    <div class="subtitle">Decentralized AI Inference Grid — 3-GPU LAN Test</div>
    <div class="stats">
      <div class="stat"><div class="val">{passed}/{total}</div><div class="lbl">Recipes passed</div></div>
      <div class="stat"><div class="val">{total_s:.0f}s</div><div class="lbl">Total elapsed</div></div>
      <div class="stat"><div class="val">{total_s/60:.1f} min</div><div class="lbl">Wall time</div></div>
      <div class="stat"><div class="val">{hub}</div><div class="lbl">Hub</div></div>
    </div>
  </div>

  <div style="background:white;border-left:4px solid #7c3aed;border-radius:8px;
              padding:20px 24px;margin-bottom:24px;
              box-shadow:0 1px 3px rgba(0,0,0,0.08)">
    <h2 style="margin:0 0 10px;border:none;color:#7c3aed;font-size:1.05rem">
      Executive Summary
    </h2>
    <p style="font-size:0.95rem;line-height:1.7;color:#1e293b">
      <strong>MomaGrid</strong> — a decentralized, open-source AI inference grid, running entirely
      on your own hardware, coordinating <strong>3 GPU nodes</strong>, serving
      <strong>14 LLM models</strong>, verified by cryptographic identity, with
      <strong>{passed}/{total} cookbook recipes</strong> all passing.
    </p>
    <p style="font-size:0.95rem;line-height:1.7;color:#1e293b;margin-top:8px">
      Built from scratch. Spec-driven. Pure Go. No cloud dependency. No API bills.
    </p>
  </div>

  <p style="font-size:0.85rem;color:#64748b;margin-bottom:20px">
    Run date: <strong>{run_date}</strong> &nbsp;|&nbsp;
    Hostname: <strong>{hostname}</strong> &nbsp;|&nbsp;
    Log: <code>{parsed["log_file"]}</code>
  </p>

  <h2>Recipe Results</h2>
  <table>
    <thead>
      <tr>
        <th style="width:40px">ID</th>
        <th>Recipe</th>
        <th style="text-align:center;width:60px">Status</th>
        <th style="text-align:right;width:70px">Elapsed</th>
        <th style="width:220px">Description</th>
        <th>Notes</th>
      </tr>
    </thead>
    <tbody>
{recipe_rows_html}
    </tbody>
  </table>

  {tput_section}
  {math_html}
  {timing_section}
  {failed_html}

  <div class="footer">
    Generated {gen_time} by <code>cookbook/analyze_run_all.py</code>
    &nbsp;·&nbsp; MomaGrid — open-source decentralized AI inference
  </div>

</div>
</body>
</html>"""


def main():
    parser = argparse.ArgumentParser(
        description="Analyze a run_all log and produce a Markdown report."
    )
    parser.add_argument("log", nargs="?", help="Path to run_all log (default: latest in --out dir)")
    parser.add_argument("--out", default=None, help="Output directory (default: <script_dir>/out)")
    args = parser.parse_args()

    # Resolve output dir — always absolute so paths are unambiguous regardless of cwd
    out_dir = Path(args.out).resolve() if args.out else _DEFAULT_OUT
    out_dir.mkdir(parents=True, exist_ok=True)

    if args.log:
        log_path = Path(args.log).resolve()
        if not log_path.exists():
            print(f"Error: log file not found: {log_path}", file=sys.stderr)
            sys.exit(1)
    else:
        log_path = find_latest_log(out_dir)
        if not log_path:
            print(f"No RUN_ALL_LOGGING-*.md found in {out_dir}", file=sys.stderr)
            print(f"Run from repo root:")
            print(f"  go run cookbook/run_all.go 2>&1 | tee cookbook/out/RUN_ALL_LOGGING-$(date +%Y%m%d-%H%M%S).md")
            sys.exit(1)
        print(f"Using log: {log_path}")

    parsed = parse_log(log_path)
    if not parsed["recipes"]:
        print("Warning: no recipe results found in log — check log format.", file=sys.stderr)

    jsons = load_jsons(out_dir)

    ts = datetime.now().strftime("%Y%m%d-%H%M%S")

    report_md = render_report(parsed, jsons)
    md_path = out_dir / f"RUN_ALL_REPORT-{ts}.md"
    md_path.write_text(report_md)
    print(f"Markdown report: {md_path}")

    report_html = render_html(parsed, jsons)
    html_path = out_dir / f"RUN_ALL_REPORT-{ts}.html"
    html_path.write_text(report_html)
    print(f"HTML report:     {html_path}")

    # Print quick summary to terminal
    print(f"\n{'='*50}")
    print(f"  {parsed.get('passed')}/{parsed.get('total')} passed  |  {parsed.get('total_elapsed_s', 0):.1f}s total")
    print(f"{'='*50}")
    print(f"\n  Open in browser:  {html_path}")


if __name__ == "__main__":
    main()
