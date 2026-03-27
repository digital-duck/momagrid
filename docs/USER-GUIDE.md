# MomaGrid User Guide
<!-- Version: 2026-03-17 -->

---

## Overview

MomaGrid is a distributed AI inference grid. The `mg` binary is a single
self-contained executable that runs both the hub (coordinator) and agent nodes
(workers). No Python, no Docker, no runtime dependencies required.

**Architecture for a 2-GPU LAN grid:**

```
[Machine A: Hub + Agent]              [Machine B: Agent]
  mg hub up  (port 9000)              mg join --hub http://A:9000
  mg join    (local agent)            Ollama running locally
  Ollama running locally
```

> **Terminology note:** In this codebase, *agent* means **worker node** — a
> compute node that executes inference chunks. The academic paper uses "worker
> node"; the implementation API uses "agent" throughout.

Tasks submitted to the hub are dispatched to whichever worker node (agent) has
the right model, tier, and available VRAM. If no local agent matches, the hub forwards
the task to a peered hub.

---

## LAN Experiments Quick Start (arXiv Paper)

*If you are here to run the LAN benchmark experiments for the GoSpark paper,
follow this sequence. Everything else in this guide is reference.*

```bash
# ── Step 1: Both machines — start Ollama and pull the model ──
# ollama serve
ollama run llama3  # test ollama

# hub IP
hostname -I
# 192.168.0.177

# ── Step 2: Machine A — start hub and join local GPU ──
mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"  --port 9000
# mg hub up --host 0.0.0.0 --port 9000 --hub-url http://192.168.0.177:9000

mg status --hub-url http://localhost:9000

mg join --hub http://localhost:9000 --name gpu-a --host 0.0.0.0 --port 9000

# ── Step 3: Machine B — join the hub ──
mg join --hub http://192.168.0.177:9000 --name gpu-b --host 0.0.0.0 --port 9000

# ── Step 4: Verify both agents online ──
mg agents

# ── Step 5: Smoke test ──
mg submit "Explain transformer attention in one sentence"

mg run ./cookbook/01_single_node_hello/hello.spl

# ── Step 6: Recipe 13 — throughput benchmark (Section 7.1) ──
go run cookbook/13_multi_agent_throughput/throughput.go \
  --hub http://localhost:9000 --n 10 --model llama3 | tee results/throughput-1agent.txt

  

# ── Step 7: Recipe 90 — two-hub cluster (Section 7.3) ──
go run cookbook/90_two_hub_cluster/cluster.go full \
  --hub-a http://192.168.0.177:9000 \
  --hub-b http://192.168.1.11:9000

# ── Step 8: Recipe 15 — failover resilience (Section 7.4) ──
go run cookbook/15_agent_failover/failover.go \
  --hub http://localhost:9000 --n 20

# ── Step 9: Collect reward accounting ──
mg rewards
```

Record outputs from Steps 6–9 for Sections 7.1–7.5 of the paper.

---

## Prerequisites

### Every Machine
- `mg` binary — copy from repo root, or build: `go build -o mg ./cmd/mg`
- [Ollama](https://ollama.ai) running: `ollama serve`
- At least one model pulled: `ollama pull llama3`

### Network
- Both machines on the same LAN
- Port 9000 (hub) open between machines
- Agent port 9000 open if hub needs to push tasks to agents
- No internet required for LAN operation

---

## Quick Start: Single Node

```bash
# Terminal 1 — start the hub
mg hub up --port 9000

# Terminal 2 — join as a local agent
mg join --hub http://localhost:9000 --name my-gpu --port 9000

# Terminal 3 — submit a task
mg submit "Explain transformer attention in one sentence"

# Check status
mg status
mg agents
mg tasks
```

---

## LAN Grid: 2-Machine Setup

### Machine A (Hub + Agent)

```bash
# 1. Start hub — bind to 0.0.0.0 so Machine B can reach it
mg hub up \
  --host 0.0.0.0 \
  --port 9000 \
  --hub-url http://192.168.0.177:9000    # replace with your LAN IP

# 2. Join Machine A's own GPU (separate terminal)
mg join \
  --hub http://localhost:9000 \
  --name gpu-a \
  --host 0.0.0.0 \
  --port 9000
```

### Machine B (Agent only)

```bash
mg join \
  --hub http://192.168.0.177:9000 \
  --name gpu-b \
  --host 0.0.0.0 \
  --port 9000
```

### Verify the Grid (from Machine A)

```bash
mg status
mg agents
```

Expected output:
```
Hub: ONLINE | Agents: 2

NAME    AGENT_ID   TIER    STATUS  TPS
gpu-a   a1b2c3d4   SILVER  ONLINE  12.4
gpu-b   e5f6g7h8   SILVER  ONLINE  11.8
```

---

## Submitting Tasks

```bash
# Simple prompt
mg submit "What is the capital of France?"

# Specify model
mg submit "Summarize the Attention is All You Need paper" --model llama3

# Require minimum compute tier
mg submit "Write a sonnet" --min-tier SILVER

# With custom timeout (seconds)
mg submit "Explain quantum entanglement" --timeout 90
```

Each response includes the **serving agent identity** and **completion timestamp**:

```
Paris is the capital of France.

[model=llama3 tokens=17+53 latency=2638ms agent=ducklover1 completed=2026-03-16T14:30:25Z]
```

- `agent=` — the agent's configured name, hostname, or ID (first non-empty)
- `completed=` — UTC timestamp when the task was written back to the hub database

---

## Running SPL Scripts

SPL (Structured Prompt Language) `WITH` blocks map to parallel CTEs — each
dispatched to a separate agent concurrently.

```bash
mg run my_script.spl --hub http://localhost:9000
```

Example SPL script (`pipeline.spl`):
```sql
WITH summary AS (
    PROMPT 'Summarize this abstract: ...'
    USING MODEL 'llama3'
),
critique AS (
    PROMPT 'List 3 weaknesses: ...'
    USING MODEL 'llama3'
)
SELECT summary.content, critique.content
FROM summary, critique;
```

The hub dispatches `summary` and `critique` to two agents in parallel. The
`SELECT` assembles the results. If only one agent is available, CTEs run
sequentially — the SPL is unchanged either way.

---

## Cookbook Recipes

```bash
# Print full catalog with active/inactive status
go run cookbook/run_all.go --catalog

# Run all active recipes (batch)
go run cookbook/run_all.go

# Run specific recipes by ID
go run cookbook/run_all.go --ids 04,13,16

# Throughput benchmark — 30 tasks
go run cookbook/13_multi_agent_throughput/throughput.go \
  --hub http://localhost:9000 --n 30 --model llama3

# Failover stress test — kill an agent mid-run
go run cookbook/15_agent_failover/failover.go \
  --hub http://localhost:9000 --n 20

# Model fingerprinting — cross-agent consistency (NEW)
go run cookbook/29_model_fingerprinting/model_fingerprinting.go \
  --hub http://localhost:9000 --runs 3

# Academic paper pipeline — 4-stage parallel workflow (NEW)
go run cookbook/30_academic_paper_pipeline/academic_pipeline.go \
  --hub http://localhost:9000

# Junior dev assistant — code review + refactoring + docs (NEW)
go run cookbook/34_junior_developer_assistant/junior_dev_assistant.go \
  --hub http://localhost:9000

# Two-hub cluster demo (requires 2 hubs on LAN)
go run cookbook/90_two_hub_cluster/cluster.go full \
  --hub-a http://192.168.0.177:9000 \
  --hub-b http://192.168.1.11:9000
```

### Active vs Inactive Recipes

Recipes are marked `isActive` in `cookbook/run_all.go`. The batch runner skips
inactive recipes by default — they are pending integration testing. To run any
inactive recipe, pass its ID explicitly:

```bash
go run cookbook/run_all.go --ids 29   # runs model fingerprinting even though inactive
```

---

## mgui — Web UI

`mgui` is an optional web-based front-end that provides:
- **Chat** — unified chat across OpenAI, Anthropic, Google, OpenRouter, and Momagrid
- **Join Grid** — one-click GPU node registration with live SSE progress
- **Grid Status** — live agents table, recent tasks, rewards ledger

Default port: **9080**.

```bash
# Build
go build -buildvcs=false -o mgui ./cmd/mgui

# Run — mgui serves on :9080, proxies to the hub at :9000
./mgui --hub http://localhost:9000 --port 9080

# Open browser
open http://localhost:9080
```

The **Join Grid** tab auto-detects your local GPU and Ollama installation.
Click **Register** to walk through a 6-step SSE-streamed wizard:

```
Step 1  Detecting hardware...        ✅  GTX 1080 Ti · 11 GB · 14 models
Step 2  Connecting to hub...         ✅  hub-4cafdc78 at 192.168.0.177:9000
Step 3  Loading Ed25519 identity...  ✅  ~/.igrid/agent_key.pem
Step 4  Registering with hub...      ✅  agent accepted
Step 5  Waiting for ONLINE status... ✅  tier=GOLD · ONLINE
Step 6  Starting heartbeat loop...   ✅  pulse every 30s
```

The tab is hidden if no GPU or Ollama is detected on the local machine.

---

## Two-Hub Cluster (LAN)

Run a hub on each machine, then peer them:

**Machine A:**
```bash
mg hub up --host 0.0.0.0 --port 9000 --hub-url http://192.168.0.177:9000
```

**Machine B:**
```bash
mg hub up --host 0.0.0.0 --port 9000 --hub-url http://192.168.1.11:9000
```

**Peer them (from Machine A):**
```bash
mg peer add http://192.168.1.11:9000 --hub http://192.168.0.177:9000
```

Or run the full cluster demo:
```bash
go run cookbook/90_two_hub_cluster/cluster.go full \
  --hub-a http://192.168.0.177:9000 \
  --hub-b http://192.168.1.11:9000
```

Once peered, tasks that cannot be dispatched locally are automatically
forwarded to the peer hub. Capability sync runs every 60 seconds.

---

## Hub Flags Reference

```
mg hub up
  --host              Bind address (use 0.0.0.0 for LAN)     default: localhost
  --port              Hub port                                 default: 9000
  --hub-url           Public URL for peer callbacks           default: (empty)
  --db                SQLite database path                    default: .igrid/hub.sqlite3
  --operator-id       Your operator ID                        default: duck
  --api-key           Require key on /join (optional)
  --admin             Require agent verification before approval
  --max-concurrent    Max in-flight tasks per agent           default: 3
  --max-prompt-chars  Prompt size limit                       default: 50000
  --max-queue-depth   Task queue depth limit                  default: 1000
  --rate-limit        Requests/min per IP before throttle     default: 60
  --burst-threshold   Requests/10s before auto-suspend        default: 200
```

---

## Agent Join Flags Reference

```
mg join
  --hub       Hub URL (required)
  --name      Display name for this agent
  --host      Bind address (use 0.0.0.0 for LAN visibility)  default: localhost
  --port      Agent port                                       default: 9000
  --models    Comma-separated models to advertise             default: auto-detect
  --pull      Pull mode via SSE (use behind NAT/firewall)
```

---

## Compute Tiers

Agents are auto-assigned a tier based on measured tokens/sec (TPS):

| Tier | TPS | Example GPU |
|------|-----|-------------|
| PLATINUM | ≥ 30 | RTX 4090, A100 |
| GOLD | ≥ 20 | RTX 3090, RTX 3080 |
| SILVER | ≥ 10 | GTX 1080 Ti, RTX 2070 |
| BRONZE | < 10 | GTX 1060, older GPUs |

---

## Monitoring Commands

```bash
mg status                  # hub health + online agent count
mg agents                  # agent table: name, tier, status, TPS
mg tasks                   # recent task list
mg tasks -d                # tasks with full result detail
mg rewards                 # credit summary by operator
mg logs                    # agent pulse history
mg watchlist               # rate-limited / blocked IPs
mg unblock <entity_id>     # remove from watchlist

# Agent approval (when --admin is enabled)
mg hub pending             # agents awaiting approval
mg hub approve <agent_id>
mg hub reject <agent_id>
```

---

## LAN Experiment Checklist (for arXiv paper)

- [ ] Both machines: `mg` binary built, `ollama serve` running
- [ ] Same model on both machines: `ollama pull llama3`
- [ ] Hub A started with `--host 0.0.0.0 --hub-url http://<LAN-IP>:9000`
- [ ] Both agents show ONLINE: `mg agents`
- [ ] Smoke test: `mg submit "hello"`
- [ ] Throughput benchmark: recipe 13 (vary N and concurrency)
- [ ] Two-hub cluster: recipe 90 (`full` command)
- [ ] Failover test: recipe 15 (kill one agent mid-run)
- [ ] Reward accounting: `mg rewards` (capture token counts)

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Agent stuck PENDING_APPROVAL | `--admin` on hub | `mg hub approve <id>` |
| Task stuck in PENDING | No online agents | Check `mg agents`; ensure Ollama is running |
| Machine B cannot reach hub | Wrong bind address | Use `--host 0.0.0.0` on hub, not `localhost` |
| Agent cannot reach hub | Firewall | Open port 9000 on Machine A |
| Task FAILED: "no agents" | Model mismatch | Ensure agent advertises the required model |
| Pull-mode agent not getting tasks | SSE dropped | Restart agent; check hub logs |
| Hub-to-hub forward fails | --hub-url not set | Set `--hub-url http://<public-IP>:9000` on both hubs |

---

## Database

All hub state is in a SQLite file (default `.igrid/hub.sqlite3`):

```bash
# Quick inspection
sqlite3 .igrid/hub.sqlite3 "SELECT name, status, tier, current_tps FROM agents;"
sqlite3 .igrid/hub.sqlite3 "SELECT task_id, state, latency_ms FROM tasks ORDER BY created_at DESC LIMIT 10;"
sqlite3 .igrid/hub.sqlite3 "SELECT * FROM reward_summary;"

# Migrate to PostgreSQL (production)
mg migrate --from .igrid/hub.sqlite3 --to postgres://user:pass@host/dbname
```

---

*momagrid — Apache 2.0 | 2026-03-17*
