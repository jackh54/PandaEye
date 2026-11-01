-- PandaEye schema v2 (postgres): moderation labels (LiteBans) and who decided a verdict.

-- A ban of a punishment plugin that the plugin classified as for cheating (kind 'ban_added', label
-- 'cheat') or lifted as wrong (kind 'ban_removed', label 'legit'). One row per entry and kind: the proxy
-- and every backend may report the same entry, and every plugin enable resends a backfill.
CREATE TABLE moderation_labels (
	tenant_id   TEXT NOT NULL REFERENCES tenants (id),
	source      TEXT NOT NULL,
	entry_id    TEXT NOT NULL,
	kind        TEXT NOT NULL,
	label       TEXT NOT NULL,
	player_uuid TEXT NOT NULL,
	ban_start   BIGINT NOT NULL,
	removed_at  BIGINT,
	permanent   BOOLEAN NOT NULL DEFAULT FALSE,
	executor    TEXT NOT NULL DEFAULT '',
	received_at BIGINT NOT NULL,
	applied_at  BIGINT,
	verdicts    BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (tenant_id, source, entry_id, kind)
);
CREATE INDEX moderation_labels_start ON moderation_labels (tenant_id, label, ban_start);
CREATE INDEX moderation_labels_player ON moderation_labels (tenant_id, player_uuid, ban_start);
CREATE INDEX moderation_labels_received ON moderation_labels (tenant_id, received_at);

-- '' = undecided, 'staff' (dashboard or API key), 'litebans' (a moderation label). Every confirmed or
-- overturned verdict so far was decided by staff.
ALTER TABLE verdicts ADD COLUMN decided_by TEXT NOT NULL DEFAULT '';
UPDATE verdicts SET decided_by = 'staff' WHERE status IN ('confirmed', 'overturned');
