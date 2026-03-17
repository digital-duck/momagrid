# Gap Report: Go Implementation vs. Python Spec

> Generated: 2026-03-08
> Reference spec: `docs/spec.md`
> Go implementation: `~85–90% feature-complete`

---

## MISSING: HTTP Routes

| Route | Spec Section | Notes |
|-------|-------------|-------|
| `GET /watchlist` | §2.8 | List watchlist entries |
| `DELETE /watchlist/{entity_id}` | §2.8 | Remove entity, reset rate limiter |

---

## MISSING: DB Tables / Columns

| Table | Spec Section | Notes |
|-------|-------------|-------|
| `watchlist` | §3 | Entire table missing from both DDL files |

**Watchlist table columns needed:**
```sql
id          INTEGER PK AUTOINCREMENT
entity_type TEXT    -- 'operator' | 'agent' | 'ip'
entity_id   TEXT
reason      TEXT    DEFAULT ''
action      TEXT    DEFAULT 'SUSPENDED'  -- SUSPENDED | BLOCKED
created_at  TEXT    DEFAULT datetime('now')
expires_at  TEXT    -- NULL = permanent
UNIQUE(entity_type, entity_id)
```

Also missing from `JoinRequest` schema struct:
- `MaxConcurrent int` — sent by agent on join; currently not mapped from request into DB

---

## MISSING: State / DB Methods

| Method | Spec Section | Notes |
|--------|-------------|-------|
| `AddToWatchlist(entityType, entityID, reason, action, expiresAt)` | §14 | |
| `RemoveFromWatchlist(entityID)` | §14 | Also reset rate limiter state |
| `IsWatchlisted(entityID) bool` | §14 | Check on join/task endpoints |
| `ListWatchlist() []map` | §2.8 | For GET /watchlist handler |

---

## MISSING: Dispatcher / Submission Features

| Feature | Spec Section | Notes |
|---------|-------------|-------|
| Rate limiting (sliding window 60 req/min) | §14 | Per-IP, on `/join` and `/tasks` |
| Burst detection (200 req/10s → watchlist) | §14 | HTTP 429 + auto-suspend IP 24h |
| Watchlist check on join/submit | §14 | HTTP 403 if watchlisted |
| Max queue depth enforcement | §2.4 | HTTP 503 if tasks PENDING > `max_queue_depth` (default 1000) |
| Prompt size enforcement | §2.4 | HTTP 413 if prompt > `max_prompt_chars` (default 50K soft) |

---

## MISSING: Cluster Features

| Feature | Spec Section | Notes |
|---------|-------------|-------|
| Cluster monitor: forward PENDING tasks to peers | §6.3, §10 | `ClusterMonitor` pushes capabilities but never calls `ForwardTask`; method exists but is unreachable from the monitor loop |

---

## MISSING: CLI Commands / Flags

| Command / Flag | Spec Section | Notes |
|----------------|-------------|-------|
| `mg watchlist` | §8 | List watchlist entries |
| `mg unblock <entity_id>` | §8 | Call DELETE /watchlist/{entity_id} |
| `hub up --max-prompt-chars` | §8 | Hub config flag |
| `hub up --max-queue-depth` | §8 | Hub config flag |
| `hub up --rate-limit` | §8 | Requests/min |
| `hub up --burst-threshold` | §8 | Requests in 10s for flood |

---

## MISSING: Cookbook Recipes

| # | Name | Python | Go |
|---|------|--------|-----|
| 03 | batch_translate | ✅ | ❌ |
| 05 | rag_on_grid | ✅ | ❌ |
| 06 | arxiv_paper_digest | ✅ | ❌ |
| 10 | chain_relay | ✅ | ❌ |
| 26 | code_guardian | ✅ | ❌ |
| 90 | two_hub_cluster | ✅ | ❌ |

---

## PRESENT: Already Implemented in Go ✅

**HTTP Routes**: `/health`, `/join`, `/leave`, `/pulse`, `POST /tasks`, `GET /tasks`, `GET /tasks/{id}`, `GET /agents`, `GET /agents/pending`, `POST /results`, `GET /task-stream/{agentID}`, `POST /agents/{id}/approve`, `POST /agents/{id}/reject`, `POST /cluster/handshake`, `POST /cluster/capabilities`, `POST /cluster/peers`, `GET /cluster/status`, `POST /cluster/result`, `GET /logs`, `GET /rewards`

**DB Schema**: hub_config, peer_hubs, operators, agents, tasks, pulse_log, reward_ledger, reward_summary view — all present in both SQLite and PostgreSQL DDLs

**Dispatcher**: Tier/VRAM/model filtering, atomic claim, push + pull delivery, retry (max 3), O(1) active-count pre-fetch, model normalization (`:latest`)

**Cluster**: Hub-to-hub handshake, capability sync (60s), task forwarding with webhook callback + polling fallback

**Monitoring**: AgentMonitor (30s eviction, 90s timeout), DispatchLoop (2s interval), ClusterMonitor (60s capability push)

**Security**: Ed25519 signing for join/pulse, key downgrade prevention, admin mode with agent verification (benchmark + geo-IP + sampling)

**Database**: SQLite WAL mode + busy_timeout; PostgreSQL pool tuning (MaxOpen 20, MaxIdle 10, ConnMaxLifetime 5m); backward-compat migrations

**CLI**: `hub up/pending/approve/reject/migrate`, `join`, `status`, `agents`, `tasks`, `submit`, `rewards`, `logs`, `export`, `test`, `run`, `peer add/list`, `config`, `version`

**Cookbook Go ports**: 03, 04, 05, 06, 07, 08, 09, 10, 12, 13, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26 (22 of 23 recipes)

---

## Priority Ranking

| Priority | Gap | Status |
|----------|-----|--------|
| High | Watchlist table + state methods + `/watchlist` routes | ✅ Done 2026-03-08 |
| High | Rate limiting + burst detection + watchlist checks on endpoints | ✅ Done 2026-03-08 |
| High | Cluster monitor task-forwarding trigger | ✅ Done 2026-03-08 |
| Medium | Queue depth + prompt size enforcement | ✅ Done 2026-03-08 |
| Medium | `hub up` flags: --rate-limit, --burst-threshold, --max-prompt-chars, --max-queue-depth | ✅ Done 2026-03-08 |
| Medium | `mg watchlist` + `mg unblock` CLI commands | ✅ Done 2026-03-08 |
| Low | `MaxConcurrent` in JoinRequest schema | Open |
| Low | Cookbook recipe 90 (two_hub_cluster) | Open |
