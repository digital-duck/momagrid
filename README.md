# momagrid - Mixture-of-Model on Ollama

A decentralized LLM Inference Network via Structured Prompting - SPL



## Decentralized LLM Inference for the Heterogeneous Edge

**momagrid** is a decentralized, peer-to-peer inference network that orchestrates Large Language Models (LLMs) across a grid of heterogeneous, consumer-grade GPUs. By utilizing the **Structured Prompt Language (SPL)**, momagrid treats a local area network (LAN) or wide area network (WAN) as a unified generative utility, intelligently routing logical tasks to the most efficient hardware profile.

---

## 🚀 Key Features

* **SPL v3.0 Native:** Fully supports the `WITH BUDGET` and `GENERATE` clauses to ensure context-aware resource management.
* **Logical Parallelism (CTE-Routing):** Decomposes massive prompts into logical **Common Table Expressions (CTEs)**. Each CTE is encrypted and dispatched to an available node.
* **Mixture-of-Model (MoM) Orchestration:** Intelligently routes reasoning-heavy tasks to **Liquid Foundation Models (LFMs)** on low-VRAM spokes while utilizing larger models (e.g., Qwen 2.5) on high-VRAM hubs.
* **Zero-Trust Security:** Implements a Hub-managed **PKI** where data is encrypted with the Spoke’s public key, ensuring clear-text context never exists on the wire.
* **Hardware Democratization:** Validated on legacy Pascal-architecture GPUs (GTX 10 series), proving that high-reasoning AI is possible without H100 clusters.

---

## 🛠 Hardware Compatibility List (Validated)

The current stable build is optimized for **NVIDIA Pascal (GTX 10-series)** and newer. Our goal is to maintain a "No GPU Left Behind" policy.

| Hardware | VRAM | Role | Recommended Model |
| --- | --- | --- | --- |
| **GTX 1080 Ti** | 11GB | Knowledge Hub / Worker | Qwen 2.5 7B, Llama 3.1 8B |
| **GTX 1070** | 8GB | General Worker | Phi-3 Mini, Gemma-2 2B |
| **GTX 1050 Ti** | 4GB | **Reasoning Spoke** | **Liquid LFM 1.2B-Thinking** |
| **Jetson Orin** | 8GB+ | Edge Worker | LFM 1.2B-Instruct |

---

## 📦 Installation & Setup

### 1. Prerequisites

* **Ubuntu 22.04+** (Recommended) or Windows (WSL2).
* **Ollama 0.17.6+** installed on all nodes.
* **Python 3.10+** for the Hub-orchestrator.

### 2. Spoke Configuration

On each worker node, start the Spoke proxy and register with your Hub's API key:

```bash
# Set your Hub's address and your authorized API Key
export MOMAGRID_HUB="192.168.1.100"
export MOMAGRID_API_KEY="your-pki-authorized-key"

# Launch the Spoke agent
python3 -m momagrid.spoke --port 11434

```

### 3. Hub Initialization

The Hub acts as the SPL compiler and dispatcher.

```bash
# Initialize the Hub and generate PKI certificates
momagrid hub init --name "my-home-grid"

# Start the Hub
momagrid hub start

```

---

## 🔐 Security Architecture

momagrid utilizes an **Identity-Based Encryption** model.

1. **Authentication:** The Hub verifies Spoke identity via a GitHub-style API_KEY.
2. **Key Exchange:** The Hub and Spoke exchange RSA public keys during the initial handshake.
3. **Encrypted Dispatch:** Each SPL-generated CTE is wrapped in a "Vault" encrypted specifically for the target Spoke.

---

## 🤝 Contributing

We welcome contributions from the community! Please see our [CONTRIBUTING.md](https://www.google.com/search?q=CONTRIBUTING.md) for details on our development workflow and our **Zero-Footprint Privacy** requirements.

---

## 📜 License

This project is licensed under the **Apache License 2.0** - see the [LICENSE](https://www.google.com/search?q=LICENSE) file for details. The patent grant in this license ensures that momagrid remains a safe, open utility for the decentralized AI community.

---

**Developed by wen.gong.research@gmail.com

---
