-- PandaEye schema v1 (sqlite). Times are unix microseconds; NULL = not set.

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value BLOB NOT NULL
);

CREATE TABLE tenants (
	id                  TEXT PRIMARY KEY,
	name                TEXT NOT NULL,
	slug                TEXT NOT NULL UNIQUE,
	leniency            INTEGER NOT NULL DEFAULT 4,
	autoban             INTEGER NOT NULL DEFAULT 0,
	autokick            INTEGER NOT NULL DEFAULT 0,
	mitigations         INTEGER NOT NULL DEFAULT 1,
	enforcement         INTEGER NOT NULL DEFAULT 0,
	reputation_opt_in   INTEGER NOT NULL DEFAULT 0,
	discord_webhook_enc BLOB,
	discord_min_action  INTEGER NOT NULL DEFAULT 2,
	plan                TEXT NOT NULL DEFAULT 'free',
	max_servers         INTEGER NOT NULL DEFAULT 0,
	license_verified_at INTEGER,
	license_valid_until INTEGER,
	over_limit_since    INTEGER,
	created_at          INTEGER NOT NULL,
	updated_at          INTEGER NOT NULL
);

CREATE TABLE tenant_checks (
	tenant_id TEXT NOT NULL REFERENCES tenants (id),
	check_id  TEXT NOT NULL,
	enabled   INTEGER NOT NULL,
	overrides TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (tenant_id, check_id)
);

CREATE TABLE licenses (
	key_hmac   BLOB PRIMARY KEY,
	tenant_id  TEXT NOT NULL REFERENCES tenants (id),
	created_at INTEGER NOT NULL,
	revoked_at INTEGER
);
CREATE INDEX licenses_tenant ON licenses (tenant_id);

CREATE TABLE servers (
	tenant_id         TEXT NOT NULL REFERENCES tenants (id),
	id                TEXT NOT NULL,
	name              TEXT NOT NULL DEFAULT '',
	network_id        TEXT NOT NULL DEFAULT '',
	platform          INTEGER NOT NULL DEFAULT 0,
	platform_version  TEXT NOT NULL DEFAULT '',
	plugin_version    TEXT NOT NULL DEFAULT '',
	minecraft_version TEXT NOT NULL DEFAULT '',
	secret_hash       BLOB NOT NULL,
	instance_id       TEXT NOT NULL DEFAULT '',
	last_seen_at      INTEGER,
	last_batch_seq    INTEGER NOT NULL DEFAULT 0,
	created_at        INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
CREATE INDEX servers_created ON servers (tenant_id, created_at);

CREATE TABLE players (
	tenant_id      TEXT NOT NULL REFERENCES tenants (id),
	uuid           TEXT NOT NULL,
	last_name      TEXT NOT NULL,
	first_seen_at  INTEGER NOT NULL,
	last_seen_at   INTEGER NOT NULL,
	last_server_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (tenant_id, uuid)
);

CREATE TABLE player_names (
	tenant_id TEXT NOT NULL REFERENCES tenants (id),
	uuid      TEXT NOT NULL,
	name      TEXT NOT NULL,
	seen_at   INTEGER NOT NULL,
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
	action         INTEGER NOT NULL,
	category       INTEGER NOT NULL DEFAULT 0,
	score          REAL NOT NULL DEFAULT 0,
	confidence     REAL NOT NULL DEFAULT 0,
	reason         TEXT NOT NULL DEFAULT '',
	checks         TEXT NOT NULL DEFAULT '[]',
	evidence       TEXT NOT NULL DEFAULT '{}',
	clip_ids       TEXT NOT NULL DEFAULT '[]',
	mitigations    TEXT NOT NULL DEFAULT '[]',
	strength       REAL NOT NULL DEFAULT 0,
	ban_duration_s INTEGER NOT NULL DEFAULT 0,
	payload        BLOB,
	shadow         INTEGER NOT NULL DEFAULT 0,
	status         TEXT NOT NULL DEFAULT 'open',
	supersedes     TEXT NOT NULL DEFAULT '',
	appeal_code    TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL,
	delivered_at   INTEGER,
	acked_at       INTEGER,
	executed       INTEGER NOT NULL DEFAULT 0,
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
	active           INTEGER NOT NULL DEFAULT 1,
	reason           TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	overturned_at    INTEGER,
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
	created_at    INTEGER NOT NULL,
	decided_at    INTEGER,
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
	window_start INTEGER NOT NULL,
	window_end   INTEGER NOT NULL,
	created_at   INTEGER NOT NULL
);
CREATE INDEX labels_player ON labels (tenant_id, player_uuid);
CREATE INDEX labels_created ON labels (tenant_id, created_at);
CREATE INDEX labels_verdict ON labels (tenant_id, verdict_id);

CREATE TABLE clips (
	id          TEXT PRIMARY KEY,
	tenant_id   TEXT NOT NULL REFERENCES tenants (id),
	server_id   TEXT NOT NULL,
	player_uuid TEXT NOT NULL,
	start_us    INTEGER NOT NULL,
	end_us      INTEGER NOT NULL,
	codec       INTEGER NOT NULL,
	data        BLOB NOT NULL,
	created_at  INTEGER NOT NULL
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
	key_hash     BLOB NOT NULL UNIQUE,
	scopes       TEXT NOT NULL DEFAULT '["read"]',
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER,
	revoked_at   INTEGER
);
CREATE INDEX api_keys_tenant ON api_keys (tenant_id);

CREATE TABLE reputation_contrib (
	tenant_id   TEXT NOT NULL REFERENCES tenants (id),
	player_uuid TEXT NOT NULL,
	verdict_id  TEXT NOT NULL,
	weight      REAL NOT NULL,
	created_at  INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, player_uuid, verdict_id)
);
CREATE INDEX reputation_player ON reputation_contrib (player_uuid);
CREATE INDEX reputation_verdict ON reputation_contrib (tenant_id, verdict_id);

CREATE TABLE usage_daily (
	tenant_id    TEXT NOT NULL REFERENCES tenants (id),
	day          INTEGER NOT NULL,
	peak_players INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, day)
);

CREATE TABLE probe_stats (
	tenant_id TEXT NOT NULL REFERENCES tenants (id),
	server_id TEXT NOT NULL,
	minute    INTEGER NOT NULL,
	count     INTEGER NOT NULL,
	p50_us    INTEGER NOT NULL,
	p95_us    INTEGER NOT NULL,
	p99_us    INTEGER NOT NULL,
	max_us    INTEGER NOT NULL,
	PRIMARY KEY (tenant_id, server_id, minute)
);
CREATE INDEX probe_stats_minute ON probe_stats (minute);

CREATE TABLE audit_log (
	id         TEXT PRIMARY KEY,
	tenant_id  TEXT NOT NULL REFERENCES tenants (id),
	principal  TEXT NOT NULL,
	action     TEXT NOT NULL,
	target     TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL
);
CREATE INDEX audit_created ON audit_log (tenant_id, created_at);

CREATE TABLE erasures (
	tenant_id    TEXT NOT NULL REFERENCES tenants (id),
	player_uuid  TEXT NOT NULL,
	requested_at INTEGER NOT NULL,
	completed_at INTEGER,
	counts       TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (tenant_id, player_uuid)
);
