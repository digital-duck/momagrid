# Task Dispatch: From Categorical Tiers to Measured Performance Scores

> **Status** — Implemented 2026-08-20, built and test-verified, pending hub
> restart to deploy. `internal/hub/dispatcher.go`'s weighted selection now
> draws on a per-agent `dispatch_score` column instead of fixed tier
> weights; tiers remain only as the hard `min_tier` eligibility cutoff.

---

## Why Move Off Categorical Tiers

Tiers are a coarse proxy for real throughput, and that coarseness has already
caused two bugs in production use:

1. **Tier misclassification** — VRAM/TPS-based auto-detection undershoots on
   unified-memory hardware (Apple Silicon reports low TPS on its first task,
   getting reclassified from an operator-set `PLATINUM` hint down to `BRONZE`).
2. **Tier starvation** — even after fixing (1) with a sticky tier-hint flag,
   two nodes in different tiers meant the higher tier absorbed 100% of
   dispatch under the old strict `ONLINE > tier > least-loaded` ranking.
   The current fix (weighted-random selection with fixed tier weights
   `{Platinum: 8, Gold: 4, Silver: 2, Bronze: 1}`) solves starvation but the
   weights themselves are still a guess, not a measurement.

Real-world data from a 2-node grid (GTX 1080 Ti vs. Mac Mini, both processing
the same 2-paper batch): GTX finished in ~300s, Mac Mini in ~450s. That's a
concrete, measurable performance ratio — no need to bucket it into "Gold" vs.
"Platinum" when the hub already has the actual numbers.

## Proposed Design

Replace the tier lookup in dispatch weighting with a per-agent
**`dispatch_score`** — a single float, initialized from an operator estimate
at join time and refined from real measured task completion times.

### Score formula

```
S = c0 + c1 / T_normalized
```

where `T_normalized` is throughput-normalized elapsed time (e.g. seconds per
unit of work — chars processed, tokens generated — not raw wall-clock
elapsed time, since task size varies run to run and raw elapsed time alone
conflates "slow node" with "big paper"). `(c0, c1)` are constants calibrated
once against a small set of measured (node, T_normalized) pairs across
representative hardware, not re-fit per node.

### Dispatch probability

Given individual agent scores `S_i` (already per-agent, not per-tier), the
selection probability collapses to a straightforward weighted draw:

```
p_i = S_i / Σ_j S_j
```

Two identical machines naturally get an equal combined share of traffic
under this formula without any separate grouping step — per-agent scoring
already generalizes what a `N_i` (count of nodes in the same tier) term
would have provided under the categorical model.

The existing load discount from `weightedPick()` (dividing by
`1 + activeCount`, plus a status penalty for non-`ONLINE` agents) still
applies on top of `S_i`, unchanged.

### Example

| Node | Measured T (2-paper batch) | Initial score estimate |
|---|---|---|
| GTX 1080 Ti | 300s | `S = 100` |
| Mac Mini (24GB unified) | 450s | `S = 60` |

```
p(GTX)      = 100 / (100 + 60) = 0.625
p(Mac Mini) =  60 / (100 + 60) = 0.375
```

Roughly 5:3 dispatch ratio — proportional to real throughput, no starvation,
no manual tier assignment.

### Updating the score over time

Don't overwrite `S_i` on every completed task — one unusually large or small
paper would swing the score too hard. Update via **EWMA** (Exponentially
Weighted Moving Average — a running average that weights recent
observations more heavily than older ones, so the score drifts smoothly
instead of jumping):

```
S_i_new = α · S_i_measured + (1 - α) · S_i_old
```

A small `α` (e.g. 0.1–0.2) keeps the score stable against noisy individual
runs while still tracking real drift (thermal throttling, background load,
hardware changes).

## What Was Actually Implemented

The v1 cut is simpler than the `c0 + c1/T` formula above: instead of a
calibrated regression, `dispatch_score` is EWMA'd directly against measured
**tokens/sec** (`output_tokens / (latency_ms/1000)`), which is already
throughput-normalized (self-correcting for task size, since it's a rate, not
a raw duration) and needs no `(c0, c1)` fitting step. `α = 0.2`.

- **Storage**: `dispatch_score REAL` column on `agents` (migration in
  `db.go`'s `migrateSQLite()`, same pattern as `tier_is_hint`).
- **Cold start**: seeded at join time, in priority order: (1) operator's
  `--score` flag, (2) historical average for that exact GPU model — see
  below, (3) tier-based default (`defaultDispatchScore` in
  `dispatcher.go`: `{Platinum:150, Gold:100, Silver:80, Bronze:40}`).
- **Persistence across rejoins**: `dispatch_score` is deliberately excluded
  from `RegisterAgent`'s `ON CONFLICT` update clause — a restarted agent
  keeps whatever the hub has already learned about it.
- **Backward compatibility**: tiers kept exactly as speculated above — a
  hard `min_tier` eligibility cutoff in `PickAgent`, no longer used for
  weighting itself.

### GPU Score History (added same day)

A `gpu_scores` table (`gpu_model TEXT PRIMARY KEY, avg_score, sample_count,
updated_at`) tracks the historical mean `dispatch_score` per GPU model
string (from `agents.gpus[0].model`), built from agents that have actually
completed at least one task (unmeasured join-time seeds don't pollute the
average).

- `GridState.RecomputeGPUScores()` aggregates and upserts it — callable
  on-demand (`POST /gpu-scores/refresh`, or `mg gpu-scores --refresh`) or
  automatically every 10 minutes via `GPUScoreMonitor` (wired into
  `NativeBackend.Start()` alongside `AgentMonitor`).
- `GET /gpu-scores` / `mg gpu-scores` lists it, highest-scoring first.
- At join time, `handleJoin` now consults `GPUScoreFor(model)` as step (2)
  in the seeding chain above — so the *second* GTX 1080 Ti that ever joins
  this grid starts from the first one's real measured performance instead
  of the flat Gold=100 tier guess, converging to accurate dispatch shares
  faster.
- Agents with no discrete GPU (unified-memory / Apple Silicon) have no
  model key and always fall through to the tier default — a known gap, not
  a bug; there's no natural key to average by yet for that class of
  hardware.

Tests: `dispatcher_test.go` (no-starvation, load adjustment, EWMA
convergence) and `gpu_scores_test.go` (per-model averaging excludes
unmeasured agents, unknown-model lookup correctly reports "no history",
rejoin never resets a learned score).

## Open Questions / Future Optimization

- **Weighting the GPU-model average**: currently a simple mean across
  agents sharing a model; could weight by `sample_count`/`tasks_completed`
  per agent so a node with 200 completed tasks counts more than one with 2.
  Not done — not enough real multi-agent-per-model data yet to justify it.
- **Unified-memory hardware**: no GPU-model key exists for Apple Silicon
  etc., so it can never benefit from cross-agent history the way discrete
  GPUs can. Could key on something like reported RAM class or a
  self-declared hardware string instead — deferred.
- **DBOS backend**: `GPUScoreMonitor` is only wired into `NativeBackend`;
  the DBOS backend doesn't run it. Not a problem today since `dispatch_score`
  weighting only matters for `PickAgent`, which the DBOS backend doesn't use
  for its own scheduling — revisit if that changes.

This is a deliberately simple first cut — the goal is a working, measured
alternative to guessed tier weights, not a fully general auto-tuning system.
Optimize further once real multi-node data is available.
