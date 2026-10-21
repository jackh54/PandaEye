package api

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// JSON shapes of docs/API.md. Field order follows the document.

// nullTime is an RFC 3339 UTC time, or null when zero.
type nullTime time.Time

func (t nullTime) MarshalJSON() ([]byte, error) {
	if time.Time(t).IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(formatTime(time.Time(t)))
}

// formatTime is the API's time format: RFC 3339 in UTC with at most microsecond precision.
func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

// num makes a float JSON-safe (NaN and infinities become 0).
func num(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// actionName is the API form of an action: "alert" ... "unban"; "" for unspecified.
func actionName(a pb.Action) string {
	if a == pb.Action_ACTION_UNSPECIFIED {
		return ""
	}
	n, ok := pb.Action_name[int32(a)]
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(n, "ACTION_"))
}

// parseAction accepts "ban", "BAN" or "ACTION_BAN".
func parseAction(s string) (pb.Action, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "ACTION_") {
		s = "ACTION_" + s
	}
	v, ok := pb.Action_value[s]
	if !ok || v == int32(pb.Action_ACTION_UNSPECIFIED) {
		return 0, false
	}
	return pb.Action(v), true
}

// categoryName is the enum name without its prefix ("HEURISTICS"); "" for unspecified.
func categoryName(c pb.Category) string {
	if c == pb.Category_CATEGORY_UNSPECIFIED {
		return ""
	}
	n, ok := pb.Category_name[int32(c)]
	if !ok {
		return ""
	}
	return strings.TrimPrefix(n, "CATEGORY_")
}

func mitigationName(m pb.Mitigation) string {
	n, ok := pb.Mitigation_name[int32(m)]
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(n, "MITIGATION_"))
}

func platformName(p pb.Platform) string {
	n, ok := pb.Platform_name[int32(p)]
	if !ok {
		return "unspecified"
	}
	return strings.ToLower(strings.TrimPrefix(n, "PLATFORM_"))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

type tenantJSON struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Slug             string   `json:"slug"`
	Plan             string   `json:"plan"`
	Leniency         int      `json:"leniency"`
	Autoban          bool     `json:"autoban"`
	Autokick         bool     `json:"autokick"`
	Mitigations      bool     `json:"mitigations"`
	Enforcement      bool     `json:"enforcement"`
	ReputationOptIn  bool     `json:"reputation_opt_in"`
	DiscordWebhook   string   `json:"discord_webhook"`
	DiscordMinAction string   `json:"discord_min_action"`
	OverLimitSince   nullTime `json:"over_limit_since"`
}

type serverJSON struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	NetworkID        string   `json:"network_id"`
	Platform         string   `json:"platform"`
	PlatformVersion  string   `json:"platform_version"`
	PluginVersion    string   `json:"plugin_version"`
	MinecraftVersion string   `json:"minecraft_version"`
	Online           bool     `json:"online"`
	OnlinePlayers    uint32   `json:"online_players"`
	LastSeenAt       nullTime `json:"last_seen_at"`
	ShadowMode       *bool    `json:"shadow_mode"` // known only while connected
}

type verdictJSON struct {
	ID          string   `json:"id"`
	ServerID    string   `json:"server_id"`
	ServerName  string   `json:"server_name,omitempty"`
	NetworkID   string   `json:"network_id"`
	PlayerUUID  string   `json:"player_uuid"`
	PlayerName  string   `json:"player_name"`
	SessionID   string   `json:"session_id"`
	Action      string   `json:"action"`
	Category    string   `json:"category"`
	Score       float64  `json:"score"`
	Confidence  float64  `json:"confidence"`
	Reason      string   `json:"reason"`
	Checks      []string `json:"checks"`
	Shadow      bool     `json:"shadow"`
	Status      string   `json:"status"`
	Supersedes  string   `json:"supersedes"`
	CreatedAt   nullTime `json:"created_at"`
	DeliveredAt nullTime `json:"delivered_at"`
	AckedAt     nullTime `json:"acked_at"`
	Executed    bool     `json:"executed"`
	AckNote     string   `json:"ack_note"`
	ClipIDs     []string `json:"clip_ids"`
	Mitigations []string `json:"mitigations"`
	Strength    float64  `json:"strength"`
}

type verdictDetailJSON struct {
	verdictJSON
	Evidence json.RawMessage `json:"evidence"`
}

func toVerdictJSON(v *store.Verdict, serverName string) verdictJSON {
	ms := make([]string, 0, len(v.Mitigations))
	for _, m := range v.Mitigations {
		if n := mitigationName(m); n != "" && m != pb.Mitigation_MITIGATION_UNSPECIFIED {
			ms = append(ms, n)
		}
	}
	return verdictJSON{
		ID: v.ID, ServerID: v.ServerID, ServerName: serverName, NetworkID: v.NetworkID, PlayerUUID: v.PlayerUUID,
		PlayerName: v.PlayerName, SessionID: v.SessionID, Action: actionName(v.Action), Category: categoryName(v.Category),
		Score: num(v.Score), Confidence: num(v.Confidence), Reason: v.Reason, Checks: nonNil(v.Checks), Shadow: v.Shadow,
		Status: v.Status, Supersedes: v.Supersedes, CreatedAt: nullTime(v.CreatedAt), DeliveredAt: nullTime(v.DeliveredAt),
		AckedAt: nullTime(v.AckedAt), Executed: v.Executed, AckNote: v.AckNote, ClipIDs: nonNil(v.ClipIDs), Mitigations: ms,
		Strength: num(v.Strength),
	}
}

// evidenceJSON returns the stored evidence as a JSON object ({} when empty or not an object).
func evidenceJSON(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if t == "" || t[0] != '{' || !json.Valid([]byte(t)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(t)
}

type banJSON struct {
	VerdictID      string   `json:"verdict_id"`
	ServerID       string   `json:"server_id"`
	PlayerUUID     string   `json:"player_uuid"`
	PlayerName     string   `json:"player_name,omitempty"`
	Active         bool     `json:"active"`
	Reason         string   `json:"reason"`
	CreatedAt      nullTime `json:"created_at"`
	OverturnedAt   nullTime `json:"overturned_at"`
	OverturnedBy   string   `json:"overturned_by"`
	OverturnNote   string   `json:"overturn_note"`
	UnbanVerdictID string   `json:"unban_verdict_id"`
	UnbanState     string   `json:"unban_state"`
}

func toBanJSON(b *store.Ban, playerName string, now time.Time) banJSON {
	state := b.UnbanState
	if state == "" {
		state = store.UnbanNone
	}
	return banJSON{
		VerdictID: b.VerdictID, ServerID: b.ServerID, PlayerUUID: b.PlayerUUID, PlayerName: playerName,
		Active: b.Ongoing(now), Reason: b.Reason, CreatedAt: nullTime(b.CreatedAt), OverturnedAt: nullTime(b.OverturnedAt),
		OverturnedBy: b.OverturnedBy, OverturnNote: b.OverturnNote, UnbanVerdictID: b.UnbanVerdictID, UnbanState: state,
	}
}

type appealJSON struct {
	ID           string   `json:"id"`
	VerdictID    string   `json:"verdict_id"`
	PlayerUUID   string   `json:"player_uuid"`
	PlayerName   string   `json:"player_name,omitempty"`
	Message      string   `json:"message"`
	Contact      string   `json:"contact"`
	Status       string   `json:"status"`
	CreatedAt    nullTime `json:"created_at"`
	DecidedAt    nullTime `json:"decided_at"`
	DecidedBy    string   `json:"decided_by"`
	DecisionNote string   `json:"decision_note"`
}

func toAppealJSON(a *store.Appeal, playerName string) appealJSON {
	return appealJSON{
		ID: a.ID, VerdictID: a.VerdictID, PlayerUUID: a.PlayerUUID, PlayerName: playerName, Message: a.Message,
		Contact: a.Contact, Status: a.Status, CreatedAt: nullTime(a.CreatedAt), DecidedAt: nullTime(a.DecidedAt),
		DecidedBy: a.DecidedBy, DecisionNote: a.DecisionNote,
	}
}

type playerJSON struct {
	UUID         string   `json:"uuid"`
	LastName     string   `json:"last_name"`
	FirstSeenAt  nullTime `json:"first_seen_at"`
	LastSeenAt   nullTime `json:"last_seen_at"`
	LastServerID string   `json:"last_server_id"`
}

func toPlayerJSON(p *store.Player) playerJSON {
	return playerJSON{UUID: p.UUID, LastName: p.LastName, FirstSeenAt: nullTime(p.FirstSeenAt),
		LastSeenAt: nullTime(p.LastSeenAt), LastServerID: p.LastServerID}
}

type playerNameJSON struct {
	Name   string   `json:"name"`
	SeenAt nullTime `json:"seen_at"`
}

type reputationJSON struct {
	Risk            float64 `json:"risk"`
	NetworksFlagged int     `json:"networks_flagged"`
	ConfirmedBans   int     `json:"confirmed_bans"`
}

type playerDetailJSON struct {
	playerJSON
	Names      []playerNameJSON `json:"names"`
	Verdicts   []verdictJSON    `json:"verdicts"`
	Bans       []banJSON        `json:"bans"`
	Reputation *reputationJSON  `json:"reputation,omitempty"`
}

type apiKeyJSON struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	CreatedAt  nullTime `json:"created_at"`
	LastUsedAt nullTime `json:"last_used_at"`
	RevokedAt  nullTime `json:"revoked_at"`
}

func toAPIKeyJSON(k *store.APIKey) apiKeyJSON {
	return apiKeyJSON{ID: k.ID, Name: k.Name, Scopes: nonNil(k.Scopes), CreatedAt: nullTime(k.CreatedAt),
		LastUsedAt: nullTime(k.LastUsedAt), RevokedAt: nullTime(k.RevokedAt)}
}

type auditJSON struct {
	ID        string          `json:"id"`
	Principal string          `json:"principal"`
	Action    string          `json:"action"`
	Target    string          `json:"target"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt nullTime        `json:"created_at"`
}

type checkJSON struct {
	ID        string             `json:"id"`
	Enabled   bool               `json:"enabled"`
	Overrides map[string]float64 `json:"overrides"`
}

type okJSON struct {
	OK bool `json:"ok"`
}
