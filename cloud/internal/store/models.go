package store

import (
	"encoding/json"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Plans.
const PlanFree = "free"

// Tenant is a customer (one licence holder).
type Tenant struct {
	ID                string // identity.NewTenantID()
	Name              string
	Slug              string // unique
	Leniency          int    // 1..5, default 4
	Autoban           bool
	Autokick          bool
	Mitigations       bool // default true
	Enforcement       bool
	ReputationOptIn   bool
	DiscordWebhookEnc []byte    // identity.Seal output; nil = none
	DiscordMinAction  pb.Action // default ACTION_MITIGATE
	Plan              string    // default PlanFree
	MaxServers        int       // from the last licence verification; 0 = unlimited
	LicenseVerifiedAt time.Time
	LicenseValidUntil time.Time // LicenseVerifiedAt + 24 h: usable while the verifier is unreachable
	OverLimitSince    time.Time // free tier over its peak-player limit since (zero = not over)
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewTenant returns a tenant with the documented defaults.
func NewTenant(id, name, slug string, now time.Time) *Tenant {
	return &Tenant{ID: id, Name: name, Slug: slug, Leniency: 4, Mitigations: true,
		DiscordMinAction: pb.Action_ACTION_MITIGATE, Plan: PlanFree, CreatedAt: now, UpdatedAt: now}
}

// TenantPatch lists tenant settings to change; nil fields stay unchanged. DiscordWebhookEnc set to a
// pointer to nil clears the webhook.
type TenantPatch struct {
	Name              *string
	Leniency          *int
	Autoban           *bool
	Autokick          *bool
	Mitigations       *bool
	Enforcement       *bool
	ReputationOptIn   *bool
	DiscordWebhookEnc *[]byte
	DiscordMinAction  *pb.Action
	Plan              *string
}

// TenantCheck is the per-tenant configuration of one cloud check (tenant_checks).
type TenantCheck struct {
	TenantID  string
	CheckID   string
	Enabled   bool
	Overrides map[string]float64 // threshold overrides by parameter name
}

// License maps a licence key (by HMAC) to its tenant.
type License struct {
	KeyHMAC   []byte
	TenantID  string
	CreatedAt time.Time
	RevokedAt time.Time
}

// Server is a game server or proxy install (tenant_id, id).
type Server struct {
	TenantID         string
	ID               string // canonical lowercase UUID
	Name             string
	NetworkID        string
	Platform         pb.Platform
	PlatformVersion  string
	PluginVersion    string
	MinecraftVersion string
	SecretHash       []byte // identity.HashSecret(server secret)
	InstanceID       string
	LastSeenAt       time.Time
	LastBatchSeq     uint64
	CreatedAt        time.Time
}

// Player is a player seen by a tenant.
type Player struct {
	TenantID     string
	UUID         string // canonical lowercase UUID
	LastName     string
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	LastServerID string
	Names        []PlayerName // filled by GetPlayer, newest first
}

// PlayerName is one name a player was seen with.
type PlayerName struct {
	Name   string
	SeenAt time.Time
}

// Verdict statuses.
const (
	StatusOpen       = "open"
	StatusConfirmed  = "confirmed"
	StatusOverturned = "overturned"
	StatusExpired    = "expired"
	StatusSuperseded = "superseded"
)

// Verdict is a stored verdict (from a VerdictRecord, plus delivery and ack state).
type Verdict struct {
	ID           string
	TenantID     string
	ServerID     string // routing target
	NetworkID    string
	PlayerUUID   string
	PlayerName   string
	SessionID    string
	Action       pb.Action
	Category     pb.Category
	Score        float64
	Confidence   float64
	Reason       string
	Checks       []string
	Evidence     json.RawMessage // evidence_json; nil stores "{}"
	ClipIDs      []string
	Mitigations  []pb.Mitigation
	Strength     float64
	BanDurationS uint32
	Payload      []byte // serialised pb.Verdict, re-sent by the gateway
	Shadow       bool
	Status       string
	Supersedes   string
	AppealCode   string
	CreatedAt    time.Time
	DeliveredAt  time.Time
	AckedAt      time.Time
	Executed     bool
	AckNote      string
}

// VerdictFilter narrows ListVerdicts; zero fields match everything.
type VerdictFilter struct {
	Action     pb.Action
	Status     string
	PlayerUUID string
	ServerID   string
	Since      time.Time // created_at >= Since
	Until      time.Time // created_at < Until
}

// Pending delivery rules (ARCHITECTURE 5.3).
const (
	PendingMaxAge     = 24 * time.Hour  // older verdicts are no longer re-sent ...
	PendingKickMaxAge = 5 * time.Minute // ... KICK sooner; UNBAN/UNMITIGATE never expire
	PendingResend     = 15 * time.Second
)

// PendingQuery selects verdicts to (re-)deliver to a server: status open, not acked, delivered_at
// NULL or before DeliveredBefore, created within PendingMaxAge of Now (KICK: PendingKickMaxAge;
// UNBAN/UNMITIGATE: any age), oldest first. The sweep passes DeliveredBefore = Now - PendingResend;
// a fresh connection passes Now.
type PendingQuery struct {
	ServerID        string
	Now             time.Time
	DeliveredBefore time.Time
	Limit           int // <= 0: 100
}

// Ack is a VerdictAck relayed through AckRecord.
type Ack struct {
	TenantID  string
	ServerID  string // the server that answered (and executed, for bans)
	VerdictID string
	Executed  bool
	Note      string
	At        time.Time
}

// AckResult reports what ApplyAck changed.
type AckResult struct {
	Verdict    *Verdict // after the update
	BanCreated bool     // an executed, non-shadow BAN created its bans row
	UnbanDone  bool     // an executed UNBAN completed its ban's unban
}

// VerdictCount is one row of the stats overview.
type VerdictCount struct {
	Action   pb.Action
	Category pb.Category
	Count    int64
}

// Unban states.
const (
	UnbanNone    = "none"
	UnbanPending = "pending"
	UnbanDone    = "done"
)

// Ban is an executed, non-shadow BAN. The row outlives the verdict's retention: it is kept while
// active and for a year after it ends.
type Ban struct {
	VerdictID      string
	TenantID       string
	ServerID       string // the server that executed it
	PlayerUUID     string
	Active         bool
	Reason         string
	CreatedAt      time.Time
	EndsAt         time.Time // timed bans (Verdict.BanDurationS): when it ends; zero = permanent
	OverturnedAt   time.Time
	OverturnedBy   string
	OverturnNote   string
	UnbanVerdictID string
	UnbanState     string
}

// Ongoing reports whether the ban is in force at t: active (not overturned) and not past EndsAt.
func (b *Ban) Ongoing(t time.Time) bool {
	return b.Active && (b.EndsAt.IsZero() || b.EndsAt.After(t))
}

// BanFilter narrows ListBans.
type BanFilter struct {
	Active     *bool     // true: ongoing at At; false: overturned or ended at At
	At         time.Time // zero = now
	PlayerUUID string
}

// BanOverturn describes a staff overturn of a ban.
type BanOverturn struct {
	By             string // principal, e.g. "user:alice"
	Note           string
	UnbanVerdictID string // the UNBAN verdict routed to the executing server; empty = none needed
	At             time.Time
}

// Appeal statuses.
const (
	AppealOpen     = "open"
	AppealAccepted = "accepted"
	AppealDenied   = "denied"
)

// Appeal field limits.
const (
	MaxAppealMessage = 2000
	MaxAppealContact = 200
)

// Appeal is a player's appeal of a ban.
type Appeal struct {
	ID           string // generated when empty
	TenantID     string
	VerdictID    string
	PlayerUUID   string
	Message      string
	Contact      string
	Status       string
	CreatedAt    time.Time
	DecidedAt    time.Time
	DecidedBy    string
	DecisionNote string
}

// AppealLimits bounds appeal creation (ARCHITECTURE 5.8).
type AppealLimits struct {
	OpenPerVerdict  int // 1
	TotalPerVerdict int // 3
	PerTenantPerDay int // 200
}

// DefaultAppealLimits are the documented limits.
var DefaultAppealLimits = AppealLimits{OpenPerVerdict: 1, TotalPerVerdict: 3, PerTenantPerDay: 200}

// AppealFilter narrows ListAppeals.
type AppealFilter struct {
	Status    string
	VerdictID string
}

// Labels and their sources.
const (
	LabelCheat = "cheat"
	LabelLegit = "legit"

	SourceConfirmed  = "confirmed"
	SourceOverturned = "overturned"
	SourceManual     = "manual"
)

// Label is ground truth for a player over a time window.
type Label struct {
	ID          string // generated when empty
	TenantID    string
	PlayerUUID  string
	VerdictID   string // empty for manual labels
	Label       string
	Source      string
	WindowStart time.Time
	WindowEnd   time.Time
	CreatedAt   time.Time
}

// LabelExport is a label joined with its verdict for the ML export.
type LabelExport struct {
	Label
	Action   pb.Action
	Score    float64
	Checks   []string
	Evidence json.RawMessage // nil when the label has no (remaining) verdict
}

// ClipCodecZstd is clips.codec 1: a zstd frame with content size of a serialised ClipData.
const ClipCodecZstd = 1

// Clip is a stored replay clip.
type Clip struct {
	ID         string
	TenantID   string
	ServerID   string
	PlayerUUID string
	StartUs    int64
	EndUs      int64
	Codec      int
	Data       []byte
	Subjects   []string // other players appearing in the clip (attack targets)
	CreatedAt  time.Time
}

// API key scopes.
const (
	ScopeRead          = "read"
	ScopeVerdictsWrite = "verdicts:write"
	ScopeSettingsWrite = "settings:write"
	ScopeErase         = "erase"
)

// APIKey is a tenant API key; only the hash of its secret is stored.
type APIKey struct {
	ID         string
	TenantID   string
	Name       string
	KeyHash    []byte // identity.HashSecret(secret)
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt time.Time
	RevokedAt  time.Time
}

// HasScope reports whether the key grants scope.
func (k *APIKey) HasScope(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ReputationContrib is one tenant's confirmed ban contributing to the reputation network.
type ReputationContrib struct {
	TenantID   string
	PlayerUUID string
	VerdictID  string
	Weight     float64
	CreatedAt  time.Time
}

// TenantReputation aggregates one other tenant's contributions for a player.
type TenantReputation struct {
	TenantID      string
	Weight        float64 // sum of weights; callers cap it per tenant
	Contributions int
	Latest        time.Time
}

// TrustInputs are the facts that decide whether a tenant's contributions count (5.11).
type TrustInputs struct {
	Plan            string
	CreatedAt       time.Time
	DistinctPlayers int64
	OptIn           bool
}

// UsageDay is the peak player count of one UTC day.
type UsageDay struct {
	Day         time.Time // 00:00 UTC
	PeakPlayers int
}

// AuditEntry is one mutating action.
type AuditEntry struct {
	ID        string // generated when empty
	TenantID  string
	Principal string // "user:<name>", "apikey:<id>", "system"
	Action    string
	Target    string
	Detail    json.RawMessage
	CreatedAt time.Time
}

// Erasure is a GDPR erasure job for one player.
type Erasure struct {
	TenantID    string
	PlayerUUID  string
	RequestedAt time.Time
	CompletedAt time.Time
	Counts      map[string]int64
}
