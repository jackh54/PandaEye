package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/app"
	"github.com/jackh54/PandaEye/cloud/internal/discord"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func (s *Service) tenantJSON(t *store.Tenant) tenantJSON {
	masked := ""
	if len(t.DiscordWebhookEnc) > 0 {
		if u, err := discord.Open(s.d.Secrets.OutboundKeys(), t.ID, t.DiscordWebhookEnc); err == nil {
			masked = discord.Mask(u)
		} else {
			masked = "****" // set but unreadable (outbound key rotated): the tenant must save it again
		}
	}
	return tenantJSON{
		ID: t.ID, Name: t.Name, Slug: t.Slug, Plan: t.Plan, Leniency: t.Leniency, Autoban: t.Autoban, Autokick: t.Autokick,
		Mitigations: t.Mitigations, Enforcement: t.Enforcement, ReputationOptIn: t.ReputationOptIn, DiscordWebhook: masked,
		DiscordMinAction: actionName(t.DiscordMinAction), OverLimitSince: nullTime(t.OverLimitSince),
	}
}

func (s *Service) handleGetTenant(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	t, err := s.d.Store.GetTenant(r.Context(), rc.tenant())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("tenant")
		}
		return err
	}
	writeJSON(w, http.StatusOK, s.tenantJSON(t))
	return nil
}

type tenantPatchReq struct {
	Name             *string `json:"name"`
	Leniency         *int    `json:"leniency"`
	Autoban          *bool   `json:"autoban"`
	Autokick         *bool   `json:"autokick"`
	Mitigations      *bool   `json:"mitigations"`
	Enforcement      *bool   `json:"enforcement"`
	ReputationOptIn  *bool   `json:"reputation_opt_in"`
	DiscordWebhook   *string `json:"discord_webhook"`
	DiscordMinAction *string `json:"discord_min_action"`
}

func (s *Service) handlePatchTenant(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req tenantPatchReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	ctx := r.Context()
	tenantID := rc.tenant()
	cur, err := s.d.Store.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("tenant")
		}
		return err
	}
	if (req.Enforcement != nil || req.Autoban != nil || req.Autokick != nil) && !rc.p.isDashboardAdmin() {
		return forbidden("enforcement, autoban and autokick can only be changed by a dashboard owner or admin")
	}
	var p store.TenantPatch
	changes := map[string]any{}
	if req.Name != nil {
		name, ok := cleanText(strings.TrimSpace(*req.Name), 100, false)
		if !ok || name == "" {
			return badRequest("name must be 1 to 100 characters")
		}
		p.Name, changes["name"] = &name, name
	}
	if req.Leniency != nil {
		if *req.Leniency < 1 || *req.Leniency > 5 {
			return badRequest("leniency must be 1 to 5")
		}
		p.Leniency, changes["leniency"] = req.Leniency, *req.Leniency
	}
	for _, b := range []struct {
		name string
		v    *bool
		dst  **bool
	}{
		{"autoban", req.Autoban, &p.Autoban}, {"autokick", req.Autokick, &p.Autokick},
		{"mitigations", req.Mitigations, &p.Mitigations}, {"enforcement", req.Enforcement, &p.Enforcement},
		{"reputation_opt_in", req.ReputationOptIn, &p.ReputationOptIn},
	} {
		if b.v != nil {
			*b.dst, changes[b.name] = b.v, *b.v
		}
	}
	if req.DiscordMinAction != nil {
		a, ok := parseAction(*req.DiscordMinAction)
		if !ok || a > pb.Action_ACTION_BAN {
			return badRequest("discord_min_action must be alert, mitigate, kick or ban")
		}
		p.DiscordMinAction, changes["discord_min_action"] = &a, actionName(a)
	}
	if req.DiscordWebhook != nil {
		enc, change, err := s.webhookPatch(ctx, cur, *req.DiscordWebhook)
		if err != nil {
			return err
		}
		if change != "" {
			p.DiscordWebhookEnc, changes["discord_webhook"] = &enc, change
		}
	}
	t, err := s.d.Store.UpdateTenant(ctx, tenantID, p, rc.now)
	if err != nil {
		return err
	}
	s.auditRC(r, rc, "tenant.update", tenantID, map[string]any{"changes": changes})
	if err := s.publishTenant(ctx, t); err != nil {
		s.log.Error("tenant settings not published", "err", err)
		return unavailable("settings were saved but could not be distributed; retry")
	}
	writeJSON(w, http.StatusOK, s.tenantJSON(t))
	return nil
}

// webhookPatch validates a new webhook ("" removes it), sends the test message when it changed and
// returns the sealed value and the audit description ("" = unchanged).
func (s *Service) webhookPatch(ctx context.Context, cur *store.Tenant, raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if len(cur.DiscordWebhookEnc) == 0 {
			return nil, "", nil
		}
		return nil, "removed", nil
	}
	wh, err := discord.ParseWebhookURL(raw)
	if err != nil {
		return nil, "", errf(http.StatusBadRequest, codeInvalidWebhook,
			"discord_webhook must be a Discord webhook URL (https://discord.com/api/webhooks/<id>/<token>)")
	}
	if len(cur.DiscordWebhookEnc) > 0 {
		if old, err := discord.Open(s.d.Secrets.OutboundKeys(), cur.ID, cur.DiscordWebhookEnc); err == nil && old == wh.URL() {
			return nil, "", nil
		}
	}
	tctx, cancel := context.WithTimeout(discord.WithTenant(ctx, cur.ID), 10*time.Second)
	defer cancel()
	if err := s.webhooks.SendTest(tctx, wh.URL()); err != nil {
		s.log.Info("discord webhook test failed", "webhook_id", wh.ID, "err", err)
		return nil, "", errf(http.StatusBadRequest, codeWebhookTest, "the test message to the Discord webhook failed; check the webhook")
	}
	enc, err := discord.Seal(s.d.Secrets.OutboundKey.Bytes(), cur.ID, wh.URL())
	if err != nil {
		return nil, "", err
	}
	return enc, "set:" + wh.ID, nil
}

// publishTenant writes the tenant's settings to KV PE_TENANTS and pushes the RemoteConfig to every
// connected server of the tenant (config_update).
func (s *Service) publishTenant(ctx context.Context, t *store.Tenant) error {
	if err := app.PublishTenant(ctx, s.d.Store, s.d.Bus, t.ID); err != nil {
		return err
	}
	checks, err := s.d.Store.ListTenantChecks(ctx, t.ID)
	if err != nil {
		return err
	}
	rcfg := app.TenantSettings(t, checks).RemoteConfig()
	conns, err := s.d.Bus.ListConnections(ctx, t.ID)
	if err != nil {
		s.log.Warn("connections not listed; config reaches servers with the next Pong", "err", err)
		return nil
	}
	for _, c := range conns {
		out := &pb.Outbound{TenantId: t.ID, ServerId: c.ServerID, Kind: &pb.Outbound_ConfigUpdate{ConfigUpdate: rcfg}}
		if err := s.sendOutbound(out); err != nil {
			s.log.Warn("config update not sent", "server_id", c.ServerID, "err", err)
		}
	}
	return nil
}

func (s *Service) handleServers(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	page, err := pageFrom(r.URL.Query())
	if err != nil {
		return err
	}
	if page.Limit == 0 {
		page.Limit = store.MaxPageLimit
	}
	ctx := r.Context()
	servers, next, err := s.d.Store.ListServers(ctx, rc.tenant(), page)
	if err != nil {
		return err
	}
	conns, err := s.d.Bus.ListConnections(ctx, rc.tenant())
	if err != nil {
		return err
	}
	online := map[string]int{}
	for i, c := range conns {
		online[c.ServerID] = i
	}
	out := make([]serverJSON, 0, len(servers))
	for _, sv := range servers {
		j := serverJSON{
			ID: sv.ID, Name: sv.Name, NetworkID: sv.NetworkID, Platform: platformName(sv.Platform),
			PlatformVersion: sv.PlatformVersion, PluginVersion: sv.PluginVersion, MinecraftVersion: sv.MinecraftVersion,
			LastSeenAt: nullTime(sv.LastSeenAt),
		}
		if i, ok := online[sv.ID]; ok {
			info := conns[i].Info
			shadow := info.ShadowMode
			j.Online, j.OnlinePlayers, j.ShadowMode = true, info.OnlinePlayers, &shadow
			if seen := time.UnixMilli(info.LastFrameUnixMs); info.LastFrameUnixMs > 0 && seen.After(sv.LastSeenAt) {
				j.LastSeenAt = nullTime(seen)
			}
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "next_cursor": next})
	return nil
}
