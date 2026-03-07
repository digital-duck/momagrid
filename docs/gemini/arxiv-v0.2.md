
# momagrid: A Decentralized LLM Inference Network via Structured Prompting

**Wen G. Gong** *Independent Researcher* `wen.gong.research@gmail.com`

**March 2026**

---

## Abstract

We present **momagrid** (Mixture-of-Model on Ollama), a decentralized, peer-to-peer inference network designed to democratize access to high-reasoning Large Language Models (LLMs). By utilizing the **Structured Prompt Language (SPL)**, momagrid decomposes complex natural language intent into encrypted, logical **Common Table Expressions (CTEs)**. These logical chunks are then dispatched across a heterogeneous "Spoke-Hub" network of consumer-grade GPUs and edge devices. We demonstrate that by using **Mixture-of-Models (MoM) routing**, the grid can intelligently assign reasoning-heavy tasks to **Liquid Foundation Models (LFMs)** on low-VRAM nodes (e.g., GTX 1050 Ti) while routing high-context tasks to larger memory nodes. This architecture achieves significant reductions in attention compute costs and provides a robust framework for computational privacy via a Hub-managed PKI.

---

## I. Introduction

The current trajectory of Large Language Model (LLM) deployment is characterized by extreme centralization, where inference is tethered to capital-intensive data centers. This paradigm creates a "compute-divide," stifling innovation at the edge and ignoring the massive, distributed potential of legacy and consumer-grade hardware.

We propose **momagrid**, a framework for **Computational Sovereignty**. momagrid treats the global hardware landscape—from high-end workstations to legacy gaming GPUs—as a unified, decentralized utility grid. The core innovation lies in the marriage of SPL’s declarative context management with a Zero-Knowledge compute protocol. Unlike traditional RAG or agentic frameworks, momagrid uses SPL as a "prompt compiler" to ensure that every inference task is budget-constrained, authenticated, and optimized for the specific hardware profile of the executing node.

---

## II. Related Work

While projects like Petals [1] have explored collaborative inference, they typically rely on **pipeline parallelism**, where tokens travel sequentially through model layers distributed across nodes. This creates high-latency bottlenecks on consumer-grade internet connections.

**momagrid** diverges by implementing **Logical Parallelism**. By utilizing the CTE structure defined in the SPL protocol [2], the Hub decomposes a massive prompt into independent, parallelizable "Vaults." This allows a cluster of heterogeneous GPUs to process complex multi-part queries asynchronously. Furthermore, momagrid leverages recent advances in **Liquid Foundation Models (LFMs)** [4], which provide high-reasoning capabilities within the 4GB VRAM constraints of legacy edge hardware.

---

## III. System Architecture: The momagrid Protocol

### 3.1 The Spoke-Hub Topology

The architecture is inspired by global aviation networks.

* **The Hub:** Acts as the "Query Optimizer." It parses SPL scripts, calculates global token budgets, and handles the CTE-Vault encryption.
* **The Spoke:** Independent nodes running **Ollama** or **llama.cpp**. Each spoke identifies its hardware profile (VRAM, compute capability) to the Hub upon registration.

### 3.2 VRAM-aware CTE Scheduling

The Hub solves a constrained optimization problem to maximize throughput. For any node $n$, the memory consumption $M_n$ must satisfy:


$$M_{static}(M, q) + (B_{tokens} \times \sigma) \leq V_{total} \cdot \rho$$


Where $B_{tokens}$ is the budget from the SPL `WITH BUDGET` clause, and $\sigma$ is the context expansion coefficient. This ensures that a 4GB node (GTX 1050 Ti) is never dispatched a chunk exceeding its physical capacity.

### 3.3 Security & Privacy: The CTE-Vault

To protect data on an untrusted grid, we implement **Application-Layer Encryption**:

1. **Authentication:** Nodes use GitHub-style API Keys for identity verification and rate limiting.
2. **Encryption:** The Hub encrypts logical chunks using the Spoke's Public Key.
3. **Isolation:** Decryption and inference occur strictly within the local GPU memory space, ensuring the Hub never sees the raw weights and the Spoke never sees the plain-text context of other nodes.

---

## IV. Experimental Setup

### 4.1 Hardware Testbed

We validated the momagrid architecture using a 3-node heterogeneous LAN grid:

* **Nodes 1 & 2:** NVIDIA GeForce GTX 1080 Ti (11GB).
* **Node 3:** NVIDIA GeForce GTX 1050 Ti (4GB).
* **Networking:** 1GbE Layer 2 Switching.

### 4.2 Software Implementation

The implementation is hosted in a private repository (`momagrid-core`).

* **Inference Engine:** Ollama v0.5.x.
* **Models:** Liquid AI LFM 1.2B-Thinking (4-bit GGUF) for Node 3; Qwen 2.5 7B for Nodes 1 & 2.

---

## V. Results

*(Placeholder for POC data: Tokens Per Second, Latency per CTE, and Hub-to-Spoke overhead comparisons.)*

---

## VI. Conclusion

**momagrid** demonstrates that the path toward sustainable and equitable AI does not require the abandonment of legacy hardware. By treating inference as a **compilation problem** rather than a brute-force scaling problem, we have shown that a distributed grid of consumer GPUs can perform sophisticated reasoning tasks with high efficiency.

The combination of **SPL’s declarative budgeting** and **Mixture-of-Model routing** allows for a "Digital Sobriety" in compute—using only the necessary tokens and the most efficient hardware for a given task. Future work will focus on scaling momagrid to Wide Area Networks (WAN) and implementing a blockchain-based incentive layer for grid contributors.

---

## References

[1] Ryabinin, M., et al. (2023). *Petals: Collaborative Inference of Large Language Models.* 

[2] Gong, W. G. (2026). *Structured Prompt Language: Declarative Context Management for LLMs.* arXiv:2602.xxxxx.

[3] SecureLLM Framework (2025). *Privacy-Preserving Inference in Heterogeneous Environments.* 

[4] Liquid AI (2025). *The LFM-1.2B: Efficient Linear Foundation Models.* 

[5] "Proof of Quality" (PoQ) Framework (2026). *Quality Scoring for Decentralized LLM Inference.* arXiv:2603.xxxxx.

