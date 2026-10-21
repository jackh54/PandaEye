package verdict

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/discord"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/metrics"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// Notifier posts verdict alerts to a Discord webhook; *discord.Client implements it (with the
// per-tenant budget of 10 posts/min keyed by discord.WithTenant, 429 handling, markdown escaping,
// no mentions and failure logging by webhook id).
type Notifier interface {
	PostVerdict(ctx context.Context, webhookURL string, e discord.VerdictEmbed) error
}

const (
	discordQueueSize = 256
	discordTimeout   = 15 * time.Second
	maxEmbedChecks   = 5
	dropDiscord      = "verdict_discord_overflow"
)

// actionRank orders ALERT < MITIGATE < KICK < BAN; corrective actions rank below everything.
func actionRank(a pb.Action) int {
	switch a {
	case pb.Action_ACTION_ALERT, pb.Action_ACTION_MITIGATE, pb.Action_ACTION_KICK, pb.Action_ACTION_BAN:
		return int(a)
	}
	return 0
}

// discordWanted reports whether a verdict reaches the tenant's Discord (>= discord_min_action,
// default MITIGATE).
func discordWanted(a pb.Action, ts stream.TenantSettings) bool {
	minA := ts.DiscordMinAction
	if actionRank(minA) == 0 {
		minA = pb.Action_ACTION_MITIGATE
	}
	return len(ts.DiscordWebhookEnc) > 0 && actionRank(a) >= actionRank(minA)
}

type discordJob struct {
	tenant string
	sealed []byte
	embed  discord.VerdictEmbed
}

// discordQueue posts alerts in the background (bounded; overflow is dropped and counted), so a slow
// or failing webhook never delays verdict delivery.
type discordQueue struct {
	n       Notifier
	keys    [][]byte // outbound keys that may open sealed webhook URLs
	log     *slog.Logger
	m       *metrics.Metrics
	ch      chan discordJob
	badSeal map[string]bool // tenants whose webhook cannot be decrypted (logged once)
}

func newDiscordQueue(n Notifier, keys [][]byte, log *slog.Logger, m *metrics.Metrics) *discordQueue {
	return &discordQueue{n: n, keys: keys, log: log, m: m, ch: make(chan discordJob, discordQueueSize), badSeal: map[string]bool{}}
}

func (q *discordQueue) enqueue(pv *pendingVerdict) {
	if q == nil || q.n == nil || !discordWanted(pv.rec.Verdict.Action, pv.settings) {
		return
	}
	v := pv.rec.Verdict
	server := pv.route.serverName
	if server == "" {
		server = pv.rec.ServerId
	}
	player := v.PlayerName
	uuid := ""
	if id, err := identity.UUIDFromBytes(v.Player); err == nil {
		uuid = id.String()
	}
	if player == "" {
		player = uuid
	}
	e := discord.VerdictEmbed{Player: player, PlayerUUID: uuid, Server: server,
		Action: strings.TrimPrefix(v.Action.String(), "ACTION_"), Shadow: v.Shadow, Score: v.Score, Confidence: v.Confidence,
		Checks: append([]string(nil), v.Checks[:min(len(v.Checks), maxEmbedChecks)]...), EvidenceURL: v.EvidenceUrl}
	select {
	case q.ch <- discordJob{tenant: pv.rec.TenantId, sealed: pv.settings.DiscordWebhookEnc, embed: e}:
	default:
		if q.m != nil {
			q.m.Drops.WithLabelValues(dropDiscord).Inc()
		}
	}
}

// run posts queued alerts until ctx is done.
func (q *discordQueue) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-q.ch:
			q.post(ctx, j)
		}
	}
}

func (q *discordQueue) post(ctx context.Context, j discordJob) {
	url, err := discord.Open(q.keys, j.tenant, j.sealed)
	if err != nil {
		if !q.badSeal[j.tenant] {
			q.badSeal[j.tenant] = true
			q.log.Warn("discord webhook of the tenant cannot be decrypted (PE_OUTBOUND_KEY changed?)", "tenant", j.tenant)
		}
		return
	}
	delete(q.badSeal, j.tenant)
	pctx, cancel := context.WithTimeout(discord.WithTenant(ctx, j.tenant), discordTimeout)
	defer cancel()
	if err := q.n.PostVerdict(pctx, url, j.embed); err != nil && !errors.Is(err, context.Canceled) {
		// discord.Client logs the first failure per webhook id; only debug here.
		q.log.Debug("discord post failed", "tenant", j.tenant, "webhook_id", discord.WebhookID(url), "err", err)
	}
}
