// Package api is the REST API (ARCHITECTURE 5.8, docs/API.md): dashboard authentication (service
// token + signed user assertion) and API keys with scopes, tenant settings, servers, verdicts and
// their confirmation or overturn (with corrective UNBAN/UNMITIGATE verdicts), clips, players and
// GDPR erasure (5.13), bans, appeals (including public appeals), statistics, usage, API keys, check
// configuration, the audit log, the ML label export and the public /status document.
//
// It also runs two leased singletons: the metering loop (every minute, usage_daily and the free-tier
// limit) and the janitor (daily retention, erasure tombstone expiry).
package api
