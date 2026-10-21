-- PandaEye ClickHouse schema (optional telemetry, PE_CLICKHOUSE_DSN; ARCHITECTURE 5.7).
--
-- Run once against an empty server (the clickhouse-server image runs it from /docker-entrypoint-initdb.d):
--   clickhouse-client --multiquery < deploy/clickhouse/init.sql
--
-- Contract with the cloud telemetry sink (internal/telemetry), the erasure job and ml/:
--   * every table is a MergeTree-family table ordered by (tenant_id, time, ...) with the TTL of 5.7:
--     signals 30 d, verdicts 90 d, feature_windows 180 d, sessions 90 d;
--   * every table has `tenant_id` and `player`, because erasure (5.13) runs
--     ALTER TABLE <t> DELETE WHERE tenant_id = ? AND player = ?   on each of them;
--   * column names are the protobuf field names of stream.proto / wire.proto; enums are stored as their
--     proto value names (e.g. 'CATEGORY_HEURISTICS', 'ACTION_BAN');
--   * UUID columns take the nil UUID when a value is absent; times are UTC.
-- feature_windows is copied verbatim from ml/FEATURES.md section 6 (owned by ml/; change it there first).
-- The PandaEye user needs SELECT, INSERT and ALTER DELETE on pandaeye.*.

CREATE DATABASE IF NOT EXISTS pandaeye;

-- One row per Signal (stream.proto) emitted by a worker or forwarded from a plugin LocalFlag.
-- Probe signals (check_id "probe") are not stored.
CREATE TABLE IF NOT EXISTS pandaeye.signals
(
    tenant_id            LowCardinality(String),
    time                 DateTime64(6, 'UTC') COMMENT 'normalised event time: (event_time_us + origin.clock_offset_us) / 1e6',
    player               UUID,
    player_name          String,
    server_id            UUID,
    server_name          String,
    network_id           String,
    session_id           UUID COMMENT 'PlayerJoin.session of the producing session',
    check_id             LowCardinality(String),
    category             LowCardinality(String),
    points               Float64 COMMENT 'raw points before decay (CHECKS.md section 4)',
    confidence           Float64,
    event_time_us        UInt64 COMMENT 'plugin clock',
    gateway_recv_unix_us UInt64 COMMENT 'cloud clock',
    worker_unix_us       UInt64 COMMENT 'cloud clock',
    info                 String,
    data                 Map(LowCardinality(String), Float64),
    clip_id              String,
    local                Bool COMMENT 'forwarded plugin LocalFlag',
    replayed             Bool,
    hot_loaded           Bool,
    shadow_mode          Bool COMMENT 'origin.shadow_mode: the plugin local lock',
    platform             LowCardinality(String),
    INDEX idx_player player TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (tenant_id, time)
TTL toDateTime(time) + INTERVAL 30 DAY DELETE;

-- One row per VerdictRecord (stream.proto) written to PE_VERDICTS, including shadow verdicts
-- ("would have banned"). Re-processing reproduces the same verdict_id: count distinct verdict_id.
-- Not stored here: appeal_code (a credential) and evidence_json (kept in the SQL store).
CREATE TABLE IF NOT EXISTS pandaeye.verdicts
(
    tenant_id           LowCardinality(String),
    time                DateTime64(6, 'UTC') COMMENT 'VerdictRecord.created_unix_us',
    verdict_id          UUID,
    player              UUID,
    player_name         String,
    server_id           UUID,
    network_id          String,
    session_id          UUID,
    action              LowCardinality(String) COMMENT 'ACTION_ALERT .. ACTION_UNBAN',
    category            LowCardinality(String),
    score               Float64,
    confidence          Float64,
    checks              Array(LowCardinality(String)) COMMENT 'contributing check ids, strongest first',
    mitigations         Array(LowCardinality(String)),
    strength            Float64,
    shadow              Bool,
    ban_duration_s      UInt32,
    supersedes          String,
    reason              String,
    clip_ids            Array(String),
    reputation_eligible Bool,
    INDEX idx_player player TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (tenant_id, time)
TTL toDateTime(time) + INTERVAL 90 DAY DELETE;

-- One row per SessionMarker (stream.proto): join, quit and metadata updates. The client columns come
-- from the PlayerJoin the worker holds for the session; writers that do not have them leave the defaults.
CREATE TABLE IF NOT EXISTS pandaeye.sessions
(
    tenant_id            LowCardinality(String),
    time                 DateTime64(6, 'UTC') COMMENT 'SessionMarker.gateway_recv_unix_us',
    player               UUID,
    player_name          String,
    kind                 LowCardinality(String) COMMENT 'KIND_JOIN | KIND_QUIT | KIND_UPDATE',
    session_id           UUID,
    session_start_us     UInt64 COMMENT 'plugin clock',
    server_id            UUID,
    server_name          String,
    network_id           String,
    network_session      String,
    platform             LowCardinality(String),
    hot_loaded           Bool,
    exempt               Bool COMMENT 'bedrock or bypass: never judged',
    shadow_mode          Bool,
    client_protocol      Int32 DEFAULT -1 COMMENT 'PlayerJoin.protocol_version, -1 unknown',
    client_version       LowCardinality(String) DEFAULT '',
    brand                String DEFAULT '' COMMENT 'first minecraft:brand value, <= 64 chars',
    bedrock              Bool DEFAULT false,
    INDEX idx_player player TYPE bloom_filter(0.01) GRANULARITY 4
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (tenant_id, time)
TTL toDateTime(time) + INTERVAL 90 DAY DELETE;

-- ML feature windows: verbatim from ml/FEATURES.md section 6 (feature version 1), database-qualified.
CREATE TABLE IF NOT EXISTS pandaeye.feature_windows
(
    tenant_id        LowCardinality(String),
    player           UUID,
    server_id        UUID,
    session_id       UUID,
    window_start_us  Int64,
    window_end_us    Int64,
    time             DateTime('UTC') DEFAULT toDateTime(intDiv(window_start_us, 1000000), 'UTC'),
    client_protocol  Int32,
    tick_client      UInt8,
    hot_loaded       UInt8,
    feature_version  UInt16,
    features         Array(Float32),
    worker_unix_us   Int64,
    inserted_at      DateTime('UTC') DEFAULT now()
)
ENGINE = ReplacingMergeTree(worker_unix_us)
PARTITION BY toYYYYMM(time)
ORDER BY (tenant_id, time, player, session_id)
TTL time + INTERVAL 180 DAY DELETE;
