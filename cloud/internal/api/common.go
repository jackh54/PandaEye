package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
	"github.com/jackh54/PandaEye/cloud/internal/stream"
	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// pageFrom reads ?limit= (1..200, larger values are clamped) and ?cursor=.
func pageFrom(q url.Values) (store.Page, error) {
	p := store.Page{Cursor: q.Get("cursor")}
	if len(p.Cursor) > 512 {
		return p, errf(http.StatusBadRequest, codeInvalidCursor, "invalid cursor")
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return p, badRequest("limit must be a positive integer")
		}
		p.Limit = min(n, store.MaxPageLimit)
	}
	return p, nil
}

// parsePlayer parses a player UUID from a path or query (any case, with or without hyphens) into its
// canonical form.
func parsePlayer(s string) (string, error) {
	u, err := identity.ParseUUID(strings.TrimSpace(s))
	if err != nil || u.IsZero() {
		return "", badRequest("invalid player UUID")
	}
	return u.String(), nil
}

// pathID validates an id path segment (verdict, appeal, clip and API key ids).
func pathID(r *http.Request, name string) (string, error) {
	id := r.PathValue(name)
	if id == "" || len(id) > 128 {
		return "", notFound("resource")
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", notFound("resource")
		}
	}
	return id, nil
}

func parseTimeParam(q url.Values, key string) (time.Time, error) {
	v := q.Get(key)
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, badRequest("%s must be an RFC 3339 time", key)
	}
	return t.UTC(), nil
}

func parseBoolParam(q url.Values, key string) (*bool, error) {
	switch q.Get(key) {
	case "":
		return nil, nil
	case "true":
		b := true
		return &b, nil
	case "false":
		b := false
		return &b, nil
	}
	return nil, badRequest("%s must be true or false", key)
}

// cleanText validates free text: at most max runes, valid UTF-8, no control characters except
// newlines and tabs when multiline.
func cleanText(s string, max int, multiline bool) (string, bool) {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return "", false
		}
	}
	return s, true
}

// audit appends an audit_log entry for a mutating call. Failures are logged, not returned: the
// change itself already happened.
func (s *Service) audit(ctx context.Context, tenantID, principalName, action, target string, detail any, at time.Time) {
	raw := json.RawMessage("{}")
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			raw = b
		}
	}
	e := &store.AuditEntry{TenantID: tenantID, Principal: principalName, Action: action, Target: target, Detail: raw, CreatedAt: at}
	if err := s.d.Store.InsertAudit(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("audit log write failed", "action", action, "err", err)
	}
}

func (s *Service) auditRC(r *http.Request, rc *reqCtx, action, target string, detail any) {
	s.audit(r.Context(), rc.tenant(), rc.p.name, action, target, detail, rc.now)
}

// lookups caches names for one response.
type lookups struct {
	s       *Service
	ctx     context.Context
	tenant  string
	servers map[string]string
	players map[string]string
}

func (s *Service) lookups(ctx context.Context, tenantID string) *lookups {
	return &lookups{s: s, ctx: ctx, tenant: tenantID, servers: map[string]string{}, players: map[string]string{}}
}

func (l *lookups) serverName(id string) string {
	if id == "" {
		return ""
	}
	if n, ok := l.servers[id]; ok {
		return n
	}
	n := ""
	if sv, err := l.s.d.Store.GetServer(l.ctx, l.tenant, id); err == nil {
		n = sv.Name
	}
	l.servers[id] = n
	return n
}

func (l *lookups) playerName(uuid string) string {
	if n, ok := l.players[uuid]; ok {
		return n
	}
	n := ""
	if p, err := l.s.d.Store.GetPlayer(l.ctx, l.tenant, uuid); err == nil {
		n = p.LastName
	}
	l.players[uuid] = n
	return n
}

func (l *lookups) verdict(v *store.Verdict) verdictJSON {
	return toVerdictJSON(v, l.serverName(v.ServerID))
}

// connected returns the live PE_CONNECTIONS entry of a server.
func (s *Service) connected(ctx context.Context, tenantID, serverID string) (stream.Connection, bool) {
	if serverID == "" {
		return stream.Connection{}, false
	}
	c, err := s.d.Bus.GetConnection(ctx, tenantID, serverID)
	if err != nil {
		if !errors.Is(err, stream.ErrKeyNotFound) && !errors.Is(err, stream.ErrBadToken) {
			s.log.Warn("connection lookup failed", "err", err)
		}
		return c, false
	}
	return c, true
}

// sendOutbound signs and publishes out to pe.out.<tenant>.<server>.
func (s *Service) sendOutbound(out *pb.Outbound) error {
	return s.d.Bus.PublishOutbound(s.d.Secrets.OutboundKey.Bytes(), out)
}

// evidenceURL is the dashboard link of a verdict (PE_PUBLIC_URL), or "".
func (s *Service) evidenceURL(verdictID string) string {
	base := strings.TrimRight(s.d.Config.PublicURL, "/")
	if base == "" {
		return ""
	}
	return base + "/verdicts/" + url.PathEscape(verdictID)
}
