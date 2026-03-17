CREATE TABLE IF NOT EXISTS hub_config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS peer_hubs (
    hub_id      TEXT PRIMARY KEY,
    hub_url     TEXT NOT NULL,
    operator_id TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'ACTIVE',
    added_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen   TEXT
);

CREATE TABLE IF NOT EXISTS operators (
    operator_id   TEXT PRIMARY KEY,
    joined_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    total_tasks   BIGINT NOT NULL DEFAULT 0,
    total_tokens  BIGINT NOT NULL DEFAULT 0,
    total_credits DOUBLE PRECISION NOT NULL DEFAULT 0.0
);

CREATE TABLE IF NOT EXISTS agents (
    agent_id         TEXT PRIMARY KEY,
    operator_id      TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    host             TEXT NOT NULL,
    port             INTEGER NOT NULL,
    status           TEXT NOT NULL DEFAULT 'ONLINE',
    tier             TEXT NOT NULL DEFAULT 'BRONZE',
    gpus             TEXT NOT NULL DEFAULT '[]',
    cpu_cores        INTEGER NOT NULL DEFAULT 0,
    ram_gb           DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    supported_models TEXT NOT NULL DEFAULT '[]',
    current_tps      DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    tasks_completed  INTEGER NOT NULL DEFAULT 0,
    pull_mode        INTEGER NOT NULL DEFAULT 0,
    joined_at        TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_pulse       TEXT,
    public_key       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS tasks (
    task_id       TEXT PRIMARY KEY,
    state         TEXT NOT NULL DEFAULT 'PENDING',
    model         TEXT NOT NULL,
    prompt        TEXT NOT NULL,
    system        TEXT NOT NULL DEFAULT '',
    max_tokens    INTEGER NOT NULL DEFAULT 1024,
    temperature   DOUBLE PRECISION NOT NULL DEFAULT 0.7,
    min_tier      TEXT    NOT NULL DEFAULT 'BRONZE',
    min_vram_gb   DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    timeout_s     INTEGER NOT NULL DEFAULT 300,
    priority      INTEGER NOT NULL DEFAULT 1,
    agent_id      TEXT,
    peer_hub_id   TEXT,
    content       TEXT,
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    latency_ms    DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    callback_url  TEXT    NOT NULL DEFAULT '',
    retries       INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    created_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state);

CREATE TABLE IF NOT EXISTS jobs (
    job_id       TEXT PRIMARY KEY,
    state         TEXT NOT NULL DEFAULT 'QUEUED',
    model         TEXT NOT NULL,
    prompt        TEXT NOT NULL,
    system        TEXT NOT NULL DEFAULT '',
    max_tokens    INTEGER NOT NULL DEFAULT 4096,
    min_tier      TEXT    NOT NULL DEFAULT 'BRONZE',
    deadline      TIMESTAMP WITH TIME ZONE,
    notify        TEXT    NOT NULL DEFAULT '{}',
    max_retries   INTEGER NOT NULL DEFAULT 3,
    result        TEXT,
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);

CREATE TABLE IF NOT EXISTS pulse_log (
    id              SERIAL PRIMARY KEY,
    agent_id        TEXT NOT NULL,
    status          TEXT NOT NULL,
    gpu_util_pct    DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    vram_used_gb    DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    current_tps     DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    tasks_completed INTEGER NOT NULL DEFAULT 0,
    logged_at       TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS reward_ledger (
    id               SERIAL PRIMARY KEY,
    operator_id      TEXT NOT NULL,
    agent_id         TEXT NOT NULL,
    task_id          TEXT NOT NULL,
    tokens_generated INTEGER NOT NULL DEFAULT 0,
    credits_earned   DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    recorded_at      TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS watchlist (
    id          SERIAL PRIMARY KEY,
    entity_type TEXT    NOT NULL,
    entity_id   TEXT    NOT NULL,
    reason      TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL DEFAULT 'SUSPENDED',
    created_at  TEXT    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TEXT,
    UNIQUE(entity_type, entity_id)
);

DROP VIEW IF EXISTS reward_summary;
CREATE VIEW reward_summary AS
SELECT operator_id,
       COUNT(*)              AS total_tasks,
       SUM(tokens_generated) AS total_tokens,
       SUM(credits_earned)   AS total_credits
FROM reward_ledger
GROUP BY operator_id;
