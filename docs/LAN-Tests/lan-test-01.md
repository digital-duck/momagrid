```db
sudo psql "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"  -c "ALTER TABLE agents ADD COLUMN IF NOT EXISTS public_key TEXT NOT NULL DEFAULT '';"    


sudo psql "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable" -c "ALTER TABLE tasks ADD COLUMN IF NOT EXISTS callback_url TEXT NOT NULL DEFAULT '';"  
```

```bash
(base) papagame@papa-game:~/projects/digital-duck/momahub.go$ mg join --name wen
Using hub: http://192.168.0.177:9000
Agent ID    : agent-dc4d1a52
Operator    : duck
Hub         : http://192.168.0.177:9000
Models      : llama3, phi3:latest, phi4-mini:latest, qwen3-embedding:4b, qwen3-embedding:0.6b, embeddinggemma:latest, phi4:latest, qwen3:latest, gemma3:latest, bge-m3:latest, gemma3:12b, qwen2.5-coder:latest, nomic-embed-text:latest, mathstral:latest, codegemma:latest, starcoder2:7b, snowflake-arctic-embed2:latest, deepseek-r1:latest, mistral:latest, qwen2.5:latest, granite-code:8b, qwen2-math:latest, llama3.1:latest, codegeex4:latest, duckdb-nsql:latest, llama3.2-vision:latest, tinyllama:latest, llama3.2:latest, llama3:latest, deepseek-coder-v2:latest
Pull mode   : false
Signed      : true

Identity    : /home/papagame/.igrid/agent_key.pem
Public key  : DQbQYc0/Nvi3sk1gCspO…

Joined grid  hub=hub-984e6191  tier=BRONZE  status=ONLINE
Message: Welcome to the grid.

Pulsing... (Ctrl+C to leave)

```

## Same machine for hub and agent

```bash
# terminal 1
go build -buildvcs=false -o mg ./cmd/mg


mg hub up --db "postgres://mguser:mgpass@localhost/momagrid?sslmode=disable"
Starting hub on 0.0.0.0:9000
  Mode: OPEN (any agent can join)
  Max concurrent tasks per agent: 3

  Other machines can join with:
    mg join http://192.168.0.177:9000

2026/03/13 09:24:37 hub hub-4cafdc78 started  url=http://192.168.0.177:9000  db=postgres://mguser:mgpass@localhost/momagrid?sslmode=disable  admin=false  max_concurrent=3

```

```bash
# terminal 2
mg join --name wen

Using hub: http://192.168.0.177:9000
Agent ID    : agent-917e4340
Operator    : duck
Hub         : http://192.168.0.177:9000
Models      : llama3, phi3:latest, phi4-mini:latest, qwen3-embedding:4b, qwen3-embedding:0.6b, embeddinggemma:latest, phi4:latest, qwen3:latest, gemma3:latest, bge-m3:latest, gemma3:12b, qwen2.5-coder:latest, nomic-embed-text:latest, mathstral:latest, starcoder2:7b, codegemma:latest, snowflake-arctic-embed2:latest, deepseek-r1:latest, mistral:latest, qwen2.5:latest, granite-code:8b, qwen2-math:latest, llama3.1:latest, codegeex4:latest, duckdb-nsql:latest, llama3.2-vision:latest, tinyllama:latest, llama3.2:latest, llama3:latest, deepseek-coder-v2:latest
Pull mode   : false
Signed      : true

Identity    : /home/papagame/.igrid/agent_key.pem
Public key  : DQbQYc0/Nvi3sk1gCspO…

Joined grid  hub=hub-4cafdc78  tier=BRONZE  status=ONLINE
Message: Welcome to the grid.

Pulsing... (Ctrl+C to leave)
  listening for pushed tasks on 0.0.0.0:9010
  pulse  agent=agent-917e43  ts=09:26:11


```

```bash
# terminal 3

(base) papagame@papa-game:~$ mg status
Hub: hub-4cafdc78  Status: ok  Agents: 1
(base) papagame@papa-game:~$ mg agents
NAME             AGENT_ID                               TIER       STATUS        TPS
--------------------------------------------------------------------------------------
wen              agent-917e4340                         BRONZE     ONLINE        0.0
(base) papagame@papa-game:~$ mg tasks
TASK_ID                                STATE        MODEL               
------------------------------------------------------------------------
1c263120-e3fc-4b1e-983a-2b3328984788   COMPLETE     llama3              
2e42c265-ee40-44d2-8367-3f87e0804058   FAILED       llama3              
5a978600-2710-4371-82b9-442dccc1f65c   FAILED       llama3              
translate-chi-e88a81                   FAILED       llama3              
translate-fre-550e6d                   FAILED       llama3              
translate-spa-e4b5e7                   FAILED       llama3              
translate-chi-5ca37a                   FAILED       llama3              
translate-spa-59472b                   FAILED       llama3              
translate-fre-141f55                   FAILED       llama3              
(base) papagame@papa-game:~$ mg submit "Explain transformer attention in one sentence"
Task submitted: a957b539-2323-4f84-b794-e64e99b44c48

Transformer attention is a mechanism that allows the model to focus on specific parts of the input sequence (such as words or tokens) and weight their importance relative to others, based on how closely they are related to each other, in order to generate contextualized representations.
[model=llama3 tokens=17+53 latency=2638ms]

```


```bash
(base) papagame@papa-game:~/projects/digital-duck/momahub.go$ mg run cookbook/05_rag_on_grid/rag_query.spl

--- [rag_answer] ---
Based on the provided context, the key benefits of hub-and-spoke inference are:

1. Reduced hardware requirements for clients - inference runs on GPU nodes, not the requester.
2. Parallel execution across multiple GPU nodes for high throughput.

These two points highlight the primary advantages of using a hub-and-spoke architecture for inference tasks.

[model=llama3 tokens=160+67 latency=35026ms]
```