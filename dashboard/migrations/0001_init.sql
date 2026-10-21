-- PandaEye dashboard schema (ARCHITECTURE section 7). All times are unix milliseconds.

CREATE TABLE users (
  id                  TEXT PRIMARY KEY,
  tenant_id           TEXT NOT NULL,
  username            TEXT NOT NULL UNIQUE CHECK (username = lower(username)),
  password_hash       TEXT NOT NULL,
  role                TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'staff', 'viewer')),
  created_at          INTEGER NOT NULL,
  disabled            INTEGER NOT NULL DEFAULT 0,
  password_changed_at INTEGER NOT NULL
);
CREATE INDEX users_tenant ON users (tenant_id);

-- Single-use invites; only sha256(token) is stored.
CREATE TABLE invites (
  token_hash TEXT PRIMARY KEY,
  tenant_id  TEXT NOT NULL,
  role       TEXT NOT NULL CHECK (role IN ('admin', 'staff', 'viewer')),
  expires_at INTEGER NOT NULL,
  used_at    INTEGER,
  used_by    TEXT,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX invites_tenant ON invites (tenant_id);

-- Sessions; the cookie carries the raw id, the table only sha256(id).
CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  csrf         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL
);
CREATE INDEX sessions_user ON sessions (user_id);
CREATE INDEX sessions_expiry ON sessions (expires_at);

-- Rate limiting. Fixed windows: count + window_start. Per-username backoff: count = consecutive
-- failures, window_start = last failure, locked_until = earliest next attempt. Keys never contain raw IPs.
CREATE TABLE login_attempts (
  key          TEXT PRIMARY KEY,
  count        INTEGER NOT NULL,
  window_start INTEGER NOT NULL,
  locked_until INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE audit_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  tenant_id  TEXT,
  principal  TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT,
  detail     TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX audit_log_tenant ON audit_log (tenant_id, created_at);
