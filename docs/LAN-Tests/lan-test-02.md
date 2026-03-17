# LAN Test 02 — Single GPU, All Recipes (2026-03-14)

**Date:** 2026-03-14
**Goal:** Verify all fixes from session (TierFromVRAM, pulse metrics, recipe 05, checkAnswer) and run full 22-recipe suite.

---

## Prerequisites

### 1 — Install Go (each machine)

Go 1.22+ is required. Skip if already installed (`go version` to check).

```bash
# Ubuntu / Debian
sudo apt update && sudo apt install -y golang-go

# Or install a specific version via the official tarball (recommended for latest)
wget https://go.dev/dl/go1.24.1.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.24.1.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

go version   # should print go1.24.x or later
```

---

### 2 — Clone the repo (each machine)

```bash
git clone https://github.com/digital-duck/momahub.go.git
cd momahub.go
```

If already cloned, pull latest:

```bash
cd momahub.go
git pull
```


### 3 — Install PostGresSQL (hub machine)

```bash


```

---
## Setup

| Role | Machine | IP | GPU |
|------|---------|----|-----|
| Hub + Agent | papa-game | 192.168.0.177 | GTX 1080 Ti (11 GB VRAM) |

14 Ollama models loaded: llama3, llama3.1, mistral, mathstral, qwen3, qwen2.5, qwen2.5-coder, qwen2-math, deepseek-r1, deepseek-coder-v2, gemma3, phi4, phi4-mini, phi3

---

### Step 1 — Build `mg`

```bash
go build -buildvcs=false -o mg ./cmd/mg

ln -sf ~/projects/digital-duck/momahub.go/mg ~/.local/bin/mg
```

---

### Step 2 — Start the hub (terminal 1)

```bash
mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"
```

---

### Step 3 — Join agent (terminal 2)

```bash
mg join http://192.168.0.177:9000
```

Expected (after TierFromVRAM fix):
```
Joined grid  hub=hub-xxxxxxxx  tier=GOLD  status=ONLINE
```

---

### Step 4 — Run all recipes

```bash
mkdir -p cookbook/out
go run cookbook/run_all.go 2>&1 | tee cookbook/out/RUN_ALL_LOGGING-$(date +%Y%m%d-%H%M%S).md
```

---

### Step 5 — Generate analysis report

```bash
python cookbook/analyze_run_all.py
```

---

## Result — 2026-03-14

**22/22 SUCCESS** — first perfect run. Total time: 1425.1s.

### Summary table

| ID | Recipe | Status | Elapsed |
|----|--------|--------|---------|
| 01 | Hello SPL | OK | 6.0s |
| 02 | Multi-CTE Parallel | OK | 22.1s |
| 03 | Batch Translate | OK | 4.7s |
| 04 | Benchmark Models | OK | 8.3s |
| 05 | RAG on Grid | OK | 8.2s |
| 07 | Stress Test (15 tasks) | OK | 154.4s |
| 08 | Model Arena | OK | 25.9s |
| 09 | Doc Pipeline | OK | 12.7s |
| 10 | Chain Relay | OK | 44.3s |
| 12 | Tier-Aware Dispatch | OK | 11.5s |
| 13 | Multi-Agent Throughput (60 tasks) | OK | 145.1s |
| 15 | Agent Failover | OK | 30.6s |
| 16 | Math Olympiad | OK | 76.5s |
| 17 | Code Review Pipeline | OK | 126.7s |
| 18 | Smart Router | OK | 57.8s |
| 19 | Privacy Chunk Demo | OK | 21.9s |
| 20 | Overnight Batch | OK | 64.4s |
| 21 | Language Accessibility | OK | 19.7s |
| 22 | Rewards Report | OK | 0.3s |
| 23 | Wake/Sleep Resilience | OK | 60.6s |
| 24 | SPL Compiler Pipeline | OK | 23.5s |
| 25 | Model Diversity (14 models) | OK | 500.0s |

### Model Diversity breakdown (recipe 25)

| Model | Pass | Avg TPS | Tokens |
|-------|------|---------|--------|
| llama3 | 1/1 | 18.5 | 4 |
| llama3.1 | 1/1 | 0.1 | 4 |
| mistral | 1/1 | 2.0 | 4 |
| mathstral | 1/1 | 3.7 | 4 |
| qwen3 | 1/1 | 0.3 | 20 |
| qwen2.5 | 1/1 | 0.1 | 4 |
| qwen2.5-coder | 1/1 | 1.6 | 4 |
| qwen2-math | 1/1 | 0.3 | 5 |
| deepseek-r1 | 1/1 | 0.5 | 20 |
| deepseek-coder-v2 | 1/1 | 0.3 | 4 |
| gemma3 | 1/1 | 0.0 | 4 |
| phi4 | 1/1 | 0.1 | 4 |
| phi4-mini | 1/1 | 0.2 | 4 |
| phi3 | 1/1 | 1.1 | 20 |

### Comparison vs first run (2026-03-13)

| Metric | Run 1 (2026-03-13) | Run 2 (2026-03-14) |
|--------|--------------------|--------------------|
| Pass rate | 21/22 (95.5%) | **22/22 (100%)** |
| Total time | 1321.5s | 1425.1s |
| Recipe 05 | FAILED | **OK (8.2s)** |
| Stress test tasks | 5 | **15** |
| Throughput tasks | 30 | **60** |
| Model Diversity | 578.6s | **500.0s** (cache warm) |

Total time is higher because stress test and throughput now run 3× more tasks — the per-task rate is faster.

### What the fixes delivered

| Fix | Evidence |
|-----|---------|
| TierFromVRAM Gold/Silver swap | Agent now joins as GOLD (11 GB VRAM) |
| Pulse sends GPU metrics | `gpu_utilization_pct` and `vram_used_gb` in pulse_log |
| Recipe 05 SPL→Go | RAG on Grid: FAILED → OK (8.2s) |
| checkAnswer normalization | Math Olympiad scores improved |

---

## 3-GPU LAN Run — Planned

See `lan-test-03.md` when ready. Expected total time: ~500s (3× parallelism on recipes 07, 13, 21).

---

## Troubleshooting

**Agent on remote machine can't reach hub:**
```bash
sudo ufw allow 9000/tcp
```

**Hub can't push tasks to remote agent:**
```bash
mg join --pull http://192.168.0.177:9000
```

**Agent joins as BRONZE despite large VRAM:**
Rebuild `mg` after `git pull` — the `TierFromVRAM` fix is in the latest commit.
