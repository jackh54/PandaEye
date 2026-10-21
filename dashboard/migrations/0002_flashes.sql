-- One-time results shown after a redirect (post-redirect-get): a new API key's plaintext, a new invite link,
-- erasure counts. Each row is bound to one session (sha256 of its id) and its payload is AES-GCM encrypted
-- with a key derived from the raw session id, which D1 never stores. Rows are deleted when read and expire
-- after 10 minutes (hourly cleanup).
CREATE TABLE flashes (
  id           TEXT PRIMARY KEY,
  session_hash TEXT NOT NULL,
  kind         TEXT NOT NULL,
  data         TEXT NOT NULL,
  expires_at   INTEGER NOT NULL
);
CREATE INDEX flashes_expiry ON flashes (expires_at);
