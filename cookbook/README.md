# Cookbook

Ready-to-run recipes demonstrating MoMaHub i-grid capabilities. Each recipe is self-contained.

## Prerequisites

```bash
go build -buildvcs=false -o mg ./cmd/mg   # build the CLI
ollama pull llama3                         # at least one model
mg hub up --host 0.0.0.0                  # start hub (default port 9000)
mg join                                    # start agent(s)
```

## Recipes

| # | Recipe | Script | Description |
|---|--------|--------|-------------|
| 01 | Single Node Hello | `hello.spl` | Minimal SPL program — verify hub + agent + Ollama work |
| 02 | Multi-CTE Parallel | `multi_cte.spl` | Two models in parallel, then synthesis — fan-out demo |
| 03 | Batch Translate | `translate.go` | One text to N languages in parallel |
| 04 | Benchmark Models | `benchmark.go` | Same prompt to multiple models, compare TPS and latency |
| 05 | RAG on Grid | `rag_query.spl` | Retrieval-augmented generation dispatched to the grid |
| 07 | Stress Test | `stress.go` | Fire N tasks, watch all GPUs light up, measure throughput |
| 08 | Model Arena | `arena.go` | Side-by-side comparison of multiple models on the same prompt |
| 09 | Doc Pipeline | `pipeline.go` | Text file → grid summarize → structured output |
| 10 | Chain Relay | `chain.go` | Multi-step reasoning: Research → Analyze → Summarize |
| 12 | Tier-Aware Dispatch | `tier_dispatch.go` | Submit tasks with VRAM hints, verify routing to correct agent tier |
| 13 | Multi-Agent Throughput | `throughput.go` | Measure tokens/s scaling: 1 agent vs 2 vs 3 — key paper metric |
| 15 | Agent Failover | `failover.go` | Kill an agent mid-run, verify tasks re-queue and complete |
| 16 | Math Olympiad | `math_olympiad.go` | Benchmark mathstral + qwen2-math on 15 problems, score accuracy |
| 17 | Code Review Pipeline | `code_review.go` | review → summarise → refactor across deepseek-coder + llama3 |
| 18 | Smart Router | `smart_router.go` | Auto-route math/code/general prompts to the optimal model |
| 19 | Privacy Chunk Demo | `privacy_demo.go` | Split document across agents — no single agent sees the full text |
| 20 | Overnight Batch | `overnight.go` | Submit 100–500 tasks overnight, full report by morning |
| 21 | Language Accessibility | `language_grid.go` | Same question in 10 languages in parallel — accessibility demo |
| 22 | Rewards Report | `rewards_report.go` | Pretty-print reward ledger: tasks, tokens, credits per operator |
| 23 | Wake/Sleep Resilience | `resilience.go` | Tasks flow continuously as agents join/leave dynamically |
| 24 | SPL Compiler Pipeline | `compiler_demo.go` | 5-step: translate → concepts → optimise → generate → format |
| 25 | Model Diversity | `model_diversity.go` | All 14 models benchmarked on 6 domains — latency, TPS, quality |
| 90 | Two-Hub Cluster | `setup.go` | Set up and test hub peering and task forwarding |

Results from each run are saved to `out/<name>_<timestamp>.json` in the recipe directory.

## Run all recipes

`run_all.go` runs every recipe sequentially, tees output to the terminal and a per-recipe log file, and prints a summary table at the end.

```bash
# Run all recipes against the default hub (http://localhost:9000)
# go run cookbook/run_all.go 2>&1 | tee cookbook/out/RUN_ALL_LOGGING-20260313_000000.md  

go run cookbook/run_all.go 2>&1 | tee cookbook/out/RUN_ALL_LOGGING-$(date +%Y%m%d-%H%M%S).md

# Custom hub URL
go run cookbook/run_all.go --hub http://192.168.0.177:9000

# Run specific recipes by ID
go run cookbook/run_all.go --ids 04,08,13

# List available recipe IDs and exit
go run cookbook/run_all.go --list
```

Each recipe's output is also written to `cookbook/<recipe_dir>/<name>_<timestamp>.log`.
Summary at the end shows pass/fail status and elapsed time per recipe.

## Quick start

```bash
# Smoke test (SPL)
mg run cookbook/01_single_node_hello/hello.spl
mg run cookbook/02_multi_cte_parallel/multi_cte.spl

# Batch translate
go run cookbook/03_batch_translate/translate.go "Hello, world!"

# With flags (hub URL, languages, model)
go run cookbook/03_batch_translate/translate.go \
  --hub http://localhost:9000 \
  --langs "French,Chinese,Spanish" \
  --model llama3 \
  "Intelligence is decentralized."

# Translate from a file
go run cookbook/03_batch_translate/translate.go \
  --file cookbook/03_batch_translate/input_msg.txt

# Benchmark models
go run cookbook/04_benchmark_models/benchmark.go \
  --models llama3,mistral,phi3

# Stress test (all GPUs)
go run cookbook/07_stress_test/stress.go --n 20

# Model arena (side-by-side comparison)
go run cookbook/08_model_arena/arena.go

# Multi-step reasoning chain
go run cookbook/10_chain_relay/chain.go "distributed AI inference"

# Tier-aware dispatch (VRAM routing)
go run cookbook/12_tier_aware_dispatch/tier_dispatch.go

# Throughput scaling (run with 1, 2, 3 agents — compare results)
go run cookbook/13_multi_agent_throughput/throughput.go \
  --n 30 --label "3-agents"

# Failover test (kill an agent mid-run)
go run cookbook/15_agent_failover/failover.go --n 30

# Math olympiad (accuracy + TPS comparison)
go run cookbook/16_math_olympiad/math_olympiad.go \
  --models mathstral,qwen2-math

# Code review pipeline (multi-step, multi-model)
go run cookbook/17_code_review_pipeline/code_review.go \
  --file internal/hub/app.go

# Smart router (auto-route by prompt type)
go run cookbook/18_smart_router/smart_router.go --demo

# Privacy chunk demo
go run cookbook/19_privacy_chunk_demo/privacy_demo.go

# Overnight batch (100 tasks)
go run cookbook/20_overnight_batch/overnight.go --tasks 100

# Language accessibility (10 languages in parallel)
go run cookbook/21_language_accessibility/language_grid.go

# Rewards report
go run cookbook/22_rewards_report/rewards_report.go

# Wake/sleep resilience (5 minutes)
go run cookbook/23_wake_sleep_resilience/resilience.go --duration 300

# SPL compiler pipeline demo
go run cookbook/24_spl_compiler_pipeline/compiler_demo.go --demo

# Model diversity — quick probe (check which models are online)
go run cookbook/25_model_diversity/model_diversity.go --probe

# Model diversity — full benchmark
go run cookbook/25_model_diversity/model_diversity.go

# Test a subset of models
go run cookbook/25_model_diversity/model_diversity.go \
  --models llama3.1,qwen3,deepseek-r1,gemma3,phi4,phi4-mini
```

## Hub setup with PostgreSQL

```bash
go build -buildvcs=false -o mg ./cmd/mg
mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable" --port 9000
```

---

## First Batch Run — 2026-03-13

First-ever full run of all recipes via `go run cookbook/run_all.go`.
Single agent (`wen`, GTX 1080 Ti, 11 GB VRAM). Run started 10:42:38, total wall time **1321.5 s** (~22 min).

### Result table

| ID | Recipe | Status | Elapsed |
|----|--------|--------|---------|
| 01 | Hello SPL | OK | 5.3s |
| 02 | Multi-CTE Parallel | OK | 42.0s |
| 03 | Batch Translate | OK | 25.2s |
| 04 | Benchmark Models | OK | 91.5s |
| 05 | RAG on Grid | **FAILED** | 2.0s |
| 07 | Stress Test | OK | 76.4s |
| 08 | Model Arena | OK | 75.1s |
| 09 | Doc Pipeline | OK | 20.2s |
| 10 | Chain Relay | OK | 42.3s |
| 12 | Tier-Aware Dispatch | OK | 8.3s |
| 13 | Multi-Agent Throughput | OK | 78.2s |
| 15 | Agent Failover | OK | 43.7s |
| 16 | Math Olympiad | OK | 89.4s |
| 17 | Code Review Pipeline | OK | 126.5s |
| 18 | Smart Router | OK | 15.1s |
| 19 | Privacy Chunk Demo | OK | 18.9s |
| 20 | Overnight Batch | OK | 71.3s |
| 21 | Language Accessibility | OK | 38.6s |
| 22 | Rewards Report | OK | 0.2s |
| 23 | Wake/Sleep Resilience | OK | 62.8s |
| 24 | SPL Compiler Pipeline | OK | 62.8s |
| 25 | Model Diversity | OK | 578.6s |

**21 / 22 passed** on the first-ever run against a single consumer GPU.

### What went well

- **95.5% pass rate out of the box** — 21 of 22 recipes succeeded on the very first run with zero tuning.
- **Sustained ~46 tokens/s** throughout the stress test (recipe 07, 5 tasks) and the throughput benchmark (recipe 13, 30 tasks). Consistent under load.
- **All parallel fan-out recipes worked** — multi-CTE (02), model arena (08), multi-agent throughput (13), and language accessibility (21) all dispatched concurrent tasks and received complete results.
- **Multi-step pipelines completed end-to-end** — chain relay (10) produced a full executive summary on quantum computing across three chained model calls in 42 s. Code review pipeline (17) coordinated deepseek-coder-v2 and llama3 across review + summarise + refactor steps.
- **Privacy chunking (19) and SPL compiler pipeline (24) worked without modification** — these are the most structurally complex recipes and both passed first time.
- **Agent failover (15): 20/20 tasks completed** — the hub correctly re-queued tasks when the agent was evicted; no tasks were lost.
- **Rewards report (22) returned in 0.2 s** — confirms the reward ledger API path is fast (pure DB read, no inference).
- **Recipe 17 (Code Review)** was the slowest pipeline at 126.5 s, dominated by a single deepseek-coder-v2 review call (76.1 s). Expected for a large code-analysis model.

### Issues found

**Recipe 05 — RAG on Grid: FAILED (now fixed)**
- Root cause: `rag_query.spl` used `RAG_QUERY('...')` which the SPL parser does not support. The keyword was silently dropped, leaving the agent with a broken prompt. The task was dispatched, the agent returned FAILED immediately (2 s).
- Fix: `run_all.go` recipe 05 now points to `go run rag.go` instead of `mg run rag_query.spl`. The SPL file was also rewritten to embed context directly in a valid `GENERATE llm('...')` prompt.

**Recipe 12 — Tier-Aware Dispatch: agent reported BRONZE with 11 GB VRAM**
- The GTX 1080 Ti has 11 GB VRAM which qualifies for SILVER tier (≥ 8 GB). However, the tier is assigned at agent join time from the VRAM reading, and the agent registered as BRONZE. This is a classification bug to investigate — possibly the VRAM threshold boundary or the VRAM detection at join time.
- Recipe completed successfully; the tier routing logic ran, but dispatch landed on BRONZE rather than SILVER.

**Recipe 02 — Multi-CTE Parallel: `analyse_cons` response was blank**
- The `analyse_cons` task (model: `mistral`) returned an empty string. The other CTE legs (`summarise` and `analyse_pros`) completed normally. Likely a transient model-side empty-generation event — nothing in the hub logs indicated a task failure.
- Re-running recipe 02 individually produced non-empty output. No structural defect.

**Recipe 16 — Math Olympiad: both models scored 1/5**
- `mathstral` and `qwen2-math` each scored 1 correct out of 5. The models produced correct reasoning and numeric answers, but the answer-extraction regex (`\boxed{...}`) did not match the models' free-form output format. The checker is too strict — the grader needs to handle plain numeric answers in addition to LaTeX box notation.

**Recipe 25 — Model Diversity: 578.6 s (9.6 min)**
- Benchmarking 14 models on a single 11 GB GPU requires Ollama to evict and reload models for every swap. Each model load adds 30–90 s of overhead. With 14 models across 6 domains, total model-swap time dominated the wall clock. This is expected behaviour on a single-GPU setup; the recipe includes `--probe` mode and `--models` filtering precisely to reduce scope when needed. See `cookbook/25_model_diversity/README.md` for details.
