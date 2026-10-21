package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/verdict"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func (s *Service) handleListPlayers(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	q := r.URL.Query()
	name, ok := cleanText(strings.TrimSpace(q.Get("name")), 64, false)
	if !ok {
		return badRequest("name must be at most 64 characters")
	}
	page, err := pageFrom(q)
	if err != nil {
		return err
	}
	ps, next, err := s.d.Store.ListPlayers(r.Context(), rc.tenant(), name, page)
	if err != nil {
		return err
	}
	out := make([]playerJSON, 0, len(ps))
	for i := range ps {
		out = append(out, toPlayerJSON(&ps[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"players": out, "next_cursor": next})
	return nil
}

// Player detail limits.
const (
	playerVerdicts = 50
	playerBans     = 50
)

func (s *Service) handleGetPlayer(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	uuid, err := parsePlayer(r.PathValue("uuid"))
	if err != nil {
		return notFound("player")
	}
	ctx := r.Context()
	tenantID := rc.tenant()
	vs, _, err := s.d.Store.ListVerdictsShadow(ctx, tenantID, store.VerdictFilter{PlayerUUID: uuid}, nil, store.Page{Limit: playerVerdicts})
	if err != nil {
		return err
	}
	bans, _, err := s.d.Store.ListBans(ctx, tenantID, store.BanFilter{PlayerUUID: uuid}, store.Page{Limit: playerBans})
	if err != nil {
		return err
	}
	p, err := s.d.Store.GetPlayer(ctx, tenantID, uuid)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if len(vs) == 0 && len(bans) == 0 {
			return notFound("player")
		}
		// Verdicts outlive the player row (or it was never written): summarise what is known.
		p = &store.Player{TenantID: tenantID, UUID: uuid}
		if len(vs) > 0 {
			p.LastName, p.LastServerID = vs[0].PlayerName, vs[0].ServerID
		}
	case err != nil:
		return err
	}
	l := s.lookups(ctx, tenantID)
	l.players[uuid] = p.LastName
	out := playerDetailJSON{playerJSON: toPlayerJSON(p), Names: []playerNameJSON{}, Verdicts: []verdictJSON{}, Bans: []banJSON{}}
	for _, n := range p.Names {
		out.Names = append(out.Names, playerNameJSON{Name: n.Name, SeenAt: nullTime(n.SeenAt)})
	}
	for i := range vs {
		out.Verdicts = append(out.Verdicts, l.verdict(&vs[i]))
	}
	for i := range bans {
		out.Bans = append(out.Bans, toBanJSON(&bans[i], p.LastName, rc.now))
	}
	if rep, err := s.playerReputation(ctx, tenantID, p, rc.now); err != nil {
		return err
	} else if rep != nil {
		out.Reputation = rep
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// Reputation (ARCHITECTURE 5.11).
const (
	reputationJoinWindow  = 24 * time.Hour
	reputationTenantCap   = 1.0 // max weight one tenant contributes
	reputationMinTenants  = 2   // risk > 0 only when this many tenants agree
	reputationLookupLimit = 100
)

// playerReputation returns the reputation block when the requesting tenant opted in and the player
// joined it in the last 24 h; nil otherwise. Only aggregates of other trusted, opted-in tenants
// count; risk is 0 unless at least two of them agree.
func (s *Service) playerReputation(ctx context.Context, tenantID string, p *store.Player, now time.Time) (*reputationJSON, error) {
	t, err := s.d.Store.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !t.ReputationOptIn || p.LastSeenAt.IsZero() || now.Sub(p.LastSeenAt) > reputationJoinWindow {
		return nil, nil
	}
	rows, err := s.d.Store.PlayerReputation(ctx, tenantID, p.UUID)
	if err != nil {
		return nil, err
	}
	var weight float64
	rep := &reputationJSON{}
	for i, row := range rows {
		if i >= reputationLookupLimit {
			break
		}
		ti, err := s.d.Store.TrustInputs(ctx, row.TenantID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if !verdict.TrustedTenant(ti, now) {
			continue
		}
		rep.NetworksFlagged++
		rep.ConfirmedBans += row.Contributions
		weight += math.Min(row.Weight, reputationTenantCap)
	}
	rep.Risk = reputationRisk(rep.NetworksFlagged, weight)
	return rep, nil
}

// reputationRisk maps the agreeing tenants and their capped weights to 0..1: 0 below two tenants,
// then 1 - 2^-(weight-1) (two full contributions 0.5, three 0.75, four 0.875).
func reputationRisk(tenants int, weight float64) float64 {
	if tenants < reputationMinTenants || weight <= 1 {
		return 0
	}
	r := 1 - math.Pow(2, -(weight-1))
	return math.Round(r*1000) / 1000
}

type watchReq struct {
	Seconds int `json:"seconds"`
}

// handleWatch asks the player's current server for full-rate telemetry (WatchRequest, 60..300 s).
func (s *Service) handleWatch(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	uuid, err := parsePlayer(r.PathValue("uuid"))
	if err != nil {
		return notFound("player")
	}
	var req watchReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if req.Seconds < 60 || req.Seconds > 300 {
		return badRequest("seconds must be 60 to 300")
	}
	ctx := r.Context()
	p, err := s.d.Store.GetPlayer(ctx, rc.tenant(), uuid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("player")
		}
		return err
	}
	if _, ok := s.connected(ctx, rc.tenant(), p.LastServerID); !ok {
		return errf(http.StatusConflict, codeNotConnected, "the player's server is not connected")
	}
	u, _ := identity.ParseCanonicalUUID(uuid)
	out := &pb.Outbound{TenantId: rc.tenant(), ServerId: p.LastServerID,
		Kind: &pb.Outbound_Watch{Watch: &pb.WatchRequest{Player: u.Bytes(), Seconds: uint32(req.Seconds)}}}
	if err := s.sendOutbound(out); err != nil {
		return unavailable("the watch request could not be sent")
	}
	s.auditRC(r, rc, "player.watch", uuid, map[string]any{"seconds": req.Seconds, "server_id": p.LastServerID})
	writeJSON(w, http.StatusOK, okJSON{OK: true})
	return nil
}
