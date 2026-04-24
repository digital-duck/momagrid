# DBOS Integration for Momagrid Hub

> **Design goal** — DBOS is an optional, swappable backend selected at startup with `mg hub up --backend dbos`. The default `native` backend is the existing implementation, untouched. A thin `DispatchBackend` interface is the only addition to the current codebase. This enables side-by-side comparative testing with zero regression risk.

---

## Implementation Status

| Component | Status |
|---|---|
| `DispatchBackend` interface | Done |
| `NativeBackend` (default) | Done |
| `DBOSBackend` — task workflow | Done |
| `DBOSBackend` — job workflow | Done |
| `DBOSBackend` — agent eviction (scheduled) | Done |
| `DBOSBackend` — `ListTasks` / `ListJobs` | Done (returns DBOS workflow summaries) |
| CLI `--backend` / `--postgres` flags | Done |
| DBOS SDK dependency (`v0.13.0`) | Done (`go get`) |
| Native backend regression testing | **TODO** |
| DBOS backend end-to-end testing | **TODO** |
| SSE pull-mode support under DBOS | **TODO** (open question) |
| `CountPendingTasks` under DBOS | **TODO** (returns 0; DBOS enforces queue depth) |

---

## Why DBOS Fits Momagrid

The Hub today manages task state with raw SQLite/PostgreSQL writes and a 2-second polling loop. That works, but has gaps:

| Current Gap | DBOS Solution |
|---|---|
| Crash mid-dispatch leaves task DISPATCHED until agent eviction (90s) | Workflow auto-resumes from last completed step on restart |
| Retry logic hand-coded in `deliverAndUpdate` | `RunAsStep` with `WithStepMaxRetries` + exponential backoff |
| Peer-forwarding callback failures require manual polling fallback | DBOS workflow survives the crash; callback is just another step |
| No idempotency on submission (UUID only, no dedup guard) | `WithWorkflowID(taskID)` guarantees single execution |
| Job deadlines implemented via polling loop (drift-prone) | `Sleep()` persists across restarts — exact to the second |
| Scale-out risks double-dispatch from two Hub replicas | Queue deduplication + `WithWorkflowID` prevents it |

Michael Stonebraker's core insight behind DBOS: **use the database you already have as the durable execution log**, instead of building ad-hoc state machines on top of it.

---

## The `--backend` Flag

```bash
# Existing behavior — zero change
mg hub up

# Explicit native
mg hub up --backend native

# DBOS backend for comparative testing
mg hub up --backend dbos --postgres "postgres://user:pass@localhost/momagrid"
```

Both backends expose identical REST API endpoints. Clients (including `SPL.py`'s `MomagridAdapter`) notice no difference. The flag purely controls what runs inside the Hub process.

---

## The `DispatchBackend` Interface

Defined in `internal/hub/backend.go`. This is the **only new abstraction added to the existing codebase** — ~30 lines.

```go
type DispatchBackend interface {
    Start() error
    Stop() error

    SubmitTask(req schema.TaskRequest) error
    GetTaskStatus(taskID string) (*schema.TaskStatusResponse, error)
    ListTasks(limit int) ([]map[string]interface{}, error)
    CountPendingTasks() (int, error)

    SubmitJob(req schema.JobRequest) error
    GetJobStatus(jobID string) (*schema.JobStatusResponse, error)
    ListJobs(limit int) ([]map[string]interface{}, error)
}
```

Everything else — HTTP routing, agent handshake, rate limiting, SSE pull mode, cluster peering — stays in `App` and is shared by both backends.

---

## Database Strategy

Each backend is the **sole source of truth** for task and job state. There is no dual-write.

| Scenario | Task/Job State | Agent/Reward/Pulse State |
|---|---|---|
| `--backend native` (SQLite) | Hub DB — `hub.tasks`, `hub.jobs` | Hub DB (unchanged) |
| `--backend native` (PostgreSQL) | Hub DB — `hub.tasks`, `hub.jobs` | Hub DB (unchanged) |
| `--backend dbos` | DBOS system DB — workflow state tables | Hub DB (agents, rewards, pulse log unchanged) |

Under the DBOS backend, `GET /tasks/{id}` and `GET /jobs/{id}` read from DBOS workflow state, not from `hub.tasks` / `hub.jobs`. Those Hub tables are not written to at all when `--backend dbos` is active. Agent registration, heartbeat, rewards, and pulse logs continue to use the Hub DB regardless of backend — those are not part of the `DispatchBackend` interface.

The DBOS system tables live in a `dbos` schema within the same PostgreSQL database as the Hub. If you prefer full isolation, a dedicated database also works — just point `--postgres` at it.

---

## What Was Implemented

### Files Created

**`internal/hub/backend.go`** — `DispatchBackend` interface definition.

**`internal/hub/backend_native.go`** — `NativeBackend` struct. Thin delegation layer; starts the existing `AgentMonitor`, `DispatchLoop`, and `JobLoop` goroutines from `Start()`. No existing logic was moved or modified.

**`internal/hub/backend_dbos.go`** — `DBOSBackend` struct and three package-level workflow functions:

| Workflow function | Replaces | Durable guarantee |
|---|---|---|
| `dbosTaskWorkflow` | `deliverAndUpdate` goroutine | Pick agent → deliver → record reward, exactly-once per step |
| `dbosJobWorkflow` | `JobLoop` + deadline polling | Durable `Sleep()` to deadline, child task dispatch, exactly-once notify |
| `dbosAgentEvictionWorkflow` | `AgentMonitor` goroutine | Scheduled every minute via DBOS cron; single invocation across replicas |

Task workflow steps:
```
Step 1: pickPushAgent()        ← retried up to 5× with 2s backoff
Step 2: DeliverTask() HTTP     ← retried up to maxRetries with 2s backoff
Step 3: RecordReward()         ← exactly-once (DBOS won't replay on crash)
```

### Files Modified

**`internal/hub/app.go`** — minimal changes:
- `HubConfig` gains `Backend string` and `PostgresDSN string` fields
- `App` struct gains `Backend DispatchBackend` field
- `NewApp` selects backend based on `cfg.Backend` (12 lines)
- `Start()` calls `Backend.Start()` instead of inline goroutine spawns; `ClusterMonitor` goroutine unchanged
- `Stop()` calls `Backend.Stop()` before closing DB
- 6 handlers updated: `handleSubmitTask`, `handleGetTask`, `handleListTasks`, `handleSubmitJob`, `handleGetJob`, `handleListJobs` — each now calls `a.Backend.*` instead of `a.State.*`

**`internal/cli/hub.go`** — 4 lines added:
```go
backend := fs.String("backend", "native", `Dispatch backend: "native" (default) or "dbos"`)
postgres := fs.String("postgres", "", `PostgreSQL DSN required for --backend dbos`)
// ... passed into HubConfig
```

**`go.mod` / `go.sum`** — DBOS SDK and its transitive dependencies added:
```
github.com/dbos-inc/dbos-transact-golang v0.13.0
github.com/jackc/pgx/v5 v5.9.1
github.com/robfig/cron/v3 v3.0.1
```

### DBOS SDK Version

`v0.13.0` — fetched via `go get github.com/dbos-inc/dbos-transact-golang@latest`.

### Key DBOS API calls used

| Our call | DBOS API |
|---|---|
| Create context | `dbos.NewDBOSContext(context.Background(), dbos.Config{DatabaseURL: dsn})` |
| Register workflow | `dbos.RegisterWorkflow(ctx, fn, opts...)` |
| Register scheduled | `dbos.RegisterWorkflow(ctx, fn, dbos.WithSchedule("* * * * *"))` |
| Create queue | `dbos.NewWorkflowQueue(ctx, name, dbos.WithWorkerConcurrency(n))` |
| Submit task | `dbos.RunWorkflow(ctx, fn, input, dbos.WithWorkflowID(id), dbos.WithQueue(name))` |
| Run step | `dbos.RunAsStep(ctx, fn, dbos.WithStepName(n), dbos.WithStepMaxRetries(n), dbos.WithBaseInterval(d))` |
| Durable sleep | `dbos.Sleep(ctx, duration)` |
| Get status | `handle.GetStatus()` → `WorkflowStatus{ID, Status WorkflowStatusType, Error error, CreatedAt, UpdatedAt, ...}` |
| Get result | `handle.GetResult()` |
| Retrieve by ID | `dbos.RetrieveWorkflow[R](ctx, workflowID)` |
| List workflows | `dbos.ListWorkflows(ctx, dbos.WithName("fn"), dbos.WithLimit(n))` |
| Shutdown | `ctx.Shutdown(10 * time.Second)` |

---

## Comparative Testing

Because both backends expose the same REST API, run them on different ports and drive the same workload:

```bash
# Terminal 1: native backend, port 9000
mg hub up --port 9000 --backend native

# Terminal 2: DBOS backend, port 9001
mg hub up --port 9001 --backend dbos --postgres "postgres://user:pass@localhost/momagrid"

# SPL.py: point at either via --hub flag or MOMAGRID_HUB_URL
spl3 run cookbook/01_hello_world/hello.spl --hub http://localhost:9000
spl3 run cookbook/01_hello_world/hello.spl --hub http://localhost:9001
```

### TODO: Test Checklist

- [ ] `mg hub up --backend native` starts without error (regression baseline)
- [ ] `POST /tasks` + `GET /tasks/{id}` polling works with native backend
- [ ] `mg hub up --backend dbos --postgres "..."` starts without error
- [ ] DBOS system tables created in PostgreSQL on first launch
- [ ] `POST /tasks` submits a DBOS workflow (visible in DBOS system DB)
- [ ] `GET /tasks/{id}` returns correct status during execution (`IN_FLIGHT`) and after (`COMPLETE`)
- [ ] Hub crash mid-task → Hub restart → task resumes without manual intervention
- [ ] Reward recorded exactly-once after Hub crash between delivery and reward steps
- [ ] `POST /jobs` with future deadline → `GET /jobs/{id}` shows QUEUED until deadline
- [ ] Agent eviction: let agent heartbeat lapse 90s → verify eviction occurs within ~60s under DBOS (vs. 30s under native)
- [ ] `GET /tasks` list returns DBOS workflow summaries
- [ ] `GET /agents`, `GET /rewards`, `GET /health` work identically under both backends
- [ ] SPL.py `MomagridAdapter` polling works end-to-end against DBOS backend

---

## Known Limitations (v1)

### SSE Pull Mode

Pull-mode agents (connected via `GET /task-stream/{agentID}`) are **excluded** from DBOS-dispatched tasks. The `pickPushAgent` helper skips any agent with `pull_mode = 1`. Tasks requiring a pull-mode agent will retry until a push-mode agent becomes available.

`POST /results` (used by pull-mode agents to submit results) continues to write directly to Hub DB via `a.State`, which is correct for the native backend. Under DBOS + pull-mode this would be inconsistent — this is the open design question deferred to v2.

### `GET /tasks` List Under DBOS

Returns DBOS workflow summaries (`task_id`, `state`, `created_at`, `updated_at`) rather than the full task row. Fields like `model`, `prompt`, `content`, `input_tokens`, `output_tokens` are not available in the list response — they require a `GET /tasks/{id}` call which fetches the full workflow result.

### `CountPendingTasks`

Returns 0 under DBOS (the 503 queue-full guard in `POST /tasks` is effectively disabled). DBOS queue concurrency limits enforce back-pressure instead. If explicit queue depth enforcement is needed, query the DBOS system tables directly.

---

## Tradeoffs

| | Native Backend | DBOS Backend |
|---|---|---|
| **Database** | SQLite or PostgreSQL | PostgreSQL required |
| **Crash recovery** | 90-second agent eviction rescue | Immediate (workflow resumes) |
| **Retry idempotency** | Best-effort | Exactly-once per step |
| **Observability** | Hub DB rows + logs | Full workflow history in DBOS tables |
| **Throughput** | Very low overhead | Small PostgreSQL checkpoint cost per step |
| **Code complexity** | Familiar, hand-rolled | DBOS determinism constraint applies |
| **External dependency** | None | DBOS Go SDK |
| **License** | Apache 2.0 | MIT |


For a **single-node LAN deployment** with SQLite, the native backend remains the right default. The DBOS backend targets **multi-node or cloud deployments** where crash safety, scale-out deduplication, and exactly-once semantics matter.

---

## Open Questions

1. **SSE pull mode** — Pull-mode task delivery goes through `SSEQueues` in `App`, which is backend-agnostic. The DBOS `taskWorkflow` needs to detect pull-mode agents and use `SSEQueues.Put()` as its delivery step instead of HTTP POST. To be designed when DBOS backend implementation begins.

2. **Workflow versioning** — Scoped to `--backend dbos` only; does not affect native deployments. Long-lived `jobWorkflow` instances (those using `Sleep()` across days) must use `dbos.Patch()` before deploying code changes that alter workflow structure. Establish a patching convention (e.g. version suffix in workflow name) when the DBOS backend is promoted beyond experimental use. Not a concern while DBOS remains an optional, opt-in backend.

---

## References

- [DBOS Transact Go — GitHub](https://github.com/dbos-inc/dbos-transact-golang)
- [DBOS Go Programming Guide](https://docs.dbos.dev/golang/programming-guide)
- [Why PostgreSQL for Durable Execution — DBOS Blog](https://www.dbos.dev/blog/why-postgres-durable-execution)
- [How We Built Golang-Native Durable Execution — DBOS Blog](https://www.dbos.dev/blog/how-we-built-golang-native-durable-execution)
- Momagrid Hub source: `internal/hub/backend.go`, `internal/hub/backend_native.go`, `internal/hub/backend_dbos.go`, `internal/hub/app.go`
