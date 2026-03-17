#!/usr/bin/env python3
"""Recipe 04: Benchmark multiple models on the grid."""
import asyncio, uuid, time
import httpx

HUB_URL = "http://localhost:9000"
MODELS = ["llama3", "mistral", "phi3"]
PROMPT = "Explain gradient descent in three sentences."
MAX_TOKENS = 150

async def submit_and_wait(client, model, task_id):
    start = time.monotonic()
    payload = {"task_id": task_id, "model": model, "prompt": PROMPT, "max_tokens": MAX_TOKENS}
    await client.post(f"{HUB_URL}/tasks", json=payload)
    deadline = time.monotonic() + 120; interval = 2.0
    while time.monotonic() < deadline:
        r = await client.get(f"{HUB_URL}/tasks/{task_id}"); data = r.json(); state = data.get("state","")
        if state == "COMPLETE":
            result = data.get("result", {}); elapsed = time.monotonic() - start
            return {
                "model": model, 
                "state": "COMPLETE",
                "output_tokens": result.get("output_tokens",0), 
                "latency_s": round(elapsed,2),
                "tps": round(result.get("output_tokens",0)/max(elapsed,0.001),1)
            }
        if state == "FAILED": return {"model": model, "state": "FAILED"}
        await asyncio.sleep(interval); interval = min(interval*1.3, 8.0)
    return {"model": model, "state": "TIMEOUT"}

async def main():
    print(f"Benchmarking {MODELS} on {HUB_URL}\n")
    async with httpx.AsyncClient(timeout=130.0) as client:
        results = await asyncio.gather(*[submit_and_wait(client, m, str(uuid.uuid4())) for m in MODELS])
    print(f"{'MODEL':<15} {'STATE':<10} {'TOKENS':>8} {'LATENCY':>10} {'TPS':>8}")
    print("-"*55)
    for r in results:
        print(f"{r['model']:<15} {r['state']:<10} {r.get('output_tokens',0):>8} {r.get('latency_s',0):>10.2f} {r.get('tps',0):>8.1f}")

if __name__ == "__main__":
    asyncio.run(main())
