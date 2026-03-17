# Momagrid Hub-and-Spoke Distributed AI Inference Network — Specification

> **Single source of truth** — `specs/momagrid_spec.md`
> Go implementation: `github.com/digital-duck/momahub.go`
> Date: 2026-03-14

---

## 1. System Overview

> **Terminology note:** Throughout this codebase, the term *agent* refers to a
> **worker node** — a distributed compute node that executes inference tasks.
> In the academic literature (GoSpark paper), we use "worker node" to avoid
> confusion with the AI-agent paradigm. In the implementation, "agent" is the
> stable API term used in all routes, CLI commands, and database schema.

### Momagrid vs Momahub

| | Momahub | Momagrid |
|-|---------|----------|
| **Language** | Python | Go |
| **Status** | Prototype — fully functional | Production |
| **CLI binary** | `moma` | `mg` |
| **Web UI** | `moma-ui` (Streamlit) | `mgui` (embedded Go web server) |
| **Security** | Basic | Ed25519 identity, signed join/pulse |
| **Database** | SQLite | SQLite + PostgreSQL (driver-agnostic) |
| **Job queue** | Task-based | Task-based + async Job Queue |
| **Repo** | `github.com/digital-duck/momahub.py` | `github.com/digital-duck/momahub.go` |

**Momahub** (Python) is the original prototype — the proof of concept that validated the hub-and-spoke architecture, SPL execution, agent join/pulse protocol, and reward ledger. It is fully functional and remains the reference for the Python ecosystem.

**Momagrid** (Go) is the production implementation built on the lessons of Momahub. It adds enterprise-grade security (Ed25519 keypairs, signed messages), robust system engineering (WAL-mode SQLite, PostgreSQL support, concurrent dispatch), an async job queue for long-running workloads, and `mgui` — a unified web UI for agent registration and multi-provider inference.

Both are open-source and community-supported. The spec below describes **Momagrid**.

---

Momagrid is a distributed AI inference network with a **hub-and-spoke architecture**:
- **Hub**: Master node — manages worker nodes (agents), dispatches tasks, and maintains cluster state
- **Agents (worker nodes)**: Distributed inference nodes running Ollama that execute tasks
- **Pull Mode**: WAN-safe Server-Sent Events (SSE) for agent task consumption
- **Push Mode**: Hub initiates HTTP POST to agent `/run` endpoint
- **Clustering**: Multiple hubs can peer and forward tasks to each other

### Open-Source, Cooperative Model

Momagrid (and its Python prototype Momahub) are fully open-source. This is not just a distribution choice — it is the foundation of the network's value. Because the software is open:

- **Any individual, research lab, enterprise, or industry consortium** — including cloud providers such as AWS or Google — is welcome to run their own Momagrid instance
- **Federated by design**: independent hubs can peer with each other across organizations, forming a larger cooperative grid without any central authority
- **No gatekeeping**: there is no license fee, no approval process, and no vendor dependency for running a hub or contributing an agent
- **Community-governed**: improvements contributed by any participant — whether an individual GPU owner or a Fortune 500 company — flow back to the shared codebase under the open-source license

The goal is a global, cooperative inference commons. Corporate deployments running their own instances do not compete with the community network — they strengthen the ecosystem and validate the architecture.

---

## 2. HTTP API Routes

### 2.1 Health & Status

#### GET `/health`
**Response**: `{"hub_id": str, "operator_id": str, "status": "ok", "agents_online": int, "time": str}`

#### GET `/tasks?limit=50`
**Response**: `{"tasks": [...]}`
- Lists recent tasks; includes `agent_name` via LEFT JOIN with agents

#### GET `/tasks/{task_id}`
**Response**: `TaskStatusResponse`
```json
{
  "task_id": "str",
  "state": "PENDING|DISPATCHED|IN_FLIGHT|COMPLETE|FAILED|FORWARDED",
  "result": { ...TaskResult... }
}
```

#### GET `/agents`
**Response**: `{"agents": [agent_dicts]}`
- Lists all non-OFFLINE agents sorted by tier

#### GET `/agents/pending`
**Response**: `{"agents": [agent_dicts]}`
- Admin mode only: agents awaiting approval

#### GET `/logs?limit=50`
**Response**: `{"logs": [pulse_log_dicts]}`

#### GET `/rewards`
**Response**: `{"summary": [reward_summary_dicts]}`

#### GET `/cluster/status`
**Response**:
```json
{
  "this_hub_id": "str",
  "peers": [{"hub_id": str, "hub_url": str, "operator_id": str, "status": str, "added_at": str, "last_seen": str}]
}
```

#### GET `/watchlist`
**Response**: `{"entries": [watchlist_dicts]}`

---

### 2.2 Agent Join/Leave

#### POST `/join`
**Request**: `JoinRequest`
```json
{
  "operator_id": "str",
  "agent_id": "str",
  "host": "str",
  "port": 8100,
  "name": "str",
  "gpus": [{"index": 0, "model": "RTX 3090", "vram_gb": 24.0}],
  "cpu_cores": 16,
  "ram_gb": 64.0,
  "supported_models": ["llama3:latest"],
  "cached_models": ["llama3:latest"],
  "max_concurrent": 3,
  "pull_mode": false,
  "api_key": ""
}
```
**Response**: `JoinAck`
```json
{
  "accepted": true,
  "hub_id": "str",
  "operator_id": "str",
  "agent_id": "str",
  "tier": "GOLD",
  "name": "str",
  "message": "str",
  "status": "ONLINE"
}
```
**Behavior**:
- Registers agent in operators & agents tables
- `admin_mode=True` → goes to PENDING_APPROVAL, spawns auto-verification
- `admin_mode=False` → goes directly to ONLINE
- Re-join in admin_mode preserves ONLINE status if previously approved
- API key validated; rate limiting enforced; watchlist checked
- HTTP 429 if rate limited or watchlisted; HTTP 401 if API key invalid

#### POST `/leave`
**Request**: `{"operator_id": str, "agent_id": str}`
**Response**: `{"ok": bool}`
- Sets agent OFFLINE; re-queues all DISPATCHED/IN_FLIGHT tasks to PENDING

---

### 2.3 Heartbeat

#### POST `/pulse`
**Request**: `PulseReport`
```json
{
  "operator_id": "str",
  "agent_id": "str",
  "status": "ONLINE",
  "gpu_utilization_pct": 75.5,
  "vram_used_gb": 8.2,
  "tasks_completed": 142,
  "current_tps": 45.3
}
```
**Response**: `{"ok": bool, "hub_time": "ISO8601"}`
- Updates tier via `tier_from_tps(current_tps)` if tps > 0
- Inserts pulse_log entry
- Resets eviction timer (last_pulse = now)

---

### 2.4 Task Submission

#### POST `/tasks`
**Request**: `TaskRequest`
```json
{
  "task_id": "",
  "model": "llama3",
  "prompt": "...",
  "system": "",
  "max_tokens": 1024,
  "temperature": 0.7,
  "min_tier": "BRONZE",
  "min_vram_gb": 0.0,
  "timeout_s": 300,
  "priority": 1
}
```
**Response** (202): `{"task_id": str, "state": "PENDING"}`
- `task_id` auto-generated if empty
- HTTP 413 if prompt > `max_prompt_chars` (default 50K soft, 200K hard ceiling)
- HTTP 503 if queue depth > `max_queue_depth` (default 1000)

---

### 2.5 Pull Mode (SSE)

#### GET `/task-stream/{agent_id}`
**Protocol**: `text/event-stream`
- `data: {TaskRequest JSON}\n\n` — task dispatched to this agent
- `: keepalive\n\n` — every 15s when idle
- Creates async queue per agent_id; auto-closes on disconnect

#### POST `/results`
**Request**: `TaskResult`
**Response**: `{"ok": bool, "detail": str}`
- Pull-mode agents POST results here
- Idempotent: skips if already COMPLETE/FAILED
- Records reward ledger entry if output_tokens > 0

---

### 2.6 Agent Management (Admin)

#### POST `/agents/{agent_id}/approve`
**Response**: `{"ok": bool, "agent_id": str, "status": "ONLINE"}`

#### POST `/agents/{agent_id}/reject`
**Response**: `{"ok": bool, "agent_id": str, "status": "OFFLINE"}`

---

### 2.7 Cluster/Peering

#### POST `/cluster/handshake`
**Request**: `PeerHandshake`
```json
{"hub_id": str, "hub_url": str, "operator_id": str, "capabilities": [...]}
```
**Response**: `PeerHandshakeAck`

#### POST `/cluster/capabilities`
**Request**: `PeerCapabilityUpdate`
**Response**: `{"ok": bool}`
- Updates last_seen for peer

#### POST `/cluster/peers`
**Request**: `{"url": str}`
**Response**: `PeerHandshakeAck`
- Client-initiated peer connection

---

### 2.8 DoS / Watchlist

#### DELETE `/watchlist/{entity_id}`
**Response**: `{"ok": bool}`
- Removes entity (IP, operator, agent) from watchlist; resets rate limiter

---

## 3. Database Schema

SQLite with WAL mode (`PRAGMA journal_mode=WAL`).

### hub_config
| Column | Type | Notes |
|--------|------|-------|
| key | TEXT PK | |
| value | TEXT | hub_id, operator_id, hub_url |

### peer_hubs
| Column | Type | Default |
|--------|------|---------|
| hub_id | TEXT PK | |
| hub_url | TEXT | |
| operator_id | TEXT | '' |
| status | TEXT | 'ACTIVE' (ACTIVE\|UNREACHABLE) |
| added_at | TEXT | datetime('now') |
| last_seen | TEXT | NULL |

### operators
| Column | Type | Default |
|--------|------|---------|
| operator_id | TEXT PK | |
| joined_at | TEXT | datetime('now') |
| total_tasks | INTEGER | 0 |
| total_tokens | INTEGER | 0 |
| total_credits | REAL | 0.0 |

### agents
| Column | Type | Default | Notes |
|--------|------|---------|-------|
| agent_id | TEXT PK | | |
| operator_id | TEXT | | FK: operators |
| name | TEXT | '' | |
| host | TEXT | | |
| port | INTEGER | | |
| status | TEXT | 'ONLINE' | ONLINE\|BUSY\|OFFLINE\|PENDING_APPROVAL |
| tier | TEXT | 'BRONZE' | PLATINUM\|GOLD\|SILVER\|BRONZE |
| gpus | TEXT | '[]' | JSON: [{index, model, vram_gb}] |
| cpu_cores | INTEGER | 0 | |
| ram_gb | REAL | 0.0 | |
| supported_models | TEXT | '[]' | JSON array |
| max_concurrent | INTEGER | 3 | |
| current_tps | REAL | 0.0 | |
| tasks_completed | INTEGER | 0 | |
| pull_mode | INTEGER | 0 | boolean |
| joined_at | TEXT | datetime('now') | |
| last_pulse | TEXT | NULL | |

Index: `idx_agents_state` on (status)

### tasks
| Column | Type | Default | Notes |
|--------|------|---------|-------|
| task_id | TEXT PK | | |
| state | TEXT | 'PENDING' | PENDING\|DISPATCHED\|IN_FLIGHT\|COMPLETE\|FAILED\|FORWARDED |
| model | TEXT | | |
| prompt | TEXT | | |
| system | TEXT | '' | |
| max_tokens | INTEGER | 1024 | |
| temperature | REAL | 0.7 | |
| min_tier | TEXT | 'BRONZE' | |
| min_vram_gb | REAL | 0.0 | |
| timeout_s | INTEGER | 300 | |
| priority | INTEGER | 1 | |
| agent_id | TEXT | NULL | FK: agents |
| peer_hub_id | TEXT | NULL | FK: peer_hubs (for forwarded tasks) |
| content | TEXT | NULL | LLM response |
| input_tokens | INTEGER | 0 | |
| output_tokens | INTEGER | 0 | |
| latency_ms | REAL | 0.0 | |
| retries | INTEGER | 0 | incremented on failure |
| error | TEXT | NULL | |
| created_at | TEXT | datetime('now') | |
| updated_at | TEXT | datetime('now') | |

Index: `idx_tasks_state` on (state)

### pulse_log
| Column | Type | Default |
|--------|------|---------|
| id | INTEGER PK AUTOINCREMENT | |
| agent_id | TEXT | |
| status | TEXT | |
| gpu_util_pct | REAL | 0.0 |
| vram_used_gb | REAL | 0.0 |
| current_tps | REAL | 0.0 |
| tasks_completed | INTEGER | 0 |
| logged_at | TEXT | datetime('now') |

### reward_ledger
| Column | Type | Default |
|--------|------|---------|
| id | INTEGER PK AUTOINCREMENT | |
| operator_id | TEXT | |
| agent_id | TEXT | |
| task_id | TEXT | |
| tokens_generated | INTEGER | 0 |
| credits_earned | REAL | 0.0 |
| recorded_at | TEXT | datetime('now') |

### reward_summary (VIEW)
```sql
SELECT operator_id, COUNT(*) AS total_tasks,
       SUM(tokens_generated) AS total_tokens,
       SUM(credits_earned) AS total_credits
FROM reward_ledger GROUP BY operator_id;
```

### watchlist
| Column | Type | Notes |
|--------|------|-------|
| id | INTEGER PK AUTOINCREMENT | |
| entity_type | TEXT | 'operator'\|'agent'\|'ip' |
| entity_id | TEXT | |
| reason | TEXT | '' |
| action | TEXT | 'SUSPENDED' (SUSPENDED\|BLOCKED) |
| created_at | TEXT | datetime('now') |
| expires_at | TEXT | NULL = permanent |
| UNIQUE(entity_type, entity_id) | | |

---

## 4. Task State Machine

### States
- **PENDING** — submitted, awaiting dispatch
- **DISPATCHED** — claimed by dispatcher, awaiting agent
- **IN_FLIGHT** — sent to agent, awaiting result
- **COMPLETE** — finished successfully
- **FAILED** — failed after MAX_RETRIES=3
- **FORWARDED** — forwarded to a peer hub

### Transitions
```
PENDING → DISPATCHED → IN_FLIGHT → COMPLETE
                                 → FAILED (if retries >= 3)
                                 → PENDING (retry if retries < 3)
PENDING → FORWARDED (no local agent, peer available)
```

### Retry Logic
- **MAX_RETRIES**: 3 total attempts
- **Trigger**: Agent returns FAILED or HTTP exception during delivery
- **On retry**: Increment `retries`, set state=PENDING, clear agent_id
- **Final failure**: After 3rd, state=FAILED, store error message

### Eviction
- **Timeout**: 90 seconds since last_pulse
- **Trigger**: Agent monitor runs every 30s
- **Action**: Agent → OFFLINE; all DISPATCHED/IN_FLIGHT tasks → PENDING

---

## 4b. Async Job Queue (Long-Running Workloads)

Tasks (section 4) are designed for interactive inference — typically seconds to a few minutes. The **Job Queue** extends this for long-running workloads (batch processing, overnight jobs, multi-step pipelines) where the user should not need to keep a connection open.

### Job vs Task

| | Task | Job |
|-|------|-----|
| **Timeout** | Up to `timeout_s` (default 300s) | Hours or unbounded |
| **Interaction** | Caller polls `GET /tasks/{id}` | Fire-and-forget; notified on completion |
| **Priority** | Numeric priority field | Queue position + deadline scheduling |
| **Progress** | Final state only | Progress updates via SSE or webhook |
| **Retry** | Up to 3 retries | Configurable max retries + backoff |

### Job Submission

```http
POST /jobs
```
```json
{
  "job_id": "",
  "model": "llama3",
  "prompt": "...",
  "system": "",
  "max_tokens": 4096,
  "min_tier": "SILVER",
  "deadline": "2026-03-15T06:00:00Z",
  "notify": {
    "webhook_url": "https://example.com/callback",
    "email": "user@example.com"
  }
}
```

**Response** (202): `{"job_id": str, "state": "QUEUED", "position": int}`

### Job States

```
QUEUED → DISPATCHED → IN_FLIGHT → COMPLETE
                               → FAILED (max retries exceeded)
                               → EXPIRED (deadline passed while QUEUED)
                               → CANCELLED (user-initiated)
```

### Notification Mechanisms

| Method | Trigger | Payload |
|--------|---------|---------|
| **Webhook** | On COMPLETE or FAILED | `{job_id, state, result, elapsed_s}` POST to `notify.webhook_url` |
| **Email** | On COMPLETE or FAILED | Summary email to `notify.email` (requires hub SMTP config) |
| **SSE polling** | Any state change | `GET /jobs/{job_id}/stream` — `text/event-stream` |
| **Poll** | On demand | `GET /jobs/{job_id}` — same shape as task status |

### Progress Updates

Long-running jobs emit progress events via SSE:
```
data: {"job_id": "...", "state": "IN_FLIGHT", "tokens_so_far": 312, "elapsed_s": 45.2}
data: {"job_id": "...", "state": "COMPLETE",  "tokens_so_far": 891, "elapsed_s": 124.7}
```

### Hub SMTP Configuration (for email notification)

```yaml
# ~/.igrid/config.yaml
notifications:
  smtp_host: "smtp.gmail.com"
  smtp_port: 587
  smtp_user: "hub@example.com"
  smtp_password: ""   # use env var IGRID_SMTP_PASSWORD
  from_address: "Momagrid Hub <hub@example.com>"
```

### mgui Job Dashboard

The `mgui` **Grid Status** tab includes a **Jobs** sub-panel:
- Job queue depth and estimated wait time
- Per-job: status, model, progress bar (tokens generated), elapsed time
- Cancel button for QUEUED/IN_FLIGHT jobs
- Re-run button for FAILED/EXPIRED jobs
- Notification settings per job

---

## 5. Dispatcher Logic

### pick_agent() Algorithm
1. Filter candidates:
   - Skip OFFLINE or PENDING_APPROVAL
   - Skip if tier worse than `req.min_tier` (PLATINUM > GOLD > SILVER > BRONZE)
   - Skip if primary GPU VRAM < `req.min_vram_gb`
   - Skip if `supported_models` non-empty and `req.model` (normalized, no `:latest`) not in list
   - Skip if active tasks >= agent's `max_concurrent` (DISPATCHED + IN_FLIGHT count)
2. Sort: ONLINE first → best tier → least loaded (fewest active tasks)
3. Return first (best) candidate

**Model normalization**: `llama3:latest` → `llama3`

### Dispatch Loop
- **Interval**: 2.0 seconds
- **Batch**: Up to 50 PENDING tasks, ORDER BY priority DESC, created_at ASC
- Per task: `pick_agent()` → `claim_task()` (atomic UPDATE RETURNING) → spawn delivery

### Delivery

**Push mode** (pull_mode=False):
- HTTP POST `http://{host}:{port}/run`
- Timeout: `timeout_s + 10` seconds
- "Agent at capacity" → fail without retry
- Other exception → fail with retry

**Pull mode** (pull_mode=True):
- Check agent's SSE queue in `app.state.sse_queues`
- Put TaskRequest on queue; agent receives via SSE, POSTs result to `/results`

---

## 6. Cluster / Peer Logic

### Handshake
1. Client hub calls `add_peer(peer_url)` via `POST /cluster/peers`
2. Hub-to-hub: POSTs `PeerHandshake` to `{peer_url}/cluster/handshake`
3. Server stores peer in peer_hubs (status=ACTIVE), returns PeerHandshakeAck with its capabilities

### Capability Sync
- **Interval**: 60 seconds
- Each hub POSTs its current capabilities to all peers via `POST /cluster/capabilities`

### Task Forwarding
- **Trigger**: cluster_monitor every 60s checks for PENDING tasks with no local eligible agent
- Batch: up to 20 tasks
- `forward_task()`: POST TaskRequest to `{peer_url}/tasks`, poll peer every 2s (exp backoff to 10s)
- On peer completion: complete task locally, mark FORWARDED

---

## 7. Agent Join/Pulse Protocol

### JoinRequest Fields
| Field | Type | Required | Validation |
|-------|------|----------|------------|
| operator_id | str | yes | max 256 |
| agent_id | str | yes | max 256 |
| host | str | yes | max 256 |
| port | int | yes | 1-65535 |
| name | str | no | default '' |
| gpus | list[GPUInfo] | no | default [], max 32 |
| cpu_cores | int | no | 0-4096 |
| ram_gb | float | no | 0.0-65536.0 |
| supported_models | list[str] | no | default [], max 200 |
| cached_models | list[str] | no | default [], max 200 |
| max_concurrent | int | no | 1-128, default 3 |
| pull_mode | bool | no | default False |
| api_key | str | no | max 512 |

### JoinAck Fields
| Field | Type | Notes |
|-------|------|-------|
| accepted | bool | |
| hub_id | str | |
| operator_id | str | |
| agent_id | str | |
| tier | ComputeTier | assigned by hub |
| name | str | |
| message | str | |
| status | str | ONLINE or PENDING_APPROVAL |

### Agent Verification (admin_mode=True)
1. Agent joins → PENDING_APPROVAL
2. Hub spawns `_verify_agent()` background task
3. Sends 1 of 8 benchmark prompts (diverse topics, 120s timeout)
4. Checks: non-empty response, output_tokens > 0, latency < 120s
5. Optional: Geo-IP check against `allowed_countries`
6. Optional: 10% random sampling for manual review
7. Pass → auto-approve (ONLINE); sample or fail → stays PENDING_APPROVAL

### PulseReport Fields
| Field | Type | Default |
|-------|------|---------|
| operator_id | str | required |
| agent_id | str | required |
| status | AgentStatus | required |
| gpu_utilization_pct | float | 0.0 |
| vram_used_gb | float | 0.0 |
| tasks_completed | int | 0 |
| current_tps | float | 0.0 |

**Interval**: 30 seconds

---

## 8. CLI Commands

### Hub Commands
| Command | Key Flags | Behavior |
|---------|-----------|----------|
| `mg hub up` | --host, --port, --hub-url, --db, --operator-id, --api-key, --admin, --max-concurrent, --max-prompt-chars, --max-queue-depth, --rate-limit, --burst-threshold | Start hub server |
| `mg hub down` | | Help message (use Ctrl+C) |
| `mg hub pending` | --hub-url | List agents awaiting approval |
| `mg hub approve <agent_id>` | --hub-url | Approve agent |
| `mg hub reject <agent_id>` | --hub-url | Reject/ban agent |
| `mg hub migrate` | --db, --pg-url | Migrate SQLite → PostgreSQL |

### Agent Commands
| Command | Key Flags | Behavior |
|---------|-----------|----------|
| `mg join <hub_url>` | --operator-id, --name, --model, --models, --ollama, --pull, --sign, --api-key, --pulse | Register as agent, start pulse loop |
| `mg agent up` | same as join | Start agent server + join |
| `mg down` | --agent-id, --hub-url | Leave grid |

### Grid Commands
| Command | Key Flags | Behavior |
|---------|-----------|----------|
| `mg status` | --hub-url | Hub health + online count |
| `mg agents` | --hub-url | Table: NAME, AGENT_ID, TIER, STATUS, TPS |
| `mg tasks [--detail]` | --hub-url, --limit, -d | Recent tasks; -d shows full detail |
| `mg logs [--follow]` | --hub-url, -f, --interval, --limit | Pulse log entries |
| `mg submit "<prompt>"` | --hub-url, --model, --max-tokens, --wait | Submit task, optionally poll |
| `mg rewards` | --hub-url | Table: OPERATOR, TASKS, TOKENS, CREDITS |
| `mg watchlist` | --hub-url | List watchlist entries |
| `mg unblock <entity_id>` | --hub-url | Remove from watchlist |

### Cluster Commands
| Command | Key Flags | Behavior |
|---------|-----------|----------|
| `mg peer add <url>` | --hub-url | Add peer hub |
| `mg peer list` | --hub-url | List peer hubs with status |

### Utility Commands
| Command | Key Flags | Behavior |
|---------|-----------|----------|
| `mg run <spl_file>` | --hub-url, --params | Execute SPL file |
| `mg test` | --hub-url, --prompts, --category, --concurrency, --repeat, --timeout, --label, --output, --list | Benchmark |
| `mg export` | --hub-url, --output, --label, --limit | Export tasks to JSON |
| `mg config [--set key=value]` | | View/update ~/.igrid/config.yaml |

---

## 9. Schema Types

### Enums

#### ComputeTier
```
PLATINUM  >= 60 TPS
GOLD      >= 30 TPS
SILVER    >= 15 TPS
BRONZE     < 15 TPS
```

#### TaskState
`PENDING | DISPATCHED | IN_FLIGHT | FORWARDED | COMPLETE | FAILED`

#### AgentStatus
`ONLINE | BUSY | OFFLINE | PENDING_APPROVAL`

### Core Types

#### GPUInfo
```json
{"index": 0, "model": "RTX 3090", "vram_gb": 24.0}
```

#### TaskResult
| Field | Type | Default | Notes |
|-------|------|---------|-------|
| task_id | str | required | |
| state | TaskState | required | |
| content | str | '' | LLM response |
| model | str | '' | |
| input_tokens | int | 0 | |
| output_tokens | int | 0 | |
| latency_ms | float | 0.0 | Ollama inference time |
| agent_id | str | '' | UUID of the serving agent |
| agent_name | str | '' | Human-readable name from agents.name |
| agent_host | str | '' | Hostname of the serving node |
| completed_at | str | '' | ISO8601 timestamp (tasks.updated_at) |
| error | str | '' | |

**CLI display format** (shown after `mg submit` and `mg run`):
```
[model=llama3 tokens=17+53 latency=2638ms agent=ducklover1 completed=2026-03-16T14:30:25Z]
```

#### PeerCapability
```json
{"tier": "GOLD", "count": 3, "models": ["llama3"]}
```

#### RewardSummary
```json
{"operator_id": str, "total_tasks": int, "total_tokens": int, "total_credits": float}
```

---

## 10. Monitor & Eviction

### Agent Monitor
- **Interval**: Every 30s
- **Eviction timeout**: 90s since last_pulse
- **Action**: Agent → OFFLINE; DISPATCHED/IN_FLIGHT tasks → PENDING

### Cluster Monitor
- **Interval**: Every 60s
- **Actions**:
  1. Push local capabilities to all peers
  2. Forward up to 20 PENDING tasks (with no local eligible agent) to peers

---

## 11. Compute Tier Assignment

### TPS-based (updated on each pulse)
```
tps >= 60  →  PLATINUM
tps >= 30  →  GOLD
tps >= 15  →  SILVER
tps <  15  →  BRONZE
```

### VRAM-based (assigned at join time, before any TPS data)
```
vram >= 16 GB  →  PLATINUM
vram >= 10 GB  →  GOLD
vram >=  6 GB  →  SILVER
vram <   6 GB  →  BRONZE
```

- Initial tier at JOIN: derived from GPU VRAM via `TierFromVRAM()` (falls back to BRONZE if no GPU detected)
- Updated on each PULSE if `current_tps > 0` via `TierFromTPS()`
- Pulse also reports `gpu_utilization_pct` and `vram_used_gb` from nvidia-smi for observability
- TPS calculated by agent: `eval_count / (eval_duration_ns / 1e9)`

---

## 12. Reward Ledger

**Trigger**: On task COMPLETE (via `/results` endpoint or push completion)
**Credits**: `output_tokens / 1000.0`

**Operations**:
1. INSERT into reward_ledger
2. UPDATE operators total_tasks, total_tokens, total_credits

---

## 13. Configuration

**File**: `~/.igrid/config.yaml`

```yaml
operator_id: "duck"
hub_host: "0.0.0.0"
hub_port: 8000
agent_host: "0.0.0.0"
agent_port: 8100
hub_urls:
  - "https://hub.momagrid.org"
db_path: ".igrid/hub.sqlite"
ollama_url: "http://localhost:11434"
api_key: ""
agent_name: ""
agent_id: ""
```

---

## 14. Rate Limiting & DoS Prevention

### RateLimiter
- **Sliding window**: `max_requests` per `window_s` (default 60 req/min)
- **Burst detection**: `burst_threshold` in `burst_window_s` (default 200 in 10s)
- **Per-key**: tracked by IP address

### Enforcement
- Applied to: `/tasks`, `/join`, task submission
- `is_flood=True` → IP added to watchlist (24h suspension) + HTTP 429
- `allowed=False` → HTTP 429

### Watchlist
- Entity types: IP, operator_id, agent_id
- Actions: SUSPENDED (with expiry) | BLOCKED (permanent)
- Checked on every rate-limited endpoint

---

## 15. Hardware Detection (Agent-Side)

### GPU Detection Priority
1. pynvml (nvidia-ml-py)
2. nvidia-smi fallback
3. CPU-only (logs WARNING)

### CPU/RAM
- CPU: `os.cpu_count()`
- RAM: `psutil.virtual_memory().total / 1024^3`

### GPU Utilization (for Pulse)
- pynvml: current utilization % and VRAM used GB

---

## 16. Agent Architecture

### Startup Flow
1. Detect hardware (GPU, CPU, RAM)
2. Connect to Ollama, list available models, run benchmark
3. Build JoinRequest (tier=BRONZE initially)
4. POST `/join` to all configured hub URLs
5. Spawn TelemetrySender (pulse every 30s)
6. Spawn SSE Consumer if pull_mode=True
7. Listen on `/run` if push_mode

### Task Execution
- Semaphore: max `max_concurrent` tasks simultaneously
- Call Ollama `POST /api/generate`
- Calculate TPS, update stats
- Return TaskResult

### Ollama Integration
**Request**: `POST {ollama_url}/api/generate`
```json
{
  "model": "llama3",
  "prompt": "...",
  "system": "...",
  "options": {"num_predict": 1024, "temperature": 0.7},
  "stream": false
}
```
**Response fields used**: `response`, `prompt_eval_count`, `eval_count`, `eval_duration`

---

## 17. Key Timeouts & Intervals

| Component | Value | Purpose |
|-----------|-------|---------|
| Pulse interval (agent) | 30s | Heartbeat |
| Agent eviction timeout | 90s | Since last_pulse |
| Agent monitor check | 30s | Evict stale agents |
| Cluster monitor check | 60s | Sync capabilities, forward tasks |
| Dispatch loop interval | 2s | Pick and dispatch PENDING tasks |
| SSE keepalive | 15s | Timeout on queue.get() |
| Delivery timeout | timeout_s + 10s | HTTP POST to agent /run |

---

## 18. HTTP Status Codes

| Code | Meaning |
|------|---------|
| 202 | Task accepted (PENDING) |
| 400 | Bad request |
| 401 | API key invalid |
| 403 | Watchlisted |
| 404 | Not found |
| 413 | Prompt exceeds size limit |
| 429 | Rate limited or flood detected |
| 503 | Queue full |

---

## 19. Cookbook Recipes

Recipes are managed in `cookbook/run_all.go`. Each has an `isActive` flag:
- **Active** (`isActive: true`) — included in the default batch run
- **Inactive** (`isActive: false`) — skipped in batch; run individually with `--ids`

Print the full catalog: `go run cookbook/run_all.go --catalog`

| # | Name | Description | Active |
|---|------|-------------|--------|
| 01 | single_node_hello | Basic hello world on single agent | ✅ |
| 02 | multi_cte_parallel | Parallel task execution with CTEs | ✅ |
| 03 | batch_translate | Batch translation across agents | ✅ |
| 04 | benchmark_models | Model throughput benchmarking | ✅ |
| 05 | rag_on_grid | Retrieval-Augmented Generation | ✅ |
| 06 | arxiv_paper_digest | Academic paper summarization | — |
| 07 | stress_test | Load testing with concurrent tasks | ✅ |
| 08 | model_arena | Multi-model comparison | ✅ |
| 09 | doc_pipeline | Document processing pipeline | ✅ |
| 10 | chain_relay | Sequential task chaining | ✅ |
| 12 | tier_aware_dispatch | Task dispatch by compute tier | ✅ |
| 13 | multi_agent_throughput | Multi-agent throughput measurement | ✅ |
| 15 | agent_failover | Agent failure handling | ✅ |
| 16 | math_olympiad | Math problem solving | ✅ |
| 17 | code_review_pipeline | Code review automation | ✅ |
| 18 | smart_router | Model routing by prompt type | ✅ |
| 19 | privacy_chunk_demo | Encrypted chunk processing | ✅ |
| 20 | overnight_batch | Overnight batch job | ✅ |
| 21 | language_accessibility | Multi-language support | ✅ |
| 22 | rewards_report | Reward distribution analysis | ✅ |
| 23 | wake_sleep_resilience | Recovery from agent failures | ✅ |
| 24 | spl_compiler_pipeline | SPL compilation workflow | ✅ |
| 25 | model_diversity | Multi-model inference | ✅ |
| 26 | code_guardian | Code security analysis | — |
| 27 | model_health | Model health check — load & TPS | — |
| 28 | federated_search | Federated search gathering & synthesis | — |
| 29 | model_fingerprinting | Cross-agent consistency & determinism | — |
| 30 | academic_paper_pipeline | 4-stage parallel academic workflow | — |
| 33 | micro_learning | Micro-learning textbook generation | — |
| 34 | junior_dev_assistant | Code review + refactoring + docs | — |
| 90 | two_hub_cluster | Multi-hub clustering demo | — |

---

## 20. `mgui` — Unified Chatbot Web UI

`mgui` is a companion Go binary (`cmd/mgui`) that serves a browser-based chatbot interface unified across multiple LLM inference backends. It is analogous to **AWS Bedrock** in concept — a single interface giving access to many model providers — but built on an entirely different foundation: open-source, community-supported, and cooperative. GPU owners contribute compute, the community maintains the software, and the network grows stronger as more participants join. No single corporation owns or controls it.

Momagrid's decentralized GPU grid is a first-class provider alongside the cloud giants, and the routing intelligence is provider-aware, not just model-aware.

No frontend build toolchain is required — all static assets are embedded via `go:embed`.

### Positioning

```
AWS Bedrock  =  managed AWS service, multi-model, cloud-only, proprietary, AWS lock-in
mgui         =  community-supported, open-source, multi-provider, cloud + decentralized grid, no lock-in
```

Where Bedrock is a proprietary managed service controlled by a single corporation, `mgui` is built on a **cooperative, open-source model**: GPU owners contribute compute, the community maintains the software, and everyone — contributors and consumers alike — benefits from the shared network. No single entity owns the grid.

Critically, because Momagrid is open-source, **AWS, Google, an enterprise, or an industry consortium is entirely free to run their own instance**. A corporate deployment is not a threat to the community network — it is a validation of the architecture and, under the open-source license, any improvements they make can flow back to everyone. The network grows stronger with every new hub, regardless of who operates it.

Where Bedrock's primary workload is Claude (with other models as options), `mgui` treats all providers equally — no provider is favored in the UI. The user's choice is informed by factual metadata (cost model, auth type, latency, quality tier) and their own preference.

### Provider Model

`mgui` abstracts over five provider backends through a unified `Provider` interface:

| Provider | API Format | Auth | Cost Model | Role |
|----------|-----------|------|------------|------|
| **OpenAI** | OpenAI REST | API key | Per token (premium) | High-quality reference |
| **Anthropic** | Messages API | API key | Per token (premium) | High-quality reference |
| **Google Gemini** | Gemini REST | API key | Per token (premium) | High-quality reference |
| **OpenRouter** | OpenAI-compatible | API key | Per token (aggregated, 300+ models) | Breadth / research |
| **Momagrid** | Native REST | Ed25519 keypair | Free tier · Credit-based paid tier | Community-supported decentralized grid |

All providers expose the same Go interface internally:

```go
type Provider interface {
    Name()        string
    ListModels()  ([]Model, error)
    Submit(req ChatRequest) (ChatResponse, error)
    AuthType()    AuthType   // APIKey | KeyPair | None
}
```

**Why include all cloud providers individually (not just OpenRouter):**
1. **User preference** — some users have existing OpenAI or Anthropic subscriptions and prefer those directly; equal access, no steering
2. **Benchmarking reference** — GPT-4o, Claude Opus, and Gemini Ultra are the industry quality benchmarks; having them in the same UI makes Momagrid's quality story testable in one click
3. **Academic research value** — provider switching behavior (which tasks go where, how cost vs. quality preferences manifest, whether users trust local inference for sensitive prompts) is a unique dataset not available anywhere else; with user consent this is publishable research
4. **Fallback resilience** — if Momagrid encounters an outage, tasks route seamlessly to a cloud provider; no interruption to user experience

**Why Momagrid's routing is better than OpenRouter's:**
OpenRouter routes between cloud APIs with no knowledge of actual compute availability. Momagrid's dispatcher has real-time visibility into GPU queue depth, agent tier, VRAM, and active task counts — enabling genuinely intelligent dispatch, not just API forwarding.

### Provider-Level Smart Routing & Fallback

Users configure a **fallback chain** per session or globally:

```yaml
# ~/.igrid/config.yaml
mgui:
  fallback_chain:
    - momagrid         # try first: free, private, local GPU
    - openrouter       # mid-tier fallback: broad model coverage
    - anthropic        # premium fallback: highest quality
  fallback_trigger:
    queue_depth: 30    # switch if Momagrid queue > 30s estimated wait
    on_error: true     # always fall back on provider error/timeout
```

Three fallback modes:

| Mode | Trigger | Behavior |
|------|---------|----------|
| **Passive** | Provider returns error or timeout | Retry automatically on next provider in chain |
| **Active** | Momagrid queue depth exceeds threshold | Pre-emptively route to next provider |
| **Manual** | User selects provider in UI | Override chain for that request |

The response footer always shows which provider and model served the request, maintaining full transparency.

### Synchronous Agent Registration (Join Grid)

Registration is a **blocking wizard** — it must complete successfully before the user can submit work. This is intentional: joining the grid is a trust-establishing act, not a background task.

```
Step 1  Detecting hardware...        ✅  GTX 1080 Ti · 11 GB · 14 Ollama models
Step 2  Connecting to hub...         ✅  hub-4cafdc78 at 192.168.0.177:9000
Step 3  Loading Ed25519 identity...  ✅  ~/.igrid/agent_key.pem
Step 4  Registering with hub...      ✅  agent-dc4d1a52 accepted
Step 5  Waiting for ONLINE status... ✅  tier=GOLD · ONLINE
Step 6  Starting heartbeat loop...   ✅  pulse every 30s

  You are now contributing to the Momagrid network.
  Earnings: 1 credit per 1000 output tokens generated.
```

The wizard is SSE-streamed from `mgui` to the browser. Each step either advances or halts with a clear error message. The Join tab is only shown when local GPU and Ollama are detected; users without GPUs see it hidden.

### UI Layout

#### Tab 1 — Chat
- **Provider selector** — dropdown: OpenAI / Anthropic / Google / OpenRouter / Momagrid (equal visual weight, no default bias)
- **Model selector** — populated per provider: cloud providers list their available models; Momagrid lists models from online agents
- **System prompt** — collapsible field
- **Prompt input** + Submit
- Response rendered as chat bubble with footer: `provider · model · latency · tokens`
- Conversation history in-browser (session only — no server-side storage)
- **Compare mode** — send same prompt to two providers side by side (benchmark view)

#### Tab 2 — Join Grid *(shown only when GPU + Ollama detected)*
- Auto-detected: GPU model, VRAM, Ollama model list, hostname
- Editable: agent name, operator ID, hub URL, push/pull mode
- **Register** button → synchronous wizard (SSE-streamed steps above)
- Live status badge after registration: tier · status · tasks served · credits earned

#### Tab 3 — Grid Status
- Agents table (auto-refreshes 10s): NAME, TIER, STATUS, TPS, GPU, VRAM used
- Recent tasks (last 20): MODEL, PROVIDER, STATE, LATENCY
- Rewards ledger: OPERATOR, TASKS, TOKENS, CREDITS
- Hub health: hub_id, queue depth, uptime

#### Tab 4 — Providers *(settings)*
- Per-provider API key entry (stored encrypted in `~/.igrid/config.yaml`)
- Fallback chain configurator (drag-to-reorder)
- Test connection button per provider

### Architecture

```
cmd/mgui/
  main.go             — flag parsing, go:embed, ListenAndServe
  handler.go          — HTTP handlers
  provider/
    interface.go      — Provider interface + ChatRequest/ChatResponse types
    openai.go         — OpenAI REST adapter
    anthropic.go      — Anthropic Messages API adapter
    gemini.go         — Google Gemini REST adapter
    openrouter.go     — OpenRouter adapter (reuses OpenAI format)
    momagrid.go       — Momagrid native REST adapter + fallback router
  static/
    index.html        — single-page app (vanilla JS, no framework)
    style.css
    app.js
```

**Provider proxy**: `mgui` is the sole HTTP client for all providers. The browser never calls external APIs directly — no CORS issues, no API key exposure in browser.

**Join handler** (`POST /api/join`): directly calls the hub `/join` endpoint and starts an in-process pulse goroutine (no subprocess dependency on `mg` binary). Streams wizard steps back to browser via SSE.

**Probe handler** (`GET /api/probe`): returns GPU and Ollama detection as JSON; called on page load to decide whether to show the Join tab.

### HTTP Endpoints (mgui-internal)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Serve single-page app |
| GET | `/api/probe` | Local GPU + Ollama detection result |
| POST | `/api/join` | Synchronous agent registration wizard (SSE) |
| POST | `/api/chat` | Unified chat submission (routes to selected provider) |
| GET | `/api/providers` | List configured providers and their status |
| ANY | `/api/hub/*` | Transparent proxy to Momagrid hub API |

### Build & Run

```bash
go build -buildvcs=false -o mgui ./cmd/mgui

mgui --hub http://localhost:9000           # UI at http://localhost:9080
mgui --hub http://192.168.0.177:9000 --port 9080
```

### Key Design Decisions

- **Equal provider treatment** — no provider is visually highlighted or set as default; the UI is neutral; provider metadata (cost, auth, latency class) is shown factually
- **API keys encrypted at rest** — stored in `~/.igrid/config.yaml` under AES-GCM encryption; never logged or transmitted beyond the target provider
- **Synchronous join** — registration must reach ONLINE before the grid becomes available; no silent background joins
- **In-process pulse** — `mgui` runs the heartbeat goroutine internally, removing the dependency on the `mg` binary being in PATH
- **No framework** — vanilla JS keeps the binary small and the embed fast; no npm, no webpack
- **Separate binary** — hub stays lean and headless; `mgui` is opt-in for community members who want a UI without changing the core network node
- **Session-only chat history** — no server-side conversation storage; privacy-preserving by default
- **Compare mode** — same prompt to two providers simultaneously enables direct quality and cost benchmarking; this is the key differentiator from both OpenRouter and AWS Bedrock


---

## 21. Inference Backend Strategy

### Current State

The Momagrid agent currently integrates with Ollama via HTTP (`POST {ollama_url}/api/generate`).
This is a deliberate thin boundary: the agent treats Ollama as an opaque inference service,
which already provides a degree of backend independence.

### Potential: Native Ollama Integration

Since both Momagrid and Ollama are implemented in Go, a deeper native integration is a natural
direction — eliminating the HTTP boundary between the agent and the inference runtime to enable:

- Direct VRAM scheduling and model load/unload control
- Native token streaming without HTTP chunked-encoding overhead
- Shared process memory for embedding vectors (RAG use cases)
- Sub-millisecond dispatch latency for short-context tasks

This could take the form of a formal partnership with the Ollama project or a Momagrid-maintained
Ollama integration library.

### Trade-off: Backend Portability

Tight Ollama coupling would come at the cost of backend portability. The inference landscape
is broader than Ollama:

| Backend | Strengths | Target hardware |
|---------|-----------|-----------------|
| Ollama (Go) | Easy setup, model management, broad model support | Consumer GPUs, Mac |
| llama.cpp | Lightweight, CPU+GPU, GGUF format, low memory | CPU clusters, edge nodes |
| vLLM (Python) | High-throughput, PagedAttention, production-grade | Data center GPUs |
| ONNX Runtime / CoreML | Mobile-optimized, hardware-accelerated | NPU, Apple Silicon |
| Qualcomm QNN / MediaTek APU | Mobile NPU inference | Android/iOS edge nodes |

A WAN community grid will realistically include all of these — a phone contributing NPU cycles
should be a first-class grid participant alongside a workstation running Ollama.

### Recommended Architecture: Backend Interface

The right abstraction is a formal **Backend Interface** that the Momagrid agent implements
against, with Ollama as the reference native backend:

```
Momagrid Agent
    └── BackendInterface
            ├── OllamaBackend     (default, native Go, current)
            ├── LlamaCppBackend   (subprocess or HTTP, GGUF)
            ├── vLLMBackend       (OpenAI-compat HTTP)
            └── MobileBackend     (ONNX / CoreML / QNN, future)
```

The current HTTP-to-Ollama layer is already close to this pattern — formalizing it as an
interface keeps Ollama as the preferred native backend while making llama.cpp and mobile NPU
participation tractable without forking the agent codebase.

### Design Principle

> Ollama is the reference backend for LAN/desktop nodes. The Backend Interface is the
> extensibility point for edge, mobile, and alternative runtimes. Tight Ollama integration
> is pursued within the interface contract, not by replacing it.
