// Package identity holds the identifiers and secrets every PandaEye component shares: tenant and
// server ids, player UUIDs, server secrets, HMAC helpers (licence keys, SignedOutbound, appeal codes),
// API key tokens, webhook encryption, siphash partitioning and deterministic verdict ids.
//
// Secrets are carried as [Secret] values, which never print their contents (fmt, slog, JSON).
package identity
