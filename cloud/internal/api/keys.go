package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackh54/PandaEye/cloud/internal/checks"
	"github.com/jackh54/PandaEye/cloud/internal/identity"
	"github.com/jackh54/PandaEye/cloud/internal/store"
)

var apiKeyScopes = []string{store.ScopeRead, store.ScopeVerdictsWrite, store.ScopeSettingsWrite, store.ScopeErase}

func (s *Service) handleListAPIKeys(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	keys, err := s.d.Store.ListAPIKeys(r.Context(), rc.tenant())
	if err != nil {
		return err
	}
	out := make([]apiKeyJSON, 0, len(keys))
	for i := range keys {
		out = append(out, toAPIKeyJSON(&keys[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": out})
	return nil
}

type createKeyReq struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

type createdKeyJSON struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	CreatedAt nullTime `json:"created_at"`
	Key       string   `json:"key"`
}

// handleCreateAPIKey creates a key; its plaintext is returned once and only its hash is stored.
func (s *Service) handleCreateAPIKey(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req createKeyReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	name, ok := cleanText(strings.TrimSpace(req.Name), 100, false)
	if !ok {
		return badRequest("name must be at most 100 characters")
	}
	scopes := []string{}
	for _, sc := range req.Scopes {
		if !slices.Contains(apiKeyScopes, sc) {
			return badRequest("unknown scope %q", sc)
		}
		if !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		scopes = []string{store.ScopeRead}
	}
	id, secret, token := identity.NewAPIKey()
	k := &store.APIKey{ID: id, TenantID: rc.tenant(), Name: name, KeyHash: identity.HashSecret(secret.Reveal()), Scopes: scopes, CreatedAt: rc.now}
	if err := s.d.Store.CreateAPIKey(r.Context(), k); err != nil {
		return err
	}
	s.auditRC(r, rc, "apikey.create", id, map[string]any{"name": name, "scopes": scopes})
	writeJSON(w, http.StatusCreated, createdKeyJSON{ID: id, Name: name, Scopes: scopes, CreatedAt: nullTime(k.CreatedAt), Key: token.Reveal()})
	return nil
}

func (s *Service) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	id, err := pathID(r, "id")
	if err != nil {
		return notFound("API key")
	}
	if err := s.d.Store.RevokeAPIKey(r.Context(), rc.tenant(), id, rc.now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return notFound("API key")
		}
		return err
	}
	s.auditRC(r, rc, "apikey.revoke", id, nil)
	writeJSON(w, http.StatusOK, okJSON{OK: true})
	return nil
}

// cloudChecks are the checks the tenant configures here (local checks live in the plugin's
// config.yml).
func cloudChecks() []checks.Spec {
	var out []checks.Spec
	for _, c := range checks.Catalog() {
		if c.Where == checks.Cloud {
			out = append(out, c)
		}
	}
	return out
}

func (s *Service) checksJSON(stored []store.TenantCheck) []checkJSON {
	byID := map[string]store.TenantCheck{}
	for _, c := range stored {
		byID[c.CheckID] = c
	}
	out := []checkJSON{}
	for _, spec := range cloudChecks() {
		j := checkJSON{ID: spec.ID, Enabled: spec.EnabledDefault, Overrides: map[string]float64{}}
		if c, ok := byID[spec.ID]; ok {
			j.Enabled = c.Enabled
			for k, v := range c.Overrides {
				j.Overrides[k] = v
			}
		}
		out = append(out, j)
	}
	return out
}

func (s *Service) handleGetChecks(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	stored, err := s.d.Store.ListTenantChecks(r.Context(), rc.tenant())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": s.checksJSON(stored)})
	return nil
}

type putChecksReq struct {
	Checks []struct {
		ID        string             `json:"id"`
		Enabled   *bool              `json:"enabled"`
		Overrides map[string]float64 `json:"overrides"`
	} `json:"checks"`
}

var overrideName = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)

const maxOverrides = 32

// handlePutChecks sets the configuration of the listed cloud checks. Ids that are not cloud checks
// of the catalog are ignored and their stored rows kept unchanged, as are checks absent from the
// list; a check set back to its default (catalog enabled flag, no overrides) loses its row.
func (s *Service) handlePutChecks(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	var req putChecksReq
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if len(req.Checks) > 500 {
		return badRequest("too many checks")
	}
	ctx := r.Context()
	stored, err := s.d.Store.ListTenantChecks(ctx, rc.tenant())
	if err != nil {
		return err
	}
	merged := map[string]store.TenantCheck{}
	for _, c := range stored {
		merged[c.CheckID] = c
	}
	defaults := map[string]bool{}
	for _, spec := range cloudChecks() {
		defaults[spec.ID] = spec.EnabledDefault
	}
	changed := []string{}
	for _, c := range req.Checks {
		def, known := defaults[c.ID]
		if !known {
			continue
		}
		if len(c.Overrides) > maxOverrides {
			return badRequest("check %s: at most %d overrides", c.ID, maxOverrides)
		}
		tc := store.TenantCheck{TenantID: rc.tenant(), CheckID: c.ID, Enabled: def, Overrides: map[string]float64{}}
		if c.Enabled != nil {
			tc.Enabled = *c.Enabled
		}
		for k, v := range c.Overrides {
			if !overrideName.MatchString(k) || math.IsNaN(v) || math.IsInf(v, 0) {
				return badRequest("check %s: invalid override %q", c.ID, k)
			}
			tc.Overrides[k] = v
		}
		if tc.Enabled == def && len(tc.Overrides) == 0 {
			delete(merged, c.ID)
		} else {
			merged[c.ID] = tc
		}
		changed = append(changed, c.ID)
	}
	list := make([]store.TenantCheck, 0, len(merged))
	for _, c := range merged {
		list = append(list, c)
	}
	slices.SortFunc(list, func(a, b store.TenantCheck) int { return strings.Compare(a.CheckID, b.CheckID) })
	if err := s.d.Store.ReplaceTenantChecks(ctx, rc.tenant(), list); err != nil {
		return err
	}
	s.auditRC(r, rc, "checks.update", rc.tenant(), map[string]any{"checks": changed})
	t, err := s.d.Store.GetTenant(ctx, rc.tenant())
	if err != nil {
		return err
	}
	if err := s.publishTenant(ctx, t); err != nil {
		s.log.Error("check settings not published", "err", err)
		return unavailable("checks were saved but could not be distributed; retry")
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": s.checksJSON(list)})
	return nil
}

func (s *Service) handleAudit(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	page, err := pageFrom(r.URL.Query())
	if err != nil {
		return err
	}
	es, next, err := s.d.Store.ListAudit(r.Context(), rc.tenant(), page)
	if err != nil {
		return err
	}
	out := make([]auditJSON, 0, len(es))
	for _, e := range es {
		d := e.Detail
		if len(d) == 0 || !json.Valid(d) {
			d = json.RawMessage("{}")
		}
		out = append(out, auditJSON{ID: e.ID, Principal: e.Principal, Action: e.Action, Target: e.Target, Detail: d,
			CreatedAt: nullTime(e.CreatedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out, "next_cursor": next})
	return nil
}

// labelExportJSON is one NDJSON line of the ML label export (ml/FEATURES.md section 7).
type labelExportJSON struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	PlayerUUID  string          `json:"player_uuid"`
	VerdictID   string          `json:"verdict_id"`
	Label       string          `json:"label"`
	Source      string          `json:"source"`
	WindowStart string          `json:"window_start"`
	WindowEnd   string          `json:"window_end"`
	CreatedAt   string          `json:"created_at"`
	Category    string          `json:"category,omitempty"`
	Action      string          `json:"action,omitempty"`
	Score       float64         `json:"score"`
	Checks      []string        `json:"checks"`
	Evidence    json.RawMessage `json:"evidence"`
}

// handleExportLabels writes one label per NDJSON line, oldest first, erased players excluded; a
// final {"next_cursor": "..."} line means more pages.
func (s *Service) handleExportLabels(w http.ResponseWriter, r *http.Request, rc *reqCtx) error {
	q := r.URL.Query()
	since, err := parseTimeParam(q, "since")
	if err != nil {
		return err
	}
	page, err := pageFrom(q)
	if err != nil {
		return err
	}
	if page.Limit == 0 {
		page.Limit = store.MaxPageLimit
	}
	ctx := r.Context()
	labels, next, err := s.d.Store.ExportLabels(ctx, rc.tenant(), since, page)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(labels))
	for _, l := range labels {
		if l.VerdictID != "" {
			ids = append(ids, l.VerdictID)
		}
	}
	cats, err := s.d.Store.VerdictCategories(ctx, rc.tenant(), ids)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	for _, l := range labels {
		line := labelExportJSON{
			ID: l.ID, TenantID: rc.tenant(), PlayerUUID: l.PlayerUUID, VerdictID: l.VerdictID, Label: l.Label.Label,
			Source: l.Source, WindowStart: formatTime(l.WindowStart), WindowEnd: formatTime(l.WindowEnd),
			CreatedAt: formatTime(l.CreatedAt), Category: categoryName(cats[l.VerdictID]), Action: actionName(l.Action),
			Score: num(l.Score), Checks: nonNil(l.Checks), Evidence: json.RawMessage("{}"),
		}
		if l.Evidence != nil {
			line.Evidence = evidenceJSON(l.Evidence)
		}
		if err := enc.Encode(line); err != nil {
			return nil // the client went away
		}
	}
	if next != "" {
		_ = enc.Encode(map[string]string{"next_cursor": next})
	}
	return nil
}
