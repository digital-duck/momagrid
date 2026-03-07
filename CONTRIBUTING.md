# Contributing to momagrid

Thank you for your interest in contributing to **momagrid**! We are building a decentralized LLM inference network that prioritizes computational sovereignty and hardware inclusivity.

By contributing to this project, you agree to abide by our Code of Conduct and the terms of the **Apache License 2.0**.

---

## 1. Our Core Philosophy: Zero-Trust Authenticity

The defining feature of **momagrid** is the "API_KEY" authenticity model. We treat all Spoke nodes as potentially untrusted environments. Therefore, all code contributions—especially those affecting the **Dispatcher** or the **Spoke Proxy**—must adhere to these three pillars:

* **Isolation of Secrets:** Private keys must never be exposed to the Hub or the network. Spoke-side logic must utilize secure OS-level keychains or hardware enclaves.
* **Cryptographic Signatures:** Every inference response must be signed by the Spoke's unique Grid-ID. Contributions that introduce new model support (e.g., new GGUF types) must include the corresponding signature-verification logic.
* **CTE Containerization:** Data must only be decrypted inside the specific logical chunk (CTE) container during the forward pass.

---

## 2. Development Workflow

### Prerequisites

* **Ollama:** Ensure you have the latest version of Ollama installed locally.
* **SPL v3.0 Parser:** Familiarize yourself with the [Structured Prompt Language specification](https://www.google.com/search?q=https://github.com/your-repo/spl-spec).
* **Hardware:** Access to at least one NVIDIA GPU (Pascal architecture or newer preferred) for local grid simulation.

### Getting Started

1. **Fork** the repository and create your branch from `main`.
2. **Install dependencies:** `pip install -r requirements-dev.txt`
3. **Local Grid Setup:** Use the provided `scripts/simulate_grid.sh` to spin up a virtual Hub and two local Spoke proxies on your machine.

---

## 3. Submission Guidelines

### The "Authenticity Check" for PRs

When submitting a Pull Request (PR), ensure your code does not bypass the authenticity layer. We will specifically review for:

* Hardcoded keys or tokens (strictly prohibited).
* Changes to the `crypto/` module (require extensive peer review).
* Modifications to the **VRAM-to-Token** scheduling math.

### Commit Messages

We follow a structured commit format to keep the history scannable:

* `feat(hub):` New features for the Hub/Dispatcher.
* `feat(spoke):` New Spoke-side capabilities (e.g., LFM support).
* `fix(security):` Fixes related to the PKI or API_KEY logic.
* `docs(spl):` Documentation for the Structured Prompt Language.

### Testing

All PRs must pass the `pytest` suite, including the **Multi-Node Integration Test**, which simulates an encrypted CTE dispatch across a heterogeneous node map (11GB vs 4GB VRAM profiles).

---

## 4. Architectural Contributions

If you are proposing a change to the **Mixture-of-Model (MoM)** routing logic or the **SPL Compiler**:

1. Open an **Issue** first to discuss the mathematical foundation.
2. Provide a benchmark showing the impact on **Inference Latency** or **Tokens Per Joule**.
3. Ensure the change remains compatible with the **Apache 2.0** license.

---

## 5. Licensing

All contributions will be licensed under the **Apache License 2.0**. By submitting a PR, you grant a permanent, non-exclusive, worldwide, royalty-free patent license to all users of **momagrid**.

---

**Thank you for helping us build the most accessible and distributed LLM inference network in the world!**

