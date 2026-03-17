# Recipe 27 — Model Health Check

Measures per-model load time and inference TPS across all online agents by sending
two probe requests per model (warmup → load, probe → hot inference).

## Usage

```bash
go run cookbook/27_model_health/model_health.go
go run cookbook/27_model_health/model_health.go --hub http://192.168.0.177:9000
go run cookbook/27_model_health/model_health.go --interval 60   # repeat every 60 min
```

## Cluster Hardware (as of 2026-03-14)

| AGENT | TIER | GPU | VRAM | NOTES |
|---|---|---|---|---|
| papa-game | GOLD | — | >8GB | Primary node, runs most large models |
| wengong | GOLD | — | >8GB | Runs qwen3-vl, lfm2.5, translagegemma |
| ducklover1 | SILVER | GTX 4060 | 8GB | Smaller models only; larger models will offload to RAM |

SILVER tier is assigned to ducklover1 due to 8GB VRAM. Models larger than ~7B parameters
risk VRAM exhaustion and CPU offload on this node.

## Known Model Categories

### Embedding Models (skipped — no inference probe)

Embedding models do not support text generation and cannot be probed via `/tasks`.
They are detected by name pattern (`embed`, `bge-`) and reported as `EMBED` in the
results table. The hub has no `/embeddings` endpoint yet.

Affected models as of 2026-03-14:
- `bge-m3:latest`
- `embeddinggemma:latest`
- `nomic-embed-text:latest`
- `nomic-embed-text-v2-moe:latest`
- `qwen3-embedding:0.6b`
- `qwen3-embedding:4b`
- `snowflake-arctic-embed2:latest`

### Vision / Multimodal Models (fail with "task failed")

`llama3.2-vision:latest` fails the text-only probe. Vision models require image input
alongside the prompt; a plain text completion task is rejected by Ollama.
Needs a separate vision probe (image + prompt) or exclusion similar to embedding models.

## Run Results — 2026-03-14 (hub: 192.168.0.177:9000)

Two agents online: **papa-game** (GOLD) and **wengong** (GOLD).

### Problem Models

| MODEL | AGENT | LOAD_TIME | INFER_MS | TPS | ISSUE |
|---|---|---|---|---|---|
| `qwen2.5-coder:latest` | papa-game | 110537ms | 11347ms | 0.4 | Extremely slow — likely VRAM exhaustion, offloading to CPU/RAM |
| `qwen3:latest` | wengong | 14967ms | 66703ms | 0.3 | Extremely slow — same root cause suspected |
| `llama3.1:latest` | wengong | 14912ms | 30050ms | 0.1 | Extremely slow — same root cause suspected |

All three show TPS < 1.0 and inference times in the tens of seconds, consistent with
model weights being partially or fully offloaded to system RAM due to insufficient VRAM.

### Top Performers

| MODEL | AGENT | TPS | INFER_MS |
|---|---|---|---|
| `tinyllama:latest` | papa-game | 146.0 | 137ms |
| `phi3:latest` | papa-game | 90.1 | 222ms |
| `deepseek-ocr:latest` | wengong | 73.3 | 273ms |
| `lfm2.5-thinking:latest` | wengong | 58.3 | 103ms |
| `mathstral:latest` | papa-game | 54.5 | 110ms |
| `duckdb-nsql:latest` | papa-game | 54.9 | 164ms |
| `qwen3:4b` | wengong | 51.5 | 388ms |

### Full Results

| AGENT | MODEL | LOAD_TIME | INFER_MS | TPS | STATUS |
|---|---|---|---|---|---|
| papa-game | codegeex4:latest | 41654ms | 147ms | 27.2 | OK |
| papa-game | codegemma:latest | 41648ms | 154ms | 26.0 | OK |
| papa-game | deepseek-coder-v2:latest | 58873ms | 92ms | 43.5 | OK |
| papa-game | deepseek-r1:latest | 33045ms | 479ms | 41.8 | OK |
| papa-game | duckdb-nsql:latest | 33046ms | 164ms | 54.9 | OK |
| papa-game | gemma3:12b | 84693ms | 488ms | 8.2 | OK |
| papa-game | gemma3:latest | 67474ms | 205ms | 19.5 | OK |
| papa-game | granite-code:8b | 33044ms | 156ms | 32.1 | OK |
| papa-game | llama3 | 50261ms | 151ms | 26.5 | OK |
| papa-game | llama3.2:latest | 25873ms | 118ms | 33.9 | OK |
| papa-game | llama3:latest | 4414ms | 146ms | 27.4 | OK |
| papa-game | mathstral:latest | 58867ms | 110ms | 54.5 | OK |
| papa-game | mistral:latest | 41651ms | 100ms | 40.0 | OK |
| papa-game | phi3:latest | 25872ms | 222ms | 90.1 | OK |
| papa-game | phi4-mini:latest | 33043ms | 140ms | 28.6 | OK |
| papa-game | phi4:latest | 67483ms | 355ms | 11.3 | OK |
| papa-game | qwen2-math:latest | 41649ms | 219ms | 18.3 | OK |
| papa-game | qwen2.5-coder:latest | 110537ms | 11347ms | 0.4 | SLOW |
| papa-game | qwen2.5:latest | 101956ms | 423ms | 9.5 | OK |
| papa-game | starcoder2:7b | 34469ms | 621ms | 32.2 | OK |
| papa-game | tinyllama:latest | 10766ms | 137ms | 146.0 | OK |
| wengong | deepseek-ocr:latest | 19890ms | 273ms | 73.3 | OK |
| wengong | lfm2.5-thinking:latest | 4419ms | 103ms | 58.3 | OK |
| wengong | llama3.1:latest | 14912ms | 30050ms | 0.1 | SLOW |
| wengong | qwen3-vl:4b | 10810ms | 422ms | 47.4 | OK |
| wengong | qwen3-vl:8b | 19939ms | 564ms | 35.5 | OK |
| wengong | qwen3:4b | 7305ms | 388ms | 51.5 | OK |
| wengong | qwen3:latest | 14967ms | 66703ms | 0.3 | SLOW |
| wengong | translagegemma:12b | 19911ms | 518ms | 9.7 | OK |
| wengong | translagegemma:latest | 10758ms | 396ms | 12.6 | OK |

## Bug History

| Date | Bug | Fix |
|---|---|---|
| 2026-03-14 | `supported_models` stored as JSON text in DB; type assertion to `[]interface{}` always failed → "No agents online" | Unmarshal string via `json.Unmarshal` (same pattern as `dispatcher.go`) |
| 2026-03-14 | Embedding models probed via `/tasks` → "task failed" | Detect by name pattern (`embed`, `bge-`), skip with `EMBED` status |
| 2026-03-14 | Vision models (`llama3.2-vision`) fail text-only probe | Document as known issue; needs dedicated vision probe or exclusion |
