# Enhancements — momagrid Go Implementation

Four features added beyond the Python baseline, each addressing a specific
weakness identified in the Gemini architecture review (2026-03-08).

---

## 1. Cookbook Batch Runner (`cookbook/run_all.go`)

**Problem:** No equivalent to Python's `run_all.py` for the Go cookbook.

**Solution:** `run_all.go` runs all 23 recipes sequentially via `go run`, tees
stdout+stderr to terminal and per-recipe `.log` files, and prints a final
summary table with pass/fail and elapsed time.

```bash
go run cookbook/run_all.go                       # run all recipes
go run cookbook/run_all.go --ids 04,08,13        # run specific recipes
go run cookbook/run_all.go --list                # list available recipes
go run cookbook/run_all.go --hub http://host:8000
```

**Files:** `cookbook/run_all.go` (new)

---

## 2. Ed25519 Agent Identity

**Problem (Gemini §3.5):** `operator_id` is a plain string — any node can
impersonate any operator and collect their rewards. Critical on a public grid.

**Solution:** Each agent generates a permanent Ed25519 keypair on first start
(`~/.igrid/agent_key.pem`). The public key is registered at join time. Every
pulse is signed. The hub rejects unsigned pulses from known-key agents.

### Why Ed25519

| Property | Ed25519 | RSA-2048 |
|----------|---------|----------|
| Key size | 32 bytes | 256 bytes |
| Signature size | 64 bytes | 256 bytes |
| Signing speed | ~70 000/s | ~1 000/s |
| Constant-time | Yes (no timing attacks) | No |
| Dependency | stdlib `crypto/ed25519` | stdlib |

Total wire overhead per join: ~108 bytes. Per pulse: ~80 bytes.

### Key lifecycle

```
First start
  ~/.igrid/agent_key.pem missing
  → generate Ed25519 keypair
  → save private key as PKCS#8 PEM (mode 0600)

Every join
  challenge  = agentID + ":" + RFC3339-timestamp
  signature  = Sign(privateKey, challenge)
  POST /join { ..., public_key, signature, timestamp }
  hub verifies → stores public_key in agents.public_key

Every pulse (30s)
  challenge  = agentID + ":" + RFC3339-timestamp
  signature  = Sign(privateKey, challenge)
  POST /pulse { ..., signature, timestamp }
  hub verifies against stored key → 401 if bad
```

### Replay protection

The timestamp is included in the signed message. The hub accepts timestamps
within ±2 minutes of its own clock — replayed pulses expire in at most 2 min.

### Backward compatibility

Agents without a key (Python CLI, trusted LAN) are still accepted — hub skips
verification when `public_key == ""`. Key downgrade is blocked: if a key is on
file for an agent, unsigned pulses from that agent are rejected (401).

### Files changed

| File | Change |
|------|--------|
| `internal/identity/identity.go` | **New** — LoadOrCreate, Sign, Verify, MakeChallenge |
| `internal/schema/handshake.go` | `PublicKey`, `Signature`, `Timestamp` added to JoinRequest |
| `internal/schema/pulse.go` | `Signature`, `Timestamp` added to PulseReport |
| `internal/hub/db.go` | Migration: adds `public_key TEXT DEFAULT ''` to agents |
| `internal/hub/state.go` | RegisterAgent stores key; AgentPublicKey() lookup |
| `internal/hub/app.go` | handleJoin and handlePulse verify signatures |
| `internal/cli/join.go` | **New** — mg join: keypair load/sign + pulse loop |
| `cmd/mg/main.go` | Registered `join` subcommand |
| `internal/hub/hub_ddl_sqlite.sql` | `public_key TEXT NOT NULL DEFAULT ''` in agents |
| `internal/hub/hub_ddl_postgresql.sql` | `public_key TEXT NOT NULL DEFAULT ''` in agents |

---

## 3. Webhook Callback for Cluster Task Forwarding

**Problem (Gemini §3.2):** Hub A polls Hub B every 2–10 s to detect forwarded
task completion. A task finishing in 2 s on Hub B may not be seen by Hub A for
up to 10 s — a 5× latency multiplier on peer-forwarded tasks.

**Solution:** Hub A includes `callback_url = hubA/cluster/result` in every
forwarded TaskRequest. Hub B's dispatcher fires a POST to that URL the moment
the task completes. Hub A's `ForwardTask` resolves in <500 ms. Polling is kept
as a fallback in case the callback is lost.

### Before vs after

```
BEFORE (polling every 2-10s):
  Hub A → POST /tasks → Hub B                 t = 0
  Hub B agent finishes                         t = 2 s
  Hub A polls GET /tasks/{id} → COMPLETE       t = up to 12 s
  Result visible at Hub A                      t = up to 12 s

AFTER (webhook):
  Hub A → POST /tasks (callback_url=…) → Hub B  t = 0
  Hub B agent finishes                            t = 2 s
  Hub B → POST /cluster/result → Hub A           t = 2 s + ε
  Result visible at Hub A                         t ≈ 2 s
  (fallback poll fires only if callback lost)
```

### Fallback behaviour

`waitForResult` polls the local DB for state != FORWARDED at 500 ms intervals.
If the callback fires, the DB row transitions to COMPLETE immediately and the
loop exits. If the callback is lost (Hub A restarted, network error), the loop
times out and `pollPeer` takes over — identical to the pre-webhook behaviour.

### Files changed

| File | Change |
|------|--------|
| `internal/schema/task.go` | `CallbackURL string` added to TaskRequest |
| `internal/hub/cluster.go` | ForwardTask sets callback_url; waitForResult polls local DB |
| `internal/hub/dispatcher.go` | fireCallback() POSTs result after task completes |
| `internal/hub/app.go` | POST /cluster/result handler + route |
| `internal/hub/hub_ddl_sqlite.sql` | `callback_url TEXT NOT NULL DEFAULT ''` in tasks |
| `internal/hub/hub_ddl_postgresql.sql` | `callback_url TEXT NOT NULL DEFAULT ''` in tasks |

---

## 4. PostgreSQL Production Hardening

**Problem (Gemini §3.3):** SQLite WAL not explicitly enabled → SQLITE_BUSY
errors under burst pulse storms. PostgreSQL supported in code but undocumented.

### SQLite WAL (applied at open time)

```go
PRAGMA journal_mode=WAL;    -- readers never block the writer
PRAGMA synchronous=NORMAL;  -- good durability under WAL, faster than FULL
PRAGMA busy_timeout=5000;   -- retry up to 5 s before returning SQLITE_BUSY
```

Sufficient for up to ~30 simultaneous agents on a LAN grid.

### PostgreSQL connection pool

```go
db.SetMaxOpenConns(20)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(5 * time.Minute)
```

### When to use each backend

| Scenario | Backend |
|----------|---------|
| Single dev, local test | SQLite (default, zero config) |
| LAN grid, ≤ 30 agents | SQLite + WAL |
| Public grid, 30–500 agents | PostgreSQL |
| Multi-hub federation | PostgreSQL (recommended) |

### Quick setup (Ubuntu)

```bash
sudo apt install postgresql
sudo -u postgres psql -c "CREATE USER mguser WITH PASSWORD 'mgpass';"
sudo -u postgres psql -c "CREATE DATABASE momagrid OWNER mguser;"

# Migrate existing SQLite → PostgreSQL
mg hub migrate --from .igrid/hub.db \
               --to "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"

# Start hub on PostgreSQL
mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"
```

### Files changed

| File | Change |
|------|--------|
| `internal/hub/db.go` | WAL PRAGMAs; PostgreSQL pool tuning |
| `internal/hub/hub_ddl_sqlite.sql` | `public_key`, `callback_url` columns added |
| `internal/hub/hub_ddl_postgresql.sql` | `public_key`, `callback_url` columns added |
| `README.md` | PostgreSQL setup, pool tuning, backend selection guide |

---

---

## 5. `mg tasks` — Timestamp, Executor, and Submission Source

**Problem:** `mg tasks` showed only `TASK_ID`, `STATE`, and `MODEL` — no time,
no indication of which agent ran the task, and no indication of where the
request originated.

**Solution (implemented):** Enhanced summary and detail views pulling fields
that are already stored in the `tasks` table.

### Summary view (`mg tasks`)

```
TIME        TASK_ID               STATE         MODEL                 AGENT            FROM
--------------------------------------------------------------------------------------------
14:32:01    arena-a1b2c3       COMPLETE      llama3                wengong          direct
14:31:55    tput-e5f6g789      COMPLETE      llama3                wen              direct
03-13 22:10 chat-i9j0k1l2      COMPLETE      mistral               wengong          hub-abc (cluster)
```

- **TIME** — `created_at` formatted as `HH:MM:SS` (today) or `MM-DD HH:MM` (older)
- **AGENT** — agent `name` from the agents table (cross-referenced by `agent_id`); shows `—` for PENDING tasks not yet dispatched
- **FROM** — `direct` if submitted to this hub; `<peer_hub_id> (cluster)` if forwarded via cluster peering

### Detail view (`mg tasks -d`)

Adds `submitted` timestamp, `updated` timestamp, and agent host IP:

```
  submitted: 2026-03-14 14:32:01  (direct)
  updated:   2026-03-14 14:32:18
  executed:  wengong  (agent-beaaac47)  @192.168.0.201
```

### Files changed

| File | Change |
|------|--------|
| `internal/cli/client.go` | Enhanced `Tasks()` — agent name lookup, `fmtTS()`, `execBy()`, `submittedFrom()` |

---

## 6. `submitted_from` — Track Submitting Client IP  *(pending)*

**Problem:** `mg tasks FROM` column shows `direct` for all locally-submitted
tasks regardless of which machine on the LAN submitted the request. There is no
`submitter_ip` column in the `tasks` table, so the hub discards that
information at insert time.

**Solution (to implement):**

1. **Schema** — add `submitted_from TEXT NOT NULL DEFAULT ''` to the `tasks`
   table (both `hub_ddl_sqlite.sql` and `hub_ddl_postgresql.sql`).

2. **Hub handler** (`internal/hub/app.go` `handleCreateTask`) — extract
   `r.RemoteAddr` (or `X-Forwarded-For` header if behind a proxy) and write it
   to the new column at insert time.

3. **State layer** (`internal/hub/state.go` `CreateTask`) — accept and persist
   the new field; update the INSERT statement.

4. **CLI** (`internal/cli/client.go` `Tasks`) — display `submitted_from` in
   both summary (`FROM` column) and detail view, replacing the current
   `peer_hub_id`-only logic.

5. **Migration** — add a `db.go` auto-migration that `ALTER TABLE tasks ADD
   COLUMN submitted_from TEXT NOT NULL DEFAULT ''` for existing databases.

**Expected outcome:**

```
FROM
────────────────────────
192.168.0.201           ← task submitted from 2nd GPU machine
192.168.0.177           ← task submitted from hub machine itself
hub-abc123 (cluster)    ← forwarded via peer hub
```

### Files to change

| File | Change |
|------|--------|
| `internal/hub/hub_ddl_sqlite.sql` | Add `submitted_from TEXT NOT NULL DEFAULT ''` to tasks |
| `internal/hub/hub_ddl_postgresql.sql` | Same |
| `internal/hub/db.go` | Auto-migration: `ALTER TABLE tasks ADD COLUMN submitted_from` |
| `internal/hub/state.go` | `CreateTask` accepts + stores `submitted_from` |
| `internal/hub/app.go` | `handleCreateTask` extracts `r.RemoteAddr` → passes to `CreateTask` |
| `internal/cli/client.go` | `Tasks()` shows `submitted_from` in FROM column |

---

## Summary

| # | Feature | Gemini ref | Key metric |
|---|---------|-----------|------------|
| 1 | Cookbook batch runner | — | Dev velocity |
| 2 | Ed25519 identity signing | §3.5 | Reward spoofing → impossible |
| 3 | Webhook callback forwarding | §3.2 | Forwarded task latency −80% |
| 4 | SQLite WAL + PostgreSQL hardening | §3.3 | No SQLITE_BUSY under burst load |
| 5 | `mg tasks` timestamp + agent + source | — | Observability |
| 6 | `submitted_from` client IP tracking | — | Full request traceability *(pending)* |
