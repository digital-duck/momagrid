# Contributing to momagrid

Thank you for your interest in contributing to **momagrid**! We are building a decentralized LLM inference network that prioritizes computational sovereignty and hardware inclusivity.

By contributing to this project, you agree to abide by our Code of Conduct and the terms of the **Apache License 2.0**.

---

## 1. Our Core Philosophy: Zero-Trust Authenticity

The defining feature of **momagrid** is its cryptographic identity model. All agent nodes are treated as potentially untrusted environments. Contributions affecting the **Dispatcher**, **PickAgent** routing, or **identity** verification must adhere to these pillars:

* **Isolation of Secrets:** Private keys must never be exposed to the Hub or transmitted over the network. The `identity/` package handles Ed25519 keypair generation and signing — do not introduce external crypto dependencies.
* **Cryptographic Signatures:** Every agent handshake is verified against its Ed25519 public key (Grid-ID). New agent capabilities or handshake fields must include corresponding verification logic.
* **Hardcoded Keys Prohibited:** No keys, tokens, or secrets may appear in source code. Configuration is YAML-based at `~/.igrid/config.yaml`.

---

## 2. Development Workflow

### Prerequisites

* **Go 1.21+** — The project uses pure-Go SQLite (`modernc.org/sqlite`), so no CGO or external C libraries are required.
* **Ollama** — Install locally to run agents for integration testing.
* **A working `mg` binary** — Build it with `go build -buildvcs=false -o mg ./cmd/mg`.

### Getting Started

1. **Fork** the repository and create your branch from `main`.
2. **Install dependencies:**
   ```bash
   go mod tidy
   ```
3. **Build the CLI:**
   ```bash
   go build -buildvcs=false -o mg ./cmd/mg
   ```
4. **Local Hub Setup:** Start a hub, register an agent, and verify connectivity:
   ```bash
   ./mg hub start
   ./mg join --hub http://localhost:9000
   ./mg status
   ```

---

## 3. Submission Guidelines

### The "Authenticity Check" for PRs

When submitting a Pull Request (PR), ensure your code does not bypass the authenticity layer. We will specifically review for:

* Hardcoded keys or tokens (strictly prohibited).
* Changes to `internal/identity/` (require extensive peer review).
* Modifications to `PickAgent()` scheduling logic in `internal/hub/` — document the impact on VRAM-tier routing (Platinum/Gold/Silver/Bronze).

### Commit Messages

We follow a structured commit format to keep the history scannable:

* `feat(hub):` New features for the Hub server or Dispatcher.
* `feat(cli):` New CLI commands or flags.
* `feat(schema):` Wire protocol changes (tasks, handshakes, pulses).
* `fix(security):` Fixes related to identity verification or key handling.
* `fix(dispatch):` Task routing or agent selection fixes.
* `docs(spl):` Documentation for the Structured Prompt Language.

### Testing

There is no formal `go test` suite. All PRs should be validated using:

* **CLI test runner:** `./mg test --prompts prompts.json`
* **SPL recipes:** `./mg run cookbook/<recipe>/*.spl`
* **Manual integration:** spin up a hub, register agents with different VRAM profiles, and verify dispatch routing behaves as expected.

When touching dispatch logic, include a note in the PR describing the node topology used for testing (model names, VRAM tiers, agent counts).

---

## 4. Architectural Contributions

If you are proposing a change to the **task routing logic** (`PickAgent`), **cluster peering**, or the **SPL runner**:

1. Open an **Issue** first to discuss the approach and trade-offs.
2. Provide a before/after comparison of dispatch behavior on a heterogeneous node map (e.g., 24GB vs 8GB vs 4GB VRAM profiles).
3. Ensure the change remains compatible with the **Apache 2.0** license and does not introduce CGO dependencies.

---

## 5. Licensing

All contributions will be licensed under the **Apache License 2.0**. By submitting a PR, you grant a permanent, non-exclusive, worldwide, royalty-free patent license to all users of **momagrid**.

---

**Thank you for helping us build the most accessible and distributed LLM inference network in the world!**
