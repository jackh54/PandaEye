-- PandaEye schema v1 (postgres). Times are unix microseconds; NULL = not set.

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value BYTEA NOT NULL
);

CREATE TABLE tenants (
	id                  TEXT PRIMARY KEY,
	name                TEXT NOT NULL,
	slug                TEXT NOT NULL UNIQUE,
	leniency            BIGINT NOT NULL DEFAULT 4,
	autoban             BOOLEAN NOT NULL DEFAULT FALSE,
	autokick            BOOLEAN NOT NULL DEFAULT FALSE,
	mitigations         BOOLEAN NOT NULL DEFAULT TRUE,
	enforcement         BOOLEAN NOT NULL DEFAULT FALSE,
	reputation_opt_in   BOOLEAN NOT NULL DEFAULT FALSE,
	discord_webhook_enc BYTEA,
	discord_min_action  BIGINT NOT NULL DEFAULT 2,
	plan                TEXT NOT NULL DEFAULT 'free',
	max_servers         BIGINT NOT NULL DEFAULT 0,
	license_verified_at BIGINT,
	license_valid_until BIGINT,
	over_limit_since    BIGINT,
	created_at          BIGINT NOT NULL,
	updated_at          BIGINT NOT NULL
);

CREATE TABLE tenant_checks (
	tenant_id TEXT NOT NULL REFERENCES tenants (id),
	check_id  TEXT NOT NULL,
	enabled   BOOLEAN NOT NULL,
	overrides JSONB NOT NULL DEFAULT '{}',
	PRIMARY KEY (tenant_id, check_id)
);

CREATE TABLE licenses (
	key_hmac   BYTEA PRIMARY KEY,
	tenant_id  TEXT NOT NULL REFERENCES tenants (id),
	created_at BIGINT NOT NULL,
	revoked_at BIGINT
);
CREATE INDEX licenses_tenant ON licenses (tenant_id);

CREATE TABLE servers (
	tenant_id         TEXT NOT NULL REFERENCES tenants (id),
	id                TEXT NOT NULL,
	name              TEXT NOT NULL DEFAULT '',
	network_id        TEXT NOT NULL DEFAULT '',
	platform          BIGINT NOT NULL DEFAULT 0,
	platform_version  TEXT NOT NULL DEFAULT '',
	plugin_version    TEXT NOT NULL DEFAULT '',
	minecraft_version TEXT NOT NULL DEFAULT '',
	secret_hash       BYTEA NOT NULL,
	instance_id       TEXT NOT NULL DEFAULT '',
	last_seen_at      BIGINT,
	last_batch_seq    BIGINT NOT NULL DEFAULT 0,
	created_at        BIGINT NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
CREATE INDEX servers_created ON servers (tenant_id, created_at);

CREATE TABLE players (
	tenant_id      TEXT NOT NULL REFERENCES tenants (id),
	uuid           TEXT NOT NULL,
	last_name      TEXT NOT NULL,
	first_seen_at  BIGINT NOT NULL,
	last_seen_at   BIGINT NOT NULL,
	last_server_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (tenant_id, uuid)
);

CREATE TABLE player_names (
	tenant_id TEXT NOT NULL REFERENCES tenants (id),
	uuid      TEXT NOT NULL,
	name      TEXT NOT NULL,
	seen_at   BIGINT NOT NULL,
	PRIMARY KEY (tenant_id, uuid, name)
);
CREATE INDEX player_names_lower ON player_names (tenant_id, lower(name));

CREATE TABLE verdicts (
	id             TEXT PRIMARY KEY,
	tenant_id      TEXT NOT NULL REFERENCES tenants (id),
	server_id      TEXT NOT NULL,
	network_id     TEXT NOT NULL DEFAULT '',
	player_uuid    TEXT NOT NULL,
	player_name    TEXT NOT NULL DEFAULT '',
	session_id     TEXT NOT NULL DEFAULT '',
	action         BIGINT NOT NULL,
	category       BIGINT NOT NULL DEFAULT 0,
	score          DOUBLE PRECISION NOT NULL DEFAULT 0,
	confidence     DOUBLE PRECISION NOT NULL DEFAULT 0,
	reason         TEXT NOT NULL DEFAULT '',
	checks         JSONB NOT NULL DEFAULT '[]',
	evidence       JSONB NOT NULL DEFAULT '{}',
	clip_ids       JSONB NOT NULL DEFAULT '[]',
	mitigations    JSONB NOT NULL DEFAULT '[]',
	strength       DOUBLE PRECISION NOT NULL DEFAULT 0,
	ban_duration_s BIGINT NOT NULL DEFAULT 0,
	payload        BYTEA,
	shadow         BOOLEAN NOT NULL DEFAULT FALSE,
	status         TEXT NOT NULL DEFAULT 'open',
	supersedes     TEXT NOT NULL DEFAULT '',
	appeal_code    TEXT NOT NULL DEFAULT '',
	created_at     BIGINT NOT NULL,
	delivered_at   BIGINT,
	acked_at       BIGINT,
	executed       BOOLEAN NOT NULL DEFAULT FALSE,
	ack_note       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX verdicts_created ON verdicts (tenant_id, created_at);
CREATE INDEX verdicts_pending ON verdicts (tenant_id, server_id, status, created_at);
CREATE INDEX verdicts_player ON verdicts (tenant_id, player_uuid, created_at);
CREATE INDEX verdicts_session ON verdicts (tenant_id, session_id);

-- Which clips a verdict references (clip retention); maintained with clips, not verdicts.
CREATE TABLE verdict_clips (
	tenant_id  TEXT NOT NULL,
	verdict_id TEXT NOT NULL,
	clip_id    TEXT NOT NULL,
	PRIMARY KEY (tenant_id, verdict_id, clip_id)
);
CREATE INDEX verdict_clips_clip ON verdict_clips (tenant_id, clip_id);

CREATE TABLE bans (
	verdict_id       TEXT PRIMARY KEY REFERENCES verdicts (id) ON DELETE CASCADE,
	tenant_id        TEXT NOT NULL REFERENCES tenants (id),
	server_id        TEXT NOT NULL,
	player_uuid      TEXT NOT NULL,
	active           BOOLEAN NOT NULL DEFAULT TRUE,
	reason           TEXT NOT NULL DEFAULT '',
	created_at       BIGINT NOT NULL,
	overturned_at    BIGINT,
	overturned_by    TEXT NOT NULL DEFAULT '',
	overturn_note    TEXT NOT NULL DEFAULT '',
	unban_verdict_id TEXT NOT NULL DEFAULT '',
	unban_state      TEXT NOT NULL DEFAULT 'none'
);
CREATE INDEX bans_tenant ON bans (tenant_id, created_at);
CREATE INDEX bans_player ON bans (tenant_id, player_uuid);

CREATE TABLE appeals (
	id            TEXT PRIMARY KEY,
	tenant_id     TEXT NOT NULL REFERENCES tenants (id),
	verdict_id    TEXT NOT NULL,
	player_uuid   TEXT NOT NULL,
	message       TEXT NOT NULL,
	contact       TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL DEFAULT 'open',
	created_at    BIGINT NOT NULL,
	decided_at    BIGINT,
	decided_by    TEXT NOT NULL DEFAULT '',
	decision_note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX appeals_verdict ON appeals (tenant_id, verdict_id);
CREATE INDEX appeals_created ON appeals (tenant_id, created_at);
CREATE INDEX appeals_player ON appeals (tenant_id, player_uuid);

CREATE TABLE labels (
	id           TEXT PRIMARY KEY,
	tenant_id    TEXT NOT NULL REFERENCES tenants (id),
	player_uuid  TEXT NOT NULL,
	verdict_id   TEXT NOT NULL DEFAULT '',
	label        TEXT NOT NULL,
	source       TEXT NOT NULL,
	window_start BIGINT NOT NULL,
	window_end   BIGINT NOT NULL,
	created_at   BIGINT NOT NULL
);
CREATE INDEX labels_player ON labels (tenant_id, player_uuid);
CREATE INDEX labels_created ON labels (tenant_id, created_at);
CREATE INDEX labels_verdict ON labels (tenant_id, verdict_id);

CREATE TABLE clips (
	id          TEXT PRIMARY KEY,
	tenant_id   TEXT NOT NULL REFERENCES tenants (id),
	server_id   TEXT NOT NULL,
	player_uuid TEXT NOT NULL,
	start_us    BIGINT NOT NULL,
	end_us      BIGINT NOT NULL,
	codec       BIGINT NOT NULL,
	data        BYTEA NOT NULL,
	created_at  BIGINT NOT NULL
);
CREATE INDEX clips_created ON clips (tenant_id, created_at);
CREATE INDEX clips_player ON clips (tenant_id, player_uuid);

CREATE TABLE clip_subjects (
	clip_id     TEXT NOT NULL REFERENCES clips (id) ON DELETE CASCADE,
	player_uuid TEXT NOT NULL,
	tenant_id   TEXT NOT NULL REFERENCES tenants (id),
	PRIMARY KEY (clip_id, player_uuid)
);
CREATE INDEX clip_subjects_player ON clip_subjects (tenant_id, player_uuid);

CREATE TABLE api_keys (
	id           TEXT PRIMARY KEY,
	tenant_id    TEXT NOT NULL REFERENCES tenants (id),
	name         TEXT NOT NULL DEFAULT '',
	key_hash     BYTEA NOT NULL UNIQUE,
	scopes       JSONB NOT NULL DEFAULT '["read"]',
	created_at   BIGINT NOT NULL,
	last_used_at BIGINT,
	revoked_at   BIGINT
);
CREATE INDEX api_keys_tenant ON api_keys (tenant_id);

CREATE TABLE reputation_contrib (
	tenant_id   TEXT NOT NULL REFERENCES tenants (id),
	player_uuid TEXT NOT NULL,
	verdict_id  TEXT NOT NULL,
	weight      DOUBLE PRECISION NOT NULL,
	created_at  BIGINT NOT NULL,
	PRIMARY KEY (tenant_id, player_uuid, verdict_id)
);
CREATE INDEX reputation_player ON reputation_contrib (player_uuid);
CREATE INDEX reputation_verdict ON reputation_contrib (tenant_id, verdict_id);

CREATE TABLE usage_daily (
	tenant_id    TEXT NOT NULL REFERENCES tenants (id),
	day          BIGINT NOT NULL,
	peak_players BIGINT NOT NULL,
	PRIMARY KEY (tenant_id, day)
);

CREATE TABLE probe_stats (
	tenant_id TEXT NOT NULL REFERENCES tenants (id),
	server_id TEXT NOT NULL,
	minute    BIGINT NOT NULL,
	count     BIGINT NOT NULL,
	p50_us    BIGINT NOT NULL,
	p95_us    BIGINT NOT NULL,
	p99_us    BIGINT NOT NULL,
	max_us    BIGINT NOT NULL,
	PRIMARY KEY (tenant_id, server_id, minute)
);
CREATE INDEX probe_stats_minute ON probe_stats (minute);

CREATE TABLE audit_log (
	id         TEXT PRIMARY KEY,
	tenant_id  TEXT NOT NULL REFERENCES tenants (id),
	principal  TEXT NOT NULL,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	detail     JSONB NOT NULL DEFAULT '{}',
	created_at BIGINT NOT NULL
);
CREATE INDEX audit_created ON audit_log (tenant_id, created_at);

CREATE TABLE erasures (
	tenant_id    TEXT NOT NULL REFERENCES tenants (id),
	player_uuid  TEXT NOT NULL,
	requested_at BIGINT NOT NULL,
	completed_at BIGINT,
	counts       JSONB NOT NULL DEFAULT '{}',
	PRIMARY KEY (tenant_id, player_uuid)
);
