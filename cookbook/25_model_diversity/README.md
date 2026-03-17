# Recipe 25 — Model Diversity

Benchmarks multiple models across 6 domain-specific tasks (general knowledge, reasoning,
math, code generation, multilingual, summarisation) and prints a summary table of
pass rate and average TPS per model.

## Usage

```bash
# Quick probe — just verify which models are online (1 task per model)
go run cookbook/25_model_diversity/model_diversity.go --probe

# Probe a specific subset of models
go run cookbook/25_model_diversity/model_diversity.go \
  --probe --models llama3,mistral,phi3

# Full 6-domain benchmark against all 14 default models
go run cookbook/25_model_diversity/model_diversity.go

# Full benchmark, skip models that error rather than stopping
go run cookbook/25_model_diversity/model_diversity.go --skip-errors

# Custom model list
go run cookbook/25_model_diversity/model_diversity.go \
  --models llama3,qwen2.5-coder,deepseek-r1
```

Results are saved to `out/model_diversity_<timestamp>.json`.

## VRAM and Ollama model swapping

With a single GPU (e.g. GTX 1080 Ti, 11GB VRAM), Ollama can only keep **one large
model loaded at a time**. When the next model does not fit alongside the current one,
Ollama evicts the current model from VRAM before loading the new one.

**Each evict + load cycle adds 30–90 seconds of overhead** depending on model size.
With 14 models, this means recipe 25 is typically the slowest recipe in the batch by
a significant margin.

Practical implications:

| Situation | Behaviour |
|-----------|-----------|
| Model fits in remaining VRAM | Loads without evicting — fast |
| Model requires evicting the current one | 30–90s swap overhead per model |
| Model is too large to fit at all | Ollama returns an error — recipe marks it as ERROR and moves on |

**Only pull the models you intend to test.** Running `--probe` first is recommended
to confirm which models are actually available before running the full 6-domain suite:

```bash
go run cookbook/25_model_diversity/model_diversity.go --probe
```

Then run the full benchmark only for models that responded:

```bash
go run cookbook/25_model_diversity/model_diversity.go \
  --models llama3,mistral,phi3 --skip-errors
```

## Models benchmarked (defaults)

```
llama3, llama3.1, mistral, mathstral,
qwen3, qwen2.5, qwen2.5-coder, qwen2-math,
deepseek-r1, deepseek-coder-v2,
gemma3, phi4, phi4-mini, phi3
```

## Benchmark domains

| ID | Domain | Max tokens |
|----|--------|-----------|
| general | General knowledge | 200 |
| reasoning | Logical reasoning | 150 |
| math | Mathematics (show working) | 300 |
| code | Python code generation | 350 |
| multilingual | Translate to 3 languages | 200 |
| summarise | One-sentence summarisation | 100 |
