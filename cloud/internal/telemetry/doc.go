// Package telemetry writes signals, verdicts, sessions and ML feature windows to ClickHouse
// (PE_CLICKHOUSE_DSN, schema deploy/clickhouse/init.sql) in batches, only the feature windows to hourly
// CSV files (PE_FEATURES_DIR, `all` only: Files, layout in ml/README.md), or nowhere when telemetry is
// disabled (ARCHITECTURE 5.7). It also runs the analytics part of a GDPR erasure (5.13).
//
// # Getting the sink
//
// Every service of a process shares one sink:
//
//	sink, err := telemetry.New(d) // d *app.Deps; a no-op Sink when PE_CLICKHOUSE_DSN and PE_FEATURES_DIR are empty
//
// The sink is closed (pending rows flushed, at most FlushTimeout) when d.Close runs. Depend on the
// narrow interface you need (SignalSink, VerdictSink, SessionSink, FeatureSink, ErasureSink) so tests
// can pass fakes; Nop implements all of them.
//
// # Writers (who records what)
//
// Each row type has exactly one writer so that a row is never stored twice:
//
//   - RecordSignal: the verdict service, once per Signal when it first consumes it live from
//     pe.signals.<NN> (never during its rebuild replay). Probe signals are not stored.
//   - RecordVerdict: the verdict service's store writer, once per VerdictRecord when the SQL upsert
//     inserted it (re-processed records are not recorded again). The api records the UNBAN /
//     UNMITIGATE verdicts it creates only if it writes them to the SQL store itself instead of
//     publishing a VerdictRecord to PE_VERDICTS.
//   - RecordSession: the worker, once per SessionMarker it publishes, passing the session's
//     PlayerJoin (client protocol, version, brand, bedrock; nil leaves the column defaults).
//   - RecordFeatureWindow: the worker, once per finalised 30 s window (ml/FEATURES.md); feature_windows
//     is a ReplacingMergeTree, so a window re-emitted after a handover replaces the earlier row.
//   - ErasePlayer: the api erasure job (DELETE /api/v1/players/{uuid}).
//
// # Semantics
//
// Record* never block and never fail: rows go to a bounded queue (Options.QueueSize, default
// 100 000 rows) and are dropped with a counter when it is full (metric
// pandaeye_dropped_total{reason="telemetry_overflow"}, Dropped()). A background loop inserts each
// table's rows in one batch every second or as soon as 10 000 rows of a table are pending; a failed
// insert drops that batch (reason "telemetry_insert"), so ClickHouse outages never grow memory.
// Rows are copied when recorded: callers may reuse their messages afterwards.
//
// ErasePlayer first inserts everything already queued, then runs
// ALTER TABLE <t> DELETE WHERE tenant_id = ? AND player = ? on signals, verdicts, sessions and
// feature_windows. It returns the first error; the caller retries the erasure job on error.
//
// The file sink follows the same rules (queue of DefaultFileQueueSize rows, a failed write drops its
// rows as "telemetry_insert") and ignores signals, verdicts and sessions. Its ErasePlayer writes the
// queued rows, appends the erasure to <dir>/erasures.jsonl (fsynced) and returns: later rows of the
// player from before the erasure are not written, and a background loop rewrites the .csv.gz files
// without them (the current hour's file when it is compressed). Rows from after the erasure are kept,
// as the worker keeps new data. The journal is re-read at start, so an interrupted rewrite resumes.
package telemetry
