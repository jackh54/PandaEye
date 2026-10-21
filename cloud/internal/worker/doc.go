// Package worker is the partition consumer of pe.events.<NN> (ARCHITECTURE 5.5): sessions, player
// state, the cloud checks, clips, feature windows, signals and session markers to pe.signals.<NN>,
// plus `pandaeye replay`.
//
// Flow per leased partition (partition.go): a new consumer epoch (stream.ResetConsumer), a silent
// warm start over the last 60 s before its start sequence, then the ordered consume loop. Each
// StreamBatch goes through the partition's engine (engine.go): stream-sequence and batch-seq
// de-duplication, sessions keyed by (tenant, player, origin server), View.Apply and one Detector per
// registered checks.Factory per session, forwarded local flags, probes, clips and feature windows.
// The derived messages are published in order by the partition's publisher; a stream position is
// acked (Consume completion) only after every message derived from it has its PubAck, and a signal
// that triggered a clip is published, with clip_id, only after the clip was committed.
package worker
