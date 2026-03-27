# momagrid (Go)

Go implementation of the **momagrid** i-grid — hub server + CLI client. Decentralized, peer-to-peer LLM inference network.

This repository contains the production-grade Go implementation of the momagrid protocol, optimized for high concurrency and scalability. It is fully compatible with the Python-based momagrid ecosystem.

## Binary

| Binary | Purpose |
|--------|---------|
| `mg` | **Unified CLI:** Hub server + all client commands |

## Prerequisites (Go Installation)

**momagrid** requires Go 1.22 or higher.

### Ubuntu / Linux
```bash
# Easiest way via snap
sudo snap install go --classic

# Or manual installation
wget https://go.dev/dl/go1.22.1.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.1.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

## Build

```bash
go mod tidy
go build -buildvcs=false -o mg ./cmd/mg
```

## Cross-compile for Linux (from Windows/Mac)

```bash
# Linux x86_64
GOOS=linux GOARCH=amd64 go build -buildvcs=false -o mg ./cmd/mg

# Linux ARM (Oracle Cloud free tier)
GOOS=linux GOARCH=arm64 go build -buildvcs=false -o mg ./cmd/mg
```

## Setup (Recommended)

To run `mg` from anywhere without typing `./`, link the binary to your local bin directory:

```bash
mkdir -p ~/.local/bin
ln -sf $(pwd)/mg ~/.local/bin/mg

# Ensure ~/.local/bin is in your PATH (add to ~/.bashrc if needed)
export PATH=$PATH:$HOME/.local/bin
```

## Database Options

**momagrid** supports two database backends:

1.  **SQLite (Default):** Zero-configuration. Perfect for local testing and small grids. No C libraries needed.
2.  **PostgreSQL:** Recommended for production and high-concurrency tasks (like parallel translations). 

### Running the Hub with SQLite (Default)

SQLite requires no installation — it's built into the binary as a pure-Go library.

```bash
# 1. Build
go build -buildvcs=false -o mg ./cmd/mg

# 2. Run (database file is created automatically at .igrid/hub.sqlite3)
mg hub up --port 9000  # default to SQLite
```

That's it. The database file is created at `.igrid/hub.sqlite3` in the current directory on first run. No configuration needed.

If your config (`~/.igrid/config.yaml`) was previously set to PostgreSQL, reset it to SQLite:

```bash
mg config --set hub.db_path=.igrid/hub.sqlite3
```

To use a custom path:

```bash
mg hub up --port 9000 --db /path/to/my.db
```

### Setup PostgreSQL on Ubuntu

If you don't have Postgres installed:

```bash
sudo apt update
sudo apt install postgresql postgresql-contrib

# Start and enable the service
sudo systemctl status postgresql
sudo systemctl start postgresql
sudo systemctl enable postgresql

# Create a database and user
sudo -u postgres psql -c "CREATE USER mguser WITH PASSWORD 'mgpass';"
sudo -u postgres psql -c "CREATE DATABASE momagrid OWNER mguser;"
```

### Running the Hub with PostgreSQL

Use the `--db` flag with a standard PostgreSQL connection string:

```bash
# 1. Build: 
go build -buildvcs=false -o mg ./cmd/mg

# 2. Migrate: 
mg hub migrate --from .igrid/hub.sqlite3 --to "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"

# 3. Run: 
# Format: postgres://<user>:<password>@<host>:<port>/<dbname>?sslmode=disable
mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"  --port 9000
```

### Migrating from SQLite to PostgreSQL

If you have an existing SQLite database and want to move its history to PostgreSQL:

1. **Stop the hub** (if it's running).
2. **Run the migration command**:
   ```bash
   mg hub migrate --from .igrid/hub.sqlite3 --to "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"
   ```
3. **Restart the hub** using the Postgres connection string.

## Usage

```bash
# Start the hub
mg hub up --port 9000

# Stop the hub
# If in foreground: Press Ctrl+C
# If in background: pkill mg

# Query the grid
mg status
mg agents
mg tasks --detail
mg rewards
mg logs --follow

# Submit a task
mg submit "What is 10 !" --model qwen3

mg submit "Explain quantum computing" --model llama3.1:8b

# Export results
mg export --label "run-1" --output results.json

# Run test suite
mg test --prompts prompts.json --concurrency 4 --repeat 3

# Admin mode
mg hub up --admin
mg hub pending
mg hub approve <agent_id>

# Cluster
mg peer add http://192.168.1.20:9000
mg peer list

# Config (reads ~/.igrid/config.yaml)
mg config
mg config --set operator_id=myname
```

## Documentation

- **[User Guide](docs/USER-GUIDE.md)** — LAN setup, two-machine grid, SPL scripts, mgui web UI (default port 9080), compute tiers, monitoring commands, troubleshooting, and cookbook recipes.

## Hub Server Flags

```
mg hub up [flags]
```
  --host            Listen address (default: 0.0.0.0)
  --port            Listen port (default: 9000)
  --hub-url         Public hub URL (default: auto-detect LAN IP)
  --db              SQLite database path (default: .igrid/hub.sqlite3)
  --operator-id     Operator ID (default: duck)
  --api-key         API key for agent registration
  --admin           Enable admin mode (agents require verification)
  --max-concurrent  Max concurrent tasks per agent (default: 3)
```

## Security — Ed25519 Agent Identity

Each agent generates a unique Ed25519 keypair on first start (`~/.igrid/agent_key.pem`). The public key is included in `JoinRequest`. On every pulse, the agent signs `agentID:timestamp` — proving it holds the private key. The hub verifies the signature and rejects pulses that fail verification.

**Why this matters for public grids:** Without signing, any node can claim any `operator_id` and collect rewards for another operator's identity. Ed25519 prevents this at zero wire overhead (32-byte keys, 64-byte signatures, stdlib only).

Agents without a key (e.g. the Python implementation, trusted LAN nodes) are still accepted — the signature check is skipped when no public key is on file.

```
internal/identity/identity.go   LoadOrCreate, Sign, Verify, MakeChallenge
```

## Cluster Forwarding — Webhook Callback

When Hub A has no eligible agent for a task, it forwards the task to a peer hub (Hub B) via `POST /tasks`. Previously, Hub A polled `GET /tasks/{id}` on Hub B until the task completed — introducing up to 10 seconds of extra latency.

Now Hub A sets `callback_url = hubA/cluster/result` in the forwarded `TaskRequest`. When Hub B completes the task, it POSTs the result to Hub A's `/cluster/result` endpoint immediately. Hub A resolves the task in <1s instead of waiting for the next poll cycle. Polling is kept as a fallback if the callback is not received within the task timeout.

## Architecture

```
cmd/mg/main.go               CLI entry point (subcommand dispatch)
internal/identity/identity.go Ed25519 keypair: generate, load, sign, verify
internal/cli/config.go       Config management (~/.igrid/config.yaml)
internal/cli/http.go         HTTP client helpers (getJSON, postJSON)
internal/cli/hub.go          Hub commands (up, pending, approve, reject)
internal/cli/client.go       Client commands (status, agents, tasks, submit, etc.)
internal/cli/peer.go         Peer commands (add, list)
internal/cli/test.go         Test runner (concurrent batch, report generation)
internal/schema/             Go structs matching the momagrid wire protocol
internal/hub/app.go          HTTP server — all 20 endpoints incl. /cluster/result
internal/hub/db.go           DB init: SQLite (WAL) + PostgreSQL, migrations
internal/hub/state.go        GridState: all DB operations + AgentPublicKey lookup
internal/hub/dispatcher.go   Task dispatch + fireCallback (webhook to origin hub)
internal/hub/cluster.go      Hub-to-hub peering + webhook-first ForwardTask
internal/hub/monitor.go      Background goroutines: eviction, cluster sync, dispatch
internal/hub/verification.go Agent auto-approval pipeline
internal/hub/sse.go          SSE task streaming for pull-mode (NAT-traversal) agents
```

## PostgreSQL — Connection Pool Tuning

For grids with 50+ simultaneous agents, increase the pool size:

```bash
# Default is 20 open connections — sufficient for most LAN deployments.
# For public grids, tune via Postgres max_connections and pool size:
mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable&pool_max_conns=50"
```

SQLite uses WAL mode automatically — no tuning needed for up to ~30 concurrent agents.

## Dependencies

| Dependency | Purpose |
|-----------|---------|
| `github.com/go-chi/chi/v5` | HTTP router |
| `github.com/google/uuid` | UUID generation |
| `modernc.org/sqlite` | Pure-Go SQLite (no CGO) |
| `gopkg.in/yaml.v3` | Config file (YAML) |

## Compatibility

The Go binaries are fully compatible with:
- Python agents (`mg join`)
- Python CLI (`mg status`, `mg tasks`, etc.)
- Streamlit dashboard (`mg-ui`)
- SPL runner (`mg run`)
- Test runner (`mg test`)

All communicate via HTTP JSON — the hub's implementation language is transparent.
