package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	"github.com/jackh54/PandaEye/cloud/internal/verdict"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
	"google.golang.org/protobuf/proto"
)

var verdictStatuses = map[string]bool{
	store.StatusOpen: true, store.StatusConfirmed: true, store.StatusOverturned: true, store.StatusExpired: true,
	store.StatusSuperseded: true,
}

func (s *Service) handleListVerdicts(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	q := r.URL.Query()
	var f store.VerdictFilter
	if v := q.Get("action"); v != "" {
		a, ok := parseAction(v)
		if !ok {
			return badRequest("unknown action %q", v)
		}
		f.Action = a
	}
	if v := q.Get("status"); v != "" {
		if !verdictStatuses[v] {
			return badRequest("unknown status %q", v)
		}
		f.Status = v
	}
	if v := q.Get("player"); v != "" {
		p, err := parsePlayer(v)
		if err != nil {
			return err
		}
		f.PlayerUUID = p
	}
	if v := q.Get("server"); v != "" {
		u, err := identity.ParseUUID(v)
		if err != nil {
			return badRequest("invalid server id")
		}
		f.ServerID = u.String()
	}
	var err error
	if f.Since, err = parseTimeParam(q, "since"); err != nil {
		return err
	}
	if f.Until, err = parseTimeParam(q, "until"); err != nil {
		return err
	}
	shadow, err := parseBoolParam(q, "shadow")
	if err != nil {
		return err
	}
	page, err := pageFrom(q)
	if err != nil {
		return err
	}
	vs, next, err := s.d.Store.ListVerdictsShadow(r.Context(), rc.tenant(), f, shadow, page)
	if err != nil {
		return err
	}
	l := s.lookups(r.Context(), rc.tenant())
	out := make([]verdictJSON, 0, len(vs))
	for i := range vs {
		out = append(out, l.verdict(&vs[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"verdicts": out, "next_cursor": next})
	return nil
}

func (s *Service) loadVerdict(ctx context.Context, tenantID string, r *http.Request) (*store.Verdict, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, notFound("verdict")
	}
	v, err := s.d.Store.GetVerdict(ctx, tenantID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("verdict")
	}
	return v, err
}

func (s *Service) handleGetVerdict(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	v, err := s.loadVerdict(r.Context(), rc.tenant(), r)
	if err != nil {
		return err
	}
	l := s.lookups(r.Context(), rc.tenant())
	writeJSON(w, http.StatusOK, verdictDetailJSON{verdictJSON: l.verdict(v), Evidence: evidenceJSON(v.Evidence)})
	return nil
}

type noteReq struct {
	Note string `json:"note"`
}

func (n noteReq) clean() (string, error) {
	note, ok := cleanText(strings.TrimSpace(n.Note), 1000, true)
	if !ok {
		return "", badRequest("note must be at most 1000 characters")
	}
	return note, nil
}

func corrective(a pb.Action) bool {
	return a == pb.Action_ACTION_UNBAN || a == pb.Action_ACTION_UNMITIGATE
}

func (s *Service) handleConfirm(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req noteReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	note, err := req.clean()
	if err != nil {
		return err
	}
	ctx := r.Context()
	v, err := s.loadVerdict(ctx, rc.tenant(), r)
	if err != nil {
		return err
	}
	if corrective(v.Action) {
		return conflict("%s verdicts cannot be confirmed", actionName(v.Action))
	}
	if err := s.d.Store.SetVerdictStatus(ctx, v.TenantID, v.ID, store.StatusConfirmed,
		store.StatusOpen, store.StatusConfirmed, store.StatusExpired, store.StatusSuperseded); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return conflict("an overturned verdict cannot be confirmed")
		}
		return err
	}
	from, to := signalWindow(v)
	if err := s.insertLabel(ctx, v, store.LabelCheat, store.SourceConfirmed, from, to, rc.now); err != nil {
		return err
	}
	// Race-safe with the store writer's ack path: the contribution is inserted only while the
	// verdict is an executed, confirmed, eligible BAN of an opted-in, trusted tenant.
	contrib, err := verdict.ContributeReputation(ctx, s.d.Store, v.TenantID, v.ID, rc.now)
	if err != nil {
		return err
	}
	s.auditRC(r, rc, "verdict.confirm", v.ID, map[string]any{"note": note, "reputation_contribution": contrib})
	return s.writeVerdict(w, r, rc, v.ID)
}

func (s *Service) writeVerdict(w http.ResponseWriter, r *http.Request, rc *reqCtx, id string) error {
	v, err := s.d.Store.GetVerdict(r.Context(), rc.tenant(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.lookups(r.Context(), rc.tenant()).verdict(v))
	return nil
}

func (s *Service) handleOverturn(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req noteReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	note, err := req.clean()
	if err != nil {
		return err
	}
	v, err := s.loadVerdict(r.Context(), rc.tenant(), r)
	if err != nil {
		return err
	}
	res, err := s.overturn(r.Context(), v, rc.p.name, note, rc.now)
	if err != nil {
		return err
	}
	s.auditRC(r, rc, "verdict.overturn", v.ID, res.detail(note))
	return s.writeVerdict(w, r, rc, v.ID)
}

type overturnResult struct {
	UnbanVerdictID      string
	UnmitigateVerdictID string
	ReputationRemoved   int64
}

func (o overturnResult) detail(note string) map[string]any {
	return map[string]any{"note": note, "unban_verdict_id": o.UnbanVerdictID,
		"unmitigate_verdict_id": o.UnmitigateVerdictID, "reputation_removed": o.ReputationRemoved}
}

// overturn marks v overturned (idempotent: a retry completes what a failed attempt left), labels
// the player legit for the signal window, removes v's reputation contribution and its cheat label,
// sends an UNBAN to the server that executed v's ban (if it was executed) and always an UNMITIGATE
// to the player's open session.
func (s *Service) overturn(ctx context.Context, v *store.Verdict, by, note string, now time.Time) (overturnResult, error) {
	var res overturnResult
	if corrective(v.Action) {
		return res, conflict("%s verdicts cannot be overturned", actionName(v.Action))
	}
	if v.Status != store.StatusOverturned {
		if err := s.d.Store.SetVerdictStatus(ctx, v.TenantID, v.ID, store.StatusOverturned); err != nil {
			return res, err
		}
	}
	if _, err := s.d.Store.DeleteVerdictLabels(ctx, v.TenantID, v.ID, store.LabelCheat); err != nil {
		return res, err
	}
	from, to := signalWindow(v)
	if err := s.insertLabel(ctx, v, store.LabelLegit, store.SourceOverturned, from, to, now); err != nil {
		return res, err
	}
	n, err := s.d.Store.RemoveReputationContrib(ctx, v.TenantID, v.ID)
	if err != nil {
		return res, err
	}
	res.ReputationRemoved = n

	if v.Action == pb.Action_ACTION_BAN {
		ban, err := s.d.Store.GetBan(ctx, v.TenantID, v.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			// Never executed (shadow, or not acked as executed): nothing to undo on a server.
		case err != nil:
			return res, err
		case ban.Active || ban.UnbanVerdictID != "":
			unban, err := s.issueCorrective(ctx, v, pb.Action_ACTION_UNBAN, ban.ServerID, "Ban overturned by staff", now)
			if err != nil {
				return res, err
			}
			res.UnbanVerdictID = unban.ID
			if ban.Active {
				err := s.d.Store.OverturnBan(ctx, v.TenantID, v.ID, store.BanOverturn{By: by, Note: note, UnbanVerdictID: unban.ID, At: now})
				if err != nil && !errors.Is(err, store.ErrConflict) {
					return res, err
				}
			}
		}
	}
	unmit, err := s.issueCorrective(ctx, v, pb.Action_ACTION_UNMITIGATE, s.sessionServer(ctx, v), "Verdict overturned by staff", now)
	if err != nil {
		return res, err
	}
	res.UnmitigateVerdictID = unmit.ID
	return res, nil
}

// sessionServer is where the player's open session most likely is: the last server the player was
// seen on when it is connected (and not a proxy), else the server of the verdict.
func (s *Service) sessionServer(ctx context.Context, v *store.Verdict) string {
	if p, err := s.d.Store.GetPlayer(ctx, v.TenantID, v.PlayerUUID); err == nil && p.LastServerID != "" && p.LastServerID != v.ServerID {
		if c, ok := s.connected(ctx, v.TenantID, p.LastServerID); ok && c.Info.Platform != pb.Platform_PLATFORM_VELOCITY {
			return p.LastServerID
		}
	}
	return v.ServerID
}

// issueCorrective creates (once: deterministic id) the UNBAN or UNMITIGATE verdict that reverts
// parent, routed to serverID: it is stored pending, published as a VerdictRecord on
// pe.verdicts.<tenant> (the store writer persists it the same way) and sent to the server as a
// SignedOutbound when it is connected; otherwise the gateway delivers it on reconnect.
func (s *Service) issueCorrective(ctx context.Context, parent *store.Verdict, action pb.Action, serverID, reason string, now time.Time) (*store.Verdict, error) {
	id := identity.DerivedVerdictID(parent.ID, int32(action))
	sv, err := s.d.Store.GetVerdict(ctx, parent.TenantID, id)
	var pv *pb.Verdict
	switch {
	case err == nil:
		pv = &pb.Verdict{}
		if err := proto.Unmarshal(sv.Payload, pv); err != nil || pv.VerdictId != id {
			return nil, errors.New("api: stored corrective verdict has no valid payload")
		}
	case errors.Is(err, store.ErrNotFound):
		player, err := identity.ParseCanonicalUUID(parent.PlayerUUID)
		if err != nil {
			return nil, err
		}
		pv = &pb.Verdict{
			VerdictId: id, Player: player.Bytes(), PlayerName: parent.PlayerName, Action: action, Reason: reason,
			Category: parent.Category, Checks: parent.Checks, CreatedUnixMs: uint64(now.UnixMilli()),
			EvidenceUrl: s.evidenceURL(parent.ID), Supersedes: parent.ID,
		}
		payload, err := proto.Marshal(pv)
		if err != nil {
			return nil, err
		}
		networkID := parent.NetworkID
		if srv, err := s.d.Store.GetServer(ctx, parent.TenantID, serverID); err == nil {
			networkID = srv.NetworkID
		}
		sv = &store.Verdict{
			ID: id, TenantID: parent.TenantID, ServerID: serverID, NetworkID: networkID, PlayerUUID: parent.PlayerUUID,
			PlayerName: parent.PlayerName, SessionID: parent.SessionID, Action: action, Category: parent.Category,
			Reason: reason, Checks: parent.Checks, Evidence: json.RawMessage(`{"supersedes":` + strconv.Quote(parent.ID) + `}`),
			Payload: payload, Status: store.StatusOpen, Supersedes: parent.ID, CreatedAt: time.UnixMilli(int64(pv.CreatedUnixMs)).UTC(),
		}
		if _, err := s.d.Store.UpsertVerdict(ctx, sv); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	rec := &pb.VerdictRecord{
		TenantId: sv.TenantID, ServerId: sv.ServerID, NetworkId: sv.NetworkID, Verdict: pv,
		EvidenceJson: string(evidenceJSON(sv.Evidence)), SessionId: sv.SessionID, CreatedUnixUs: uint64(sv.CreatedAt.UnixMicro()),
	}
	data, err := proto.Marshal(rec)
	if err != nil {
		return nil, err
	}
	subj, err := stream.VerdictsSubject(sv.TenantID)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.d.Bus.Publish(pctx, stream.Message{Subject: subj, Data: data, MsgID: stream.VerdictRecordMsgID(id)}); err != nil {
		return nil, err
	}
	if _, ok := s.connected(ctx, sv.TenantID, sv.ServerID); ok && sv.AckedAt.IsZero() {
		out := &pb.Outbound{TenantId: sv.TenantID, ServerId: sv.ServerID, Kind: &pb.Outbound_Verdict{Verdict: pv}}
		if err := s.sendOutbound(out); err != nil {
			s.log.Warn("corrective verdict not sent; the gateway sweep delivers it", "verdict_id", id, "err", err)
		}
	}
	return sv, nil
}

// labelNamespace makes label ids deterministic per (verdict, label), so retries insert one label.
var labelNamespace = uuid.MustParse("1d0f3c52-7a4e-5b8f-9c21-3e6a8b4d2f10")

func (s *Service) insertLabel(ctx context.Context, v *store.Verdict, label, source string, from, to, now time.Time) error {
	l := &store.Label{
		ID:       uuid.NewSHA1(labelNamespace, []byte(v.TenantID+"|"+v.ID+"|"+label)).String(),
		TenantID: v.TenantID, PlayerUUID: v.PlayerUUID, VerdictID: v.ID, Label: label, Source: source,
		WindowStart: from, WindowEnd: to, CreatedAt: now,
	}
	err := s.d.Store.InsertLabel(ctx, l)
	if errors.Is(err, store.ErrConflict) {
		return nil
	}
	return err
}

// defaultSignalWindow is the label window before a verdict when its evidence carries no signal
// times (the verdict service's stale-data limit).
const defaultSignalWindow = 10 * time.Minute

// signalWindow returns the time span of the verdict's evidence signals (ARCHITECTURE 5.6 evidence
// "signals[].time": unix s/ms/us or RFC 3339), extended to the verdict's creation.
func signalWindow(v *store.Verdict) (time.Time, time.Time) {
	var ev struct {
		Signals []struct {
			Time json.RawMessage `json:"time"`
		} `json:"signals"`
	}
	var from, to time.Time
	if json.Unmarshal(v.Evidence, &ev) == nil {
		for _, sig := range ev.Signals {
			t, ok := parseLooseTime(sig.Time)
			if !ok {
				continue
			}
			if from.IsZero() || t.Before(from) {
				from = t
			}
			if t.After(to) {
				to = t
			}
		}
	}
	created := v.CreatedAt
	if from.IsZero() || from.After(created) {
		from = created.Add(-defaultSignalWindow)
	}
	if to.Before(created) {
		to = created
	}
	return from.UTC(), to.UTC()
}

// parseLooseTime reads a JSON number or numeric string (seconds, milliseconds or microseconds by
// magnitude) or an RFC 3339 string; only plausible times (2000..2200) are accepted.
func parseLooseTime(raw json.RawMessage) (time.Time, bool) {
	var n json.Number
	var str string
	var t time.Time
	if json.Unmarshal(raw, &n) == nil {
		str = n.String()
	} else if json.Unmarshal(raw, &str) != nil {
		return t, false
	}
	if f, err := strconv.ParseFloat(str, 64); err == nil {
		switch a := f; {
		case a >= 1e14:
			t = time.UnixMicro(int64(f))
		case a >= 1e11:
			t = time.UnixMilli(int64(f))
		default:
			t = time.Unix(int64(f), 0)
		}
	} else if p, err := time.Parse(time.RFC3339Nano, str); err == nil {
		t = p
	} else {
		return t, false
	}
	if t.Year() < 2000 || t.Year() > 2200 {
		return time.Time{}, false
	}
	return t, true
}
