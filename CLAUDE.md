# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**momagrid** (`mg`) is a decentralized, peer-to-peer LLM inference network using a Spoke-Hub topology. It coordinates LLM tasks across heterogeneous GPUs with VRAM-aware routing, Ed25519 identity verification, and webhook-based cluster forwarding between hubs.

## Build Commands

```bash
# Build the CLI
go build -buildvcs=false -o mg ./cmd/mg

# Cross-compile examples
GOOS=linux GOARCH=arm64 go build -buildvcs=false -o mg ./cmd/mg
GOOS=linux GOARCH=amd64 go build -buildvcs=false -o mg ./cmd/mg

# Dependency management
go mod tidy
```

There is no Makefile, formal test suite, or linter configuration. Testing is done via the CLI test runner (`mg test --prompts prompts.json`), SPL recipes (`mg run cookbook/<recipe>/*.spl`), and cookbook examples.

## Architecture

**Single binary** from `cmd/`:
- `mg` — Unified CLI with 15 commands (hub server + client operations)

**`internal/` packages:**
- `hub/` — HTTP server (chi/v5 router, 20 endpoints), database layer, task dispatcher, cluster peering, SSE, rate limiting, agent monitoring
- `cli/` — 10 command modules covering hub ops, client queries, join, config, test, peer, SPL runner, migration, watchlist
- `schema/` — Wire protocol structs (tasks, handshakes, pulses, cluster, enums)
- `identity/` — Ed25519 keypair generation, signing, and verification

**Database:** Driver-agnostic abstraction supporting SQLite (default, WAL mode) and PostgreSQL. SQL placeholders and time formats are handled via `GridState.q(n)` and `GridState.now()`. Schemas are embedded via `go:embed` from `hub_ddl_sqlite.sql` and `hub_ddl_postgresql.sql`.

**Concurrency:** Three background goroutines — `AgentMonitor` (evicts stale agents, 30s), `ClusterMonitor` (syncs capabilities + forwards tasks, 60s), `DispatchLoop` (task scheduling). Shutdown via channel signaling.

**Task dispatch:** `PickAgent()` selects agents by model support (with `:latest` normalization), compute tier (Platinum > Gold > Silver > Bronze based on VRAM), and active task count (pre-fetched via single GROUP BY query).

## Key Conventions

- Configuration is YAML-based at `~/.igrid/config.yaml` with sensible defaults
- Standard Go `log` package for logging (no structured logging library)
- JSON error responses with appropriate HTTP status codes
- Pure-Go SQLite via `modernc.org/sqlite` (no CGO required)
- Ed25519 signatures use stdlib `crypto/ed25519` — no external crypto dependencies
- Agent status flow: ONLINE → PENDING_APPROVAL (admin mode) → ONLINE → BUSY → OFFLINE (90s without pulse)

## Dependencies

chi/v5 (HTTP router), google/uuid, lib/pq (PostgreSQL), yaml.v3 (config), modernc.org/sqlite (pure-Go SQLite). ~4,400 lines of Go across 25 files.
