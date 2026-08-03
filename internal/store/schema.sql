-- Firecrawl 多 Key 反向代理：SQLite schema
-- 启动时幂等执行（CREATE TABLE IF NOT EXISTS），表结构定型前直接改本文件。

-- 上游 Firecrawl Key
CREATE TABLE IF NOT EXISTS upstream_keys (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT    NOT NULL,
    api_key           TEXT    NOT NULL UNIQUE,
    key_suffix        TEXT    NOT NULL,             -- 末 4 位，展示用
    enabled           INTEGER NOT NULL DEFAULT 1,   -- 管理员手动启停
    state             TEXT    NOT NULL DEFAULT 'available',
                                                    -- available | cooling | exhausted | invalid
    cooldown_until    INTEGER,                      -- unix 秒，state=cooling 时有效
    credits_total     INTEGER,
    credits_remaining INTEGER,
    credits_synced_at INTEGER,
    request_count     INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_used_at      INTEGER,
    created_at        INTEGER NOT NULL
);

-- 下游代理 API Key
CREATE TABLE IF NOT EXISTS proxy_keys (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    key_hash      TEXT    NOT NULL UNIQUE,   -- sha256(明文) 的十六进制
    key_prefix    TEXT    NOT NULL,          -- 明文前 12 位，展示用
    revoked       INTEGER NOT NULL DEFAULT 0,
    request_count INTEGER NOT NULL DEFAULT 0,
    last_used_at  INTEGER,
    created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proxy_keys_hash ON proxy_keys(key_hash);

-- 异步任务 → 上游 Key 粘连
CREATE TABLE IF NOT EXISTS job_routes (
    job_id          TEXT    PRIMARY KEY,
    upstream_key_id INTEGER NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE,
    kind            TEXT    NOT NULL,        -- crawl | batch_scrape | extract
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_job_routes_expires ON job_routes(expires_at);

-- 调用统计：按「小时桶 × 上游 Key × 状态类别」聚合，不存请求明细。
-- 高频写入走内存缓冲 + 批量 upsert 刷盘（同 request_count），避免每请求一次 DB 写。
CREATE TABLE IF NOT EXISTS call_stats_buckets (
    hour            INTEGER NOT NULL,  -- unix 秒，对齐到小时起点
    upstream_key_id INTEGER NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE,
    status_class    INTEGER NOT NULL,  -- 1=2xx 2=3xx 3=4xx 4=5xx
    calls           INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, upstream_key_id, status_class)
);
CREATE INDEX IF NOT EXISTS idx_call_stats_hour ON call_stats_buckets(hour);

-- 面板会话
CREATE TABLE IF NOT EXISTS admin_sessions (
    token_hash TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
