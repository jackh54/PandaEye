package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

func (s *Service) handleBans(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	q := r.URL.Query()
	active, err := parseBoolParam(q, "active")
	if err != nil {
		return err
	}
	page, err := pageFrom(q)
	if err != nil {
		return err
	}
	bans, next, err := s.d.Store.ListBans(r.Context(), rc.tenant(), store.BanFilter{Active: active, At: rc.now}, page)
	if err != nil {
		return err
	}
	l := s.lookups(r.Context(), rc.tenant())
	out := make([]banJSON, 0, len(bans))
	for i := range bans {
		out = append(out, toBanJSON(&bans[i], l.playerName(bans[i].PlayerUUID), rc.now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"bans": out, "next_cursor": next})
	return nil
}

var appealStatuses = map[string]bool{store.AppealOpen: true, store.AppealAccepted: true, store.AppealDenied: true}

func (s *Service) handleListAppeals(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	q := r.URL.Query()
	var f store.AppealFilter
	if v := q.Get("status"); v != "" {
		if !appealStatuses[v] {
			return badRequest("unknown status %q", v)
		}
		f.Status = v
	}
	page, err := pageFrom(q)
	if err != nil {
		return err
	}
	as, next, err := s.d.Store.ListAppeals(r.Context(), rc.tenant(), f, page)
	if err != nil {
		return err
	}
	l := s.lookups(r.Context(), rc.tenant())
	out := make([]appealJSON, 0, len(as))
	for i := range as {
		out = append(out, toAppealJSON(&as[i], l.playerName(as[i].PlayerUUID)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"appeals": out, "next_cursor": next})
	return nil
}

type decideReq struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// handleDecideAppeal accepts (= overturns the verdict) or denies an open appeal.
func (s *Service) handleDecideAppeal(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req decideReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if req.Decision != "accept" && req.Decision != "deny" {
		return badRequest("decision must be accept or deny")
	}
	note, err := noteReq{Note: req.Note}.clean()
	if err != nil {
		return err
	}
	id, err := pathID(r, "id")
	if err != nil {
		return notFound("appeal")
	}
	ctx := r.Context()
	a, err := s.d.Store.GetAppeal(ctx, rc.tenant(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("appeal")
		}
		return err
	}
	if a.Status != store.AppealOpen {
		return conflict("the appeal was already decided")
	}
	status := store.AppealDenied
	detail := map[string]any{"decision": req.Decision, "note": note, "verdict_id": a.VerdictID}
	if req.Decision == "accept" {
		status = store.AppealAccepted
		v, err := s.d.Store.GetVerdict(ctx, rc.tenant(), a.VerdictID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return conflict("the appealed verdict no longer exists")
			}
			return err
		}
		res, err := s.overturn(ctx, v, rc.p.name, note, rc.now)
		if err != nil {
			return err
		}
		for k, x := range res.detail(note) {
			detail[k] = x
		}
	}
	a, err = s.d.Store.DecideAppeal(ctx, rc.tenant(), id, status, rc.p.name, note, rc.now)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			return conflict("the appeal was already decided")
		}
		return err
	}
	s.auditRC(r, rc, "appeal.decide", id, detail)
	writeJSON(w, http.StatusOK, toAppealJSON(a, s.lookups(ctx, rc.tenant()).playerName(a.PlayerUUID)))
	return nil
}

type publicAppealReq struct {
	VerdictID  string `json:"verdict_id"`
	AppealCode string `json:"appeal_code"`
	Message    string `json:"message"`
	Contact    string `json:"contact"`
}

// handlePublicAppeal stores a player's appeal (the dashboard Worker verified Turnstile). Only
// executed, non-shadow, active BANs can be appealed; every other case, an unknown verdict and a
// wrong code all get the same 202 {ok: true}. Limits: 5/h per client IP (X-PandaEye-Client-IP),
// 1 open and 3 total per verdict, 200/day per tenant.
func (s *Service) handlePublicAppeal(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	if ok, wait := s.appealLimits.allow(ipKey(rc.p.clientIP), rc.now); !ok {
		return &apiError{status: http.StatusTooManyRequests, code: codeRateLimited, msg: "too many appeals; try again later", retryAfter: wait}
	}
	var req publicAppealReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	msg, ok := cleanText(strings.TrimSpace(req.Message), store.MaxAppealMessage, true)
	if !ok || msg == "" {
		return badRequest("message must be 1 to %d characters", store.MaxAppealMessage)
	}
	contact, ok := cleanText(strings.TrimSpace(req.Contact), store.MaxAppealContact, false)
	if !ok {
		return badRequest("contact must be at most %d characters", store.MaxAppealContact)
	}
	if len(req.VerdictID) > 64 || len(req.AppealCode) > 64 {
		return badRequest("invalid verdict id or appeal code")
	}
	accepted := func() error {
		writeJSON(w, http.StatusAccepted, okJSON{OK: true})
		return nil
	}
	ctx := r.Context()
	v, ok, err := s.appealable(ctx, strings.TrimSpace(req.VerdictID), req.AppealCode, rc)
	if err != nil {
		return err
	}
	if !ok {
		return accepted()
	}
	a := &store.Appeal{TenantID: v.TenantID, VerdictID: v.ID, PlayerUUID: v.PlayerUUID, Message: msg, Contact: contact, CreatedAt: rc.now}
	if err := s.d.Store.CreateAppeal(ctx, a, store.DefaultAppealLimits); err != nil {
		if errors.Is(err, store.ErrLimitExceeded) {
			return errf(http.StatusTooManyRequests, codeAppealLimit, "an appeal for this ban is already open or the limit is reached")
		}
		return err
	}
	s.audit(ctx, v.TenantID, kindPublic, "appeal.create", a.ID, map[string]any{"verdict_id": v.ID}, rc.now)
	return accepted()
}

// appealable finds the verdict and checks the code and the ban; ok is false (without an error) for
// every case that must look like success to the caller.
func (s *Service) appealable(ctx context.Context, verdictID, code string, rc *reqCtx) (*store.Verdict, bool, error) {
	if _, err := identity.ParseCanonicalUUID(verdictID); err != nil {
		return nil, false, nil
	}
	tenantID, err := s.d.Store.LookupVerdictTenant(ctx, verdictID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	v, err := s.d.Store.GetVerdict(ctx, tenantID, verdictID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	if !s.appealCodeOK(v, code) {
		return nil, false, nil
	}
	if v.Action != pb.Action_ACTION_BAN || v.Shadow || !v.Executed {
		return nil, false, nil
	}
	ban, err := s.d.Store.GetBan(ctx, tenantID, verdictID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	if !ban.Ongoing(rc.now) {
		return nil, false, nil
	}
	return v, true, nil
}

// appealCodeOK compares in constant time with the stored code, or (none stored) with the code
// derived from the current and rotation outbound keys.
func (s *Service) appealCodeOK(v *store.Verdict, code string) bool {
	if v.AppealCode != "" {
		return identity.EqualAppealCode(v.AppealCode, code)
	}
	ok := false
	for _, k := range s.d.Secrets.OutboundKeys() {
		if len(k) > 0 && identity.EqualAppealCode(identity.AppealCode(k, v.ID), code) {
			ok = true
		}
	}
	return ok
}
