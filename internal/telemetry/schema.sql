CREATE TABLE IF NOT EXISTS telemetry_events (
    time           TIMESTAMPTZ NOT NULL,
    run_id         TEXT NOT NULL,
    submission_id  TEXT NOT NULL,
    order_id       TEXT NOT NULL,
    intended_at_ns BIGINT NOT NULL,
    received_at_ns BIGINT NOT NULL,
    latency_us     BIGINT NOT NULL,
    acked          BOOLEAN NOT NULL,
    violation      TEXT NOT NULL DEFAULT ''
);

SELECT create_hypertable('telemetry_events', 'time', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS idx_telemetry_submission
    ON telemetry_events (submission_id, time DESC);

CREATE TABLE IF NOT EXISTS run_scores (
    submission_id  TEXT PRIMARY KEY,
    team_id        TEXT NOT NULL,
    attempt        INT NOT NULL,
    run_id         TEXT NOT NULL,
    language       TEXT NOT NULL DEFAULT 'unknown',
    ticks_sent     BIGINT NOT NULL DEFAULT 0,
    ticks_acked    BIGINT NOT NULL DEFAULT 0,
    peak_tps       FLOAT NOT NULL DEFAULT 0,
    capacity_tps   FLOAT NOT NULL DEFAULT 0,
    p50_us         BIGINT NOT NULL DEFAULT 0,
    p90_us         BIGINT NOT NULL DEFAULT 0,
    p99_us         BIGINT NOT NULL DEFAULT 0,
    correctness    FLOAT NOT NULL DEFAULT 0,
    composite_score FLOAT NOT NULL DEFAULT 0,
    critical_flag  BOOLEAN NOT NULL DEFAULT FALSE,
    completed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_run_scores_team
    ON run_scores (team_id, composite_score DESC);