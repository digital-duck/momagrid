# GEMINI.md - momagrid (Go Implementation)

## Project Overview
**momagrid** (formerly momagrid) is a decentralized, peer-to-peer LLM inference network designed to democratize access to high-reasoning models. It implements a "Spoke-Hub" topology where a central Hub coordinates tasks across a heterogeneous grid of consumer-grade GPUs and edge devices.

This Go implementation is the high-performance production version of the protocol, optimized for concurrency and scalability. It serves as the primary implementation for the upcoming arXiv paper.

### Key Concepts
- **Mixture-of-Model (MoM) Routing:** Intelligent task assignment based on hardware profiles (VRAM, compute capability) and model requirements.
- **Structured Prompt Language (SPL):** Uses SPL to decompose complex intents into parallelizable chunks (CTEs).
- **VRAM-aware Scheduling:** Ensures tasks are only dispatched to nodes that can physically handle the model and context window.
- **Computational Sovereignty:** Enables the use of legacy hardware (e.g., GTX 1050 Ti) for advanced reasoning via Liquid Foundation Models (LFMs).

### Core Technologies
- **Language:** Go 1.22+ (optimized for high-concurrency goroutines)
- **HTTP Framework:** `github.com/go-chi/chi/v5`
- **Database:** SQLite (Initial persistence, migrating to PostgreSQL later)
- **Configuration:** YAML (`~/.igrid/config.yaml`)
- **Compatibility:** Fully compatible with the Python-based `momagrid` ecosystem.

## Building and Running

### Build Commands
The primary binary is `mg`, which includes both the hub and client functionality.
```bash
# Build the unified momagrid CLI
go build -o mg ./cmd/mg
```

### Running the Grid
```bash
# Start the hub server
./mg up

# Start the hub in admin mode (requires agent verification)
./mg up --admin
```

### Grid Operations
```bash
# Check grid status
./mg status

# List online agents
./mg agents

# Submit a task
./mg submit "Explain quantum computing" --model llama3.1:8b

# View task rewards
./mg rewards
```

## Development Conventions

### Coding Style
- **Concurrency First:** Leverage Go's goroutine model for non-blocking task delivery and agent monitoring.
- **Pure-Go SQLite:** Use `modernc.org/sqlite` to maintain a CGO-free build process for easy cross-platform deployment.
- **Surgical Transitions:** Maintain strict API compatibility with the Python prototype while improving internal performance.

### Roadmap
1.  **Rebranding:** Complete transition from `mg` to `mg` / `momagrid`.
2.  **PKI Integration:** Implement public-private key infrastructure for agent authentication (replacing simple API keys).
3.  **PostgreSQL Migration:** Transition from SQLite to PostgreSQL for production-scale grid management.
4.  **arXiv Release:** This Go implementation will be the official reference for the `momagrid` protocol release.
